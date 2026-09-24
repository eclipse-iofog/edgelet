//go:build linux

package cri

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/constants"
	"github.com/eclipse-iofog/edgelet/internal/containerapply"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/eclipse-iofog/edgelet/internal/utils"
	"github.com/eclipse-iofog/edgelet/internal/volumemount"
	"github.com/eclipse-iofog/edgelet/internal/workloadmeta"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

// Runtime handler names — must match containerd config.
const (
	RuntimeHandlerCrun = "crun"
)

const (
	AnnotationIofogNetwork = "iofog.network"
)

type CNINetworkSelection struct {
	Scope       string
	NetworkName string
	HostNetwork bool
}

var ErrUnknownRuntimeClass = errors.New("unknown runtime class")

var listRuntimeClassesForHandler = func() ([]*models.LocalRuntimeClass, error) {
	db := store.GetInstance()
	if db == nil || db.Conn() == nil {
		return nil, nil
	}
	return db.ListLocalRuntimeClasses()
}

func ResolveCNINetwork(scope string, hostNetwork bool) string {
	if hostNetwork {
		return constants.EdgeletNetworkName
	}
	_ = scope // Scope remains metadata-only in single-bridge mode.
	return constants.EdgeletNetworkName
}

func SelectCNINetworkForMicroservice(ms *models.Microservice) CNINetworkSelection {
	selection := CNINetworkSelection{
		Scope:       workloadmeta.ScopeManaged,
		NetworkName: constants.EdgeletNetworkName,
	}
	if ms == nil {
		return selection
	}
	scope := workloadmeta.ResolveScope(ms.ApplicationName, ms.HostNetworkMode)
	return CNINetworkSelection{
		Scope:       scope,
		NetworkName: ResolveCNINetwork(scope, ms.HostNetworkMode),
		HostNetwork: ms.HostNetworkMode,
	}
}

// linuxNamespaceOptionsFromMicroservice returns namespace options for both sandbox and
// container from the same Microservice fields (single source of truth).
func linuxNamespaceOptionsFromMicroservice(ms *models.Microservice) *runtimeapi.NamespaceOption {
	network := runtimeapi.NamespaceMode_POD
	if ms.HostNetworkMode {
		network = runtimeapi.NamespaceMode_NODE
	}
	pid := runtimeapi.NamespaceMode_CONTAINER
	if ms.PidMode != nil && strings.TrimSpace(*ms.PidMode) == "host" {
		pid = runtimeapi.NamespaceMode_NODE
	}
	ipc := runtimeapi.NamespaceMode_CONTAINER
	if ms.IpcMode != nil && strings.TrimSpace(*ms.IpcMode) == "host" {
		ipc = runtimeapi.NamespaceMode_NODE
	}
	return &runtimeapi.NamespaceOption{
		Network: network,
		Pid:     pid,
		Ipc:     ipc,
	}
}

func podSandboxNeedsLinuxBlock(ms *models.Microservice) bool {
	if ms == nil {
		return false
	}
	if ms.IsPrivileged || ms.HostNetworkMode {
		return true
	}
	if ms.PidMode != nil && strings.TrimSpace(*ms.PidMode) == "host" {
		return true
	}
	if ms.IpcMode != nil && strings.TrimSpace(*ms.IpcMode) == "host" {
		return true
	}
	return false
}

// PodSandboxConfigFromMicroservice builds a CRI PodSandboxConfig from a
// microservice. 1 microservice = 1 pod sandbox.
func PodSandboxConfigFromMicroservice(ms *models.Microservice, hostname, logDir, nodeUID string) *runtimeapi.PodSandboxConfig {
	containerName := utils.EdgeletDockerContainerNamePrefix + ms.MicroserviceUUID
	metadata := &runtimeapi.PodSandboxMetadata{
		Name:      containerName,
		Namespace: constants.EdgeletContainerdNamespace,
		Uid:       ms.MicroserviceUUID,
		Attempt:   0,
	}

	portMappings := buildCRIPortMappings(ms.PortMappings)
	labels := workloadmeta.BuildLabels(workloadmeta.BuildInput{
		MicroserviceUUID: ms.MicroserviceUUID,
		MicroserviceName: ms.MicroserviceName,
		ApplicationName:  ms.ApplicationName,
		NodeUUID:         nodeUID,
		RuntimeEngine:    workloadmeta.RuntimeEngineEdgelet,
		IsRouter:         ms.IsRouter,
		IsNats:           ms.IsNats,
		IsController:     ms.IsController,
		HostNetwork:      ms.HostNetworkMode,
		IsSystem:         ms.IsSystem || ms.IsController,
		UserLabels:       ms.Labels,
	})
	networkSelection := SelectCNINetworkForMicroservice(ms)
	annotations := map[string]string{
		AnnotationIofogNetwork: networkSelection.NetworkName,
	}

	// Hostname: must be empty when using host network (NODE) — CRI spec and OCI runtimes
	// require it ("unable to set hostname without a private UTS namespace").
	// For non-host-network pods, use container name as hostname.
	sandboxHostname := ""
	if !ms.HostNetworkMode {
		sandboxHostname = containerName
	}
	config := &runtimeapi.PodSandboxConfig{
		Metadata:     metadata,
		Hostname:     sandboxHostname,
		LogDirectory: logDir,
		PortMappings: portMappings,
		Labels:       labels,
		Annotations:  annotations,
	}

	if podSandboxNeedsLinuxBlock(ms) {
		config.Linux = &runtimeapi.LinuxPodSandboxConfig{
			SecurityContext: &runtimeapi.LinuxSandboxSecurityContext{
				Privileged:       ms.IsPrivileged,
				NamespaceOptions: linuxNamespaceOptionsFromMicroservice(ms),
			},
		}
	}

	if len(ms.Sysctls) > 0 {
		if config.Linux == nil {
			config.Linux = &runtimeapi.LinuxPodSandboxConfig{}
		}
		config.Linux.Sysctls = cloneStringMap(ms.Sysctls)
	}

	return config
}

