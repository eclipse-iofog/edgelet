package docker

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/containerapply"
	"github.com/eclipse-iofog/edgelet/internal/dnsresolver"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/utils"
	"github.com/eclipse-iofog/edgelet/internal/workloadmeta"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	mobyclient "github.com/moby/moby/client"
)

const (
	canonicalAgentHost  = "edgelet.default.svc.bridge.local"
	canonicalRouterHost = "router.default.svc.bridge.local"
	canonicalNatsHost   = "nats.default.svc.bridge.local"
)

// Container represents a Docker container
type Container struct {
	ID     string
	Names  []string
	Image  string
	Status string
	State  string
	Labels map[string]string
}

// GetContainer retrieves a container by microservice UUID
func (c *Client) GetContainer(microserviceUUID string) (*Container, error) {
	cli := c.GetClient()
	if cli == nil {
		return nil, errors.New("docker client not initialized")
	}

	ctx := c.GetContext()
	listResult, err := cli.ContainerList(ctx, mobyclient.ContainerListOptions{
		All:     true,
		Filters: make(mobyclient.Filters).Add("name", utils.EdgeletDockerContainerNamePrefix+microserviceUUID),
	})

	if err != nil {
		return nil, err
	}

	containers := listResult.Items

	if len(containers) == 0 {
		return nil, nil
	}

	cont := containers[0]
	return &Container{
		ID:     cont.ID,
		Names:  cont.Names,
		Image:  cont.Image,
		Status: cont.Status,
		State:  string(cont.State),
		Labels: cont.Labels,
	}, nil
}

// GetContainerByID retrieves a container by its Docker-assigned ID.
func (c *Client) GetContainerByID(containerID string) (*Container, error) {
	cli := c.GetClient()
	if cli == nil {
		return nil, errors.New("docker client not initialized")
	}
	ctx := c.GetContext()
	listResult, err := cli.ContainerList(ctx, mobyclient.ContainerListOptions{
		All:     true,
		Filters: make(mobyclient.Filters).Add("id", containerID),
	})
	if err != nil {
		return nil, err
	}
	containers := listResult.Items
	if len(containers) == 0 {
		return nil, nil
	}
	cont := containers[0]
	return &Container{
		ID:     cont.ID,
		Names:  cont.Names,
		Image:  cont.Image,
		Status: cont.Status,
		State:  string(cont.State),
		Labels: cont.Labels,
	}, nil
}

func (c *Client) listContainers(all bool) ([]Container, error) {
	cli := c.GetClient()
	if cli == nil {
		return nil, errors.New("docker client not initialized")
	}

	ctx := c.GetContext()
	listResult, err := cli.ContainerList(ctx, mobyclient.ContainerListOptions{
		All: all,
	})
	if err != nil {
		return nil, err
	}

	return containersFromDockerList(all, listResult.Items), nil
}

func containersFromDockerList(all bool, containers []container.Summary) []Container {
	result := make([]Container, 0, len(containers))
	for _, cont := range containers {
		if !all && cont.State != "running" {
			continue
		}
		result = append(result, Container{
			ID:     cont.ID,
			Names:  cont.Names,
			Image:  cont.Image,
			Status: cont.Status,
			State:  string(cont.State),
			Labels: cont.Labels,
		})
	}
	return result
}

// GetAllContainers returns all containers regardless of state.
func (c *Client) GetAllContainers() ([]Container, error) {
	return c.listContainers(true)
}

// GetRunningContainers returns running containers only.
func (c *Client) GetRunningContainers() ([]Container, error) {
	return c.listContainers(false)
}

// GetContainerStatus retrieves the status of a container
func (c *Client) GetContainerStatus(containerID string) (string, error) {
	cli := c.GetClient()
	if cli == nil {
		return "", errors.New("docker client not initialized")
	}

	ctx := c.GetContext()
	inspectResult, err := cli.ContainerInspect(ctx, containerID, mobyclient.ContainerInspectOptions{})
	if err != nil {
		return "", err
	}
	inspect := inspectResult.Container

	return string(inspect.State.Status), nil
}

// IsContainerRunning checks if a container is running
func (c *Client) IsContainerRunning(containerID string) (bool, error) {
	status, err := c.GetContainerStatus(containerID)
	if err != nil {
		return false, err
	}
	return status == "running", nil
}

// StartContainer starts a container
func (c *Client) StartContainer(containerID string) error {
	cli := c.GetClient()
	if cli == nil {
		return errors.New("docker client not initialized")
	}

	ctx := c.GetContext()
	_, err := cli.ContainerStart(ctx, containerID, mobyclient.ContainerStartOptions{})
	return err
}

