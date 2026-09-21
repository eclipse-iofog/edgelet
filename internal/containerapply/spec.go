package containerapply

import (
	"encoding/json"
	"math"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/knowledgecatalog"
	"github.com/eclipse-iofog/edgelet/internal/modelcatalog"
	"github.com/eclipse-iofog/edgelet/internal/models"
	specs "github.com/opencontainers/runtime-spec/specs-go"
)

const (
	// LabelFingerprint is stored on the container so spec drift recreates it.
	LabelFingerprint = "iofog-container-apply"

	// CPUCFSPeriod is the CFS period (100ms) used with CPU quota.
	CPUCFSPeriod int64 = 100000
)

var rlimitTypes = map[string]string{
	"core":       "RLIMIT_CORE",
	"cpu":        "RLIMIT_CPU",
	"data":       "RLIMIT_DATA",
	"fsize":      "RLIMIT_FSIZE",
	"locks":      "RLIMIT_LOCKS",
	"memlock":    "RLIMIT_MEMLOCK",
	"msgqueue":   "RLIMIT_MSGQUEUE",
	"nice":       "RLIMIT_NICE",
	"nofile":     "RLIMIT_NOFILE",
	"nproc":      "RLIMIT_NPROC",
	"rss":        "RLIMIT_RSS",
	"rtprio":     "RLIMIT_RTPRIO",
	"rttime":     "RLIMIT_RTTIME",
	"sigpending": "RLIMIT_SIGPENDING",
	"stack":      "RLIMIT_STACK",
}

// Fingerprint is the recreate-relevant apply snapshot. Catalog item membership
// is omitted so add/remove of models or knowledge stays in-place when bindPath
// and permissions are unchanged.
type Fingerprint struct {
	BindPath             string                   `json:"bindPath,omitempty"`
	Permissions          string                   `json:"permissions,omitempty"`
	KnowledgeBindPath    string                   `json:"knowledgeBindPath,omitempty"`
	KnowledgePermissions string                   `json:"knowledgePermissions,omitempty"`
	Entrypoint           []string                 `json:"entrypoint,omitempty"`
	Commands             []string                 `json:"commands,omitempty"`
	WorkingDir           string                   `json:"workingDir,omitempty"`
	RunAsGroup           string                   `json:"runAsGroup,omitempty"`
	ReadOnlyRoot         bool                     `json:"readOnlyRoot,omitempty"`
	Tmpfs                []models.TmpfsMount      `json:"tmpfs,omitempty"`
	ShmSize              *int64                   `json:"shmSize,omitempty"`
	Cpus                 *float64                 `json:"cpus,omitempty"`
	MemoryReservation    *int64                   `json:"memoryReservation,omitempty"`
	MemorySwap           *int64                   `json:"memorySwap,omitempty"`
	Sysctls              map[string]string        `json:"sysctls,omitempty"`
	Ulimits              map[string]models.Ulimit `json:"ulimits,omitempty"`
	Devices              []models.DeviceMapping   `json:"devices,omitempty"`
}

// FromMicroservice builds the apply fingerprint for a microservice.
func FromMicroservice(ms *models.Microservice) Fingerprint {
	if ms == nil {
		return Fingerprint{}
	}
	fp := Fingerprint{
		ReadOnlyRoot:      ms.ReadOnlyRootFilesystem,
		Tmpfs:             append([]models.TmpfsMount(nil), ms.Tmpfs...),
		ShmSize:           cloneInt64(ms.ShmSize),
		Cpus:              cloneFloat64(ms.Cpus),
		MemoryReservation: cloneInt64(ms.MemoryReservation),
		MemorySwap:        cloneInt64(ms.MemorySwap),
		Sysctls:           cloneStringMap(ms.Sysctls),
		Ulimits:           cloneUlimits(ms.Ulimits),
		Devices:           append([]models.DeviceMapping(nil), ms.Devices...),
	}
	if ms.Models.HasItems() {
		fp.BindPath = strings.TrimSpace(ms.Models.BindPath)
		fp.Permissions = strings.ToLower(strings.TrimSpace(ms.Models.Permissions))
		if fp.Permissions == "" {
			fp.Permissions = models.ModelCatalogPermRO
		}
	}
	if ms.Knowledge.HasItems() {
		fp.KnowledgeBindPath = strings.TrimSpace(ms.Knowledge.BindPath)
		fp.KnowledgePermissions = strings.ToLower(strings.TrimSpace(ms.Knowledge.Permissions))
		if fp.KnowledgePermissions == "" {
			fp.KnowledgePermissions = models.KnowledgeCatalogPermRO
		}
	}
	if !models.UsesImageDefault(ms.Entrypoint) {
		fp.Entrypoint = append([]string{}, (*ms.Entrypoint)...)
	}
	if !models.UsesImageDefault(ms.Commands) {
		fp.Commands = append([]string{}, (*ms.Commands)...)
	}
	if ms.WorkingDir != nil {
		fp.WorkingDir = strings.TrimSpace(*ms.WorkingDir)
	}
	if ms.RunAsGroup != nil {
		fp.RunAsGroup = strings.TrimSpace(*ms.RunAsGroup)
	}
	return fp
}