// ContainerConfigFromMicroservice builds a CRI ContainerConfig from a
// microservice. Includes image, env, args, mounts, and security context.
// sandboxID is stored in labels for teardown and recovery.
func ContainerConfigFromMicroservice(ms *models.Microservice, hostname string, envVars []string, logPath string, hostsFilePath string, resolvFilePath string, sandboxID string, nodeUID string) (*runtimeapi.ContainerConfig, error) {
	containerName := utils.EdgeletDockerContainerNamePrefix + ms.MicroserviceUUID
	metadata := &runtimeapi.ContainerMetadata{
		Name:    containerName,
		Attempt: 0,
	}

	image := &runtimeapi.ImageSpec{Image: ms.ImageName}

	envs := make([]*runtimeapi.KeyValue, 0, len(envVars))
	for _, e := range envVars {
		// envVars are "KEY=VALUE".
		for i := 0; i < len(e); i++ {
			if e[i] == '=' {
				envs = append(envs, &runtimeapi.KeyValue{Key: e[:i], Value: []byte(e[i+1:])})
				break
			}
		}
	}

	mounts, err := buildCRIMounts(ms, hostsFilePath, resolvFilePath)
	if err != nil {
		return nil, err
	}

	labels := workloadmeta.BuildLabels(workloadmeta.BuildInput{
		MicroserviceUUID: ms.MicroserviceUUID,
		MicroserviceName: ms.MicroserviceName,
		ApplicationName:  ms.ApplicationName,
		NodeUUID:         nodeUID,
		RuntimeEngine:    workloadmeta.RuntimeEngineEdgelet,
		IsRouter:         ms.IsRouter,
		IsNats:           ms.IsNats,
		IsController:     ms.IsController,
		HostNetwork:      ms.HostNetworkMode,
		IsSystem:         ms.IsSystem || ms.IsController,
		SandboxID:        sandboxID,
		UserLabels:       ms.Labels,
	})

	secCtx := &runtimeapi.LinuxContainerSecurityContext{
		Privileged: ms.IsPrivileged,
	}

	// CapAdd / CapDrop
	if len(ms.CapAdd) > 0 || len(ms.CapDrop) > 0 {
		secCtx.Capabilities = &runtimeapi.Capability{
			AddCapabilities:  ms.CapAdd,
			DropCapabilities: ms.CapDrop,
		}
	}

	// RunAsUser (uid) or RunAsUsername
	if ms.RunAsUser != nil && *ms.RunAsUser != "" {
		s := strings.TrimSpace(*ms.RunAsUser)
		if uid, err := strconv.ParseInt(s, 10, 64); err == nil {
			secCtx.RunAsUser = &runtimeapi.Int64Value{Value: uid}
		} else {
			secCtx.RunAsUsername = s
		}
	}
	if ms.RunAsGroup != nil && strings.TrimSpace(*ms.RunAsGroup) != "" {
		s := strings.TrimSpace(*ms.RunAsGroup)
		if gid, err := strconv.ParseInt(s, 10, 64); err == nil {
			if secCtx.RunAsUser == nil && secCtx.RunAsUsername == "" {
				secCtx.RunAsUser = &runtimeapi.Int64Value{Value: 0}
			}
			secCtx.RunAsGroup = &runtimeapi.Int64Value{Value: gid}
		}
	}
	secCtx.ReadonlyRootfs = ms.ReadOnlyRootFilesystem

	secCtx.NamespaceOptions = linuxNamespaceOptionsFromMicroservice(ms)

	// Resources: CPU set, memory limit, CPU quota, reservation, swap
	resources := &runtimeapi.LinuxContainerResources{}
	if ms.CPUSetCpus != nil && *ms.CPUSetCpus != "" {
		resources.CpusetCpus = *ms.CPUSetCpus
	}
	if ms.MemoryLimit != nil && *ms.MemoryLimit > 0 {
		resources.MemoryLimitInBytes = *ms.MemoryLimit
	}
	if ms.Cpus != nil && *ms.Cpus > 0 {
		resources.CpuPeriod = containerapply.CPUCFSPeriod
		resources.CpuQuota = containerapply.CPUQuota(*ms.Cpus)
	}
	if ms.MemoryReservation != nil && *ms.MemoryReservation > 0 {
		resources.Unified = map[string]string{
			"memory.low": strconv.FormatInt(*ms.MemoryReservation, 10),
		}
	}
	if ms.MemorySwap != nil {
		resources.MemorySwapLimitInBytes = *ms.MemorySwap
	}

	// Annotations
	annotations := make(map[string]string)
	if ms.Annotations != nil && *ms.Annotations != "" {
		var ann map[string]string
		if err := json.Unmarshal([]byte(*ms.Annotations), &ann); err == nil {
			for k, v := range ann {
				annotations[k] = v
			}
		}
	}

	// CDI devices
	var cdiDevices []*runtimeapi.CDIDevice
	for _, d := range ms.CdiDevs {
		d = strings.TrimSpace(d)
		if d != "" {
			cdiDevices = append(cdiDevices, &runtimeapi.CDIDevice{Name: d})
		}
	}

	var devices []*runtimeapi.Device
	for _, d := range ms.Devices {
		perms := strings.TrimSpace(d.Permissions)
		if perms == "" {
			perms = "rwm"
		}
		devices = append(devices, &runtimeapi.Device{
			ContainerPath: d.ContainerPath,
			HostPath:      d.HostPath,
			Permissions:   perms,
		})
	}

	var command []string
	if !models.UsesImageDefault(ms.Entrypoint) {
		command = append(command, (*ms.Entrypoint)...)
	}
	args := containerapply.CommandArgs(ms)

	workingDir := ""
	if ms.WorkingDir != nil {
		workingDir = strings.TrimSpace(*ms.WorkingDir)
	}

	config := &runtimeapi.ContainerConfig{
		Metadata:    metadata,
		Image:       image,
		Command:     command,
		Args:        args,
		WorkingDir:  workingDir,
		Envs:        envs,
		Mounts:      mounts,
		Devices:     devices,
		Labels:      labels,
		Annotations: annotations,
		LogPath:     logPath,
		CDIDevices:  cdiDevices,
		Linux: &runtimeapi.LinuxContainerConfig{
			SecurityContext: secCtx,
			Resources:       resources,
		},
	}

	if fp, err := containerapply.Marshal(containerapply.FromMicroservice(ms)); err == nil && fp != "" && fp != "{}" {
		if config.Labels == nil {
			config.Labels = make(map[string]string)
		}
		config.Labels[containerapply.LabelFingerprint] = fp
	}

	return config, nil
}

