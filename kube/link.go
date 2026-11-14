package edgeletkube

import (
	"github.com/eclipse-iofog/edgelet/pkg/data"
	"github.com/eclipse-iofog/edgelet/pkg/datadir"
	"github.com/eclipse-iofog/edgelet/pkg/flock"
)

// Bundle extract, the data directory, and the file lock come from the root module.
var (
	resolveDataDir = datadir.Resolve
	bundleNames    = data.AssetNames
	acquireLock    = flock.Acquire
)

// Linked reports that those packages are part of this module.
func Linked() bool {
	return resolveDataDir != nil && bundleNames != nil && acquireLock != nil
}
