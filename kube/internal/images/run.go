package images

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/eclipse-iofog/edgelet-kube/internal/paths"
	"github.com/eclipse-iofog/edgelet-kube/scripts/airgap"
	"github.com/fsnotify/fsnotify"
)

// Config selects the images directory and the containerd socket.
type Config struct {
	// Dir is the images directory. An empty value uses paths.ImagesDir.
	Dir string
	// Socket is the containerd gRPC socket.
	Socket string
	// Pull replaces the registry fetch. A nil Pull uses the CRI image service.
	Pull func(ctx context.Context, ref string) error
	// Registry is the prefix applied to pin-list images. Empty leaves those names unchanged.
	Registry string
	// Settle is how long a new archive must stay unchanged before import.
	// An empty value waits one second.
	Settle time.Duration
	// Log receives watch errors. A nil Log uses standard error.
	Log io.Writer
}

type fileState struct {
	size int64
	mod  time.Time
}

type runner struct {
	dir      string
	store    Store
	settle   time.Duration
	log      io.Writer
	registry string

	mu   sync.Mutex
	seen map[string]fileState
}

// Run imports archives from the images directory into containerd and pins them.
// An empty directory pulls the pinned list. Run returns after that initial import.
// The directory stays watched until ctx is canceled, so a tar added later is
// imported without a process restart. Kubelet is not started.
func Run(ctx context.Context, cfg Config) error {
	if cfg.Dir == "" {
		cfg.Dir = paths.ImagesDir
	}
	if cfg.Settle <= 0 {
		cfg.Settle = time.Second
	}
	if cfg.Log == nil {
		cfg.Log = os.Stderr
	}
	store, err := openStore(cfg.Socket)
	if err != nil {
		return err
	}
	if cfg.Pull != nil {
		store = &pullFunc{Store: store, pull: cfg.Pull}
	}
	return run(ctx, cfg.Dir, cfg.Settle, cfg.Log, store, cfg.Registry)
}

type pullFunc struct {
	Store
	pull func(context.Context, string) error
}

func (p *pullFunc) Pull(ctx context.Context, ref string) error {
	return p.pull(ctx, ref)
}

func run(ctx context.Context, dir string, settle time.Duration, log io.Writer, store Store, registry string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil { // #nosec G301 -- image import directory is operator-visible
		_ = store.Close()
		return fmt.Errorf("create images directory: %w", err)
	}
	r := &runner{
		dir:      dir,
		store:    store,
		settle:   settle,
		log:      log,
		registry: registry,
		seen:     map[string]fileState{},
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("watch images directory: %w", err)
	}
	if err := watcher.Add(dir); err != nil {
		_ = watcher.Close()
		_ = store.Close()
		return fmt.Errorf("watch images directory: %w", err)
	}
	done := make(chan struct{})
	go func() {
		r.loop(ctx, watcher)
		close(done)
	}()
	err = r.scan(ctx)
	if err != nil {
		_ = watcher.Close()
		<-done
		return err
	}
	go func() {
		<-ctx.Done()
		_ = watcher.Close()
	}()
	return nil
}

func (r *runner) scan(ctx context.Context) error {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return fmt.Errorf("read images directory: %w", err)
	}
	var archives []string
	visible := 0
	for _, entry := range entries {
		name := entry.Name()
		if name == "" || strings.HasPrefix(name, ".") {
			continue
		}
		visible++
		path := filepath.Join(r.dir, name)
		if entry.IsDir() || !isArchive(name) {
			continue
		}
		archives = append(archives, path)
	}
	if visible == 0 {
		return r.pullList(ctx)
	}
	for _, path := range archives {
		if err := r.importFile(ctx, path); err != nil {
			return err
		}
	}
	return nil
}

func (r *runner) pullList(ctx context.Context) error {
	for _, ref := range airgap.ImagesFor(runtime.GOARCH) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := r.store.Pull(ctx, ref); err != nil {
			return err
		}
		names, err := r.tag(ctx, []string{ref})
		if err != nil {
			return err
		}
		if err := r.store.Pin(ctx, names); err != nil {
			return err
		}
	}
	return nil
}

func (r *runner) importFile(ctx context.Context, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.IsDir() || !isArchive(path) {
		return nil
	}
	r.mu.Lock()
	prev, ok := r.seen[path]
	r.mu.Unlock()
	if ok && prev.size == info.Size() && prev.mod.Equal(info.ModTime()) {
		return nil
	}
	names, err := r.store.Import(ctx, path)
	if err != nil {
		return err
	}
	names, err = r.tag(ctx, names)
	if err != nil {
		return err
	}
	if err := r.store.Pin(ctx, names); err != nil {
		return err
	}
	r.mu.Lock()
	r.seen[path] = fileState{size: info.Size(), mod: info.ModTime()}
	r.mu.Unlock()
	return nil
}

func (r *runner) tag(ctx context.Context, names []string) ([]string, error) {
	out := slices.Clone(names)
	for _, name := range names {
		for _, alias := range airgap.PrefixedNames(r.registry, name) {
			if slices.Contains(out, alias) {
				continue
			}
			if err := r.store.Retag(ctx, name, alias); err != nil {
				return nil, err
			}
			out = append(out, alias)
		}
	}
	return out, nil
}

func (r *runner) loop(ctx context.Context, watcher *fsnotify.Watcher) {
	defer func() {
		_ = watcher.Close()
		_ = r.store.Close()
	}()
	var timer *time.Timer
	var timerC <-chan time.Time
	pending := map[string]struct{}{}
	for {
		select {
		case <-ctx.Done():
			return
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			_, _ = fmt.Fprintf(r.log, "edgelet-kube: watch images: %v\n", err)
		case event, ok := <-watcher.Events:
			if !ok {
				return
			}
			if !event.Has(fsnotify.Create) && !event.Has(fsnotify.Write) && !event.Has(fsnotify.Rename) {
				continue
			}
			if !isArchive(event.Name) {
				continue
			}
			pending[event.Name] = struct{}{}
			if timer == nil {
				timer = time.NewTimer(r.settle)
				timerC = timer.C
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(r.settle)
			}
		case <-timerC:
			timer = nil
			timerC = nil
			for path := range pending {
				delete(pending, path)
				if err := r.importFile(ctx, path); err != nil && ctx.Err() == nil {
					_, _ = fmt.Fprintf(r.log, "edgelet-kube: import %s: %v\n", path, err)
				}
			}
		}
	}
}