// StopContainer stops a container
func (c *Client) StopContainer(containerID string) error {
	cli := c.GetClient()
	if cli == nil {
		return errors.New("docker client not initialized")
	}

	// Check if container is running first
	running, err := c.IsContainerRunning(containerID)
	if err != nil {
		return err
	}

	if !running {
		return nil // Already stopped
	}

	ctx := c.GetContext()
	_, err = cli.ContainerStop(ctx, containerID, mobyclient.ContainerStopOptions{})
	return err
}

// KillContainer sends SIGKILL to a container.
func (c *Client) KillContainer(containerID string) error {
	cli := c.GetClient()
	if cli == nil {
		return errors.New("docker client not initialized")
	}
	ctx := c.GetContext()
	_, err := cli.ContainerKill(ctx, containerID, mobyclient.ContainerKillOptions{Signal: "SIGKILL"})
	return err
}

// RemoveContainer removes a container
func (c *Client) RemoveContainer(containerID string, removeVolumes bool) error {
	cli := c.GetClient()
	if cli == nil {
		return errors.New("docker client not initialized")
	}

	ctx := c.GetContext()
	_, err := cli.ContainerRemove(ctx, containerID, mobyclient.ContainerRemoveOptions{
		Force:         true,
		RemoveVolumes: removeVolumes,
	})
	return err
}

// GetContainerIPAddress gets the IPv4 address of a container
func (c *Client) GetContainerIPAddress(containerID string) (string, error) {
	cli := c.GetClient()
	if cli == nil {
		return "", errors.New("docker client not initialized")
	}

	ctx := c.GetContext()
	inspectResult, err := cli.ContainerInspect(ctx, containerID, mobyclient.ContainerInspectOptions{})
	if err != nil {
		return "", err
	}
	inspect := inspectResult.Container

	// Check if container is using host network mode
	if inspect.HostConfig != nil && inspect.HostConfig.NetworkMode == "host" {
		// Return host IP address (will be set by network interface manager)
		// For now, return empty string - this will be handled by the caller
		return "", nil
	}

	// For containers with their own network namespace
	networks := inspect.NetworkSettings.Networks
	if networks != nil {
		// Try bridge network first
		if bridge, ok := networks["bridge"]; ok && bridge.IPAddress.IsValid() {
			return bridge.IPAddress.String(), nil
		}
		// Fallback to first available network
		for _, network := range networks {
			if network.IPAddress.IsValid() {
				return network.IPAddress.String(), nil
			}
		}
	}

	return "", errors.New("no IP address found for container")
}

// GetContainerStartedAt returns container last start epoch time in milliseconds
func (c *Client) GetContainerStartedAt(containerID string) (int64, error) {
	cli := c.GetClient()
	if cli == nil {
		return 0, errors.New("docker client not initialized")
	}

	ctx := c.GetContext()
	inspectResult, err := cli.ContainerInspect(ctx, containerID, mobyclient.ContainerInspectOptions{})
	if err != nil {
		return 0, err
	}
	startedAt := inspectResult.Container.State.StartedAt
	if startedAt == "" {
		return time.Now().UnixMilli(), nil
	}

	// Parse RFC3339Nano format
	if startTime, err := time.Parse(time.RFC3339Nano, startedAt); err == nil {
		return startTime.UnixMilli(), nil
	}

	// Fallback to RFC3339
	if startTime, err := time.Parse(time.RFC3339, startedAt); err == nil {
		return startTime.UnixMilli(), nil
	}

	return time.Now().UnixMilli(), nil
}

// GetContainerInspectRaw returns raw Docker inspect JSON payload for a container.
func (c *Client) GetContainerInspectRaw(containerID string) ([]byte, error) {
	cli := c.GetClient()
	if cli == nil {
		return nil, errors.New("docker client not initialized")
	}
	ctx := c.GetContext()
	inspectResult, err := cli.ContainerInspect(ctx, containerID, mobyclient.ContainerInspectOptions{})
	if err != nil {
		return nil, err
	}
	return inspectResult.Raw, nil
}

// GetRunningNonIofogContainers returns running containers not managed by ioFog
func (c *Client) GetRunningNonIofogContainers() ([]Container, error) {
	containers, err := c.GetRunningContainers()
	if err != nil {
		return nil, err
	}

	result := make([]Container, 0)
	for _, cont := range containers {
		// Check if container name doesn't start with ioFog prefix
		name := c.GetContainerName(cont)
		if !strings.HasPrefix(name, utils.EdgeletDockerContainerNamePrefix) {
			result = append(result, cont)
		}
	}

	return result, nil
}

