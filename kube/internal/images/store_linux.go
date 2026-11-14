//go:build linux && cgo

package images

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	containerd "github.com/containerd/containerd/v2/client"
	ctdimages "github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/errdefs"
	kubecontainerd "github.com/eclipse-iofog/edgelet-kube/internal/containerd"
	"google.golang.org/grpc"
	runtimev1 "k8s.io/cri-api/pkg/apis/runtime/v1"
)

type criStore struct {
	client *containerd.Client
	conn   *grpc.ClientConn
	images runtimev1.ImageServiceClient
}

func openStore(socket string) (Store, error) {
	if socket == "" {
		return nil, errors.New("containerd socket is required")
	}
	client, err := containerd.New(socket, containerd.WithDefaultNamespace(Namespace))
	if err != nil {
		return nil, fmt.Errorf("containerd client: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.NamespaceService().Create(ctx, Namespace, nil); err != nil && !errdefs.IsAlreadyExists(err) {
		_ = client.Close()
		return nil, fmt.Errorf("containerd namespace: %w", err)
	}
	conn, err := kubecontainerd.Dial(socket)
	if err != nil {
		_ = client.Close()
		return nil, err
	}
	return &criStore{
		client: client,
		conn:   conn,
		images: runtimev1.NewImageServiceClient(conn),
	}, nil
}

func (s *criStore) Pull(ctx context.Context, ref string) error {
	if _, err := s.images.PullImage(ctx, &runtimev1.PullImageRequest{
		Image: &runtimev1.ImageSpec{Image: ref},
	}); err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	// The image service records the tag and the digest separately. Keep the
	// requested name, and the tag without a digest, so a later retag can find it.
	return s.keepNames(ctx, ref)
}

func (s *criStore) keepNames(ctx context.Context, ref string) error {
	ctx = namespaces.WithNamespace(ctx, Namespace)
	service := s.client.ImageService()
	source, err := imageByName(ctx, service, ref)
	if err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	tag, _, _ := strings.Cut(ref, "@")
	for _, name := range []string{ref, tag} {
		if name == "" || name == source.Name {
			continue
		}
		image := source
		image.Name = name
		if _, err := service.Create(ctx, image); err != nil && !errdefs.IsAlreadyExists(err) {
			return fmt.Errorf("pull %s: %w", name, err)
		}
	}
	return nil
}

func imageByName(ctx context.Context, service ctdimages.Store, ref string) (ctdimages.Image, error) {
	if image, err := service.Get(ctx, ref); err == nil || !errdefs.IsNotFound(err) {
		return image, err
	}
	tag, digest, hasDigest := strings.Cut(ref, "@")
	if image, err := service.Get(ctx, tag); err == nil || !errdefs.IsNotFound(err) {
		return image, err
	}
	if hasDigest {
		if i := strings.LastIndex(tag, ":"); i > 0 {
			if image, err := service.Get(ctx, tag[:i]+"@"+digest); err == nil || !errdefs.IsNotFound(err) {
				return image, err
			}
		}
	}
	return ctdimages.Image{}, errdefs.ErrNotFound
}

func (s *criStore) Import(ctx context.Context, path string) ([]string, error) {
	reader, err := openArchive(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = reader.Close()
	}()
	ctx = namespaces.WithNamespace(ctx, Namespace)
	imported, err := s.client.Import(ctx, reader, containerd.WithAllPlatforms(true))
	if err != nil {
		return nil, fmt.Errorf("import %s: %w", path, err)
	}
	names := make([]string, 0, len(imported))
	for _, image := range imported {
		if image.Name != "" {
			names = append(names, image.Name)
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("import %s: archive contained no images", path)
	}
	return names, nil
}

func (s *criStore) Retag(ctx context.Context, from, to string) error {
	if from == to {
		return nil
	}
	ctx = namespaces.WithNamespace(ctx, Namespace)
	service := s.client.ImageService()
	image, err := service.Get(ctx, from)
	if err != nil {
		return fmt.Errorf("retag %s: %w", from, err)
	}
	image.Name = to
	if _, err := service.Create(ctx, image); err != nil {
		if !errdefs.IsAlreadyExists(err) {
			return fmt.Errorf("retag %s: %w", to, err)
		}
		if err := service.Delete(ctx, to); err != nil {
			return fmt.Errorf("retag %s: %w", to, err)
		}
		if _, err := service.Create(ctx, image); err != nil {
			return fmt.Errorf("retag %s: %w", to, err)
		}
	}
	return nil
}

func (s *criStore) Pin(ctx context.Context, names []string) error {
	ctx = namespaces.WithNamespace(ctx, Namespace)
	service := s.client.ImageService()
	for _, name := range names {
		image, err := service.Get(ctx, name)
		if err != nil {
			return fmt.Errorf("pin %s: %w", name, err)
		}
		if image.Labels[PinnedLabel] == PinnedValue && image.Labels[managedLabel] == managedValue {
			continue
		}
		if image.Labels == nil {
			image.Labels = map[string]string{}
		}
		image.Labels[PinnedLabel] = PinnedValue
		image.Labels[managedLabel] = managedValue
		if _, err := service.Update(ctx, image, "labels"); err != nil {
			return fmt.Errorf("pin %s: %w", name, err)
		}
	}
	return nil
}

func (s *criStore) Close() error {
	var err error
	if s.conn != nil {
		err = s.conn.Close()
	}
	if s.client != nil {
		err = errors.Join(err, s.client.Close())
	}
	return err
}
