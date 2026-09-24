package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

// Kubernetes safe sysctls accepted on a microservice.
// net.* is rejected when host network is on. IPC-namespaced names are rejected when ipcMode is host.
var safeSysctls = map[string]struct{}{
	"kernel.shm_rmid_forced":              {},
	"net.ipv4.ip_local_port_range":        {},
	"net.ipv4.tcp_syncookies":             {},
	"net.ipv4.ping_group_range":           {},
	"net.ipv4.ip_unprivileged_port_start": {},
	"net.ipv4.ip_local_reserved_ports":    {},
	"net.ipv4.tcp_keepalive_time":         {},
	"net.ipv4.tcp_fin_timeout":            {},
	"net.ipv4.tcp_keepalive_intvl":        {},
	"net.ipv4.tcp_keepalive_probes":       {},
	"net.ipv4.tcp_rmem":                   {},
	"net.ipv4.tcp_wmem":                   {},
	"net.ipv4.tcp_slow_start_after_idle":  {},
	"net.ipv4.tcp_notsent_lowat":          {},
}

// Docker / RLIMIT names. Nested "cpu" is RLIMIT_CPU (seconds), not container.cpus.
var allowedUlimits = map[string]struct{}{
	"core":       {},
	"cpu":        {},
	"data":       {},
	"fsize":      {},
	"locks":      {},
	"memlock":    {},
	"msgqueue":   {},
	"nice":       {},
	"nofile":     {},
	"nproc":      {},
	"rss":        {},
	"rtprio":     {},
	"rttime":     {},
	"sigpending": {},
	"stack":      {},
}

// Ulimit is a soft/hard resource limit. -1 means unlimited.
type Ulimit struct {
	Soft int64 `json:"soft" yaml:"soft"`
	Hard int64 `json:"hard" yaml:"hard"`
}

// UnmarshalJSON rejects scalar ulimit values; only {soft,hard} is accepted.
func (u *Ulimit) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed[0] != '{' {
		return errors.New("ulimit must be an object with soft and hard")
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return errors.New("ulimit must be an object with soft and hard")
	}
	softRaw, okSoft := raw["soft"]
	hardRaw, okHard := raw["hard"]
	if !okSoft || !okHard || len(raw) != 2 {
		return errors.New("ulimit must be an object with soft and hard")
	}
	var soft, hard int64
	if err := json.Unmarshal(softRaw, &soft); err != nil {
		return errors.New("ulimit must be an object with soft and hard")
	}
	if err := json.Unmarshal(hardRaw, &hard); err != nil {
		return errors.New("ulimit must be an object with soft and hard")
	}
	u.Soft = soft
	u.Hard = hard
	return nil
}

// UnmarshalYAML rejects scalar ulimit values; only {soft,hard} is accepted.
func (u *Ulimit) UnmarshalYAML(value *yaml.Node) error {
	if value == nil || value.Kind != yaml.MappingNode {
		return errors.New("ulimit must be an object with soft and hard")
	}
	var raw map[string]int64
	if err := value.Decode(&raw); err != nil {
		return errors.New("ulimit must be an object with soft and hard")
	}
	soft, okSoft := raw["soft"]
	hard, okHard := raw["hard"]
	if !okSoft || !okHard || len(raw) != 2 {
		return errors.New("ulimit must be an object with soft and hard")
	}
	u.Soft = soft
	u.Hard = hard
	return nil
}

// DeviceMapping is a host /dev device exposed into the container.
type DeviceMapping struct {
	HostPath      string `json:"hostPath" yaml:"hostPath"`
	ContainerPath string `json:"containerPath" yaml:"containerPath"`
	Permissions   string `json:"permissions,omitempty" yaml:"permissions,omitempty"`
}

// TmpfsMount is an in-memory filesystem mount inside the container. Size is MiB.
type TmpfsMount struct {
	ContainerPath string `json:"containerPath" yaml:"containerPath"`
	Size          *int64 `json:"size,omitempty" yaml:"size,omitempty"`
	Mode          string `json:"mode,omitempty" yaml:"mode,omitempty"`
}

// UsesImageDefault reports whether an argv field should leave the image default
// (omitted or an explicit empty list).
func UsesImageDefault(argv *[]string) bool {
	return argv == nil || len(*argv) == 0
}

// CloneStringSlicePtr copies an optional string list, preserving omitted vs empty.
func CloneStringSlicePtr(in *[]string) *[]string {
	if in == nil {
		return nil
	}
	out := make([]string, len(*in))
	copy(out, *in)
	return &out
}

func isSafeSysctl(name string) bool {
	_, ok := safeSysctls[strings.TrimSpace(name)]
	return ok
}

func ipcModeIsHost(ipcMode string) bool {
	return strings.EqualFold(strings.TrimSpace(ipcMode), "host")
}

func isIPCNamespacedSysctl(name string) bool {
	return strings.HasPrefix(name, "kernel.shm") ||
		strings.HasPrefix(name, "kernel.msg") ||
		strings.HasPrefix(name, "kernel.sem") ||
		strings.HasPrefix(name, "fs.mqueue.")
}

func isDevHostPath(p string) bool {
	cleaned := path.Clean(strings.TrimSpace(p))
	if cleaned == "/dev" {
		return true
	}
	return strings.HasPrefix(cleaned, "/dev/")
}

func validDevicePermissions(p string) bool {
	if p == "" {
		return true
	}
	for _, r := range p {
		switch r {
		case 'r', 'w', 'm':
		default:
			return false
		}
	}
	return true
}

