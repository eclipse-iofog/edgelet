// Package volumereclaim destroys persistent VOLUME directories on explicit reclaim paths.
package volumereclaim

import (
	"errors"
	"strings"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/store"
)

const (
	moduleName = "VolumeReclaim"

	triggerVolumeRM           = "volume-rm"
	triggerVolumePrune        = "volume-prune"
	triggerPurgeVolumes       = "purge-volumes"
	triggerControlPlaneDelete = "controlplane-delete"

	orphanGrace = 24 * time.Hour
)

var (
	// ErrKeepSet is returned when desired state or the keep-set still owns the volume.
	ErrKeepSet = errors.New("persistent volume is still in the keep-set")
	// ErrInUse is returned when a listed container still mounts the host path.
	ErrInUse = errors.New("persistent volume is mounted by a listed container")
	// ErrPathJail is returned when the host path is outside the allowed volume trees.
	ErrPathJail = errors.New("persistent volume path is outside the allowed directories")
	// ErrControlPlane is returned when a control-plane volume is targeted by workload reclaim.
	ErrControlPlane = errors.New("control-plane volumes can only be deleted with control-plane delete")
	// ErrNotFound is returned when no matching persistent volume claim exists.
	ErrNotFound = errors.New("persistent volume not found")
)

// UsageProvider reports listed-container keep-set members and in-use mounts.
type UsageProvider interface {
	ListedPrivateUUIDs() []string
	ListedSharedNames() []string
	PathMounted(hostPath string) bool
}

// RemoveOptions controls explicit private or shared destroy.
type RemoveOptions struct {
	Force bool
}

// PruneOrphansOptions controls orphan prune. Zero value is a dry-run.
type PruneOrphansOptions struct {
	Confirm      bool
	Force        bool
	CleanupUUIDs []string
}

// Candidate is a persistent volume claim considered for reclaim.
type Candidate struct {
	UUID           string
	Name           string
	Scope          string
	Kind           string
	HostPath       string
	UnreferencedAt time.Time
	Reason         string
}

// PruneOrphansResult is the dry-run or confirm outcome of orphan prune.
type PruneOrphansResult struct {
	Candidates []Candidate
	Deleted    []Candidate
	Skipped    []Candidate
}

// Reclaimer destroys persistent VOLUME data under volumes/data and volumes/shared.
type Reclaimer struct {
	db            *store.DB
	diskDirectory string
	now           func() time.Time
	usage         UsageProvider
}

// New returns a reclaim helper. It does not start a background sweeper.
func New(db *store.DB, diskDirectory string) *Reclaimer {
	return &Reclaimer{
		db:            db,
		diskDirectory: strings.TrimSpace(diskDirectory),
		now:           time.Now,
	}
}

// SetUsageProvider supplies listed-container UUID/name keep-set members and mount checks.
func (r *Reclaimer) SetUsageProvider(usage UsageProvider) {
	if r == nil {
		return
	}
	r.usage = usage
}

func (r *Reclaimer) setNow(now func() time.Time) {
	if now == nil {
		r.now = time.Now
		return
	}
	r.now = now
}

func (r *Reclaimer) currentTime() time.Time {
	if r.now == nil {
		return time.Now()
	}
	return r.now()
}

func (r *Reclaimer) keepSet() (store.PersistentVolumeKeepSet, error) {
	var private, shared []string
	if r.usage != nil {
		private = r.usage.ListedPrivateUUIDs()
		shared = r.usage.ListedSharedNames()
	}
	return r.db.PersistentVolumeKeepSet(private, shared)
}

func (r *Reclaimer) pathMounted(hostPath string) bool {
	if r == nil || r.usage == nil {
		return false
	}
	return r.usage.PathMounted(hostPath)
}

func privateUUIDKept(keep store.PersistentVolumeKeepSet, uuid string) bool {
	uuid = strings.TrimSpace(uuid)
	for _, kept := range keep.PrivateUUIDs {
		if kept == uuid {
			return true
		}
	}
	return false
}

func sharedNameKept(keep store.PersistentVolumeKeepSet, name string) bool {
	name = strings.TrimSpace(name)
	for _, kept := range keep.SharedNames {
		if kept == name {
			return true
		}
	}
	return false
}

func cleanupSet(uuids []string) map[string]struct{} {
	out := make(map[string]struct{}, len(uuids))
	for _, uuid := range uuids {
		uuid = strings.TrimSpace(uuid)
		if uuid == "" {
			continue
		}
		out[uuid] = struct{}{}
	}
	return out
}
