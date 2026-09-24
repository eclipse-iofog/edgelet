//go:build linux

// Package edgelet implements the ContainerEngine interface using the embedded
// containerd runtime. It communicates directly with the in-process containerd
// via the containerd Go client (not the Docker SDK), connecting to the private
// socket at /run/edgelet/containerd.sock.
package edgelet

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	v1stats "github.com/containerd/cgroups/v3/cgroup1/stats"
	v2stats "github.com/containerd/cgroups/v3/cgroup2/stats"
	"github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/core/content"
	"github.com/containerd/containerd/v2/core/images"
	dockerresolver "github.com/containerd/containerd/v2/core/remotes/docker"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/namespaces"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/containerd/errdefs"
	tuypeurl "github.com/containerd/typeurl/v2"
	"github.com/eclipse-iofog/edgelet/internal/dnsresolver"
	"github.com/eclipse-iofog/edgelet/pkg/engine/edgelet/cri"
	"github.com/nxadm/tail"
	specs "github.com/opencontainers/runtime-spec/specs-go"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/constants"
	"github.com/eclipse-iofog/edgelet/internal/containerapply"
	"github.com/eclipse-iofog/edgelet/internal/containerexec"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/runtimeops"
	"github.com/eclipse-iofog/edgelet/internal/utils"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/eclipse-iofog/edgelet/internal/workloadmeta"
	"github.com/eclipse-iofog/edgelet/pkg/engine"
	"github.com/eclipse-iofog/edgelet/pkg/imageref"
)

const (
	// Full containerd runtime type identifiers — must be passed to
	// client.WithRuntime(); short names like "runc" are invalid in v2.1+.
	runcRuntimeType = "io.containerd.runc.v2"
	spinRuntimeType = "io.containerd.spin.v2"

	// hostsDir is the directory where per-container /etc/hosts files are written.
	hostsDir = "/run/edgelet/hosts"
	// resolvDir is the directory where per-container /etc/resolv.conf files are written.
	resolvDir = "/run/edgelet/resolv"
)

var log = logging.NewModuleLogger("EdgeletEngine")

// pendingExec holds the information needed to start a containerd exec process.
// It is stored between CreateExecSession (which validates the container and builds
// the process spec) and StartExecSession (which wires I/O and starts the process).
type pendingExec struct {
	containerID string
	execID      string
	spec        *specs.Process
}

// Engine implements engine.ContainerEngine using the embedded containerd.
type Engine struct {
	client              *client.Client
	criClient           *cri.Client
	logDir              string
	store               *stateStore
	pendingExecs        map[string]*pendingExec   // execID -> pending (supports concurrent exec sessions)
	runningProcs        map[string]client.Process // execID -> process (for status/stop)
	execExitCode        map[string]int            // execID -> exit code after process completion
	execMu              sync.Mutex                // protects pendingExecs and runningProcs
	execSessions        sync.Map                  // containerID -> []string (active exec IDs)
	execToCont          sync.Map                  // execID -> containerID (for removal on StopExecSession)
	dnsResolver         *dnsresolver.Resolver
	runtimeEventsCancel context.CancelFunc
}

// New returns an uninitialised iofog engine. logDir is the directory to write
// per-container log files to.
func New(logDir string) *Engine {
	if logDir == "" {
		logDir = "/var/log/edgelet/containers"
	}
	return &Engine{
		logDir:       logDir,
		store:        newStateStore(),
		pendingExecs: make(map[string]*pendingExec),
		runningProcs: make(map[string]client.Process),
		execExitCode: make(map[string]int),
	}
}

// Init connects to the embedded containerd socket, initializes CNI, and
// recovers per-container state from existing container labels.
func (e *Engine) Init(cfg engine.EngineConfig) error {
	initStart := time.Now()
	socketPath := cfg.SocketURL
	if socketPath == "" {
		socketPath = constants.EdgeletContainerdSocket
	}
	socketPath = strings.TrimPrefix(socketPath, "unix://")

	c, err := client.New(socketPath, client.WithDefaultNamespace(constants.EdgeletContainerdNamespace))
	if err != nil {
		return fmt.Errorf("connect to containerd at %s: %w", socketPath, err)
	}
	e.client = c

	// Import embedded pause (sandbox) image so CRI podsandbox can use it.
	if err := e.importPauseImage(); err != nil {
		e.emitEngineWarn(runtimeops.EventEngineInit, "", "", "", "", "pause image import failed (non-fatal)", initStart, err, map[string]any{"operation": "importPauseImage"})
	}

	// CRI client for RunPodSandbox, CreateContainer, etc. CNI lifecycle is managed by containerd.
	criClient, err := cri.NewClient(socketPath)
	if err != nil {
		return fmt.Errorf("create CRI client: %w", err)
	}
	e.criClient = criClient

	e.dnsResolver = dnsresolver.GetInstance()
	e.dnsResolver.SetRuntimeSnapshotProvider(e.runtimeDNSSnapshot)
	if err := e.dnsResolver.Start(); err != nil {
		e.emitEngineWarn(runtimeops.EventEngineInit, "", "", "", "", "embedded DNS start failed (non-fatal)", initStart, err, map[string]any{"operation": "dnsStart"})
	}
	// Recover per-container state from existing container labels.
	e.recoverState()
	e.startContainerdRuntimeEventMonitor()
	e.emitRuntime(runtimeops.RuntimeEvent{
		Event:      runtimeops.EventEngineInit,
		Level:      runtimeops.LevelInfo,
		Message:    "iofog engine initialized",
		Result:     runtimeops.ResultOK,
		DurationMs: time.Since(initStart).Milliseconds(),
		Fields: map[string]any{
			"socket":    socketPath,
			"namespace": constants.EdgeletContainerdNamespace,
		},
	})
	return nil
}

// importPauseImage imports the embedded pause (sandbox) image into containerd's
// content store so the CRI plugin can use it for pod sandboxes.
func (e *Engine) importPauseImage() error {
	pausePath, err := pathUnderBase(constants.EdgeletContainerdImagesDir, "pause.tar.gz")
	if err != nil {
		return err
	}
	if _, err := os.Stat(pausePath); err != nil {
		if os.IsNotExist(err) {
			log.Debugf("Pause image not found at %s, skipping import", pausePath)
			return nil
		}
		return fmt.Errorf("stat pause image: %w", err)
	}
	f, err := func() (*os.File, error) {
		root, err := os.OpenRoot(constants.EdgeletContainerdImagesDir)
		if err != nil {
			return nil, fmt.Errorf("open images root: %w", err)
		}
		defer root.Close()
		return root.Open("pause.tar.gz")
	}()
	if err != nil {
		return fmt.Errorf("open pause image: %w", err)
	}
	defer func() {
		_ = f.Close()
	}()
	archive, err := decompressImageArchive(f)
	if err != nil {
		return err
	}
	defer func() {
		_ = archive.Close()
	}()
	if _, err := e.client.Import(e.ctx(), archive); err != nil {
		return fmt.Errorf("import pause image: %w", err)
	}
	log.Debugf("Pause image imported from %s", pausePath)
	return nil
}

// recoverState populates the in-memory state store from labels on all existing containers.
func (e *Engine) recoverState() {
	cs, err := e.client.Containers(e.ctx())
	if err != nil {
		log.Warnf("recoverState: failed to list containers: %v", err)
		return
	}
	for _, c := range cs {
		if isSandboxContainer(e.ctx(), c) {
			continue
		}
		info, err := c.Info(e.ctx())
		if err != nil {
			continue
		}
		if info.Labels == nil {
			continue
		}
		st := stateFromLabels(info.Labels)
		if st.ip != "" || st.netnsPath != "" || st.sandboxID != "" || st.startedAt != 0 {
			e.store.set(c.ID(), st)
		}
		rec := dnsRecordFromLabels(info.Labels, st.ip)
		if rec.UUID == "" {
			continue
		}
		if task, err := c.Task(e.ctx(), nil); err == nil {
			if status, err := task.Status(e.ctx()); err == nil {
				rec.Active = status.Status == client.Running
			}
		}
		rec.StartedAt = st.startedAt
		e.ensureDNSResolver()
		e.dnsResolver.UpsertWorkload(rec)
	}
	log.Debugf("recoverState: recovered state for %d containers", len(cs))
}

// Close releases the containerd and CRI clients.
func (e *Engine) Close() error {
	if e.runtimeEventsCancel != nil {
		e.runtimeEventsCancel()
		e.runtimeEventsCancel = nil
	}
	if e.dnsResolver != nil {
		_ = e.dnsResolver.Stop()
	}
	if e.criClient != nil {
		_ = e.criClient.Close()
	}
	if e.client != nil {
		return e.client.Close()
	}
	return nil
}

// ctx returns a context with the iofog containerd namespace set.
func (e *Engine) ctx() context.Context {
	return namespaces.WithNamespace(context.Background(), constants.EdgeletContainerdNamespace)
}

// isSandboxContainer returns true if the container is the CRI pod sandbox (pause).
// We must exclude sandbox containers from GetContainer/GetAllContainers so we never
// return or operate on the pause container instead of the actual microservice.
func isSandboxContainer(ctx context.Context, c client.Container) bool {
	info, err := c.Info(ctx)
	if err != nil {
		return false
	}
	// Sandbox uses portainer/pause image; main containers use microservice images.
	return info.Image != "" && (info.Image == constants.EdgeletSandboxImage ||
		strings.Contains(info.Image, "portainer/pause"))
}

// --- Container lifecycle ---

func (e *Engine) GetContainer(microserviceUUID string) (*engine.Container, error) {
	ctx := e.ctx()
	cs, err := e.client.Containers(ctx, fmt.Sprintf(`labels."%s"=="%s"`, workloadmeta.LabelMicroserviceUID, microserviceUUID))
	if err != nil {
		return nil, err
	}
	if len(cs) == 0 {
		return nil, nil
	}
	// Exclude sandbox (pause) container — return the main microservice container.
	for _, c := range cs {
		if isSandboxContainer(ctx, c) {
			continue
		}
		return containerFromContainerd(ctx, c)
	}
	return nil, nil
}

func (e *Engine) GetContainerByID(containerID string) (*engine.Container, error) {
	ctx := e.ctx()
	c, err := e.client.LoadContainer(ctx, containerID)
	if err != nil {
		return nil, nil // Not found
	}
	if isSandboxContainer(ctx, c) {
		return nil, nil
	}
	return containerFromContainerd(ctx, c)
}

func (e *Engine) GetContainerSandboxID(containerID string) (string, error) {
	containerID = strings.TrimSpace(containerID)
	if containerID == "" {
		return "", nil
	}
	if st, ok := e.store.get(containerID); ok && strings.TrimSpace(st.sandboxID) != "" {
		return st.sandboxID, nil
	}
	if e.client == nil {
		return "", nil
	}
	ctx := e.ctx()
	c, err := e.client.LoadContainer(ctx, containerID)
	if err != nil {
		return "", nil
	}
	info, err := c.Info(ctx)
	if err != nil {
		return "", nil
	}
	st := stateFromLabels(info.Labels)
	if st == nil || strings.TrimSpace(st.sandboxID) == "" {
		return "", nil
	}
	e.store.set(containerID, st)
	return st.sandboxID, nil
}

