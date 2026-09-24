package processmanager

import (
	"sync"
	"time"
)

const (
	// IntervalInMinutes is the sliding window used to detect restart loops.
	IntervalInMinutes = 10
	// AbnormalNumberOfRestarts is how many recorded crash/recreate events
	// inside the window mark a microservice stuck.
	AbnormalNumberOfRestarts = 10

	restartBackoffBase = 10 * time.Second
	restartBackoffCap  = 300 * time.Second
)

// recreateSkip is why a crash-driven recreate may run without waiting.
type recreateSkip int

const (
	recreateSkipNone recreateSkip = iota
	recreateSkipRebuild
	recreateSkipCatalogReady
	recreateSkipNonRestartable
)

type uuidRestartState struct {
	n             int
	nextAttemptAt time.Time
	pending       bool
	criSkipUsed   bool
	catalogWait   bool
}

// RestartStuckChecker tracks real crash/recreate events and consecutive-failure backoff.
type RestartStuckChecker struct {
	restarts          map[string][]time.Time
	containerCreation map[string][]time.Time
	state             map[string]*uuidRestartState
	parked            map[string]*ContainerTask
	now               func() time.Time
	mu                sync.Mutex
}

var (
	restartCheckerInstance *RestartStuckChecker
	restartCheckerOnce     sync.Once
)

// GetRestartStuckChecker returns the singleton RestartStuckChecker instance
func GetRestartStuckChecker() *RestartStuckChecker {
	restartCheckerOnce.Do(func() {
		restartCheckerInstance = newRestartStuckChecker(time.Now)
	})
	return restartCheckerInstance
}

func newRestartStuckChecker(now func() time.Time) *RestartStuckChecker {
	if now == nil {
		now = time.Now
	}
	return &RestartStuckChecker{
		restarts:          make(map[string][]time.Time),
		containerCreation: make(map[string][]time.Time),
		state:             make(map[string]*uuidRestartState),
		parked:            make(map[string]*ContainerTask),
		now:               now,
	}
}

func resetRestartStuckCheckerForTest(now func() time.Time) {
	c := GetRestartStuckChecker()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.restarts = make(map[string][]time.Time)
	c.containerCreation = make(map[string][]time.Time)
	c.state = make(map[string]*uuidRestartState)
	c.parked = make(map[string]*ContainerTask)
	if now != nil {
		c.now = now
	} else {
		c.now = time.Now
	}
}

func restartDelay(n int) time.Duration {
	if n <= 0 {
		return 0
	}
	shift := n - 1
	if shift >= 5 {
		return restartBackoffCap
	}
	return restartBackoffBase << shift
}

func pruneTimestamps(dates []time.Time, cutoff time.Time) []time.Time {
	filtered := make([]time.Time, 0, len(dates))
	for _, date := range dates {
		if date.After(cutoff) {
			filtered = append(filtered, date)
		}
	}
	return filtered
}

func (r *RestartStuckChecker) currentState(uuid string) *uuidRestartState {
	st := r.state[uuid]
	if st == nil {
		st = &uuidRestartState{}
		r.state[uuid] = st
	}
	return st
}

func (r *RestartStuckChecker) armFailureLocked(uuid string) {
	st := r.currentState(uuid)
	st.n++
	st.nextAttemptAt = r.now().Add(restartDelay(st.n))
}

func (r *RestartStuckChecker) appendLocked(bucket map[string][]time.Time, uuid string) {
	now := r.now()
	cutoff := now.Add(-IntervalInMinutes * time.Minute)
	filtered := pruneTimestamps(bucket[uuid], cutoff)
	bucket[uuid] = append(filtered, now)
}

// IsStuck reports whether the microservice has enough recorded crash/recreate
// events in the sliding window. It does not record a new event.
func (r *RestartStuckChecker) IsStuck(microserviceUUID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	cutoff := r.now().Add(-IntervalInMinutes * time.Minute)
	filtered := pruneTimestamps(r.restarts[microserviceUUID], cutoff)
	r.restarts[microserviceUUID] = filtered
	return len(filtered) >= AbnormalNumberOfRestarts
}

// IsStuckInContainerCreation reports whether start/create attempts in the
// sliding window exceeded the threshold. It does not record a new attempt.
func (r *RestartStuckChecker) IsStuckInContainerCreation(microserviceUUID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	cutoff := r.now().Add(-IntervalInMinutes * time.Minute)
	filtered := pruneTimestamps(r.containerCreation[microserviceUUID], cutoff)
	r.containerCreation[microserviceUUID] = filtered
	return len(filtered) >= AbnormalNumberOfRestarts
}

