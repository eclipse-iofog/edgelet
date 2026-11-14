//go:build linux && cgo

package boot

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/errdefs"
	kubecontainerd "github.com/eclipse-iofog/edgelet-kube/internal/containerd"
	kubeimages "github.com/eclipse-iofog/edgelet-kube/internal/images"
	"github.com/eclipse-iofog/edgelet-kube/scripts/airgap"
	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"golang.org/x/sys/unix"
)

func TestMain(m *testing.M) {
	handled, err := kubecontainerd.MaybeRunChild(os.Args)
	if handled {
		if err != nil {
			fmt.Fprintf(os.Stderr, "containerd child: %v\n", err)
			unix.Exit(1)
		}
		unix.Exit(0)
	}
	m.Run()
}

func TestEmptyDirectoryPinsListBeforeKubelet(t *testing.T) {
	dir := filepath.Join("/tmp", fmt.Sprintf("edgelet-kube-images-%d", os.Getpid()))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})
	logFile, err := os.Create(filepath.Join(dir, "child.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = logFile.Close()
	}()

	imagesDir := filepath.Join(dir, "images")
	sock := filepath.Join(dir, "containerd.sock")
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)

	want := airgap.ImagesFor(runtime.GOARCH)
	var pulled []string
	importedBeforeDatastore := false
	proc, err := Start(ctx, Config{
		Socket:     sock,
		Root:       filepath.Join(dir, "root"),
		State:      filepath.Join(dir, "state"),
		CNIConfDir: filepath.Join(dir, "net.d"),
		ImagesDir:  imagesDir,
		Settle:     200 * time.Millisecond,
		Log:        logFile,
		Pull: func(ctx context.Context, ref string) error {
			pulled = append(pulled, ref)
			return seedImage(ctx, sock, ref)
		},
		Datastore: func(context.Context) error {
			importedBeforeDatastore = imagePinned(t, sock, want[0])
			return nil
		},
	})
	if err != nil {
		body, _ := os.ReadFile(logFile.Name())
		t.Fatalf("start: %v\n%s", err, body)
	}
	t.Cleanup(proc.Stop)
	if !importedBeforeDatastore {
		t.Fatal("datastore opened before image import")
	}
	if strings.Join(pulled, "\n") != strings.Join(want, "\n") {
		t.Fatalf("pulled %v", pulled)
	}
	assertPinned(t, sock, want)
	assertAbsentFromDefaultNamespace(t, sock, want[0])
	if kubeletRunning() {
		t.Fatal("kubelet is running")
	}
	pid := proc.Pid()
	cmdline := processCmdline(t, pid)
	if !strings.Contains(cmdline, kubecontainerd.ChildArg) {
		t.Fatalf("cmdline %q", cmdline)
	}

	const added = "example.com/edgelet-kube-added:1"
	if err := os.WriteFile(filepath.Join(imagesDir, "added.tar"), dockerTar(t, added), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for !imagePinned(t, sock, added) {
		if time.Now().After(deadline) {
			body, _ := os.ReadFile(logFile.Name())
			names := imageNames(t, sock)
			t.Fatalf("added image was not imported, have %v\n%s", names, body)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if proc.Pid() != pid {
		t.Fatalf("containerd pid %d, was %d", proc.Pid(), pid)
	}
	if processCmdline(t, pid) == "" {
		t.Fatal("containerd process exited")
	}
	if kubeletRunning() {
		t.Fatal("kubelet is running")
	}
}

func seedImage(ctx context.Context, socket, ref string) error {
	client, err := containerd.New(socket, containerd.WithDefaultNamespace(kubeimages.Namespace))
	if err != nil {
		return err
	}
	defer func() {
		_ = client.Close()
	}()
	ctx = namespaces.WithNamespace(ctx, kubeimages.Namespace)
	_, err = client.ImageService().Create(ctx, images.Image{
		Name: ref,
		Target: ocispec.Descriptor{
			MediaType: ocispec.MediaTypeImageManifest,
			Digest:    digest.FromString(ref),
			Size:      int64(len(ref)),
		},
	})
	if err != nil && !errdefs.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func assertPinned(t *testing.T, socket string, names []string) {
	t.Helper()
	for _, name := range names {
		if !imagePinned(t, socket, name) {
			t.Fatalf("%s is not pinned in %s, have %v", name, kubeimages.Namespace, imageNames(t, socket))
		}
	}
}

func imagePinned(t *testing.T, socket, name string) bool {
	t.Helper()
	client := imageClient(t, socket)
	defer func() {
		_ = client.Close()
	}()
	ctx := namespaces.WithNamespace(t.Context(), kubeimages.Namespace)
	image, err := client.ImageService().Get(ctx, name)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return false
		}
		t.Fatal(err)
	}
	return image.Labels[kubeimages.PinnedLabel] == kubeimages.PinnedValue
}

func assertAbsentFromDefaultNamespace(t *testing.T, socket, name string) {
	t.Helper()
	client, err := containerd.New(socket, containerd.WithDefaultNamespace("default"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = client.Close()
	}()
	ctx := namespaces.WithNamespace(t.Context(), "default")
	_, err = client.ImageService().Get(ctx, name)
	if err == nil {
		t.Fatalf("%s is visible outside %s", name, kubeimages.Namespace)
	}
}

func imageNames(t *testing.T, socket string) []string {
	t.Helper()
	client := imageClient(t, socket)
	defer func() {
		_ = client.Close()
	}()
	ctx := namespaces.WithNamespace(t.Context(), kubeimages.Namespace)
	listed, err := client.ImageService().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(listed))
	for _, image := range listed {
		names = append(names, image.Name)
	}
	return names
}

func imageClient(t *testing.T, socket string) *containerd.Client {
	t.Helper()
	client, err := containerd.New(socket, containerd.WithDefaultNamespace(kubeimages.Namespace))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func processCmdline(t *testing.T, pid int) string {
	t.Helper()
	body, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatal(err)
	}
	return string(body)
}

func kubeletRunning() bool {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		body, err := os.ReadFile("/proc/" + entry.Name() + "/cmdline")
		if err != nil || len(body) == 0 {
			continue
		}
		argv0 := strings.TrimRight(string(body), "\x00")
		if i := strings.IndexByte(argv0, 0); i >= 0 {
			argv0 = argv0[:i]
		}
		if filepath.Base(argv0) == "kubelet" {
			return true
		}
	}
	return false
}

func dockerTar(t *testing.T, ref string) []byte {
	t.Helper()
	config := []byte(`{"architecture":"amd64","os":"linux","config":{},"rootfs":{"type":"layers","diff_ids":[]}}`)
	manifest := []byte(fmt.Sprintf(`[{"Config":"config.json","RepoTags":[%q],"Layers":[]}]`, ref))
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, file := range []struct {
		name string
		body []byte
	}{
		{name: "config.json", body: config},
		{name: "manifest.json", body: manifest},
	} {
		hdr := &tar.Header{Name: file.name, Mode: 0o644, Size: int64(len(file.body))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(file.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