func (e *Engine) GetRunningContainers() ([]engine.Container, error) {
	ctx := e.ctx()
	all, err := e.client.Containers(ctx)
	if err != nil {
		return nil, err
	}
	var out []engine.Container
	for _, c := range all {
		if isSandboxContainer(ctx, c) {
			continue
		}
		task, err := c.Task(ctx, nil)
		if err != nil {
			continue // No task → not running.
		}
		st, err := task.Status(ctx)
		if err != nil || st.Status != client.Running {
			continue
		}
		ec, err := containerFromContainerd(ctx, c)
		if err != nil {
			log.Warnf("Skipping container %s: %v", c.ID(), err)
			continue
		}
		out = append(out, *ec)
	}
	return out, nil
}

func (e *Engine) GetAllContainers() ([]engine.Container, error) {
	ctx := e.ctx()
	cs, err := e.client.Containers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]engine.Container, 0, len(cs))
	for _, c := range cs {
		if isSandboxContainer(ctx, c) {
			continue
		}
		ec, err := containerFromContainerd(ctx, c)
		if err != nil {
			log.Warnf("Skipping container %s: %v", c.ID(), err)
			continue
		}
		out = append(out, *ec)
	}
	return out, nil
}

// CreateContainer creates the container via CRI: RunPodSandbox (CNI managed by containerd)
// then CreateContainer. It does NOT start the container — that is deferred to StartContainer.
func (e *Engine) CreateContainer(ms *models.Microservice, hostname string) (string, error) {
	createStart := time.Now()
	ctx := e.ctx()
	cfg := config.GetInstance()
	containerName := utils.EdgeletDockerContainerNamePrefix + ms.MicroserviceUUID
	networkSelection := cri.SelectCNINetworkForMicroservice(ms)

	runtimeHandler, err := cri.GetRuntimeHandler(ms)
	if err != nil {
		return "", fmt.Errorf("resolve runtime handler for %s: %w", containerName, err)
	}

	// Ensure image exists.
	if _, err := e.client.GetImage(ctx, ms.ImageName); err != nil {
		return "", fmt.Errorf("image %s not found (pull first): %w", ms.ImageName, err)
	}

	envVars := buildIofogContainerEnv(ms, cfg)

	diskDir := ""
	if cfg != nil {
		diskDir = strings.TrimSpace(cfg.DiskDirectory)
	}
	if err := cri.PrepareHostTmpfs(diskDir, ms); err != nil {
		return "", fmt.Errorf("prepare tmpfs for %s: %w", containerName, err)
	}
	if models.NeedsReadOnlyRootTmpfsWarning(ms.ReadOnlyRootFilesystem, ms.Tmpfs) {
		log.Warn(models.ReadOnlyRootWithoutTmpfsWarning)
	}

	// Build /etc/hosts before RunPodSandbox (needed for ContainerConfig mounts).
	hostsFilePath := ""
	resolvFilePath := ""
	if !ms.HostNetworkMode {
		hostsFilePath = filepath.Join(hostsDir, containerName)
		gatewayIP, gwErr := dnsresolver.GatewayIPForScope(dnsresolver.ScopeManaged)
		if gwErr == nil && gatewayIP != "" {
			resolvFilePath = filepath.Join(resolvDir, containerName+".conf")
			if err := buildResolvConfFile(resolvFilePath, gatewayIP); err != nil {
				e.emitEngineWarn(runtimeops.EventEngineCRIContainerCreated, "", "", ms.ImageName, runtimeops.ReasonCreateFailed, "build resolv.conf failed", createStart, err, map[string]any{"step": "buildResolvConf"})
				resolvFilePath = ""
			}
		}
		if err := buildHostsFile(hostsFilePath, ms.ExtraHosts); err != nil {
			e.emitEngineWarn(runtimeops.EventEngineCRIContainerCreated, "", "", ms.ImageName, runtimeops.ReasonCreateFailed, "build hosts file failed", createStart, err, map[string]any{"step": "buildHostsFile"})
			hostsFilePath = ""
		}
	}

	logDirectory, logPath := cri.LogPathsForCRI(e.logDir, ms.MicroserviceUUID)
	podConfig := cri.PodSandboxConfigFromMicroservice(ms, hostname, logDirectory, cfg.IOFogUUID)

	sandboxStart := time.Now()
	sandboxID, err := e.runPodSandboxWithRecovery(ctx, podConfig, runtimeHandler, ms.MicroserviceUUID)
	if err != nil {
		if !errors.Is(err, engine.ErrReconcilePaused) {
			e.emitCRISubstep(runtimeops.EventEngineCRISandboxCreated, "criRunPodSandbox", "", "", ms.ImageName, runtimeops.ReasonCreateFailed, sandboxStart, err)
		}
		return "", fmt.Errorf("RunPodSandbox for %s: %w", containerName, err)
	}
	e.emitEngineInfo(runtimeops.EventEngineCRISandboxCreated, "", sandboxID, ms.ImageName, "pod sandbox created", sandboxStart, map[string]any{
		"msUUID":         ms.MicroserviceUUID,
		"runtimeHandler": runtimeHandler,
		"scope":          networkSelection.Scope,
		"cniNetwork":     networkSelection.NetworkName,
		"hostNetwork":    networkSelection.HostNetwork,
	})
	e.emitCRISubstep(runtimeops.EventEngineCRISandboxCreated, "criRunPodSandbox", "", sandboxID, ms.ImageName, runtimeops.ReasonCreateFailed, sandboxStart, nil)

	// Get pod IP from sandbox status (for bridge network).
	ipAddr := ""
	if !ms.HostNetworkMode {
		if status, err := e.criClient.PodSandboxStatus(ctx, sandboxID); err == nil && status.Status != nil && status.Status.Network != nil {
			ipAddr = status.Status.Network.Ip
		}
	}

	containerConfig, err := cri.ContainerConfigFromMicroservice(ms, hostname, envVars, logPath, hostsFilePath, resolvFilePath, sandboxID, cfg.IOFogUUID)
	if err != nil {
		_ = e.criClient.StopPodSandbox(ctx, sandboxID)
		_ = e.criClient.RemovePodSandbox(ctx, sandboxID)
		return "", fmt.Errorf("build container config for %s: %w", containerName, err)
	}

	// Operational labels (runtime state not covered by workloadmeta BuildLabels).
	portsJSON, _ := json.Marshal(ms.PortMappings)
	containerConfig.Labels[labelPorts] = string(portsJSON)
	containerConfig.Labels[labelLogSize] = labelInt64(ms.LogSize)
	containerConfig.Labels[labelIP] = ipAddr
	if ms.Healthcheck != nil {
		if hcJSON, err := json.Marshal(ms.Healthcheck); err == nil {
			containerConfig.Labels[labelHealthcheck] = string(hcJSON)
		}
	}

	containerStart := time.Now()
	containerID, err := e.criClient.CreateContainer(ctx, sandboxID, containerConfig, podConfig)
	if err != nil {
		e.emitCRISubstep(runtimeops.EventEngineCRIContainerCreated, "criCreateContainer", "", sandboxID, ms.ImageName, runtimeops.ReasonCreateFailed, containerStart, err)
		_ = e.criClient.StopPodSandbox(ctx, sandboxID)
		_ = e.criClient.RemovePodSandbox(ctx, sandboxID)
		return "", fmt.Errorf("CreateContainer for %s: %w", containerName, err)
	}
	e.emitCRISubstep(runtimeops.EventEngineCRIContainerCreated, "criCreateContainer", containerID, sandboxID, ms.ImageName, runtimeops.ReasonCreateFailed, containerStart, nil)

	if err := e.applyOCIRlimits(ctx, containerID, ms); err != nil {
		_ = e.criClient.StopContainer(ctx, containerID, 0)
		_ = e.criClient.RemoveContainer(ctx, containerID)
		_ = e.criClient.StopPodSandbox(ctx, sandboxID)
		_ = e.criClient.RemovePodSandbox(ctx, sandboxID)
		return "", fmt.Errorf("apply ulimits for %s: %w", containerName, err)
	}

	e.store.set(containerID, &containerState{
		sandboxID: sandboxID,
		ip:        ipAddr,
	})
	e.ensureDNSResolver()
	e.dnsResolver.UpsertWorkload(dnsresolver.WorkloadRecord{
		UUID:         ms.MicroserviceUUID,
		Application:  ms.ApplicationName,
		Name:         ms.MicroserviceName,
		Scope:        dnsScopeFromMicroservice(ms),
		IP:           ipAddr,
		HostNetwork:  ms.HostNetworkMode,
		IsRouter:     ms.IsRouter,
		IsNats:       ms.IsNats,
		IsController: ms.IsController,
		Active:       false,
	})
	e.emitEngineSuccess(runtimeops.EventEngineCRIContainerCreated, containerID, sandboxID, ms.ImageName, "CRI create complete", createStart, map[string]any{
		"step":           "criCreateComplete",
		"msUUID":         ms.MicroserviceUUID,
		"ip":             ipAddr,
		"runtimeHandler": runtimeHandler,
		"scope":          networkSelection.Scope,
		"cniNetwork":     networkSelection.NetworkName,
		"hostNetwork":    networkSelection.HostNetwork,
	})
	return containerID, nil
}

func (e *Engine) applyOCIRlimits(ctx context.Context, containerID string, ms *models.Microservice) error {
	if ms == nil || len(ms.Ulimits) == 0 {
		return nil
	}
	c, err := e.client.LoadContainer(ctx, containerID)
	if err != nil {
		return err
	}
	spec, err := c.Spec(ctx)
	if err != nil {
		return err
	}
	cri.ApplyOCIRlimits(spec, ms)
	return c.Update(ctx, func(_ context.Context, _ *client.Client, cntr *containers.Container) error {
		anySpec, err := tuypeurl.MarshalAny(spec)
		if err != nil {
			return err
		}
		cntr.Spec = anySpec
		return nil
	})
}

// StartContainer starts the container via CRI. CRI manages logs; we only record start time.
func (e *Engine) StartContainer(containerID string) error {
	start := time.Now()
	ctx := e.ctx()
	sandboxID := e.sandboxIDFor(containerID)
	if err := e.criClient.StartContainer(ctx, containerID); err != nil {
		reason, exitCode, message := e.readCRIContainerFailure(ctx, containerID)
		if engine.IsNonRestartableCRIReason(reason) || strings.Contains(strings.ToUpper(err.Error()), engine.CRIReasonContainerExited) {
			if strings.TrimSpace(reason) == "" {
				reason = engine.CRIReasonContainerExited
			}
			nr := &engine.NonRestartableContainerError{
				ContainerID: containerID,
				Reason:      strings.TrimSpace(reason),
				ExitCode:    exitCode,
				Message:     fallbackMessage(message, err.Error()),
			}
			e.emitEngineWarn(runtimeops.EventEngineContainerStart, containerID, sandboxID, "", runtimeops.ReasonNonRestartableExit, "non-restartable terminal state on start", start, err, map[string]any{
				"criReason":  nr.Reason,
				"exitCode":   nr.ExitCode,
				"criMessage": nr.Message,
			})
			return nr
		}
		e.emitEngineWarn(runtimeops.EventEngineContainerStart, containerID, sandboxID, "", runtimeops.ReasonStartFailed, "transient start failure", start, err, map[string]any{
			"criReason":  reasonOrUnknown(reason),
			"exitCode":   exitCode,
			"criMessage": strings.TrimSpace(message),
		})
		return fmt.Errorf("StartContainer %s: %w", containerID, err)
	}
	c, err := e.client.LoadContainer(ctx, containerID)
	if err != nil {
		return nil // Container started; recording start time is best-effort.
	}
	e.recordStartTime(ctx, c, containerID)
	if msUUID := e.microserviceUUIDForContainer(ctx, containerID); msUUID != "" {
		e.ensureDNSResolver()
		e.dnsResolver.SetWorkloadActive(msUUID, true, time.Now().UnixMilli())
	}
	e.emitEngineSuccess(runtimeops.EventEngineContainerStart, containerID, sandboxID, "", "container started", start, nil)
	return nil
}

