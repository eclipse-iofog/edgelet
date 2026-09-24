# CRI Implementation Coverage Checklist

## Microservice Fields → CRI Mapping

| Microservice Field | CRI Equivalent | Status |
|--------------------|----------------|--------|
| PortMappings | PodSandboxConfig.PortMappings | DONE |
| HostNetworkMode | PodSandboxConfig.Linux.SecurityContext.NamespaceOptions.Network=NODE | DONE |
| ImageName | ContainerConfig.Image | DONE |
| EnvVars | ContainerConfig.Envs | DONE |
| Args | ContainerConfig.Args | DONE |
| VolumeMappings | ContainerConfig.Mounts | DONE |
| ExtraHosts | buildHostsFile + Mount /etc/hosts | DONE |
| IsPrivileged | LinuxContainerSecurityContext.Privileged | DONE |
| Runtime | runtimeHandler (`crun`/`crun-local`) | DONE |
| CapAdd | LinuxContainerSecurityContext.Capabilities.AddCapabilities | DONE |
| CapDrop | LinuxContainerSecurityContext.Capabilities.DropCapabilities | DONE |
| RunAsUser | LinuxContainerSecurityContext.RunAsUser / RunAsUsername | DONE |
| PidMode (host) | NamespaceOptions.Pid=NODE | DONE |
| IpcMode (host) | NamespaceOptions.Ipc=NODE | DONE |
| CPUSetCpus | LinuxContainerResources.CpusetCpus | DONE |
| MemoryLimit | LinuxContainerResources.MemoryLimitInBytes | DONE |
| Annotations | ContainerConfig.Annotations | DONE |
| CdiDevs | ContainerConfig.CDIDevices | DONE |
| Healthcheck | `iofog.org/healthcheck` label (JSON); exec runner | DONE (labels) |
| Catalog bind (`bindPath` + permissions) | One bind of the per-MS models projection | DONE |
| Entrypoint / commands (omit or `[]` = image default) | ContainerConfig.Command / Args | DONE |
| WorkingDir | ContainerConfig.WorkingDir | DONE |
| RunAsGroup | LinuxContainerSecurityContext.RunAsGroup | DONE |
| ReadOnlyRootFilesystem | LinuxContainerSecurityContext.ReadonlyRootfs | DONE |
| Tmpfs | Host tmpfs + CRI bind mount | DONE |
| ShmSize | Sized host tmpfs bind at `/dev/shm` | DONE |
| Cpus | CpuQuota / CpuPeriod (period 100000) | DONE |
| MemoryReservation | LinuxContainerResources.Unified `memory.low` | DONE |
| MemorySwap | MemorySwapLimitInBytes (`-1` unlimited) | DONE |
| Sysctls | LinuxPodSandboxConfig.Sysctls | DONE |
| Ulimits | OCI Process.Rlimits after CRI create | DONE |
| Devices | ContainerConfig.Devices | DONE |

## Pod Sandbox

| Feature | Status |
|---------|--------|
| Hostname | DONE |
| Log directory | DONE |
| Port mappings | DONE |
| Host network (NamespaceOptions) | DONE |
| Annotations on sandbox | TO FIX (optional) |

## Container Lifecycle

| Operation | Status |
|-----------|--------|
| CreateContainer (RunPodSandbox + CreateContainer) | DONE |
| StartContainer | DONE (CRI) |
| StopContainer | DONE (CRI StopContainer) |
| RemoveContainer | DONE (CRI teardown) |

## Log Tailing

| Aspect | Status |
|--------|--------|
| CRI writes to LogDirectory+LogPath | DONE |
| Containerd CRI uses single file for stdout+stderr | DONE |
| Parse CRI log format (timestamp stream flag msg) | DONE |
| Fallback for stdout.log/stderr.log if separate | DONE |

## Exec Session

| Aspect | Status |
|--------|--------|
| CRI-created containers are containerd containers | DONE |
| task.Exec works for CRI containers | DONE (containerd client) |
| Alternative: CRI Exec (streaming URL) | Optional |
