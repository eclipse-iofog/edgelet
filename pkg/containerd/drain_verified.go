package containerd

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/eclipse-iofog/edgelet/internal/constants"
)

const drainVerifiedMarkerContents = "verified\n"

var (
	drainVerifiedMarkerMu   sync.RWMutex
	drainVerifiedMarkerPath = filepath.Join(constants.EdgeletRunDir, "drain-verified")
)

// DrainVerifiedMarkerPath is the ExecStopPost receipt written after a verified drain.
func DrainVerifiedMarkerPath() string {
	drainVerifiedMarkerMu.RLock()
	defer drainVerifiedMarkerMu.RUnlock()
	return drainVerifiedMarkerPath
}

// SetDrainVerifiedMarkerPath overrides the receipt path (tests only).
func SetDrainVerifiedMarkerPath(path string) {
	drainVerifiedMarkerMu.Lock()
	defer drainVerifiedMarkerMu.Unlock()
	drainVerifiedMarkerPath = strings.TrimSpace(path)
}

// ClearDrainVerifiedMarker removes a stale drain-verified receipt.
func ClearDrainVerifiedMarker() error {
	path := DrainVerifiedMarkerPath()
	if path == "" {
		return nil
	}
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// WriteDrainVerifiedMarker records that grace → force → verify passed.
func WriteDrainVerifiedMarker() error {
	path := DrainVerifiedMarkerPath()
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil { // #nosec G301 -- drain receipt dir under /run/edgelet must be traversable
		return err
	}
	return os.WriteFile(path, []byte(drainVerifiedMarkerContents), 0o600)
}

// HasDrainVerifiedMarker reports whether ExecStopPost may reap shims/children.
func HasDrainVerifiedMarker() bool {
	path := DrainVerifiedMarkerPath()
	if path == "" {
		return false
	}
	data, err := os.ReadFile(path) // #nosec G304 -- path is the drain-verified receipt
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == strings.TrimSpace(drainVerifiedMarkerContents)
}