// recordStartTime stores the current time as startedAt in the state map and container label.
func (e *Engine) recordStartTime(ctx context.Context, c client.Container, containerID string) {
	now := time.Now().UnixMilli()
	if st, ok := e.store.get(containerID); ok {
		st.startedAt = now
	} else {
		e.store.set(containerID, &containerState{startedAt: now})
	}
	_, _ = c.SetLabels(ctx, map[string]string{labelStartedAt: labelInt64(now)})
}

// StopContainer stops the container via CRI (SIGTERM, then SIGKILL after timeout).
func (e *Engine) StopContainer(containerID string) error {
	start := time.Now()
	ctx := e.ctx()
	sandboxID := e.sandboxIDFor(containerID)
	if err := e.criClient.StopContainer(ctx, containerID, 10); err != nil {
		e.emitEngineWarn(runtimeops.EventEngineContainerStop, containerID, sandboxID, "", runtimeops.ReasonStopFailed, "container stop failed", start, err, nil)
		return fmt.Errorf("StopContainer %s: %w", containerID, err)
	}
	if msUUID := e.microserviceUUIDForContainer(ctx, containerID); msUUID != "" {
		e.ensureDNSResolver()
		e.dnsResolver.SetWorkloadActive(msUUID, false, 0)
	}
	e.emitEngineSuccess(runtimeops.EventEngineContainerStop, containerID, sandboxID, "", "container stopped", start, nil)
	return nil
}

// KillContainer forcefully terminates the container task.
func (e *Engine) KillContainer(containerID string) error {
	start := time.Now()
	ctx := e.ctx()
	sandboxID := e.sandboxIDFor(containerID)
	if err := e.criClient.StopContainer(ctx, containerID, 0); err != nil {
		e.emitEngineWarn(runtimeops.EventEngineContainerStop, containerID, sandboxID, "", runtimeops.ReasonStopFailed, "container kill failed", start, err, map[string]any{"force": true})
		return fmt.Errorf("KillContainer %s: %w", containerID, err)
	}
	if msUUID := e.microserviceUUIDForContainer(ctx, containerID); msUUID != "" {
		e.ensureDNSResolver()
		e.dnsResolver.SetWorkloadActive(msUUID, false, 0)
	}
	e.emitEngineSuccess(runtimeops.EventEngineContainerStop, containerID, sandboxID, "", "container killed", start, map[string]any{"force": true})
	return nil
}

// RemoveContainer stops and removes the container via CRI, then tears down the pod sandbox.
// CRI handles CNI teardown in the correct order (StopPodSandbox triggers CNI DEL).
func (e *Engine) RemoveContainer(containerID string, _ bool) error {
	removeStart := time.Now()
	ctx := e.ctx()
	sandboxIDStr := ""
	msUUID := ""
	if c, err := e.client.LoadContainer(ctx, containerID); err == nil {
		if info, err := c.Info(ctx); err == nil && info.Labels != nil {
			msUUID = workloadmeta.MicroserviceUIDFromLabels(info.Labels)
		}
	}
	if st, ok := e.store.get(containerID); ok {
		sandboxIDStr = st.sandboxID
		if msUUID == "" {
			msUUID = strings.TrimPrefix(containerID, utils.EdgeletDockerContainerNamePrefix)
		}
	}
	if msUUID == "" {
		msUUID = strings.TrimPrefix(containerID, utils.EdgeletDockerContainerNamePrefix)
	}

	// CRI teardown order: StopContainer, RemoveContainer, StopPodSandbox, RemovePodSandbox.
	stopStart := time.Now()
	stopErr := e.criClient.StopContainer(ctx, containerID, 10)

	removeStepStart := time.Now()
	removeErr := e.criClient.RemoveContainer(ctx, containerID)
	if removeErr != nil && !containerGone(removeErr) {
		e.emitCRITeardownStep("criStopContainer", containerID, sandboxIDStr, stopStart, stopErr, false)
		e.emitCRITeardownStep("criRemoveContainer", containerID, sandboxIDStr, removeStepStart, removeErr, false)
		// Fallback path for non-CRI/manual containers (e.g. created via ctr run).
		nativeStart := time.Now()
		if err := e.removeContainerNative(ctx, containerID); err != nil && !containerGone(err) {
			e.emitEngineWarn(runtimeops.EventEngineContainerRemove, containerID, sandboxIDStr, "", runtimeops.ReasonRemoveFailed, "native fallback remove failed", removeStart, err, map[string]any{"step": "nativeFallback"})
			return fmt.Errorf("remove container %s (CRI: %w, native fallback: %w)", containerID, removeErr, err)
		}
		e.emitCRITeardownStep("nativeRemoveContainer", containerID, sandboxIDStr, nativeStart, nil, false)
	} else {
		e.emitCRITeardownStep("criStopContainer", containerID, sandboxIDStr, stopStart, stopErr, stopErr != nil)
		e.emitCRITeardownStep("criRemoveContainer", containerID, sandboxIDStr, removeStepStart, nil, false)
	}

	// Some manually created containers (ctr run) may survive CRI remove without
	// returning an error. If the container still exists, force native removal.
	if _, err := e.client.LoadContainer(ctx, containerID); err == nil {
		if err := e.removeContainerNative(ctx, containerID); err != nil {
			return fmt.Errorf("remove container %s (post-CRI native fallback: %w)", containerID, err)
		}
	} else if !errdefs.IsNotFound(err) {
		return fmt.Errorf("post-CRI verification for %s: %w", containerID, err)
	}

	if sandboxIDStr != "" {
		sandboxStopStart := time.Now()
		stopSandboxErr := e.criClient.StopPodSandbox(ctx, sandboxIDStr)
		if stopSandboxErr != nil && errdefs.IsNotFound(stopSandboxErr) {
			stopSandboxErr = nil
		}
		e.emitCRITeardownStep("criStopPodSandbox", containerID, sandboxIDStr, sandboxStopStart, stopSandboxErr, false)

		sandboxRemoveStart := time.Now()
		removeSandboxErr := e.criClient.RemovePodSandbox(ctx, sandboxIDStr)
		if removeSandboxErr != nil && errdefs.IsNotFound(removeSandboxErr) {
			removeSandboxErr = nil
		}
		e.emitCRITeardownStep("criRemovePodSandbox", containerID, sandboxIDStr, sandboxRemoveStart, removeSandboxErr, false)
	}

	if _, err := e.client.LoadContainer(ctx, containerID); err == nil {
		return fmt.Errorf("container %s still exists after removal attempt", containerID)
	} else if !errdefs.IsNotFound(err) {
		return fmt.Errorf("post-remove verification for %s: %w", containerID, err)
	}

	// Remove per-container hosts file (path uses container name, not CRI-generated ID).
	hostsFile := filepath.Join(hostsDir, utils.EdgeletDockerContainerNamePrefix+msUUID)
	_ = os.Remove(hostsFile)
	resolvFile := filepath.Join(resolvDir, utils.EdgeletDockerContainerNamePrefix+msUUID+".conf")
	_ = os.Remove(resolvFile)
	diskDir := ""
	if cfg := config.GetInstance(); cfg != nil {
		diskDir = strings.TrimSpace(cfg.DiskDirectory)
	}
	cri.ReleaseHostTmpfs(diskDir, msUUID)

	// Remove per-container log directory.
	logPath := filepath.Join(e.logDir, msUUID)
	_ = os.RemoveAll(logPath)

	e.store.delete(containerID)
	e.ensureDNSResolver()
	e.dnsResolver.RemoveWorkload(msUUID)
	e.emitEngineSuccess(runtimeops.EventEngineContainerRemove, containerID, sandboxIDStr, "", "container remove complete", removeStart, map[string]any{
		"msUUID": msUUID,
	})
	return nil
}

func containerGone(err error) bool {
	if err == nil {
		return true
	}
	if errdefs.IsNotFound(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "not found") ||
		strings.Contains(msg, "no such container") ||
		strings.Contains(msg, "already in removing")
}

func (e *Engine) ensureDNSResolver() {
	if e.dnsResolver == nil {
		e.dnsResolver = dnsresolver.GetInstance()
		e.dnsResolver.SetRuntimeSnapshotProvider(e.runtimeDNSSnapshot)
		if err := e.dnsResolver.Start(); err != nil {
			e.emitEngineWarn(runtimeops.EventEngineInit, "", "", "", "", "embedded DNS start failed (non-fatal)", time.Now(), err, map[string]any{"operation": "dnsStart"})
		}
	}
}

func dnsScopeFromMicroservice(ms *models.Microservice) dnsresolver.Scope {
	if ms == nil {
		return dnsresolver.ScopeManaged
	}
	scope := workloadmeta.ResolveScope(ms.ApplicationName, ms.HostNetworkMode)
	return dnsScopeFromWorkloadScope(scope)
}

func dnsScopeFromWorkloadScope(scope string) dnsresolver.Scope {
	if workloadmeta.IsLocalScope(scope) {
		return dnsresolver.ScopeLocal
	}
	return dnsresolver.ScopeManaged
}

func dnsRecordFromLabels(labels map[string]string, ip string) dnsresolver.WorkloadRecord {
	role := strings.TrimSpace(labels[workloadmeta.LabelRole])
	rec := dnsresolver.WorkloadRecord{
		UUID:         workloadmeta.MicroserviceUIDFromLabels(labels),
		Name:         strings.TrimSpace(labels[workloadmeta.LabelAppName]),
		Application:  strings.TrimSpace(labels[workloadmeta.LabelAppPartOf]),
		IP:           strings.TrimSpace(ip),
		HostNetwork:  strings.EqualFold(strings.TrimSpace(labels[workloadmeta.LabelHostNetwork]), "true"),
		IsRouter:     role == workloadmeta.RoleRouter,
		IsNats:       role == workloadmeta.RoleNats,
		IsController: role == workloadmeta.RoleController,
	}
	rec.Scope = dnsScopeFromWorkloadScope(workloadmeta.ResolveScope(rec.Application, rec.HostNetwork))
	return rec
}