func validateDeviceMapping(d DeviceMapping, index int) error {
	host := strings.TrimSpace(d.HostPath)
	if host == "" {
		return fmt.Errorf("spec.container.devices[%d].hostPath is required", index)
	}
	if !path.IsAbs(host) || !isDevHostPath(host) {
		return fmt.Errorf("spec.container.devices[%d].hostPath must be under /dev", index)
	}
	if strings.TrimSpace(d.ContainerPath) == "" {
		return fmt.Errorf("spec.container.devices[%d].containerPath is required", index)
	}
	perms := strings.TrimSpace(d.Permissions)
	if !validDevicePermissions(perms) {
		return fmt.Errorf("spec.container.devices[%d].permissions must be a combination of r, w, m", index)
	}
	return nil
}

func validateTmpfsMount(t TmpfsMount, index int) error {
	p := strings.TrimSpace(t.ContainerPath)
	if p == "" {
		return fmt.Errorf("spec.container.tmpfs[%d].containerPath is required", index)
	}
	if !path.IsAbs(p) {
		return fmt.Errorf("spec.container.tmpfs[%d].containerPath must be an absolute container path", index)
	}
	if t.Size != nil && *t.Size <= 0 {
		return fmt.Errorf("spec.container.tmpfs[%d].size must be greater than 0", index)
	}
	return nil
}

func validateSysctls(sysctls map[string]string, hostNetwork, hostIPC bool) error {
	for key := range sysctls {
		name := strings.TrimSpace(key)
		if name == "" {
			return errors.New("spec.container.sysctls keys must not be empty")
		}
		if !isSafeSysctl(name) {
			return fmt.Errorf("spec.container.sysctls %q is not on the allowlist", name)
		}
		if hostNetwork && strings.HasPrefix(name, "net.") {
			return fmt.Errorf("spec.container.sysctls %q cannot be set when hostNetworkMode is true", name)
		}
		if hostIPC && isIPCNamespacedSysctl(name) {
			return fmt.Errorf("spec.container.sysctls %q cannot be set when ipcMode is host", name)
		}
	}
	return nil
}

func isAllowedUlimit(name string) bool {
	_, ok := allowedUlimits[strings.ToLower(strings.TrimSpace(name))]
	return ok
}

func validateUlimits(ulimits map[string]Ulimit) error {
	if len(ulimits) == 0 {
		return nil
	}
	normalized := make(map[string]Ulimit, len(ulimits))
	for name, limit := range ulimits {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			return errors.New("spec.container.ulimits keys must not be empty")
		}
		if !isAllowedUlimit(key) {
			return fmt.Errorf("spec.container.ulimits %q is not a known limit name", name)
		}
		if _, dup := normalized[key]; dup {
			return fmt.Errorf("spec.container.ulimits %q is duplicated", key)
		}
		if err := validateUlimitRange(key, limit); err != nil {
			return err
		}
		normalized[key] = limit
	}
	for name := range ulimits {
		delete(ulimits, name)
	}
	for key, limit := range normalized {
		ulimits[key] = limit
	}
	return nil
}

func validateUlimitRange(name string, limit Ulimit) error {
	if limit.Soft < -1 || limit.Hard < -1 {
		return fmt.Errorf("spec.container.ulimits.%s soft and hard must be -1 or >= 0", name)
	}
	if limit.Soft == -1 && limit.Hard != -1 {
		return fmt.Errorf("spec.container.ulimits.%s soft is unlimited so hard must be -1", name)
	}
	if limit.Soft != -1 && limit.Hard != -1 && limit.Soft > limit.Hard {
		return fmt.Errorf("spec.container.ulimits.%s soft must be <= hard", name)
	}
	return nil
}

func validateRunAsUserGroup(runAsUser, runAsGroup string) error {
	user := strings.TrimSpace(runAsUser)
	group := strings.TrimSpace(runAsGroup)
	if group != "" && strings.Contains(user, ":") {
		return errors.New("runAsUser must not contain ':' when runAsGroup is set")
	}
	return nil
}

// ReadOnlyRootWithoutTmpfsWarning is logged when the root filesystem is read-only
// and the operator did not mount tmpfs at /tmp. Edgelet does not inject /tmp.
const ReadOnlyRootWithoutTmpfsWarning = "read-only root filesystem is enabled and no tmpfs is mounted at /tmp"

// HasTmpfsAt reports whether tmpfs lists the given absolute container path.
func HasTmpfsAt(tmpfs []TmpfsMount, containerPath string) bool {
	want := path.Clean(strings.TrimSpace(containerPath))
	if want == "" || want == "." {
		return false
	}
	for _, t := range tmpfs {
		if path.Clean(strings.TrimSpace(t.ContainerPath)) == want {
			return true
		}
	}
	return false
}

// NeedsReadOnlyRootTmpfsWarning is true when the root is read-only and /tmp is not a tmpfs.
func NeedsReadOnlyRootTmpfsWarning(readOnly bool, tmpfs []TmpfsMount) bool {
	return readOnly && !HasTmpfsAt(tmpfs, "/tmp")
}

func validateMemorySwap(memoryLimitSet bool, memorySwap *int64) error {
	if memorySwap == nil {
		return nil
	}
	if *memorySwap == -1 {
		return nil
	}
	if *memorySwap <= 0 {
		return errors.New("memorySwap must be -1 or a positive MiB total")
	}
	if !memoryLimitSet {
		return errors.New("memorySwap requires memoryLimit unless memorySwap is -1")
	}
	return nil
}