// GetRouterMicroserviceIP gets the IP address of a running router microservice container.
func (c *Client) GetRouterMicroserviceIP() (string, error) {
	cli := c.GetClient()
	if cli == nil {
		return "", errors.New("docker client not initialized")
	}
	ctx := c.GetContext()
	listResult, err := cli.ContainerList(ctx, mobyclient.ContainerListOptions{
		Filters: make(mobyclient.Filters).
			Add("label", workloadmeta.LabelRole+"="+workloadmeta.RoleRouter).
			Add("status", "running"),
	})
	if err != nil {
		return "", err
	}
	for _, cont := range listResult.Items {
		ip, ipErr := c.GetContainerIPAddress(cont.ID)
		if ipErr != nil || strings.TrimSpace(ip) == "" {
			continue
		}
		return ip, nil
	}
	return "", nil
}

// GetNatsMicroserviceIP gets the IP address of a running NATS microservice container.
func (c *Client) GetNatsMicroserviceIP() (string, error) {
	cli := c.GetClient()
	if cli == nil {
		return "", errors.New("docker client not initialized")
	}
	ctx := c.GetContext()
	listResult, err := cli.ContainerList(ctx, mobyclient.ContainerListOptions{
		Filters: make(mobyclient.Filters).
			Add("label", workloadmeta.LabelRole+"="+workloadmeta.RoleNats).
			Add("status", "running"),
	})
	if err != nil {
		return "", err
	}
	for _, cont := range listResult.Items {
		ip, ipErr := c.GetContainerIPAddress(cont.ID)
		if ipErr != nil || strings.TrimSpace(ip) == "" {
			continue
		}
		return ip, nil
	}
	return "", nil
}

// ParseAnnotationsString parses annotations JSON string into a map
func ParseAnnotationsString(annotationsString string) (map[string]string, error) {
	annotationsMap := make(map[string]string)
	if annotationsString == "" {
		return annotationsMap, nil
	}

	// Parse the JSON string
	var jsonMap map[string]any
	if err := json.Unmarshal([]byte(annotationsString), &jsonMap); err != nil {
		return nil, fmt.Errorf("failed to parse annotations JSON: %w", err)
	}

	// Convert to map[string]string
	for key, value := range jsonMap {
		if strValue, ok := value.(string); ok {
			annotationsMap[key] = strValue
		}
	}

	return annotationsMap, nil
}

// GetContainerMicroserviceUUID extracts microservice UUID from container name
func (c *Client) GetContainerMicroserviceUUID(cont Container) string {
	if len(cont.Names) == 0 {
		return ""
	}

	name := cont.Names[0]
	// Remove leading slash if present
	if len(name) > 0 && name[0] == '/' {
		name = name[1:]
	}

	// Remove prefix
	prefix := utils.EdgeletDockerContainerNamePrefix
	if len(name) > len(prefix) && name[:len(prefix)] == prefix {
		return name[len(prefix):]
	}

	return name
}

// GetContainerName extracts container name from container
func (c *Client) GetContainerName(cont Container) string {
	if len(cont.Names) == 0 {
		return ""
	}

	name := cont.Names[0]
	// Remove leading slash if present
	if len(name) > 0 && name[0] == '/' {
		return name[1:]
	}
	return name
}

// GetInspectContainersImage gets the image name from container inspection
func (c *Client) GetInspectContainersImage(containerID string) (string, error) {
	cli := c.GetClient()
	if cli == nil {
		return "", errors.New("docker client not initialized")
	}

	ctx := c.GetContext()
	inspectResult, err := cli.ContainerInspect(ctx, containerID, mobyclient.ContainerInspectOptions{})
	if err != nil {
		return "", err
	}

	return inspectResult.Container.Config.Image, nil
}