func (e *Engine) runtimeDNSSnapshot(ctx context.Context) ([]dnsresolver.WorkloadRecord, error) {
	if e.client == nil {
		return nil, nil
	}

	nsCtx := namespaces.WithNamespace(ctx, constants.EdgeletContainerdNamespace)
	cs, err := e.client.Containers(nsCtx)
	if err != nil {
		return nil, fmt.Errorf("list containers for dns reconcile: %w", err)
	}

	records := make([]dnsresolver.WorkloadRecord, 0, len(cs))
	for _, c := range cs {
		if isSandboxContainer(nsCtx, c) {
			continue
		}
		info, err := c.Info(nsCtx)
		if err != nil || info.Labels == nil {
			continue
		}

		st := stateFromLabels(info.Labels)
		if cached, ok := e.store.get(c.ID()); ok && cached != nil {
			if st.ip == "" {
				st.ip = cached.ip
			}
			if st.startedAt == 0 {
				st.startedAt = cached.startedAt
			}
		}

		rec := dnsRecordFromLabels(info.Labels, st.ip)
		if rec.UUID == "" {
			continue
		}
		if task, err := c.Task(nsCtx, nil); err == nil {
			if status, err := task.Status(nsCtx); err == nil {
				rec.Active = status.Status == client.Running
			}
		}
		rec.StartedAt = st.startedAt
		records = append(records, rec)
	}

	return records, nil
}

func (e *Engine) microserviceUUIDForContainer(ctx context.Context, containerID string) string {
	c, err := e.client.LoadContainer(ctx, containerID)
	if err != nil {
		return ""
	}
	info, err := c.Info(ctx)
	if err != nil || info.Labels == nil {
		return ""
	}
	return workloadmeta.MicroserviceUIDFromLabels(info.Labels)
}

// removeContainerNative removes a container directly via containerd APIs.
// This is required for workloads that are not managed through CRI metadata.
func (e *Engine) removeContainerNative(ctx context.Context, containerID string) error {
	c, err := e.client.LoadContainer(ctx, containerID)
	if err != nil {
		if errdefs.IsNotFound(err) {
			return nil
		}
		return err
	}
	if task, err := c.Task(ctx, nil); err == nil {
		if err := task.Kill(ctx, syscall.SIGKILL); err != nil && !errdefs.IsNotFound(err) {
			e.emitEngineWarn(runtimeops.EventEngineContainerRemove, containerID, e.sandboxIDFor(containerID), "", runtimeops.ReasonRemoveFailed, "native kill task failed", time.Now(), err, map[string]any{"step": "nativeKillTask"})
		}
		if _, err := task.Delete(ctx, client.WithProcessKill); err != nil && !errdefs.IsNotFound(err) {
			e.emitEngineWarn(runtimeops.EventEngineContainerRemove, containerID, e.sandboxIDFor(containerID), "", runtimeops.ReasonRemoveFailed, "native delete task failed", time.Now(), err, map[string]any{"step": "nativeDeleteTask"})
		}
	}
	return c.Delete(ctx, client.WithSnapshotCleanup)
}

// --- Image management ---

func (e *Engine) PullImage(imageRef string, registry *models.Registry, opts *engine.PullImageOptions) error {
	pullStart := time.Now()
	ctx := e.ctx()

	var cb func(float32)
	var platform string
	if opts != nil {
		cb = opts.ProgressCallback
		platform = opts.Platform
	}
	if cb != nil {
		cb(0)
	}

	var remoteOpts []client.RemoteOpt
	resolverOpts, useResolver, err := imagePullResolverOptions(registry)
	if err != nil {
		e.emitEngineWarn(runtimeops.EventEngineImagePulled, "", "", imageRef, runtimeops.ReasonPullFailed, "image pull failed", pullStart, err, nil)
		return fmt.Errorf("pull image %s: %w", imageRef, err)
	}
	if useResolver {
		remoteOpts = append(remoteOpts, client.WithResolver(dockerresolver.NewResolver(resolverOpts)))
	}
	if platform != "" {
		remoteOpts = append(remoteOpts, client.WithPlatform(platform))
	}

	stopProgress := make(chan struct{})
	if cb != nil {
		go e.reportPullProgress(ctx, cb, stopProgress)
		defer close(stopProgress)
	}

	img, err := e.client.Pull(ctx, imageRef, append(remoteOpts, client.WithPullUnpack)...)
	if err != nil {
		e.emitEngineWarn(runtimeops.EventEngineImagePulled, "", "", imageRef, runtimeops.ReasonPullFailed, "image pull failed", pullStart, err, nil)
		return fmt.Errorf("pull image %s: %w", imageRef, err)
	}
	e.emitEngineSuccess(runtimeops.EventEngineImagePulled, "", "", imageRef, "image pulled", pullStart, map[string]any{
		"imageId": img.Name(),
	})
	if cb != nil {
		cb(100)
	}
	return nil
}

func (e *Engine) FindLocalImage(imageRef, registryURL string, fromCache bool) (bool, error) {
	_, aliases := imageref.Resolve(imageRef, strings.TrimSpace(registryURL), fromCache)
	imgs, err := e.client.ListImages(e.ctx())
	if err != nil {
		return false, err
	}
	matches := func(imgName, query string) bool {
		return imgName == query || strings.HasPrefix(imgName, query+":")
	}
	for _, img := range imgs {
		for _, q := range aliases {
			if matches(img.Name(), q) {
				return true, nil
			}
		}
	}
	return false, nil
}

func (e *Engine) reportPullProgress(ctx context.Context, cb func(float32), stop <-chan struct{}) {
	ticker := time.NewTicker(300 * time.Millisecond)
	defer ticker.Stop()
	var last float32
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			statuses, err := e.client.ContentStore().ListStatuses(ctx)
			if err != nil {
				continue
			}
			next := computePullProgress(statuses, last)
			if next > last {
				last = next
				cb(next)
			}
		}
	}
}

func computePullProgress(statuses []content.Status, prev float32) float32 {
	var sumOffset int64
	var sumTotal int64
	for _, s := range statuses {
		if s.Total <= 0 {
			continue
		}
		sumTotal += s.Total
		off := s.Offset
		if off > s.Total {
			off = s.Total
		}
		sumOffset += off
	}
	if sumTotal <= 0 {
		return prev
	}
	pct := float32(sumOffset) * 100 / float32(sumTotal)
	if pct < prev {
		pct = prev
	}
	if pct > 99 {
		pct = 99
	}
	return pct
}

func (e *Engine) RemoveImage(imageRef string) error {
	return e.client.ImageService().Delete(e.ctx(), imageRef, images.SynchronousDelete())
}

// PruneImages deletes images that are not referenced by any existing container.
func (e *Engine) PruneImages() error {
	pruneStart := time.Now()
	ctx := e.ctx()

	// Build set of images currently referenced by containers.
	containers, err := e.client.Containers(ctx)
	if err != nil {
		return err
	}
	inUse := make(map[string]bool, len(containers))
	for _, c := range containers {
		if info, err := c.Info(ctx); err == nil {
			inUse[info.Image] = true
		}
	}

	imgs, err := e.client.ListImages(ctx)
	if err != nil {
		return err
	}
	is := e.client.ImageService()
	deletedCount := 0
	for _, img := range imgs {
		if inUse[img.Name()] {
			continue
		}
		if err := is.Delete(ctx, img.Name(), images.SynchronousDelete()); err != nil {
			e.emitEngineWarn(runtimeops.EventEnginePrune, "", "", img.Name(), runtimeops.ReasonRemoveFailed, "prune image failed", pruneStart, err, map[string]any{"operation": "pruneImages"})
			continue
		}
		deletedCount++
	}
	e.emitEnginePruneSummary("pruneImages", pruneStart, map[string]any{"deletedCount": deletedCount})
	return nil
}

// ListImages returns all images available in the iofog containerd namespace.
func (e *Engine) ListImages(_ context.Context) ([]engine.ImageInfo, error) {
	ctx := e.ctx()
	imgs, err := e.client.ListImages(ctx)
	if err != nil {
		return nil, err
	}

	inUseCounts, inUseErr := e.listImageInUseCounts(ctx)
	inUseKnown := inUseErr == nil

	byDigest := groupImagesByDigest(imgs)
	result := make([]engine.ImageInfo, 0, len(byDigest))
	for digest, group := range byDigest {
		result = append(result, imageInfoFromDigestGroup(ctx, e.client, digest, group, inUseCounts, inUseKnown))
	}
	sortImageInfos(result)
	return result, nil
}

func (e *Engine) LoadImageFromPath(_ context.Context, archivePath string) ([]engine.LoadedImage, error) {
	f, err := os.Open(archivePath) // #nosec G304 daemon validates path before calling engine
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = f.Close()
	}()
	archive, err := decompressImageArchive(f)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = archive.Close()
	}()
	imported, err := e.client.Import(e.ctx(), archive)
	if err != nil {
		return nil, err
	}
	out := make([]engine.LoadedImage, 0, len(imported))
	for _, img := range imported {
		id := img.Target.Digest.String()
		out = append(out, engine.LoadedImage{
			Name: img.Name,
			ID:   id,
		})
	}
	return out, nil
}

// DeleteImage removes an image from the iofog containerd namespace by name or digest.
func (e *Engine) DeleteImage(_ context.Context, nameOrID string) error {
	ctx := e.ctx()
	return e.client.ImageService().Delete(ctx, nameOrID, images.SynchronousDelete())
}

// PruneDangling removes images with no active containers referencing them and no tags,
// docker system prune --filter dangling=true semantics.
func (e *Engine) PruneDangling(_ context.Context) (*engine.ImagePruneReport, error) {
	pruneStart := time.Now()
	ctx := e.ctx()

	containers, err := e.client.Containers(ctx)
	if err != nil {
		return nil, err
	}
	inUse := make(map[string]bool, len(containers))
	for _, c := range containers {
		if info, err := c.Info(ctx); err == nil {
			inUse[info.Image] = true
		}
	}

	imgs, err := e.client.ListImages(ctx)
	if err != nil {
		return nil, err
	}
	is := e.client.ImageService()
	deleted := make([]string, 0)
	var reclaimed int64
	for _, img := range imgs {
		name := img.Name()
		// "Dangling" in containerd terms: not referenced by any container AND the name
		// looks like a digest-only reference (no human-readable tag).
		if inUse[name] {
			continue
		}
		// Skip images that have a proper name:tag (non-dangling).
		if !strings.HasPrefix(name, "sha256:") && strings.Contains(name, ":") {
			continue
		}
		_, diskUsage := imageDiskUsage(ctx, e.client, img)
		reclaimed += diskUsage
		if err := is.Delete(ctx, name, images.SynchronousDelete()); err != nil {
			e.emitEngineWarn(runtimeops.EventEnginePrune, "", "", name, runtimeops.ReasonRemoveFailed, "prune dangling image failed", pruneStart, err, map[string]any{"operation": "pruneDangling"})
			continue
		}
		deleted = append(deleted, name)
	}
	report := &engine.ImagePruneReport{
		Deleted:             deleted,
		DeletedCount:        len(deleted),
		SpaceReclaimedBytes: reclaimed,
	}
	e.emitEnginePruneSummary("pruneDangling", pruneStart, map[string]any{
		"deletedCount":        report.DeletedCount,
		"spaceReclaimedBytes": report.SpaceReclaimedBytes,
	})
	return report, nil
}