// Empty reports whether no recreate-relevant apply fields are set.
func (f Fingerprint) Empty() bool {
	return f.BindPath == "" &&
		f.Permissions == "" &&
		f.KnowledgeBindPath == "" &&
		f.KnowledgePermissions == "" &&
		len(f.Entrypoint) == 0 &&
		len(f.Commands) == 0 &&
		f.WorkingDir == "" &&
		f.RunAsGroup == "" &&
		!f.ReadOnlyRoot &&
		len(f.Tmpfs) == 0 &&
		f.ShmSize == nil &&
		f.Cpus == nil &&
		f.MemoryReservation == nil &&
		f.MemorySwap == nil &&
		len(f.Sysctls) == 0 &&
		len(f.Ulimits) == 0 &&
		len(f.Devices) == 0
}

// Marshal encodes the fingerprint as a compact JSON label value.
func Marshal(fp Fingerprint) (string, error) {
	raw, err := json.Marshal(fp)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// MatchesLabel reports whether the stored fingerprint matches the microservice.
// A missing label matches only when the spec has no apply fields (legacy containers).
func MatchesLabel(label string, ms *models.Microservice) bool {
	want := FromMicroservice(ms)
	trimmed := strings.TrimSpace(label)
	if trimmed == "" {
		return want.Empty()
	}
	var got Fingerprint
	if err := json.Unmarshal([]byte(trimmed), &got); err != nil {
		return false
	}
	wantJSON, err := Marshal(want)
	if err != nil {
		return false
	}
	gotJSON, err := Marshal(got)
	if err != nil {
		return false
	}
	var wantNorm, gotNorm any
	if err := json.Unmarshal([]byte(wantJSON), &wantNorm); err != nil {
		return false
	}
	if err := json.Unmarshal([]byte(gotJSON), &gotNorm); err != nil {
		return false
	}
	return jsonEqual(wantNorm, gotNorm)
}

// CatalogBind is the single parent catalog mount (host projection → bindPath).
func CatalogBind(ms *models.Microservice, diskDir string) (host, container string, readOnly bool, ok bool) {
	if ms == nil || !ms.Models.HasItems() {
		return "", "", false, false
	}
	container = strings.TrimSpace(ms.Models.BindPath)
	if container == "" {
		return "", "", false, false
	}
	host = modelcatalog.HostDir(diskDir, ms.MicroserviceUUID)
	perms := strings.ToLower(strings.TrimSpace(ms.Models.Permissions))
	readOnly = perms != models.ModelCatalogPermRW
	return host, container, readOnly, true
}

// KnowledgeCatalogBind is the single parent knowledge catalog mount (host projection → bindPath).
func KnowledgeCatalogBind(ms *models.Microservice, diskDir string) (host, container string, readOnly bool, ok bool) {
	if ms == nil || !ms.Knowledge.HasItems() {
		return "", "", false, false
	}
	container = strings.TrimSpace(ms.Knowledge.BindPath)
	if container == "" {
		return "", "", false, false
	}
	host = knowledgecatalog.HostDir(diskDir, ms.MicroserviceUUID)
	perms := strings.ToLower(strings.TrimSpace(ms.Knowledge.Permissions))
	readOnly = perms != models.KnowledgeCatalogPermRW
	return host, container, readOnly, true
}

// CommandArgs is the engine command list, or nil to leave the image default.
func CommandArgs(ms *models.Microservice) []string {
	if ms == nil {
		return nil
	}
	if !models.UsesImageDefault(ms.Commands) {
		return append([]string{}, (*ms.Commands)...)
	}
	if len(ms.Args) > 0 {
		return ms.Args
	}
	return nil
}

// NanoCPUs converts a CPU count to Docker NanoCPUs (1e9 per CPU).
func NanoCPUs(cpus float64) int64 {
	return int64(math.Round(cpus * 1e9))
}

// CPUQuota converts a CPU count to CFS quota with period CPUCFSPeriod.
func CPUQuota(cpus float64) int64 {
	return int64(math.Round(cpus * float64(CPUCFSPeriod)))
}

// DockerUser is Config.User. Group is appended as uid:gid only when both are set
// and runAsUser does not already contain ':'.
func DockerUser(runAsUser, runAsGroup *string) string {
	user := ""
	if runAsUser != nil {
		user = strings.TrimSpace(*runAsUser)
	}
	group := ""
	if runAsGroup != nil {
		group = strings.TrimSpace(*runAsGroup)
	}
	if user != "" && group != "" && !strings.Contains(user, ":") {
		return user + ":" + group
	}
	return user
}

// DockerTmpfsOptions formats tmpfs size (MiB) and mode for HostConfig.Tmpfs.
func DockerTmpfsOptions(t models.TmpfsMount) string {
	parts := make([]string, 0, 2)
	if t.Size != nil && *t.Size > 0 {
		parts = append(parts, "size="+strconv.FormatInt(*t.Size, 10)+"m")
	}
	if mode := strings.TrimSpace(t.Mode); mode != "" {
		parts = append(parts, "mode="+mode)
	}
	return strings.Join(parts, ",")
}

// TmpfsHostDir is the host directory used for a CRI tmpfs bind.
func TmpfsHostDir(diskDir, msUUID, containerPath string) string {
	name := strings.ReplaceAll(strings.Trim(path.Clean(strings.TrimSpace(containerPath)), "/"), "/", "_")
	if name == "" || name == "." {
		name = "root"
	}
	return filepath.Join(strings.TrimSpace(diskDir), "volumes", "microservices", strings.TrimSpace(msUUID), "tmpfs", name)
}

// ShmHostDir is the host directory used for a sized CRI /dev/shm bind.
func ShmHostDir(diskDir, msUUID string) string {
	return filepath.Join(strings.TrimSpace(diskDir), "volumes", "microservices", strings.TrimSpace(msUUID), "shm")
}

// POSIXRlimits maps ulimit names to OCI rlimits. -1 becomes unlimited (max uint64).
func POSIXRlimits(ulimits map[string]models.Ulimit) []specs.POSIXRlimit {
	if len(ulimits) == 0 {
		return nil
	}
	out := make([]specs.POSIXRlimit, 0, len(ulimits))
	for name, limit := range ulimits {
		typ, ok := rlimitTypes[strings.ToLower(strings.TrimSpace(name))]
		if !ok {
			continue
		}
		out = append(out, specs.POSIXRlimit{
			Type: typ,
			Hard: rlimitValue(limit.Hard),
			Soft: rlimitValue(limit.Soft),
		})
	}
	return out
}

func rlimitValue(v int64) uint64 {
	if v < 0 {
		return ^uint64(0)
	}
	return uint64(v)
}

func cloneInt64(v *int64) *int64 {
	if v == nil {
		return nil
	}
	n := *v
	return &n
}

func cloneFloat64(v *float64) *float64 {
	if v == nil {
		return nil
	}
	n := *v
	return &n
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneUlimits(in map[string]models.Ulimit) map[string]models.Ulimit {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]models.Ulimit, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func jsonEqual(a, b any) bool {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for k, v := range av {
			if !jsonEqual(v, bv[k]) {
				return false
			}
		}
		return true
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return false
		}
		for i := range av {
			if !jsonEqual(av[i], bv[i]) {
				return false
			}
		}
		return true
	default:
		ab, err := json.Marshal(a)
		if err != nil {
			return false
		}
		bb, err := json.Marshal(b)
		if err != nil {
			return false
		}
		return string(ab) == string(bb)
	}
}
