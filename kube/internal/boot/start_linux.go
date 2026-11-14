//go:build linux && cgo

package boot

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/eclipse-iofog/edgelet-kube/internal/containerd"
	"github.com/eclipse-iofog/edgelet-kube/internal/images"
	"github.com/eclipse-iofog/edgelet-kube/internal/paths"
)

// Config is the containerd child and the images directory.
// Empty paths use the installed runtime directories.
type Config struct {
	// Socket is the containerd gRPC socket.
	Socket string
	// Root is the containerd persistent directory.
	Root string
	// State is the containerd transient directory.
	State string
	// CNIConfDir is the CNI configuration directory.
	CNIConfDir string
	// ImagesDir is the directory of image archives.
	ImagesDir string
	// Pull replaces the registry fetch used when ImagesDir is empty.
	Pull func(ctx context.Context, ref string) error
	// RegistryFile is the registries file. A missing file uses upstream endpoints.
	RegistryFile string
	// DisableDefaultEndpoint omits the upstream registry for names listed in RegistryFile.
	DisableDefaultEndpoint bool
	// PauseImage is the sandbox image.
	PauseImage string
	// Registry prefixes pin-list images. Empty leaves those names unchanged.
	Registry string
	// Settle is how long a new archive must stay unchanged before import.
	Settle time.Duration
	// Log receives containerd output. A nil Log uses standard output and error.
	Log io.Writer
	// Datastore opens the database after image import. Nil leaves it unopened.
	// Kube-apiserver is not started.
	Datastore func(ctx context.Context) error
}

// Process is the containerd child started before image import.
type Process struct {
	cmd  *exec.Cmd
	stop sync.Once
}

// Pid is the containerd child process id.
func (p *Process) Pid() int {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

// Stop ends the containerd child.
func (p *Process) Stop() {
	if p == nil {
		return
	}
	p.stop.Do(func() {
		containerd.StopChild(p.cmd)
	})
}

// Start starts the containerd child, imports and pins images, then opens the
// datastore when Config.Datastore is set. An empty images directory pulls the
// pinned list. Start returns after that import and the datastore open. The
// images directory stays watched until ctx is canceled. Image import finishes
// before Start returns. Kubelet is not started.
func Start(ctx context.Context, cfg Config) (*Process, error) {
	socket, root, state, cniConf, imagesDir := cfg.paths()
	for _, dir := range []string{root, state, cniConf, filepath.Dir(socket), imagesDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil { // #nosec G301 -- containerd and CNI traverse these directories
			return nil, fmt.Errorf("create runtime directory %s: %w", dir, err)
		}
	}
	stdout, stderr := io.Writer(os.Stdout), io.Writer(os.Stderr)
	if cfg.Log != nil {
		stdout, stderr = cfg.Log, cfg.Log
	}
	cmd, err := containerd.StartChild(childArgs(cfg), stdout, stderr)
	if err != nil {
		return nil, err
	}
	proc := &Process{cmd: cmd}
	if err := waitReady(ctx, socket); err != nil {
		proc.Stop()
		return nil, err
	}
	if err := images.Run(ctx, images.Config{
		Dir:      imagesDir,
		Socket:   socket,
		Pull:     cfg.Pull,
		Registry: cfg.Registry,
		Settle:   cfg.Settle,
		Log:      cfg.Log,
	}); err != nil {
		proc.Stop()
		return nil, err
	}
	if cfg.Datastore != nil {
		if err := cfg.Datastore(ctx); err != nil {
			proc.Stop()
			return nil, err
		}
	}
	go func() {
		<-ctx.Done()
		proc.Stop()
	}()
	return proc, nil
}

func (cfg Config) paths() (socket, root, state, cniConf, imagesDir string) {
	socket = cfg.Socket
	if socket == "" {
		socket = paths.ContainerdSocket
	}
	root = cfg.Root
	if root == "" {
		root = paths.ContainerdRoot
	}
	state = cfg.State
	if state == "" {
		state = paths.ContainerdState
	}
	cniConf = cfg.CNIConfDir
	if cniConf == "" {
		cniConf = paths.CNIConfDir
	}
	imagesDir = cfg.ImagesDir
	if imagesDir == "" {
		imagesDir = paths.ImagesDir
	}
	return socket, root, state, cniConf, imagesDir
}

func childArgs(cfg Config) []string {
	var args []string
	add := func(name, value string) {
		if value != "" {
			args = append(args, "--"+name, value)
		}
	}
	add("address", cfg.Socket)
	add("root", cfg.Root)
	add("state", cfg.State)
	add("cni-conf-dir", cfg.CNIConfDir)
	add("private-registry", cfg.RegistryFile)
	add("pause-image", cfg.PauseImage)
	if cfg.DisableDefaultEndpoint {
		args = append(args, "--disable-default-registry-endpoint=true")
	}
	return args
}

func waitReady(ctx context.Context, socket string) error {
	deadline := time.NewTimer(45 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var last error
	for {
		if _, last = containerd.RuntimeVersion(ctx, socket); last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("containerd did not become ready: %w", last)
		case <-ticker.C:
		}
	}
}