func portToInt32(port int) (int32, bool) {
	if port < 0 || port > math.MaxInt32 {
		return 0, false
	}
	return int32(port), true
}

func buildCRIPortMappings(ports []*models.PortMapping) []*runtimeapi.PortMapping {
	out := make([]*runtimeapi.PortMapping, 0, len(ports))
	for _, p := range ports {
		if p.Outside <= 0 {
			// Skip dynamic/invalid host ports — CRI and CNI portmap ignore HostPort <= 0.
			continue
		}
		hostPort, ok := portToInt32(p.Outside)
		if !ok {
			continue
		}
		containerPort, ok := portToInt32(p.Inside)
		if !ok {
			continue
		}
		proto := runtimeapi.Protocol_TCP
		if p.UDP {
			proto = runtimeapi.Protocol_UDP
		}
		out = append(out, &runtimeapi.PortMapping{
			HostPort:      hostPort,
			ContainerPort: containerPort,
			Protocol:      proto,
		})
	}
	return out
}

func buildCRIMounts(ms *models.Microservice, hostsFilePath string, resolvFilePath string) ([]*runtimeapi.Mount, error) {
	vmm := volumemount.GetInstance()
	var mounts []*runtimeapi.Mount

	for _, vm := range ms.VolumeMappings {
		isVolumeMount := vm.Type == models.VolumeMappingTypeVolumeMount
		source, err := vmm.ResolveHostPath(ms.MicroserviceUUID, vm.HostDestination, isVolumeMount, ms.RunAsUser, vm.EffectiveVolumeScope())
		if err != nil {
			return nil, err
		}
		readOnly := vm.AccessMode == "ro"
		mounts = append(mounts, &runtimeapi.Mount{
			ContainerPath: vm.ContainerDestination,
			HostPath:      source,
			Readonly:      readOnly,
		})
	}

	if hostsFilePath != "" {
		mounts = append(mounts, &runtimeapi.Mount{
			ContainerPath: "/etc/hosts",
			HostPath:      hostsFilePath,
			Readonly:      false,
		})
	}

	if !ms.HostNetworkMode {
		hostResolvPath := "/etc/resolv.conf"
		if strings.TrimSpace(resolvFilePath) != "" {
			hostResolvPath = strings.TrimSpace(resolvFilePath)
		}
		mounts = append(mounts, &runtimeapi.Mount{
			ContainerPath: "/etc/resolv.conf",
			HostPath:      hostResolvPath,
			Readonly:      true,
		})
	}

	diskDir := workloadDiskDirectory()
	if host, dest, readOnly, ok := containerapply.CatalogBind(ms, diskDir); ok {
		mounts = append(mounts, &runtimeapi.Mount{
			ContainerPath: dest,
			HostPath:      host,
			Readonly:      readOnly,
		})
	}
	if host, dest, readOnly, ok := containerapply.KnowledgeCatalogBind(ms, diskDir); ok {
		mounts = append(mounts, &runtimeapi.Mount{
			ContainerPath: dest,
			HostPath:      host,
			Readonly:      readOnly,
		})
	}

	for _, t := range ms.Tmpfs {
		p := strings.TrimSpace(t.ContainerPath)
		if p == "" {
			continue
		}
		mounts = append(mounts, &runtimeapi.Mount{
			ContainerPath: p,
			HostPath:      containerapply.TmpfsHostDir(diskDir, ms.MicroserviceUUID, p),
			Readonly:      false,
		})
	}

	if ms.ShmSize != nil && *ms.ShmSize > 0 {
		mounts = append(mounts, &runtimeapi.Mount{
			ContainerPath: "/dev/shm",
			HostPath:      containerapply.ShmHostDir(diskDir, ms.MicroserviceUUID),
			Readonly:      false,
		})
	}

	return mounts, nil
}

