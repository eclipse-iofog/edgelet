# Edgelet documentation

Operator and developer documentation for the Edgelet edge agent.

## Architecture and deployment

| Document | Description |
|----------|-------------|
| [architecture.md](architecture.md) | Module layout, data flows, EdgeletAPI vs Controller API |
| [modules/README.md](modules/README.md) | Runtime module deep dives (all tiers) |
| [installation.md](installation.md) | install.sh, OTA, upgrade/rollback, controller readiness |
| [deployment.md](deployment.md) | Production topology, engines, systemd, provisioning |
| [persistence.md](persistence.md) | SQLite backup/restore, schema v4 in-place upgrade, secrets threat model |
| [troubleshooting.md](troubleshooting.md) | Daemon, containerd, auth, CLI connectivity, leftover volume locks, microservice crashes |
| [logging.md](logging.md) | Structured events, log levels, journald queries |

## Runtime and workloads

| Document | Description |
|----------|-------------|
| [container-engine.md](container-engine.md) | `edgelet` / `docker` / `podman` engines, CNI, RuntimeClass, containerd config drop-ins, CDI |
| [dns.md](dns.md) | Bridge DNS, embedded resolver, docker/podman aliases and ExtraHosts |
| [workload-metadata.md](workload-metadata.md) | Container labels and `EDGELET_*` env contract |
| [workload-continuity.md](workload-continuity.md) | Reconcile behavior across restarts and engine changes |
| [volumes.md](volumes.md) | Persistent VOLUME retain/reclaim, private vs shared, BIND |
| [edgeguard.md](edgeguard.md) | Hardware attestation (`edgeGuardFrequency`) |
| [control-plane.md](control-plane.md) | Local Datasance Controller deployment |
| [exec-sessions.md](exec-sessions.md) | Multi-session exec (local CLI and controller-initiated) |
| [manifest-reference.md](manifest-reference.md) | Deploy YAML (`Microservice`, `Registry`, `Model`, `Knowledge`, `RuntimeClass`, `ControlPlane`) |
| [models.md](models.md) | Model artifact pull, catalog bind, prune, on-disk layout |
| [knowledge.md](knowledge.md) | Knowledge artifact pull, catalog bind, prune, on-disk layout |
| [oci-artifacts.md](oci-artifacts.md) | Publish Model or Knowledge as an OCI / ORAS artifact |
| [examples/](examples/) | Reference manifest YAML samples |

## EdgeletAPI

| Document | Description |
|----------|-------------|
| [edgelet-api-v1.md](edgelet-api-v1.md) | Operator guide — transport, auth, errors, route behavior |
| [edgelet-api-v1-openapi.yaml](edgelet-api-v1-openapi.yaml) | OpenAPI 3.1 contract |
| [edgelet-api-v1-rbac-resources.md](edgelet-api-v1-rbac-resources.md) | RBAC resource/verb mapping |
| [CONTROLLER-HANDOFF-MODELS.md](CONTROLLER-HANDOFF-MODELS.md) | Controller contract: HAL drop, RuntimeClass, status keys, catalog flag, image TLS, prune, microservice last-crash extras |
| [CONTROLLER-HANDOFF-KNOWLEDGE.md](CONTROLLER-HANDOFF-KNOWLEDGE.md) | Controller contract: Knowledge GET, catalog flag, status keys, prune |
| [CONTROLLER-HANDOFF-VOLUMES.md](CONTROLLER-HANDOFF-VOLUMES.md) | Controller contract: additive `volumeMappings[].scope` (`private` \| `shared`) |

## Migration

| Document | Description |
|----------|-------------|
| [migration-from-iofog-agent-cli.md](migration-from-iofog-agent-cli.md) | Legacy CLI → `edgelet` command mapping |

## CLI reference

| Resource | Path |
|----------|------|
| CLI overview | [../cli/README.md](../cli/README.md) |
| JSON/YAML output shapes | [../cli/output-schemas.md](../cli/output-schemas.md) |
| Generated per-command pages | [../cli/generated/](../cli/generated/) |

## Legal

| Document | Path |
|----------|------|
| License (EPL-2.0) | [../../LICENSE](../../LICENSE) |
| Copyright notice | [../../NOTICE](../../NOTICE) |
| Maintainers | [../../MAINTAINERS](../../MAINTAINERS) |