// GetMicroserviceStatus gets the microservice status from container inspection
func (c *Client) GetMicroserviceStatus(containerID, _ string) (*models.MicroserviceStatus, error) {
	cli := c.GetClient()
	if cli == nil {
		return nil, errors.New("docker client not initialized")
	}

	ctx := c.GetContext()
	inspectResult, err := cli.ContainerInspect(ctx, containerID, mobyclient.ContainerInspectOptions{})
	if err != nil {
		return nil, err
	}
	inspect := inspectResult.Container

	status := models.NewMicroserviceStatus()
	status.ContainerID = containerID

	// Map Docker state to microservice state
	state := string(inspect.State.Status)
	switch state {
	case "running":
		status.Status = models.MicroserviceStateRunning
	case "exited":
		status.Status = models.MicroserviceStateExiting
	case "created":
		status.Status = models.MicroserviceStateCreated
	case "restarting":
		status.Status = models.MicroserviceStateRestarting
	default:
		status.Status = models.MicroserviceStateFromText(state)
	}

	// Get start time
	if inspect.State.StartedAt != "" {
		// Parse time and convert to milliseconds
		// Docker uses RFC3339Nano format: "2006-01-02T15:04:05.999999999Z07:00"
		if startTime, err := time.Parse(time.RFC3339Nano, inspect.State.StartedAt); err == nil {
			status.StartTime = startTime.UnixMilli()
		} else {
			// Fallback: try RFC3339 format
			if startTime, err := time.Parse(time.RFC3339, inspect.State.StartedAt); err == nil {
				status.StartTime = startTime.UnixMilli()
			}
		}
	}

	// Get IP address
	if ip, err := c.GetContainerIPAddress(containerID); err == nil {
		status.IPAddress = &ip
	}

	// Get health status if available (for all container states)
	if inspect.State.Health != nil {
		healthStatus := string(inspect.State.Health.Status)
		status.HealthStatus = &healthStatus
	}

	if msg := formatContainerExitMessage(string(inspect.State.Status), inspect.State.ExitCode, inspect.State.OOMKilled, inspect.State.Error); msg != "" {
		status.ErrorMessage = &msg
	}

	// Note: RestartStuckChecker integration will be handled in ProcessManager
	// to avoid circular dependencies. The checker will be called after getting status
	// to determine if status should be STUCK_IN_RESTART

	return status, nil
}

// formatContainerExitMessage builds inspect failure text for Docker/Podman
// workloads. A healthy running container with exit 0, no OOM, and no engine
// error returns empty so recovery does not keep a stale current error.
func formatContainerExitMessage(status string, exitCode int, oomKilled bool, stateError string) string {
	if !shouldAttachContainerExitMessage(status, exitCode, oomKilled, stateError) {
		return ""
	}
	oom := "false"
	if oomKilled {
		oom = "true"
	}
	msg := fmt.Sprintf("exitCode=%d oomKilled=%s", exitCode, oom)
	if errText := strings.TrimSpace(stateError); errText != "" {
		msg += " error=" + errText
	}
	return strings.TrimSpace(msg)
}

func shouldAttachContainerExitMessage(status string, exitCode int, oomKilled bool, stateError string) bool {
	if oomKilled {
		return true
	}
	errText := strings.TrimSpace(stateError)
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running", "created":
		// created: only when an exit was already recorded. running: only when
		// inspect is not a healthy start (exit 0, empty error).
		return exitCode != 0 || errText != ""
	default:
		return true
	}
}

// AreMicroserviceAndContainerEqual checks if a microservice configuration matches a container
func (c *Client) AreMicroserviceAndContainerEqual(containerID string, ms *models.Microservice) bool {
	cli := c.GetClient()
	if cli == nil {
		return false
	}

	ctx := c.GetContext()
	inspectResult, err := cli.ContainerInspect(ctx, containerID, mobyclient.ContainerInspectOptions{})
	if err != nil {
		return false
	}
	inspect := inspectResult.Container

	// Check port mappings
	if !c.isPortMappingEqual(inspect, ms) {
		return false
	}

	// Check network mode
	if !c.isNetworkModeEqual(inspect, ms) {
		return false
	}

	// Check environment variables
	if !c.isEnvVarsEqual(inspect, ms) {
		return false
	}

	label := ""
	if inspect.Config != nil && inspect.Config.Labels != nil {
		label = inspect.Config.Labels[containerapply.LabelFingerprint]
	}
	if !containerapply.MatchesLabel(label, ms) {
		return false
	}

	return true
}

// isPortMappingEqual compares if microservice port mapping is equal to container port mapping
func (c *Client) isPortMappingEqual(inspect container.InspectResponse, ms *models.Microservice) bool {
	// Get microservice ports
	microservicePorts := c.getMicroservicePorts(ms)

	// Get container ports
	containerPorts := c.getContainerPorts(inspect)

	// Sort both lists for comparison
	sortPortMappings(microservicePorts)
	sortPortMappings(containerPorts)

	// Compare
	if len(microservicePorts) != len(containerPorts) {
		return false
	}

	for i := range microservicePorts {
		if !portMappingsEqual(microservicePorts[i], containerPorts[i]) {
			return false
		}
	}

	return true
}