func (e *Engine) PruneContainers(_ context.Context) (*engine.ContainerPruneReport, error) {
	pruneStart := time.Now()
	ctx := e.ctx()
	containers, err := e.client.Containers(ctx)
	if err != nil {
		return nil, err
	}

	deleted := make([]string, 0)
	for _, c := range containers {
		id := c.ID()
		task, taskErr := c.Task(ctx, nil)
		if taskErr == nil {
			st, stErr := task.Status(ctx)
			if stErr == nil && st.Status == client.Running {
				continue
			}
			if _, delErr := task.Delete(ctx, client.WithProcessKill); delErr != nil && !errdefs.IsNotFound(delErr) {
				e.emitEngineWarn(runtimeops.EventEnginePrune, id, "", "", runtimeops.ReasonRemoveFailed, "prune container task delete failed", pruneStart, delErr, map[string]any{"operation": "pruneContainers"})
				continue
			}
		}
		if err := c.Delete(ctx, client.WithSnapshotCleanup); err != nil && !errdefs.IsNotFound(err) {
			e.emitEngineWarn(runtimeops.EventEnginePrune, id, "", "", runtimeops.ReasonRemoveFailed, "prune container delete failed", pruneStart, err, map[string]any{"operation": "pruneContainers"})
			continue
		}
		deleted = append(deleted, id)
	}
	report := &engine.ContainerPruneReport{
		Deleted:      deleted,
		DeletedCount: len(deleted),
	}
	e.emitEnginePruneSummary("pruneContainers", pruneStart, map[string]any{"deletedCount": report.DeletedCount})
	return report, nil
}

// PruneVolumes removes rebuildable staging under volumes/microservices.
// Persistent VOLUME data under volumes/data and volumes/shared is not deleted.
func (e *Engine) PruneVolumes(_ context.Context) (*engine.VolumePruneReport, error) {
	pruneStart := time.Now()
	ctx := e.ctx()
	containers, err := e.client.Containers(ctx)
	if err != nil {
		return nil, err
	}
	keepUUIDs := make(map[string]struct{}, len(containers))
	for _, c := range containers {
		info, infoErr := c.Info(ctx)
		if infoErr != nil {
			continue
		}
		if uuid := workloadmeta.MicroserviceUIDFromLabels(info.Labels); strings.TrimSpace(uuid) != "" {
			keepUUIDs[uuid] = struct{}{}
		}
	}

	baseVolumesDir := filepath.Join(config.GetInstance().DiskDirectory, "volumes")
	targets, err := rebuildableVolumeStagingTargets(baseVolumesDir, keepUUIDs)
	if err != nil {
		return nil, err
	}
	deleted := make([]string, 0, len(targets))
	for _, target := range targets {
		if err := os.RemoveAll(target); err != nil {
			e.emitEngineWarn(runtimeops.EventEnginePrune, "", "", "", runtimeops.ReasonRemoveFailed, "prune volume path failed", pruneStart, err, map[string]any{"operation": "pruneVolumes"})
			continue
		}
		deleted = append(deleted, target)
	}

	report := &engine.VolumePruneReport{
		Deleted:      deleted,
		DeletedCount: len(deleted),
	}
	e.emitEnginePruneSummary("pruneVolumes", pruneStart, map[string]any{"deletedCount": report.DeletedCount})
	return report, nil
}

func (e *Engine) RemoveNamedVolume(_ context.Context, name string) error {
	return discardNamedVolume(name)
}

// --- Inspection / stats ---

func (e *Engine) GetContainerStatus(containerID, _ string) (*models.MicroserviceStatus, error) {
	ctx := e.ctx()
	c, err := e.client.LoadContainer(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("load container %s: %w", containerID, err)
	}

	status := models.NewMicroserviceStatus()
	status.ContainerID = containerID
	if sandboxID, _ := e.GetContainerSandboxID(containerID); sandboxID != "" {
		status.PodID = sandboxID
	}

	task, err := c.Task(ctx, nil)
	if err != nil {
		// Container created but not yet started — report Created, not Exiting
		status.Status = models.MicroserviceStateCreated
		if reason, exitCode, message := e.readCRIContainerFailure(ctx, containerID); reason != "" || message != "" || exitCode != 0 {
			enriched := fmt.Sprintf("CRI reason=%s exitCode=%d message=%s", reasonOrUnknown(reason), exitCode, strings.TrimSpace(message))
			status.ErrorMessage = &enriched
		}
		return status, nil
	}

	tStatus, err := task.Status(ctx)
	if err != nil {
		status.Status = models.MicroserviceStateUnknown
		return status, nil
	}

	switch tStatus.Status {
	case client.Running:
		status.Status = models.MicroserviceStateRunning
	case client.Stopped:
		status.Status = models.MicroserviceStateExiting
	case client.Created:
		status.Status = models.MicroserviceStateCreated
	default:
		status.Status = models.MicroserviceStateUnknown
	}

	if reason, exitCode, message := e.readCRIContainerFailure(ctx, containerID); reason != "" || message != "" || exitCode != 0 {
		enriched := fmt.Sprintf("CRI reason=%s exitCode=%d message=%s", reasonOrUnknown(reason), exitCode, strings.TrimSpace(message))
		status.ErrorMessage = &enriched
	}

	// Wire StartTime and IPAddress for status reporting.
	if startTime, err := e.GetContainerStartedAt(containerID); err == nil && startTime > 0 {
		status.StartTime = startTime
	}
	if ip, err := e.GetContainerIPAddress(containerID); err == nil && ip != "" {
		status.IPAddress = &ip
	}

	return status, nil
}

func (e *Engine) readCRIContainerFailure(ctx context.Context, containerID string) (reason string, exitCode int32, message string) {
	resp, err := e.criClient.ContainerStatus(ctx, containerID)
	if err != nil || resp == nil || resp.Status == nil {
		return "", 0, ""
	}
	return criStatusDetails(resp.Status)
}

func criStatusDetails(st *runtimeapi.ContainerStatus) (reason string, exitCode int32, message string) {
	if st == nil {
		return "", 0, ""
	}
	return strings.TrimSpace(st.Reason), st.ExitCode, strings.TrimSpace(st.Message)
}

func reasonOrUnknown(reason string) string {
	trimmed := strings.TrimSpace(reason)
	if trimmed == "" {
		return "UNKNOWN"
	}
	return trimmed
}

func fallbackMessage(primary, fallback string) string {
	primary = strings.TrimSpace(primary)
	if primary != "" {
		return primary
	}
	return strings.TrimSpace(fallback)
}

// GetContainerStats returns live CPU and memory usage for the container.
// It uses a two-sample approach for CPU percentage: the first call returns 0%
// CPU while storing the baseline; subsequent calls compute the delta.
func (e *Engine) GetContainerStats(containerID string) (*engine.ContainerStats, error) {
	ctx := e.ctx()
	c, err := e.client.LoadContainer(ctx, containerID)
	if err != nil {
		return nil, fmt.Errorf("load container %s: %w", containerID, err)
	}
	task, err := c.Task(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("get task for %s: %w", containerID, err)
	}
	statsCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	metric, err := task.Metrics(statsCtx)
	if err != nil {
		return nil, fmt.Errorf("get metrics for %s: %w", containerID, err)
	}

	stats := &engine.ContainerStats{}
	now := time.Now()

	switch {
	case tuypeurl.Is(metric.Data, (*v2stats.Metrics)(nil)):
		var m v2stats.Metrics
		if err := tuypeurl.UnmarshalTo(metric.Data, &m); err != nil {
			return stats, nil
		}
		if m.Memory != nil {
			stats.MemoryUsage = clampUint64ToInt64(m.Memory.Usage)
		}
		if m.CPU != nil {
			stats.CPUUsage = e.cpuPercent(containerID, clampUint64ToInt64(m.CPU.UsageUsec), now)
		}

	case tuypeurl.Is(metric.Data, (*v1stats.Metrics)(nil)):
		var m v1stats.Metrics
		if err := tuypeurl.UnmarshalTo(metric.Data, &m); err != nil {
			return stats, nil
		}
		if m.Memory != nil && m.Memory.Usage != nil {
			stats.MemoryUsage = clampUint64ToInt64(m.Memory.Usage.Usage)
		}
		if m.CPU != nil && m.CPU.Usage != nil {
			// v1 CPU total is in nanoseconds; convert to microseconds for a consistent unit.
			cpuUsec := clampUint64ToInt64(m.CPU.Usage.Total) / 1000
			stats.CPUUsage = e.cpuPercent(containerID, cpuUsec, now)
		}
	}

	return stats, nil
}

// cpuPercent computes a CPU usage percentage using the delta between the current
// and previous CPU time sample stored in the state map.
func (e *Engine) cpuPercent(containerID string, usageUsec int64, now time.Time) float32 {
	st, ok := e.store.get(containerID)
	if !ok {
		st = &containerState{}
		e.store.set(containerID, st)
	}

	var pct float32
	if !st.prevCPUSample.IsZero() {
		elapsed := now.Sub(st.prevCPUSample).Microseconds()
		if elapsed > 0 {
			delta := usageUsec - st.prevCPUTime
			if delta < 0 {
				delta = 0
			}
			pct = float32(delta) / float32(elapsed) * 100
		}
	}
	st.prevCPUTime = usageUsec
	st.prevCPUSample = now
	return pct
}

// GetContainerIPAddress returns the container's IP from the in-memory state map,
// falling back to the persisted label for recovery after an agent restart.
func (e *Engine) GetContainerIPAddress(containerID string) (string, error) {
	if st, ok := e.store.get(containerID); ok && st.ip != "" {
		return st.ip, nil
	}
	// Label fallback.
	ctx := e.ctx()
	c, err := e.client.LoadContainer(ctx, containerID)
	if err != nil {
		return "", nil
	}
	info, err := c.Info(ctx)
	if err != nil {
		return "", nil
	}
	if info.Labels != nil {
		return info.Labels[labelIP], nil
	}
	return "", nil
}

// GetContainerStartedAt returns the Unix-millisecond timestamp when the container
// was last started, from the in-memory state or the persisted label.
func (e *Engine) GetContainerStartedAt(containerID string) (int64, error) {
	if st, ok := e.store.get(containerID); ok && st.startedAt != 0 {
		return st.startedAt, nil
	}
	// Label fallback.
	ctx := e.ctx()
	c, err := e.client.LoadContainer(ctx, containerID)
	if err != nil {
		return time.Now().UnixMilli(), nil
	}
	info, err := c.Info(ctx)
	if err != nil {
		return time.Now().UnixMilli(), nil
	}
	if info.Labels != nil {
		if ts := readInt64Label(info.Labels, labelStartedAt); ts != 0 {
			return ts, nil
		}
	}
	return time.Now().UnixMilli(), nil
}

