// Package runtimeops provides structured runtime lifecycle logging for ProcessManager and container engines.
package runtimeops

// RuntimeEvent is a structured log record for container runtime operations.
// Fields are merged with context (operationId, engine, msUUID) at emit time.
type RuntimeEvent struct {
	Event       string
	Level       string
	Module      string
	OperationID string
	Engine      string
	MsUUID      string
	ContainerID string
	SandboxID   string
	Image       string
	Phase       string
	Result      string
	DurationMs  int64
	ReasonCode  string
	Source      string
	Message     string
	Error       string
	Fields      map[string]any
}

// ProcessManager / ContainerManager events (typically Info).
const (
	EventPMStarted = "pm.started"
	EventPMStopped = "pm.stopped"

	EventTaskEnqueued  = "task.enqueued"
	EventTaskStarted   = "task.started"
	EventTaskCompleted = "task.completed"
	EventTaskFailed    = "task.failed"
	EventTaskRetry     = "task.retry"

	EventReconcileCycle    = "reconcile.cycle"
	EventReconcileDecision = "reconcile.decision"

	EventContainerPulling       = "container.pulling"
	EventContainerPullCompleted = "container.pull.completed"
	EventContainerCreating      = "container.creating"
	EventContainerCreated       = "container.created"
	EventContainerStarting      = "container.starting"
	EventContainerStarted       = "container.started"
	EventContainerStopping      = "container.stopping"
	EventContainerStopped       = "container.stopped"
	EventContainerRemoving      = "container.removing"
	EventContainerRemoved       = "container.removed"
	EventContainerUpdatePhase   = "container.update.phase"
	EventContainerDrift         = "container.drift"

	EventShutdownDrain = "shutdown.drain"
)

// Engine events (typically Debug unless failure).
const (
	EventEngineInit                = "engine.init"
	EventEngineCRISandboxCreated   = "engine.cri.sandbox.created"
	EventEngineCRIContainerCreated = "engine.cri.container.created"
	EventEngineContainerStart      = "engine.container.start"
	EventEngineContainerStop       = "engine.container.stop"
	EventEngineContainerRemove     = "engine.container.remove"
	EventEngineImagePulled         = "engine.image.pulled"
	EventEnginePrune               = "engine.prune"
	EventVolumeReclaim             = "volume.reclaim"

	EventContainerRuntimeEvent = "container.runtime.event"
)

// Log levels passed to the logging backend.
const (
	LevelDebug = "debug"
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// Stable reason codes for alerting and SIEM rules.
const (
	ReasonPullFailed           = "PULL_FAILED"
	ReasonPullCacheFallback    = "PULL_CACHE_FALLBACK"
	ReasonCreateFailed         = "CREATE_FAILED"
	ReasonStartFailed          = "START_FAILED"
	ReasonNonRestartableExit   = "NON_RESTARTABLE_EXIT"
	ReasonStopFailed           = "STOP_FAILED"
	ReasonRemoveFailed         = "REMOVE_FAILED"
	ReasonRuntimeDrift         = "RUNTIME_DRIFT"
	ReasonStuckInRestart       = "STUCK_IN_RESTART"
	ReasonTaskExhaustedRetries = "TASK_EXHAUSTED_RETRIES"
	ReasonShutdownDrainTimeout = "SHUTDOWN_DRAIN_TIMEOUT"
	ReasonVolumeKeepSet        = "VOLUME_KEEP_SET"
	ReasonVolumeInUse          = "VOLUME_IN_USE"
	ReasonVolumePathJail       = "VOLUME_PATH_JAIL"
)

// Source identifies who initiated the operation.
const (
	SourceTask         = "task"
	SourceReconcile    = "reconcile"
	SourceShutdown     = "shutdown"
	SourceRuntimeWatch = "runtime_watch"
	SourceAPI          = "api"
	SourceWatchdog     = "watchdog"
)

// Result values for lifecycle events.
const (
	ResultOK      = "ok"
	ResultFailed  = "failed"
	ResultSkipped = "skipped"
)
