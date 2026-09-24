package models

import (
	"fmt"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
)

const (
	// VolumeScopePrivate is the default persistent VOLUME identity (per microservice UUID).
	VolumeScopePrivate = "private"
	// VolumeScopeShared is an opt-in node-global persistent VOLUME name.
	VolumeScopeShared = "shared"
)

const volumeMappingLogModule = "VolumeMapping"

// VolumeMapping represents microservice volume mappings for Docker run options
type VolumeMapping struct {
	HostDestination      string            `json:"hostDestination" yaml:"hostDestination"`
	ContainerDestination string            `json:"containerDestination" yaml:"containerDestination"`
	AccessMode           string            `json:"accessMode" yaml:"accessMode"`
	Type                 VolumeMappingType `json:"type" yaml:"type"`
	Scope                string            `json:"scope,omitempty" yaml:"scope,omitempty"`
}

// NewVolumeMapping creates a new VolumeMapping
func NewVolumeMapping(hostDestination, containerDestination, accessMode string, volumeType VolumeMappingType) *VolumeMapping {
	return &VolumeMapping{
		HostDestination:      hostDestination,
		ContainerDestination: containerDestination,
		AccessMode:           accessMode,
		Type:                 volumeType,
		Scope:                VolumeScopePrivate,
	}
}

// CanonicalVolumeScope returns the stored volume scope.
// Only exact lowercase "shared" is shared; omit, empty, and any other value are private.
func CanonicalVolumeScope(raw string) string {
	trimmed := strings.TrimSpace(raw)
	switch trimmed {
	case "", VolumeScopePrivate:
		return VolumeScopePrivate
	case VolumeScopeShared:
		return VolumeScopeShared
	default:
		logging.LogWarn(volumeMappingLogModule, fmt.Sprintf("unknown volume scope %q; treating as private", trimmed))
		return VolumeScopePrivate
	}
}

// VolumeScopeKnown reports whether raw is omit/empty, private, or shared.
func VolumeScopeKnown(raw string) bool {
	switch strings.TrimSpace(raw) {
	case "", VolumeScopePrivate, VolumeScopeShared:
		return true
	default:
		return false
	}
}

// EffectiveVolumeScope returns the ledger scope for this mapping.
// Scope is meaningful only for VOLUME; BIND and VOLUME_MOUNT are always private.
func (v *VolumeMapping) EffectiveVolumeScope() string {
	if v == nil || v.Type != VolumeMappingTypeVolume {
		return VolumeScopePrivate
	}
	return CanonicalVolumeScope(v.Scope)
}

// Equals checks if two VolumeMappings are equal
func (v *VolumeMapping) Equals(other *VolumeMapping) bool {
	if other == nil {
		return false
	}
	return v.HostDestination == other.HostDestination &&
		v.ContainerDestination == other.ContainerDestination &&
		v.AccessMode == other.AccessMode &&
		v.Type == other.Type &&
		v.EffectiveVolumeScope() == other.EffectiveVolumeScope()
}