// isNetworkModeEqual compares if microservice network mode matches container network mode.
// host-network microservice → container must have NetworkMode "host"
// non-host-network microservice → container must be on the "edgelet" user-defined bridge
func (c *Client) isNetworkModeEqual(inspect container.InspectResponse, ms *models.Microservice) bool {
	hostConfig := inspect.HostConfig
	if hostConfig == nil {
		return false
	}

	containerNetworkMode := string(hostConfig.NetworkMode)

	if ms.HostNetworkMode {
		return containerNetworkMode == "host"
	}

	expectedNetwork := resolveIofogBridgeNetworkName(ms.ApplicationName, ms.HostNetworkMode)
	return containerNetworkMode == expectedNetwork
}

// isEnvVarsEqual compares if microservice environment variables are equal to container environment variables
func (c *Client) isEnvVarsEqual(inspect container.InspectResponse, ms *models.Microservice) bool {
	// Get microservice environment variables
	microserviceEnvVars := ms.EnvVars
	if microserviceEnvVars == nil {
		microserviceEnvVars = make([]*models.EnvVar, 0)
	}

	// Get container environment variables from inspect info
	containerEnvArray := inspect.Config.Env
	containerEnvVars := make(map[string]string)
	for _, envVar := range containerEnvArray {
		if envVar != "" {
			parts := strings.SplitN(envVar, "=", 2)
			if len(parts) == 2 {
				containerEnvVars[parts[0]] = parts[1]
			}
		}
	}

	// Check if all microservice env vars exist in container with same values
	for _, microserviceEnvVar := range microserviceEnvVars {
		key := microserviceEnvVar.Key
		expectedValue := microserviceEnvVar.Value

		actualValue, exists := containerEnvVars[key]
		if !exists {
			return false
		}

		if expectedValue != actualValue {
			return false
		}
	}

	return true
}

// getMicroservicePorts gets port mappings from microservice
func (c *Client) getMicroservicePorts(ms *models.Microservice) []*models.PortMapping {
	if ms.PortMappings == nil {
		return make([]*models.PortMapping, 0)
	}
	return ms.PortMappings
}

// getContainerPorts extracts port mappings from container inspect info
func (c *Client) getContainerPorts(inspect container.InspectResponse) []*models.PortMapping {
	hostConfig := inspect.HostConfig
	if hostConfig == nil || hostConfig.PortBindings == nil {
		return make([]*models.PortMapping, 0)
	}

	portMappings := make([]*models.PortMapping, 0)
	for port, bindings := range hostConfig.PortBindings {
		if len(bindings) == 0 {
			continue
		}

		// Get protocol (tcp or udp)
		isUDP := port.Proto() == network.UDP
		insidePort := int(port.Num())

		// Process each binding
		for _, binding := range bindings {
			if binding.HostPort != "" {
				hostPort, err := strconv.Atoi(binding.HostPort)
				if err == nil {
					portMappings = append(portMappings, &models.PortMapping{
						Outside: hostPort,
						Inside:  insidePort,
						UDP:     isUDP,
					})
				}
			}
		}
	}

	return portMappings
}

// sortPortMappings sorts port mappings by inside port, then outside port
func sortPortMappings(ports []*models.PortMapping) {
	slices.SortFunc(ports, func(a, b *models.PortMapping) int {
		if c := cmp.Compare(a.Inside, b.Inside); c != 0 {
			return c
		}
		return cmp.Compare(a.Outside, b.Outside)
	})
}

// portMappingsEqual checks if two port mappings are equal
func portMappingsEqual(a, b *models.PortMapping) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Outside == b.Outside && a.Inside == b.Inside && a.UDP == b.UDP
}

func hostKey(extraHost string) (string, bool) {
	trimmed := strings.TrimSpace(extraHost)
	idx := strings.Index(trimmed, ":")
	if idx <= 0 {
		return "", false
	}
	key := strings.ToLower(strings.TrimSpace(trimmed[:idx]))
	if key == "" {
		return "", false
	}
	return key, true
}

func hasHostMapping(extraHosts []string, hostName string) bool {
	target := strings.ToLower(strings.TrimSpace(hostName))
	if target == "" {
		return false
	}
	for _, h := range extraHosts {
		if key, ok := hostKey(h); ok && key == target {
			return true
		}
	}
	return false
}

func appendHostMapping(extraHosts []string, hostName string, ip string) []string {
	hostName = strings.TrimSpace(hostName)
	ip = strings.TrimSpace(ip)
	if hostName == "" || ip == "" {
		return extraHosts
	}
	if hasHostMapping(extraHosts, hostName) {
		return extraHosts
	}
	return append(extraHosts, hostName+":"+ip)
}

