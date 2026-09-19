package edgelet

import (
	"errors"
	"strings"
)

// discardNamedVolume is a no-op. Persistent VOLUME data lives under
// volumes/data and volumes/shared and is destroyed only by explicit reclaim
// (including control-plane delete). Named-volume removal must not treat
// volumes/{name} as the on-disk path.
func discardNamedVolume(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("volume name is required")
	}
	return nil
}
