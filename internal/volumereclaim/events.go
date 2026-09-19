package volumereclaim

import (
	"context"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/runtimeops"
)

func (r *Reclaimer) emit(ev runtimeops.RuntimeEvent) {
	if ev.Module == "" {
		ev.Module = moduleName
	}
	if ev.Event == "" {
		ev.Event = runtimeops.EventVolumeReclaim
	}
	if ev.Source == "" {
		ev.Source = runtimeops.SourceAPI
	}
	runtimeops.Emit(context.Background(), ev)
}

func (r *Reclaimer) emitAbort(reason, message, trigger string, err error, fields map[string]any, start time.Time) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["trigger"] = trigger
	ev := runtimeops.RuntimeEvent{
		Level:      runtimeops.LevelError,
		ReasonCode: reason,
		Message:    message,
		Result:     runtimeops.ResultFailed,
		DurationMs: time.Since(start).Milliseconds(),
		Fields:     fields,
	}
	if err != nil {
		ev.Error = err.Error()
	}
	r.emit(ev)
}

func (r *Reclaimer) emitDeleted(message, trigger string, fields map[string]any, start time.Time) {
	if fields == nil {
		fields = map[string]any{}
	}
	fields["trigger"] = trigger
	r.emit(runtimeops.RuntimeEvent{
		Level:      runtimeops.LevelInfo,
		Message:    message,
		Result:     runtimeops.ResultOK,
		DurationMs: time.Since(start).Milliseconds(),
		Fields:     fields,
	})
}