// buildExtraHostsWithIoFog returns extraHosts with canonical agent host prepended for
// non-host-network containers, unless the user already has the same hostname entry.
func buildExtraHostsWithIoFog(extraHosts []string, hostIP string) []string {
	hostIP = strings.TrimSpace(hostIP)
	if hostIP != "" && !hasHostMapping(extraHosts, canonicalAgentHost) {
		return append([]string{canonicalAgentHost + ":" + hostIP}, extraHosts...)
	}
	return extraHosts
}

func appendCanonicalReservedHosts(extraHosts []string, routerIP string, natsIP string) []string {
	extraHosts = appendHostMapping(extraHosts, canonicalRouterHost, routerIP)
	extraHosts = appendHostMapping(extraHosts, canonicalNatsHost, natsIP)
	return extraHosts
}

// CreateContainer creates a container from microservice configuration
func (c *Client) CreateContainer(ms *models.Microservice, hostName string) (string, error) {
	cli := c.GetClient()
	if cli == nil {
		return "", errors.New("docker client not initialized")
	}

	// Ensure the "edgelet" bridge network exists before attempting container creation.
	// This guards against races where the network hasn't been created yet (e.g. after
	// a Docker client re-init)
	if !ms.HostNetworkMode {
		if err := c.ensureNetworkLockFree(c.GetContext(), c.GetClient()); err != nil {
			return "", fmt.Errorf("failed to ensure iofog network: %w", err)
		}
	}

	ctx := c.GetContext()

	// Get config instance (needed for TZ and other config values)
	cfg := config.GetInstance()

	labels, envVars := buildCanonicalContainerMetadata(ms, cfg)

	config := &container.Config{
		Image: ms.ImageName,
		Env:   envVars,
	}

	// Set user
	if ms.RunAsUser != nil && *ms.RunAsUser != "" {
		config.User = *ms.RunAsUser
	}

	// Platform is set via ContainerCreate options, not Config

	// Set canonical labels.
	config.Labels = labels

	// Build host config — NetworkMode is set later after ExtraHosts are resolved,
	// networkMode("edgelet") is only applied when extraHosts is non-empty.
	hostConfig := &container.HostConfig{
		Privileged:      ms.IsPrivileged,
		PublishAllPorts: false,
	}

	// Set runtime if specified
	if ms.Runtime != nil && *ms.Runtime != "" {
		hostConfig.Runtime = *ms.Runtime
	}

	// Set log configuration
	logFiles := 1
	if ms.LogSize > 2 {
		logFiles = int(ms.LogSize / 2)
	}
	hostConfig.LogConfig = container.LogConfig{
		Type: "json-file",
		Config: map[string]string{
			"max-file": fmt.Sprintf("%d", logFiles),
			"max-size": "100m",
		},
	}

	// Build port bindings
	if len(ms.PortMappings) > 0 {
		portBindings, err := buildPortBindings(ms.PortMappings)
		if err != nil {
			return "", err
		}
		hostConfig.PortBindings = portBindings
	}

	// Build volume bindings and mounts
	// Note: We need to handle VOLUME_MOUNT type specially
	if len(ms.VolumeMappings) > 0 {
		binds, mounts, err := buildVolumeBindsAndMounts(ms.VolumeMappings, ms.MicroserviceUUID)
		if err != nil {
			return "", err
		}
		if len(binds) > 0 {
			hostConfig.Binds = binds
		}
		if len(mounts) > 0 {
			hostConfig.Mounts = mounts
		}
	}

	// Build resource limits
	if ms.MemoryLimit != nil {
		hostConfig.Memory = *ms.MemoryLimit
	}

	// Build security options
	if len(ms.CapAdd) > 0 || len(ms.CapDrop) > 0 {
		hostConfig.CapAdd = ms.CapAdd
		hostConfig.CapDrop = ms.CapDrop
	}

	// Build extra hosts with canonical reserved names.
	extraHosts := buildExtraHostsWithIoFog(ms.ExtraHosts, hostName)

	routerIP := ""
	if !ms.HostNetworkMode && !ms.IsRouter {
		if !cfg.IsRouterInterior {
			resolvedRouterIP, err := c.GetRouterMicroserviceIP()
			if err == nil && resolvedRouterIP != "" {
				routerIP = resolvedRouterIP
			}
		} else {
			if hostName != "" {
				routerIP = hostName
			}
		}
	}
	natsIP := ""
	if !ms.HostNetworkMode {
		if resolvedNatsIP, err := c.GetNatsMicroserviceIP(); err == nil {
			natsIP = resolvedNatsIP
		}
	}
	extraHosts = appendCanonicalReservedHosts(extraHosts, routerIP, natsIP)

	// Filter and validate extra hosts
	validHosts := make([]string, 0)
	for _, host := range extraHosts {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		// Validate format: should be "hostname:ip" with non-empty IP
		colonIndex := strings.Index(host, ":")
		if colonIndex > 0 && colonIndex < len(host)-1 {
			ipPart := strings.TrimSpace(host[colonIndex+1:])
			if ipPart != "" {
				validHosts = append(validHosts, host)
			}
		}
	}
	// Apply network mode + extra hosts
	//   hostNetworkMode → NetworkMode "host"  (no ExtraHosts, no iofog network)
	//   else            → NetworkMode "edgelet" (always; ExtraHosts only when non-empty)
	//
	// All non-host-network containers must be on the "edgelet" user-defined bridge so that
	// Docker DNS aliases (service discovery) work correctly.  ExtraHosts are independent
	// — they add entries to /etc/hosts and are only set when there are valid entries.
	targetNetwork := resolveIofogBridgeNetworkName(ms.ApplicationName, ms.HostNetworkMode)
	if ms.HostNetworkMode {
		hostConfig.NetworkMode = container.NetworkMode("host")
	} else {
		hostConfig.NetworkMode = container.NetworkMode(targetNetwork)
		if len(validHosts) > 0 {
			hostConfig.ExtraHosts = validHosts
		}
	}

	// Set PID mode
	if ms.PidMode != nil {
		hostConfig.PidMode = container.PidMode(*ms.PidMode)
	}

	// Set IPC mode
	if ms.IpcMode != nil {
		hostConfig.IpcMode = container.IpcMode(*ms.IpcMode)
	}

	// Set CPU set
	if ms.CPUSetCpus != nil && *ms.CPUSetCpus != "" {
		hostConfig.CpusetCpus = *ms.CPUSetCpus
	}

	// Set CDI devices if specified
	if len(ms.CdiDevs) > 0 {
		deviceRequests := make([]container.DeviceRequest, 0)
		deviceRequest := container.DeviceRequest{
			Driver:    "cdi",
			DeviceIDs: ms.CdiDevs,
		}
		deviceRequests = append(deviceRequests, deviceRequest)
		hostConfig.DeviceRequests = deviceRequests
	}

	// Set annotations if specified
	if ms.Annotations != nil && *ms.Annotations != "" {
		annotations, err := ParseAnnotationsString(*ms.Annotations)
		if err == nil {
			// Docker doesn't have a direct annotations field in HostConfig
			// Annotations are typically set via labels or stored separately
			// For now, we'll add them to labels with a prefix
			if config.Labels == nil {
				config.Labels = make(map[string]string)
			}
			for key, value := range annotations {
				config.Labels["annotation."+key] = value
			}
		}
	}

	// Build health check
	// Note: Healthcheck is set in container.Config, not HostConfig
	if ms.Healthcheck != nil {
		healthConfig := buildHealthCheck(ms.Healthcheck)
		if healthConfig != nil {
			config.Healthcheck = healthConfig
		}
	}

	diskDir := ""
	if cfg != nil {
		diskDir = strings.TrimSpace(cfg.DiskDirectory)
	}
	for _, w := range applyWorkloadCreateConfig(config, hostConfig, ms, diskDir) {
		if c.logger != nil {
			c.logger.Warn(w)
		}
	}

	// ioFog MS lifecycle is owned by edgelet reconcile — not Docker RestartPolicy.
	hostConfig.RestartPolicy = container.RestartPolicy{
		Name: "no",
	}

	// Container name
	containerName := utils.EdgeletDockerContainerNamePrefix + ms.MicroserviceUUID

	// Build networking config with DNS alias for service discovery
	var networkingConfig *network.NetworkingConfig
	if !ms.HostNetworkMode {
		aliases := dnsresolver.WorkloadBridgeNetworkAliases(ms.ApplicationName, ms.MicroserviceName, ms.IsController)
		if len(aliases) > 0 {
			networkingConfig = &network.NetworkingConfig{
				EndpointsConfig: map[string]*network.EndpointSettings{
					targetNetwork: {
						Aliases: aliases,
					},
				},
			}
		}
	}

	// Create container
	createResp, err := cli.ContainerCreate(ctx, mobyclient.ContainerCreateOptions{
		Config:           config,
		HostConfig:       hostConfig,
		NetworkingConfig: networkingConfig,
		Name:             containerName,
	})
	if err != nil {
		return "", err
	}

	return createResp.ID, nil
}