// RecordIfNew records a crash/recreate event once per episode. A later
// ObserveRunning (or rebuild / stable-running reset) starts a new episode.
func (r *RestartStuckChecker) RecordIfNew(microserviceUUID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.currentState(microserviceUUID)
	if st.pending {
		return false
	}
	r.appendLocked(r.restarts, microserviceUUID)
	st.pending = true
	r.armFailureLocked(microserviceUUID)
	return true
}

// RecordFailedStart records a failed start or container-create attempt.
func (r *RestartStuckChecker) RecordFailedStart(microserviceUUID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.appendLocked(r.containerCreation, microserviceUUID)
	r.currentState(microserviceUUID).pending = true
	r.armFailureLocked(microserviceUUID)
}

// ObserveRunning clears the in-episode crash flag so the next crash is a new event.
func (r *RestartStuckChecker) ObserveRunning(microserviceUUID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if st := r.state[microserviceUUID]; st != nil {
		st.pending = false
	}
}

// ResetBackoff clears consecutive-failure delay after stable running or rebuild.
func (r *RestartStuckChecker) ResetBackoff(microserviceUUID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.state, microserviceUUID)
	delete(r.parked, microserviceUUID)
}

// ResetAfterRebuild clears stuck timestamps and backoff so the operator retry can proceed.
func (r *RestartStuckChecker) ResetAfterRebuild(microserviceUUID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.restarts, microserviceUUID)
	delete(r.containerCreation, microserviceUUID)
	delete(r.state, microserviceUUID)
	delete(r.parked, microserviceUUID)
}

// Ready reports whether a crash-driven recreate may be enqueued now.
func (r *RestartStuckChecker) Ready(microserviceUUID string, skip recreateSkip) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.currentState(microserviceUUID)
	switch skip {
	case recreateSkipRebuild, recreateSkipCatalogReady:
		st.nextAttemptAt = time.Time{}
		return true
	case recreateSkipNonRestartable:
		if !st.criSkipUsed {
			st.criSkipUsed = true
			st.nextAttemptAt = time.Time{}
			return true
		}
	}
	if !st.nextAttemptAt.IsZero() && r.now().Before(st.nextAttemptAt) {
		return false
	}
	return true
}

// RemainingDelay is how long crash-driven recreate should still wait.
func (r *RestartStuckChecker) RemainingDelay(microserviceUUID string) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.state[microserviceUUID]
	if st == nil || st.nextAttemptAt.IsZero() {
		return 0
	}
	rem := st.nextAttemptAt.Sub(r.now())
	if rem < 0 {
		return 0
	}
	return rem
}

// ConsecutiveFailures is the in-memory consecutive failure count n.
func (r *RestartStuckChecker) ConsecutiveFailures(microserviceUUID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if st := r.state[microserviceUUID]; st != nil {
		return st.n
	}
	return 0
}

// MarkCatalogWaiting notes that start is blocked on catalog becoming Ready.
func (r *RestartStuckChecker) MarkCatalogWaiting(microserviceUUID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.currentState(microserviceUUID).catalogWait = true
}

// ConsumeCatalogReadySkip returns true once when catalog transitions from waiting to Ready.
func (r *RestartStuckChecker) ConsumeCatalogReadySkip(microserviceUUID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	st := r.state[microserviceUUID]
	if st == nil || !st.catalogWait {
		return false
	}
	st.catalogWait = false
	return true
}

// ArmFailure increments consecutive failures and returns the delay until the next attempt.
func (r *RestartStuckChecker) ArmFailure(microserviceUUID string) time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.armFailureLocked(microserviceUUID)
	return restartDelay(r.currentState(microserviceUUID).n)
}

// ParkTask holds a task retry until DelayUntil elapses.
func (r *RestartStuckChecker) ParkTask(task *ContainerTask) {
	if task == nil || task.MicroserviceUUID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.parked[task.MicroserviceUUID] = task
}

// TakeDueTasks returns parked retries whose wait has elapsed.
func (r *RestartStuckChecker) TakeDueTasks() []*ContainerTask {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	due := make([]*ContainerTask, 0, len(r.parked))
	for uuid, task := range r.parked {
		st := r.state[uuid]
		if st != nil && !st.nextAttemptAt.IsZero() && now.Before(st.nextAttemptAt) {
			continue
		}
		due = append(due, task)
		delete(r.parked, uuid)
	}
	return due
}

// Clear removes all tracking data for a microservice (useful for cleanup)
func (r *RestartStuckChecker) Clear(microserviceUUID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	delete(r.restarts, microserviceUUID)
	delete(r.containerCreation, microserviceUUID)
	delete(r.state, microserviceUUID)
	delete(r.parked, microserviceUUID)
}