func (e *Engine) InspectContainerRaw(containerID string) (map[string]any, error) {
	ctx := e.ctx()
	c, err := e.client.LoadContainer(ctx, containerID)
	if err != nil {
		return nil, err
	}
	info, err := c.Info(ctx)
	if err != nil {
		return nil, err
	}
	spec, _ := c.Spec(ctx)
	taskState := map[string]any{}
	if task, taskErr := c.Task(ctx, nil); taskErr == nil {
		if st, stErr := task.Status(ctx); stErr == nil {
			taskState["status"] = fmt.Sprintf("%v", st.Status)
			taskState["exitStatus"] = st.ExitStatus
			taskState["exitedAt"] = st.ExitTime.UTC().Format(time.RFC3339Nano)
		}
	}
	ip, _ := e.GetContainerIPAddress(containerID)
	startedAt, _ := e.GetContainerStartedAt(containerID)
	out := map[string]any{
		"id":          c.ID(),
		"image":       info.Image,
		"labels":      info.Labels,
		"runtime":     info.Runtime,
		"snapshotter": info.Snapshotter,
		"createdAt":   info.CreatedAt.UTC().Format(time.RFC3339Nano),
		"updatedAt":   info.UpdatedAt.UTC().Format(time.RFC3339Nano),
		"spec":        spec,
		"task":        taskState,
		"network": map[string]any{
			"ipAddress": ip,
		},
		"state": map[string]any{
			"startedAtUnixMs": startedAt,
		},
	}
	if raw, err := json.Marshal(out); err == nil {
		normalized := map[string]any{}
		if err := json.Unmarshal(raw, &normalized); err == nil {
			return normalized, nil
		}
	}
	return out, nil
}

// --- Log streaming ---

// parseCRITimestamp extracts the timestamp from a CRI log line "timestamp stream flag message".
// Returns zero time and false if the line cannot be parsed.
func parseCRITimestamp(line string) (time.Time, bool) {
	parts := strings.SplitN(line, " ", 4)
	if len(parts) < 2 {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		// Try without nanoseconds
		t, err = time.Parse(time.RFC3339, parts[0])
		if err != nil {
			return time.Time{}, false
		}
	}
	return t, true
}

// shouldIncludeCRILine returns true if the line is within the since/until range.
func shouldIncludeCRILine(line string, since, until *time.Time) bool {
	if since == nil && until == nil {
		return true
	}
	t, ok := parseCRITimestamp(line)
	if !ok {
		return true // Include if we can't parse
	}
	if since != nil && t.Before(*since) {
		return false
	}
	if until != nil && t.After(*until) {
		return false
	}
	return true
}

// parseCRILogLine parses a CRI log line "timestamp stream flag message" and returns
// (message, streamType). If the format is not recognized, returns (line, Stdout).
func parseCRILogLine(line string) ([]byte, engine.StreamType) {
	// CRI format: "2020-01-10T18:10:40.01576219Z stdout F actual message"
	parts := strings.SplitN(line, " ", 4)
	if len(parts) >= 3 {
		stream := parts[1]
		msg := line
		if len(parts) == 4 {
			msg = parts[3]
		}
		if stream == "stderr" {
			return []byte(msg), engine.Stderr
		}
		return []byte(msg), engine.Stdout
	}
	return []byte(line), engine.Stdout
}

// parseISOTimestamp parses an ISO 8601 timestamp string.
func parseISOTimestamp(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, errors.New("empty timestamp")
	}
	t, err := time.Parse(time.RFC3339Nano, s)
	if err == nil {
		return t, nil
	}
	t, err = time.Parse(time.RFC3339, s)
	if err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid timestamp: %s", s)
}

// TailContainerLogs streams log lines from the CRI container log file.
// Containerd CRI writes both stdout and stderr to a single file (0.log) with
// CRI format: "timestamp stream flag message". We parse and dispatch by stream.
// Falls back to stdout.log/stderr.log if 0.log is absent.
func (e *Engine) TailContainerLogs(containerID, sessionID, microserviceUUID string, handler engine.LogTailHandler, cfg *engine.TailConfig) error {
	logDir := filepath.Join(e.logDir, microserviceUUID)
	criLogPath := filepath.Join(logDir, "0.log")

	nLines := 100
	if cfg != nil && cfg.Lines > 0 {
		nLines = cfg.Lines
	}
	follow := cfg != nil && cfg.Follow

	var since, until *time.Time
	if cfg != nil {
		if cfg.Since != "" {
			if t, err := parseISOTimestamp(cfg.Since); err == nil {
				since = &t
			}
		}
		if cfg.Until != "" {
			if t, err := parseISOTimestamp(cfg.Until); err == nil {
				until = &t
			}
		}
	}

	// Try CRI single-file format first (0.log)
	tailCtx := tailContext(cfg)
	if _, err := os.Stat(criLogPath); err == nil {
		return e.tailCRILogFile(tailCtx, criLogPath, sessionID, microserviceUUID, handler, nLines, follow, since, until)
	}

	// Fallback: separate stdout.log / stderr.log (no timestamp filtering for plain format)
	return e.tailSeparateLogFiles(tailCtx, logDir, sessionID, microserviceUUID, handler, nLines, follow)
}

func tailContext(cfg *engine.TailConfig) context.Context {
	if cfg != nil && cfg.Ctx != nil {
		return cfg.Ctx
	}
	return context.Background()
}

func tailCancelled(ctx context.Context) bool {
	select {
	case <-ctx.Done():
		return true
	default:
		return false
	}
}

func (e *Engine) tailCRILogFile(ctx context.Context, logPath, sessionID, microserviceUUID string, handler engine.LogTailHandler, nLines int, follow bool, since, until *time.Time) error {
	if !follow {
		// Historical query: read from start, return last N lines (filtered by since/until)
		whence := 0 // io.SeekStart
		t, err := tail.TailFile(logPath, tail.Config{
			Follow:    false,
			ReOpen:    false,
			MustExist: false,
			Location:  &tail.SeekInfo{Offset: 0, Whence: whence},
			Logger:    tail.DiscardingLogger,
		})
		if err != nil {
			handler.OnError(sessionID, fmt.Errorf("tail %s: %w", logPath, err))
			return err
		}
		defer t.Cleanup()
		var buf []struct {
			msg    []byte
			stream engine.StreamType
			text   string
		}
		for line := range t.Lines {
			if tailCancelled(ctx) {
				handler.OnComplete(sessionID)
				return nil
			}
			if line.Err != nil {
				handler.OnError(sessionID, line.Err)
				return line.Err
			}
			if !shouldIncludeCRILine(line.Text, since, until) {
				continue
			}
			msg, st := parseCRILogLine(line.Text)
			buf = append(buf, struct {
				msg    []byte
				stream engine.StreamType
				text   string
			}{msg, st, line.Text})
			if len(buf) > nLines {
				buf = buf[len(buf)-nLines:]
			}
		}
		for _, item := range buf {
			handler.OnLogLine(sessionID, microserviceUUID, item.msg, item.stream)
		}
		handler.OnComplete(sessionID)
		return nil
	}

	// Follow mode: send last N lines first (filtered), then follow from EOF
	initialLines, err := readLastCRILines(logPath, nLines, since, until)
	if err != nil && !os.IsNotExist(err) {
		handler.OnError(sessionID, fmt.Errorf("read initial lines %s: %w", logPath, err))
		return err
	}
	for _, item := range initialLines {
		if tailCancelled(ctx) {
			handler.OnComplete(sessionID)
			return nil
		}
		handler.OnLogLine(sessionID, microserviceUUID, item.msg, item.stream)
	}

	// Now follow from EOF
	stat, err := os.Stat(logPath)
	if err != nil {
		handler.OnComplete(sessionID)
		return nil
	}
	startOffset := stat.Size()

	t, err := tail.TailFile(logPath, tail.Config{
		Follow:    true,
		ReOpen:    true,
		MustExist: false,
		Location:  &tail.SeekInfo{Offset: startOffset, Whence: 0}, // seek to EOF
		Logger:    tail.DiscardingLogger,
	})
	if err != nil {
		handler.OnError(sessionID, fmt.Errorf("tail %s: %w", logPath, err))
		return err
	}
	defer t.Cleanup()

	for {
		select {
		case <-ctx.Done():
			handler.OnComplete(sessionID)
			return nil
		case line, ok := <-t.Lines:
			if !ok {
				handler.OnComplete(sessionID)
				return nil
			}
			if line.Err != nil {
				handler.OnError(sessionID, line.Err)
				return line.Err
			}
			if !shouldIncludeCRILine(line.Text, since, until) {
				// If we've passed until, stop following
				if until != nil {
					if ts, ok := parseCRITimestamp(line.Text); ok && ts.After(*until) {
						handler.OnComplete(sessionID)
						return nil
					}
				}
				continue
			}
			msg, st := parseCRILogLine(line.Text)
			handler.OnLogLine(sessionID, microserviceUUID, msg, st)
		}
	}
}

type criLine struct {
	msg    []byte
	stream engine.StreamType
}