// Helper functions for building Docker configurations

func envVarMap(envVars []*models.EnvVar) map[string]string {
	result := make(map[string]string, len(envVars))
	for _, env := range envVars {
		if env.Key != "" || env.Value != "" {
			result[env.Key] = env.Value
		}
	}
	return result
}

func buildCanonicalContainerMetadata(ms *models.Microservice, cfg *config.Config) (map[string]string, []string) {
	in := workloadmeta.BuildInput{
		MicroserviceUUID: ms.MicroserviceUUID,
		MicroserviceName: ms.MicroserviceName,
		ApplicationName:  ms.ApplicationName,
		NodeUUID:         cfg.IOFogUUID,
		RuntimeEngine:    workloadmeta.RuntimeEngineDocker,
		IsRouter:         ms.IsRouter,
		IsNats:           ms.IsNats,
		IsController:     ms.IsController,
		HostNetwork:      ms.HostNetworkMode,
		IsSystem:         ms.IsSystem || ms.IsController,
		TimeZone:         cfg.TimeZone,
		UserEnv:          envVarMap(ms.EnvVars),
		UserLabels:       ms.Labels,
	}
	return workloadmeta.BuildLabels(in), workloadmeta.BuildEnv(in)
}

func buildPortBindings(portMappings []*models.PortMapping) (network.PortMap, error) {
	if len(portMappings) == 0 {
		return nil, nil
	}

	bindings := make(network.PortMap)
	for _, pm := range portMappings {
		proto := network.TCP
		if pm.UDP {
			proto = network.UDP
		}

		port, ok := network.PortFrom(uint16(pm.Inside), proto) // #nosec G115 -- port numbers are validated upstream
		if !ok {
			return nil, fmt.Errorf("invalid container port %d", pm.Inside)
		}

		binding := network.PortBinding{
			HostPort: fmt.Sprintf("%d", pm.Outside),
		}

		bindings[port] = append(bindings[port], binding)
	}

	return bindings, nil
}

