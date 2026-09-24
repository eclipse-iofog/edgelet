package processmanager

import (
	"strings"
	"sync"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/statusreporter"
)

// errorClearGrace is how long a microservice must stay RUNNING before the
// current errorMessage is cleared. lastError is kept.
const errorClearGrace = 30 * time.Second

type runningGraceTracker struct {
	mu           sync.Mutex
	runningSince map[string]time.Time
	containerID  map[string]string
	now          func() time.Time
	grace        time.Duration
}

func newRunningGraceTracker() *runningGraceTracker {
	return &runningGraceTracker{
		runningSince: make(map[string]time.Time),
		containerID:  make(map[string]string),
		now:          time.Now,
		grace:        errorClearGrace,
	}
}

func (t *runningGraceTracker) reset(now func() time.Time, grace time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.runningSince = make(map[string]time.Time)
	t.containerID = make(map[string]string)
	if now != nil {
		t.now = now
	} else {
		t.now = time.Now
	}
	if grace > 0 {
		t.grace = grace
	} else {
		t.grace = errorClearGrace
	}
}

func (t *runningGraceTracker) observe(uuid string, prevState models.MicroserviceState, prevContainerID string, next *models.MicroserviceStatus) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if next == nil || next.Status != models.MicroserviceStateRunning {
		delete(t.runningSince, uuid)
		delete(t.containerID, uuid)
		return
	}
	nextCID := strings.TrimSpace(next.ContainerID)
	_, haveClock := t.runningSince[uuid]
	newGeneration := nextCID != "" && strings.TrimSpace(prevContainerID) != "" && nextCID != strings.TrimSpace(prevContainerID)
	if !haveClock || prevState != models.MicroserviceStateRunning || newGeneration {
		t.runningSince[uuid] = t.now()
		t.containerID[uuid] = nextCID
	}
}

func (t *runningGraceTracker) elapsed(uuid string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	since, ok := t.runningSince[uuid]
	if !ok {
		return false
	}
	return t.now().Sub(since) >= t.grace
}

func (t *runningGraceTracker) nowMilli() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.now().UnixMilli()
}

var statusGrace = newRunningGraceTracker()

func resetStatusSyncStateForTest(now func() time.Time) {
	statusGrace.reset(now, errorClearGrace)
}

func setStatusSyncGraceForTest(d time.Duration) {
	statusGrace.reset(statusGrace.now, d)
}

func explicitEmptyErrorMessage() *string {
	empty := ""
	return &empty
}

// AdvanceRunningErrorClear clears the current error text once a workload has
// stayed running for the grace period. It uses the status already stored and
// does not inspect the runtime. The usage sampler can call this on its own loop.
func AdvanceRunningErrorClear() {
	statusreporter.GetInstance().UpdateProcessManagerStatus(func(s *models.ProcessManagerStatus) {
		if s == nil {
			return
		}
		for uuid, st := range s.MicroservicesStatus {
			if st == nil || st.Status != models.MicroserviceStateRunning {
				continue
			}
			if st.ErrorMessage == nil || strings.TrimSpace(*st.ErrorMessage) == "" {
				continue
			}
			if !statusGrace.elapsed(uuid) {
				continue
			}
			st.ErrorMessage = explicitEmptyErrorMessage()
		}
	})
}

// syncMicroserviceStatusToReporter stores runtime status for Pot reporting.
// Current errorMessage is kept until the microservice has been continuously
// RUNNING for errorClearGrace; lastError is overwritten only on a new failure.
func syncMicroserviceStatusToReporter(pmStatus *models.ProcessManagerStatus, uuid string, status *models.MicroserviceStatus) {
	if pmStatus == nil || uuid == "" || status == nil {
		return
	}
	prev := pmStatus.LookupMicroserviceStatus(uuid)
	prevState := models.MicroserviceStateUnknown
	prevCID := ""
	var prevErr *string
	prevLast := ""
	prevLastAt := int64(0)
	prevRestarts := 0
	if prev != nil {
		prevState = prev.Status
		prevCID = prev.ContainerID
		prevErr = prev.ErrorMessage
		prevLast = prev.LastError
		prevLastAt = prev.LastErrorAt
		prevRestarts = prev.RestartCount
	}

	statusGrace.observe(uuid, prevState, prevCID, status)

	status.RestartCount = prevRestarts
	engineMsg := ""
	if status.ErrorMessage != nil {
		engineMsg = strings.TrimSpace(*status.ErrorMessage)
	}
	if engineMsg != "" {
		status.LastError = engineMsg
		status.LastErrorAt = statusGrace.nowMilli()
	} else {
		status.LastError = prevLast
		status.LastErrorAt = prevLastAt
		if status.Status == models.MicroserviceStateRunning && statusGrace.elapsed(uuid) {
			status.ErrorMessage = explicitEmptyErrorMessage()
		} else {
			status.ErrorMessage = prevErr
		}
	}

	pmStatus.SetMicroservicesStatus(uuid, status)
}