// readLastCRILines reads the last nLines from a CRI log file, filtered by since/until.
func readLastCRILines(logPath string, nLines int, since, until *time.Time) ([]criLine, error) {
	f, err := os.Open(filepath.Clean(logPath))
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = f.Close()
	}()

	var lines []criLine
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		text := scanner.Text()
		if !shouldIncludeCRILine(text, since, until) {
			continue
		}
		msg, st := parseCRILogLine(text)
		lines = append(lines, criLine{msg: msg, stream: st})
		if len(lines) > nLines {
			lines = lines[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

func (e *Engine) tailSeparateLogFiles(ctx context.Context, logDir, sessionID, microserviceUUID string, handler engine.LogTailHandler, nLines int, follow bool) error {
	stdoutPath := filepath.Join(logDir, "stdout.log")
	stderrPath := filepath.Join(logDir, "stderr.log")

	readLastLines := func(path string, st engine.StreamType) {
		lines, err := readLastPlainLines(path, nLines)
		if err != nil && !os.IsNotExist(err) {
			handler.OnError(sessionID, fmt.Errorf("read initial lines %s: %w", path, err))
			return
		}
		for _, l := range lines {
			if tailCancelled(ctx) {
				return
			}
			handler.OnLogLine(sessionID, microserviceUUID, l, st)
		}
	}

	tailFile := func(path string, st engine.StreamType, wg *sync.WaitGroup) {
		if wg != nil {
			defer wg.Done()
		}
		if !follow {
			t, err := tail.TailFile(path, tail.Config{
				Follow:    false,
				MustExist: false,
				Location:  &tail.SeekInfo{Offset: 0, Whence: 0},
				Logger:    tail.DiscardingLogger,
			})
			if err != nil {
				handler.OnError(sessionID, fmt.Errorf("tail %s: %w", path, err))
				return
			}
			defer t.Cleanup()
			var buf [][]byte
			for line := range t.Lines {
				if line.Err != nil {
					handler.OnError(sessionID, line.Err)
					return
				}
				buf = append(buf, []byte(line.Text))
				if len(buf) > nLines {
					buf = buf[len(buf)-nLines:]
				}
			}
			for _, l := range buf {
				handler.OnLogLine(sessionID, microserviceUUID, l, st)
			}
			return
		}
		// Follow mode: send last N lines first, then follow from EOF
		readLastLines(path, st)
		stat, err := os.Stat(path)
		if err != nil {
			return
		}
		t, err := tail.TailFile(path, tail.Config{
			Follow:    true,
			ReOpen:    true,
			MustExist: false,
			Location:  &tail.SeekInfo{Offset: stat.Size(), Whence: 0},
			Logger:    tail.DiscardingLogger,
		})
		if err != nil {
			handler.OnError(sessionID, fmt.Errorf("tail %s: %w", path, err))
			return
		}
		defer t.Cleanup()
		for {
			select {
			case <-ctx.Done():
				return
			case line, ok := <-t.Lines:
				if !ok {
					return
				}
				if line.Err != nil {
					handler.OnError(sessionID, line.Err)
					return
				}
				handler.OnLogLine(sessionID, microserviceUUID, []byte(line.Text), st)
			}
		}
	}

	if follow {
		var wg sync.WaitGroup
		wg.Add(2)
		go tailFile(stdoutPath, engine.Stdout, &wg)
		go tailFile(stderrPath, engine.Stderr, &wg)
		wg.Wait()
		handler.OnComplete(sessionID)
		return nil
	}
	tailFile(stdoutPath, engine.Stdout, nil)
	tailFile(stderrPath, engine.Stderr, nil)
	handler.OnComplete(sessionID)
	return nil
}

// readLastPlainLines reads the last nLines from a plain text log file (no CRI format).
func readLastPlainLines(logPath string, nLines int) ([][]byte, error) {
	f, err := os.Open(filepath.Clean(logPath))
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = f.Close()
	}()

	var lines [][]byte
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		lines = append(lines, []byte(scanner.Text()))
		if len(lines) > nLines {
			lines = lines[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return lines, nil
}

// --- Configuration drift detection ---

// AreMicroserviceAndContainerEqual returns true if the running container's
// configuration matches the desired microservice spec. It compares:
//  1. Image name
//  2. Environment variables (set equality)
//  3. Port mappings (from persisted label vs desired) — skipped for host-network mode
//  4. Network mode (host vs bridge)
func (e *Engine) AreMicroserviceAndContainerEqual(containerID string, ms *models.Microservice, registry *models.Registry) bool {
	ctx := e.ctx()
	shortID := containerID
	if len(shortID) > 12 {
		shortID = shortID[:12]
	}

	c, err := e.client.LoadContainer(ctx, containerID)
	if err != nil {
		log.Debugf("AreMicroserviceAndContainerEqual %s: LoadContainer error: %v", shortID, err)
		return false
	}

	spec, err := c.Spec(ctx)
	if err != nil {
		log.Debugf("AreMicroserviceAndContainerEqual %s: Spec error: %v", shortID, err)
		return false
	}
	info, err := c.Info(ctx)
	if err != nil {
		log.Debugf("AreMicroserviceAndContainerEqual %s: Info error: %v", shortID, err)
		return false
	}

	// 1. Image — containerd stores qualified refs (e.g. docker.io/, quay.io/); controller
	// often sends hostless names. imageref.Match uses the same registry context as pull.
	registryURL, fromCache := imageref.MatchParamsOptional(registryURLFromRegistry(registry))
	if !microserviceImageMatches(info.Image, ms.ImageName, registryURL, fromCache) {
		log.Debugf("AreMicroserviceAndContainerEqual %s: image mismatch: got %q want %q",
			shortID, info.Image, ms.ImageName)
		return false
	}

	// 2. Environment variables (compare as sets; only desired keys are checked against actual).
	desiredEnv := buildIofogContainerEnv(ms, config.GetInstance())
	if !envSetsEqual(spec.Process.Env, desiredEnv) {
		log.Debugf("AreMicroserviceAndContainerEqual %s: env mismatch", shortID)
		return false
	}

	// 3. Port mappings — skipped for host-network containers because CRI ignores port
	// bindings when the container shares the host network namespace. Comparing stored
	// labels against desired mappings would produce false positives on every cycle.
	if !ms.HostNetworkMode {
		if info.Labels != nil {
			var storedPorts []*models.PortMapping
			if v, ok := info.Labels[labelPorts]; ok && v != "" {
				_ = json.Unmarshal([]byte(v), &storedPorts)
			}
			if !portMappingsEqual(storedPorts, ms.PortMappings) {
				log.Debugf("AreMicroserviceAndContainerEqual %s: port mismatch stored=%v desired=%v",
					shortID, storedPorts, ms.PortMappings)
				return false
			}
		}
	}

	// 4. Network mode — read canonical host-network label (OCI netns path is ambiguous for CRI).
	storedHostNet := strings.EqualFold(strings.TrimSpace(info.Labels[workloadmeta.LabelHostNetwork]), "true")
	if ms.HostNetworkMode != storedHostNet {
		log.Debugf("AreMicroserviceAndContainerEqual %s: network mismatch want hostNet=%v stored=%v",
			shortID, ms.HostNetworkMode, storedHostNet)
		return false
	}

	label := ""
	if info.Labels != nil {
		label = info.Labels[containerapply.LabelFingerprint]
	}
	if !containerapply.MatchesLabel(label, ms) {
		log.Debugf("AreMicroserviceAndContainerEqual %s: apply fingerprint mismatch", shortID)
		return false
	}

	return true
}

// registryURLFromRegistry returns the registry URL for imageref helpers.
func registryURLFromRegistry(registry *models.Registry) string {
	if registry == nil {
		return ""
	}
	return registry.URL
}

// microserviceImageMatches reports whether containerImage matches the desired microservice
// image ref for drift detection using the same imageref rules as pull/cache lookup.
func microserviceImageMatches(containerImage, desiredImage, registryURL string, fromCache bool) bool {
	return imageref.Match(containerImage, desiredImage, registryURL, fromCache)
}

// envSetsEqual returns true if actual and desired contain the same KEY=VALUE pairs.
// System entries injected by the runtime (e.g. PATH) are ignored — we only check
// keys that appear in either the desired or actual set.
func envSetsEqual(actual, desired []string) bool {
	toMap := func(envs []string) map[string]string {
		m := make(map[string]string, len(envs))
		for _, e := range envs {
			idx := strings.Index(e, "=")
			if idx < 0 {
				m[e] = ""
			} else {
				m[e[:idx]] = e[idx+1:]
			}
		}
		return m
	}
	desiredMap := toMap(desired)
	actualMap := toMap(actual)
	for k, v := range desiredMap {
		if actualMap[k] != v {
			return false
		}
	}
	return true
}

// portMappingsEqual returns true if both slices contain the same port mappings.
func portMappingsEqual(a, b []*models.PortMapping) bool {
	if len(a) != len(b) {
		return false
	}
	type key struct{ outside, inside int }
	set := make(map[key]bool, len(a))
	for _, p := range a {
		set[key{p.Outside, p.Inside}] = true
	}
	for _, p := range b {
		if !set[key{p.Outside, p.Inside}] {
			return false
		}
	}
	return true
}

// hasHostNetNS returns true if the OCI spec uses the host's network namespace.
func hasHostNetNS(spec *oci.Spec) bool {
	if spec.Linux == nil {
		return false
	}
	for _, ns := range spec.Linux.Namespaces {
		if ns.Type == specs.NetworkNamespace {
			// An empty Path means the namespace is inherited from the host.
			return ns.Path == ""
		}
	}
	// No network namespace entry → inherits host namespace.
	return true
}

// --- Network ---

func (e *Engine) EnsureNetwork(_ string) error {
	// The iofog bridge network is created by the CNI plugin on first container attach.
	return nil
}

const (
	execStopWaitTimeout = 5 * time.Second
)

func (e *Engine) addExecSession(containerID, execID string) {
	e.execToCont.Store(execID, containerID)
	var ids []string
	if v, ok := e.execSessions.Load(containerID); ok {
		ids, ok = v.([]string)
		if !ok {
			ids = []string{}
		}
	}
	ids = append(ids, execID)
	e.execSessions.Store(containerID, ids)
}

func (e *Engine) removeExecSession(execID string) {
	if v, ok := e.execToCont.LoadAndDelete(execID); ok {
		containerID, ok := v.(string)
		if !ok {
			containerID = ""
		}
		if v2, ok2 := e.execSessions.Load(containerID); ok2 {
			ids, ok := v2.([]string)
			if !ok {
				ids = []string{}
			}
			newIds := make([]string, 0, len(ids)-1)
			for _, id := range ids {
				if id != execID {
					newIds = append(newIds, id)
				}
			}
			if len(newIds) == 0 {
				e.execSessions.Delete(containerID)
			} else {
				e.execSessions.Store(containerID, newIds)
			}
		}
	}
}

// CreateExecSession validates the container, builds the exec process spec, and stores
// it as a pending exec. runtimeExecID must be unique per container (e.g. {cid[:12]}-exec-{session[:8]}).
// The process is NOT started yet — call StartExecSession to attach I/O and launch.
func (e *Engine) CreateExecSession(containerID string, runtimeExecID string, cmd []string) (string, error) {
	execID := strings.TrimSpace(runtimeExecID)
	if execID == "" {
		return "", errors.New("runtime exec id is required")
	}
	ctx := e.ctx()
	c, err := e.client.LoadContainer(ctx, containerID)
	if err != nil {
		return "", fmt.Errorf("load container %s: %w", containerID, err)
	}

	spec, err := c.Spec(ctx)
	if err != nil {
		return "", fmt.Errorf("get spec for %s: %w", containerID, err)
	}

	execSpec := &specs.Process{
		Args:     cmd,
		Cwd:      "/",
		Env:      containerexec.ExecEnvForTTY(spec.Process.Env, ""),
		Terminal: true,
	}
	if spec.Process.Cwd != "" {
		execSpec.Cwd = spec.Process.Cwd
	}

	e.execMu.Lock()
	e.pendingExecs[execID] = &pendingExec{
		containerID: containerID,
		execID:      execID,
		spec:        execSpec,
	}
	e.execMu.Unlock()

	return execID, nil
}

// StartExecSession looks up the pending exec registered by CreateExecSession, wires
// stdin/stdout/stderr pipes to the container, and starts the process. Blocks until
// the process exits so the caller only fires OnComplete() when the shell actually ends.
func (e *Engine) StartExecSession(execID string, stdin io.Reader, stdout, stderr io.Writer) error {
	e.execMu.Lock()
	pending := e.pendingExecs[execID]
	if pending != nil {
		delete(e.pendingExecs, execID)
	}
	e.execMu.Unlock()

	if pending == nil {
		return fmt.Errorf("no pending exec session with ID %s (call CreateExecSession first)", execID)
	}

	ctx := e.ctx()
	c, err := e.client.LoadContainer(ctx, pending.containerID)
	if err != nil {
		return fmt.Errorf("load container %s: %w", pending.containerID, err)
	}
	task, err := c.Task(ctx, nil)
	if err != nil {
		return fmt.Errorf("get task for %s: %w", pending.containerID, err)
	}

	fifoDir := filepath.Join(constants.EdgeletRunDir, "exec-fifo")
	if err := os.MkdirAll(fifoDir, 0700); err != nil {
		return fmt.Errorf("create exec fifo dir: %w", err)
	}
	// For TTY: stdout and stderr are combined
	creator := cio.NewCreator(
		cio.WithFIFODir(fifoDir),
		cio.WithStreams(stdin, stdout, stdout), // stderr → stdout for TTY
		cio.WithTerminal,
	)

	proc, err := task.Exec(ctx, execID, pending.spec, creator)
	if err != nil && errdefs.IsAlreadyExists(err) {
		if sweepErr := e.stopAndDeleteExecProcess(execID, pending.containerID); sweepErr != nil {
			return fmt.Errorf("exec in %s: %w (orphan sweep: %w)", pending.containerID, err, sweepErr)
		}
		proc, err = task.Exec(ctx, execID, pending.spec, creator)
	}
	if err != nil {
		return fmt.Errorf("exec in %s: %w", pending.containerID, err)
	}
	if err := proc.Start(ctx); err != nil {
		return fmt.Errorf("start exec process %s: %w", execID, err)
	}

	e.addExecSession(pending.containerID, execID)

	e.execMu.Lock()
	e.runningProcs[execID] = proc
	e.execMu.Unlock()

	// Block until the exec process exits so the caller (ProcessManager goroutine)
	// only fires OnComplete() when the shell actually ends, not immediately after Start().
	exitCode := 0
	exitCh, err := proc.Wait(ctx)
	if err == nil {
		status := <-exitCh
		exitCode = int(status.ExitCode())
	}

	e.removeExecSession(execID)

	// Deregister the exec from containerd so the exec ID can be reused.
	if _, delErr := proc.Delete(ctx); delErr != nil {
		log.Warnf("exec %s delete after exit: %v", execID, delErr)
	}

	e.execMu.Lock()
	delete(e.runningProcs, execID)
	e.execExitCode[execID] = exitCode
	e.execMu.Unlock()

	return nil
}

// ExecWithExitCode runs a command in the container and returns the exit code.
// Used for healthcheck execution. Returns (exitCode, error). On timeout, kills
// the process and returns (-1, context.DeadlineExceeded).
func (e *Engine) ExecWithExitCode(containerID string, cmd []string, timeout time.Duration) (int, error) {
	ctx := e.ctx()
	c, err := e.client.LoadContainer(ctx, containerID)
	if err != nil {
		return -1, fmt.Errorf("load container %s: %w", containerID, err)
	}

	spec, err := c.Spec(ctx)
	if err != nil {
		return -1, fmt.Errorf("get spec for %s: %w", containerID, err)
	}

	execSpec := &specs.Process{
		Args:     cmd,
		Cwd:      "/",
		Env:      spec.Process.Env,
		Terminal: false,
	}
	if spec.Process.Cwd != "" {
		execSpec.Cwd = spec.Process.Cwd
	}

	// Containerd enforces a 76-char max on exec IDs. The full container ID is 64 chars,
	// so we use the first 12 chars + a base-36 nanosecond timestamp (~13 chars) = ~29 chars total.
	execID := containerID[:12] + "-hc-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	task, err := c.Task(ctx, nil)
	if err != nil {
		return -1, fmt.Errorf("get task for %s: %w", containerID, err)
	}

	proc, err := task.Exec(ctx, execID, execSpec, cio.NullIO)
	if err != nil {
		return -1, fmt.Errorf("exec in %s: %w", containerID, err)
	}
	if err := proc.Start(ctx); err != nil {
		return -1, fmt.Errorf("start exec %s: %w", execID, err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	exitCh, err := proc.Wait(waitCtx)
	if err != nil {
		_, _ = proc.Delete(ctx)
		return -1, err
	}

	select {
	case exitStatus := <-exitCh:
		code := exitStatus.ExitCode()
		_, _ = proc.Delete(ctx)
		return int(code), nil
	case <-waitCtx.Done():
		_ = proc.Kill(ctx, syscall.SIGKILL)
		_, _ = proc.Delete(ctx)
		return -1, waitCtx.Err()
	}
}

// StopExecSession kills the running exec process, waits for exit, and deregisters it from containerd.
func (e *Engine) StopExecSession(execID string) error {
	return e.stopAndDeleteExecProcess(execID, "")
}

// SweepOrphanExecSessions stops interactive exec processes tracked for containerID that are not in keepRuntimeExecIDs.
func (e *Engine) SweepOrphanExecSessions(containerID string, keepRuntimeExecIDs map[string]struct{}) error {
	containerID = strings.TrimSpace(containerID)
	if containerID == "" {
		return nil
	}
	var tracked []string
	if v, ok := e.execSessions.Load(containerID); ok {
		if ids, ok := v.([]string); ok {
			tracked = append(tracked, ids...)
		}
	}
	for _, execID := range tracked {
		if _, keep := keepRuntimeExecIDs[execID]; keep {
			continue
		}
		if !isInteractiveExecID(execID) {
			continue
		}
		if err := e.stopAndDeleteExecProcess(execID, containerID); err != nil {
			log.Warnf("orphan exec sweep %s on %s: %v", execID, containerID, err)
		}
	}
	return nil
}

func isInteractiveExecID(execID string) bool {
	return strings.Contains(execID, "-exec-") || strings.Contains(execID, "-local-")
}

func (e *Engine) stopAndDeleteExecProcess(execID, containerIDHint string) error {
	execID = strings.TrimSpace(execID)
	if execID == "" {
		return nil
	}

	ctx := e.ctx()
	var proc client.Process

	e.execMu.Lock()
	if p := e.runningProcs[execID]; p != nil {
		proc = p
		delete(e.runningProcs, execID)
	}
	e.execMu.Unlock()

	if proc == nil {
		if containerIDHint == "" {
			if v, ok := e.execToCont.Load(execID); ok {
				if hint, ok := v.(string); ok {
					containerIDHint = hint
				}
			}
		}
		if containerIDHint == "" {
			return nil
		}
		c, err := e.client.LoadContainer(ctx, containerIDHint)
		if err != nil {
			if errdefs.IsNotFound(err) {
				return nil
			}
			return err
		}
		task, err := c.Task(ctx, nil)
		if err != nil {
			if errdefs.IsNotFound(err) {
				return nil
			}
			return err
		}
		proc, err = task.LoadProcess(ctx, execID, nil)
		if err != nil {
			if errdefs.IsNotFound(err) {
				e.removeExecSession(execID)
				return nil
			}
			return err
		}
	}

	if err := proc.Kill(ctx, syscall.SIGTERM); err != nil && !errdefs.IsNotFound(err) {
		log.Warnf("exec %s SIGTERM: %v", execID, err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, execStopWaitTimeout)
	defer cancel()
	if exitCh, err := proc.Wait(waitCtx); err == nil {
		select {
		case <-exitCh:
		case <-waitCtx.Done():
			if killErr := proc.Kill(ctx, syscall.SIGKILL); killErr != nil && !errdefs.IsNotFound(killErr) {
				log.Warnf("exec %s SIGKILL: %v", execID, killErr)
			}
		}
	} else if !errdefs.IsNotFound(err) {
		if killErr := proc.Kill(ctx, syscall.SIGKILL); killErr != nil && !errdefs.IsNotFound(killErr) {
			log.Warnf("exec %s SIGKILL: %v", execID, killErr)
		}
	}

	if _, err := proc.Delete(ctx); err != nil && !errdefs.IsNotFound(err) {
		log.Warnf("exec %s delete: %v", execID, err)
	}

	e.removeExecSession(execID)
	e.execMu.Lock()
	delete(e.execExitCode, execID)
	delete(e.pendingExecs, execID)
	e.execMu.Unlock()
	return nil
}

// GetExecSessionStatus reports whether the exec process for execID is still running.
func (e *Engine) GetExecSessionStatus(execID string) (bool, error) {
	e.execMu.Lock()
	proc := e.runningProcs[execID]
	e.execMu.Unlock()

	if proc == nil {
		return false, nil
	}

	ctx := e.ctx()
	status, err := proc.Status(ctx)
	if err != nil {
		return false, fmt.Errorf("get exec status %s: %w", execID, err)
	}
	// containerd reports the process as running when its status is "running".
	return status.Status == client.Running, nil
}

// GetExecSessionExitCode reports the exit code for a completed exec process.
func (e *Engine) GetExecSessionExitCode(execID string) (int, error) {
	e.execMu.Lock()
	defer e.execMu.Unlock()
	if _, running := e.runningProcs[execID]; running {
		return 0, errors.New("exec session is still running")
	}
	code, ok := e.execExitCode[execID]
	if !ok {
		return 0, errors.New("exec session exit code is unavailable")
	}
	return code, nil
}

// ResizeExecSession resizes a running tty exec process.
func (e *Engine) ResizeExecSession(execID string, cols, rows uint32) error {
	e.execMu.Lock()
	proc := e.runningProcs[execID]
	e.execMu.Unlock()
	if proc == nil {
		return errors.New("exec session is not running")
	}
	return proc.Resize(e.ctx(), cols, rows)
}

// --- Helpers ---

func (e *Engine) GetContainerMicroserviceUUID(cont engine.Container) string {
	return workloadmeta.MicroserviceUIDFromLabels(cont.Labels)
}

func (e *Engine) GetContainerName(cont engine.Container) string {
	if len(cont.Names) > 0 {
		return cont.Names[0]
	}
	return cont.ID
}

// containerFromContainerd adapts a containerd Container to engine.Container,
// querying the actual task state instead of hardcoding "running".
func containerFromContainerd(ctx context.Context, c client.Container) (*engine.Container, error) {
	info, err := c.Info(ctx)
	if err != nil {
		return nil, err
	}

	state := "stopped"
	status := "Exited"

	task, err := c.Task(ctx, nil)
	if err == nil {
		ts, err := task.Status(ctx)
		if err == nil {
			switch ts.Status {
			case client.Running:
				state, status = "running", "Up"
			case client.Created:
				state, status = "created", "Created"
			case client.Paused, client.Pausing:
				state, status = "paused", "Paused"
			default:
				state, status = "stopped", "Exited"
			}
		}
	}

	// Reconstruct the iofog_<uuid> name from the label so GetContainerName returns a
	// value with the expected prefix and updateRunningMicroservicesCount counts correctly.
	// Pause containers are already excluded by isSandboxContainer before this is called.
	name := c.ID()
	if uuid := workloadmeta.MicroserviceUIDFromLabels(info.Labels); uuid != "" {
		name = utils.EdgeletDockerContainerNamePrefix + uuid
	}

	return &engine.Container{
		ID:     c.ID(),
		Names:  []string{name},
		Image:  info.Image,
		Labels: info.Labels,
		State:  state,
		Status: status,
	}, nil
}

func envVarMapFromMicroservice(envVars []*models.EnvVar) map[string]string {
	m := make(map[string]string, len(envVars))
	for _, ev := range envVars {
		if ev != nil && (ev.Key != "" || ev.Value != "") {
			m[ev.Key] = ev.Value
		}
	}
	return m
}

// buildIofogContainerEnv returns canonical EDGELET_* env and user env with reserved-key and TZ policy.
func buildIofogContainerEnv(ms *models.Microservice, cfg *config.Config) []string {
	if cfg == nil {
		cfg = config.GetInstance()
	}
	in := workloadmeta.BuildInput{
		MicroserviceUUID: ms.MicroserviceUUID,
		MicroserviceName: ms.MicroserviceName,
		ApplicationName:  ms.ApplicationName,
		NodeUUID:         cfg.IOFogUUID,
		RuntimeEngine:    workloadmeta.RuntimeEngineEdgelet,
		IsRouter:         ms.IsRouter,
		IsNats:           ms.IsNats,
		IsController:     ms.IsController,
		HostNetwork:      ms.HostNetworkMode,
		IsSystem:         ms.IsSystem || ms.IsController,
		TimeZone:         cfg.TimeZone,
		UserEnv:          envVarMapFromMicroservice(ms.EnvVars),
		UserLabels:       ms.Labels,
	}
	return workloadmeta.BuildEnv(in)
}