func buildVolumeBindsAndMounts(volumeMappings []*models.VolumeMapping, microserviceUUID string) ([]string, []mount.Mount, error) {
	if len(volumeMappings) == 0 {
		return nil, nil, nil
	}

	binds := make([]string, 0)
	mounts := make([]mount.Mount, 0)

	for _, vm := range volumeMappings {
		// Resolve host destination for volume mounts
		resolvedHostDestination := vm.HostDestination
		if vm.Type == models.VolumeMappingTypeVolumeMount {
			// Use volume mount resolution (will be implemented in volume.go)
			var err error
			resolvedHostDestination, err = ResolveVolumeMountPath(vm.HostDestination, vm.Type, microserviceUUID)
			if err != nil {
				return nil, nil, fmt.Errorf("failed to resolve volume mount path: %w", err)
			}
		}

		// Determine access mode
		isReadOnly := strings.ToLower(vm.AccessMode) == "ro"

		switch vm.Type {
		case models.VolumeMappingTypeVolumeMount:
			// Use Mount API for VOLUME_MOUNT type
			m := mount.Mount{
				Type:     mount.TypeBind,
				Source:   resolvedHostDestination,
				Target:   vm.ContainerDestination,
				ReadOnly: isReadOnly,
			}
			mounts = append(mounts, m)
		case models.VolumeMappingTypeBind, models.VolumeMappingTypeVolume:
			// Use bind mount (legacy format; named volumes handled separately)
			bind := fmt.Sprintf("%s:%s:%s", resolvedHostDestination, vm.ContainerDestination, vm.AccessMode)
			binds = append(binds, bind)
		}
	}

	return binds, mounts, nil
}

func buildHealthCheck(hc *models.Healthcheck) *container.HealthConfig {
	if hc == nil {
		return nil
	}

	config := &container.HealthConfig{
		Test: hc.Test,
	}

	if hc.Interval != nil {
		// Convert seconds to nanoseconds
		config.Interval = time.Duration(*hc.Interval) * time.Second
	}
	if hc.Timeout != nil {
		// Convert seconds to nanoseconds
		config.Timeout = time.Duration(*hc.Timeout) * time.Second
	}
	if hc.StartPeriod != nil {
		// Convert seconds to nanoseconds
		config.StartPeriod = time.Duration(*hc.StartPeriod) * time.Second
	}
	// Note: StartInterval is not supported in Docker's HealthConfig
	// It's a Docker Compose feature that's not part of the Docker API
	if hc.Retries != nil {
		config.Retries = *hc.Retries
	}

	return config
}
