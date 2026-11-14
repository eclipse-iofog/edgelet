//go:build linux && cgo

package containerd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/containerd/containerd/v2/cmd/ctr/app"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	runtimev1 "k8s.io/cri-api/pkg/apis/runtime/v1"
)

// RunCtr runs ctr against the containerd socket.
func RunCtr(args []string) error {
	socket, rest, err := clientSocket(args)
	if err != nil {
		return err
	}
	argv := append([]string{"ctr", "--address", socket}, rest...)
	if err := app.New().Run(argv); err != nil {
		return fmt.Errorf("ctr: %w", err)
	}
	return nil
}

// RunCrictl runs crictl against the containerd CRI socket.
func RunCrictl(args []string) error {
	socket, rest, err := clientSocket(args)
	if err != nil {
		return err
	}
	if len(rest) == 0 || rest[0] != "version" {
		command := ""
		if len(rest) > 0 {
			command = rest[0]
		}
		return fmt.Errorf("crictl command %q is unavailable", command)
	}
	version, err := RuntimeVersion(context.Background(), socket)
	if err != nil {
		return err
	}
	fmt.Printf("RuntimeName: %s\nRuntimeVersion: %s\nRuntimeApiVersion: %s\n", version.RuntimeName, version.RuntimeVersion, version.RuntimeAPIVersion)
	return nil
}

// RuntimeInfo is the CRI runtime version.
type RuntimeInfo struct {
	// RuntimeName is the runtime that answered the version call.
	RuntimeName string
	// RuntimeVersion is the runtime version.
	RuntimeVersion string
	// RuntimeAPIVersion is the CRI API version.
	RuntimeAPIVersion string
}

// Dial opens a gRPC connection to the containerd socket.
func Dial(socket string) (*grpc.ClientConn, error) {
	if err := checkSocket(socket); err != nil {
		return nil, err
	}
	conn, err := grpc.NewClient("passthrough:///containerd",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socket)
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("dial cri: %w", err)
	}
	return conn, nil
}

// RuntimeVersion calls the CRI version method on socket.
func RuntimeVersion(ctx context.Context, socket string) (RuntimeInfo, error) {
	dialCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	conn, err := Dial(socket)
	if err != nil {
		return RuntimeInfo{}, err
	}
	defer func() {
		_ = conn.Close()
	}()
	resp, err := runtimev1.NewRuntimeServiceClient(conn).Version(dialCtx, &runtimev1.VersionRequest{})
	if err != nil {
		return RuntimeInfo{}, fmt.Errorf("cri version: %w", err)
	}
	info := RuntimeInfo{
		RuntimeName:       resp.GetRuntimeName(),
		RuntimeVersion:    resp.GetRuntimeVersion(),
		RuntimeAPIVersion: resp.GetRuntimeApiVersion(),
	}
	if info.RuntimeName == "" && info.RuntimeVersion == "" {
		return RuntimeInfo{}, errors.New("cri version response was empty")
	}
	return info, nil
}