func workloadDiskDirectory() string {
	cfg := config.GetInstance()
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.DiskDirectory)
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// ResolveRuntimeHandler resolves runtime handler using deterministic fallback
// and optional RuntimeClass mappings.
func ResolveRuntimeHandler(ms *models.Microservice, runtimeClasses []*models.LocalRuntimeClass) (string, error) {
	if ms == nil {
		return RuntimeHandlerCrun, nil
	}
	needsCrun := ms.IsPrivileged || ms.HostNetworkMode ||
		(ms.PidMode != nil && strings.TrimSpace(*ms.PidMode) == "host") ||
		(ms.IpcMode != nil && strings.TrimSpace(*ms.IpcMode) == "host")
	if needsCrun {
		return RuntimeHandlerCrun, nil
	}

	requested := ""
	if ms.Runtime != nil {
		requested = strings.TrimSpace(strings.ToLower(*ms.Runtime))
	}
	if requested == RuntimeHandlerCrun {
		return RuntimeHandlerCrun, nil
	}
	if requested != "" {
		for _, rc := range runtimeClasses {
			if rc == nil {
				continue
			}
			rc.Normalize()
			if requested != rc.Name && requested != rc.RuntimeName {
				continue
			}
			return rc.Name, nil
		}
		return "", fmt.Errorf("%w: %s", ErrUnknownRuntimeClass, requested)
	}
	return RuntimeHandlerCrun, nil
}

// GetRuntimeHandler returns the CRI runtime handler for the microservice.
func GetRuntimeHandler(ms *models.Microservice) (string, error) {
	runtimeClasses, err := listRuntimeClassesForHandler()
	if err != nil {
		return "", fmt.Errorf("list runtime classes: %w", err)
	}
	return ResolveRuntimeHandler(ms, runtimeClasses)
}

// LogPathsForCRI returns the log directory and log path for the container.
// CRI expects log_directory on the pod sandbox and log_path on the container.
// Uses "0.log" (Kubernetes convention) — containerd CRI writes both stdout and stderr
// to this single file with CRI log format (timestamp stream flag message).
func LogPathsForCRI(logDir, microserviceUUID string) (logDirectory, logPath string) {
	logDirectory = filepath.Join(logDir, microserviceUUID)
	logPath = "0.log"
	return logDirectory, logPath
}
