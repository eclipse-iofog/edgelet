package images

import "context"

const (
	// Namespace is the containerd namespace kubelet uses.
	Namespace = "k8s.io"
	// PinnedLabel marks an image so garbage collection does not remove it.
	PinnedLabel = "io.cri-containerd.pinned"
	// PinnedValue is the value of PinnedLabel.
	PinnedValue = "pinned"

	managedLabel = "io.cri-containerd.image"
	managedValue = "managed"
)

// Store pulls, imports, and pins images in Namespace.
type Store interface {
	Pull(ctx context.Context, ref string) error
	Import(ctx context.Context, path string) ([]string, error)
	// Retag copies from to another name in Namespace.
	Retag(ctx context.Context, from, to string) error
	Pin(ctx context.Context, names []string) error
	Close() error
}
