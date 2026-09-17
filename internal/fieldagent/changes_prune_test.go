package fieldagent

import (
	"sync/atomic"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/config"
)

func TestProcessChanges_PruneFlagInvokesImageAndModelPrune(t *testing.T) {
	var imageCalls, modelCalls atomic.Int32
	fa := &FieldAgent{
		config: config.GetInstance(),
		state:  NewState(),
		pruneImagesFn: func() error {
			imageCalls.Add(1)
			return nil
		},
		pruneModelsFn: func() error {
			modelCalls.Add(1)
			return nil
		},
	}
	fa.state.SetInitialization(false)

	_ = fa.processChanges(map[string]any{"prune": true})
	if imageCalls.Load() != 1 || modelCalls.Load() != 1 {
		t.Fatalf("expected image and model prune once, images=%d models=%d", imageCalls.Load(), modelCalls.Load())
	}
}

func TestProcessChanges_PruneFlagSkippedDuringInitialization(t *testing.T) {
	var imageCalls, modelCalls atomic.Int32
	fa := &FieldAgent{
		config: config.GetInstance(),
		state:  NewState(),
		pruneImagesFn: func() error {
			imageCalls.Add(1)
			return nil
		},
		pruneModelsFn: func() error {
			modelCalls.Add(1)
			return nil
		},
	}
	fa.state.SetInitialization(true)

	_ = fa.processChanges(map[string]any{"prune": true})
	if imageCalls.Load() != 0 || modelCalls.Load() != 0 {
		t.Fatalf("expected prune skipped during initialization, images=%d models=%d", imageCalls.Load(), modelCalls.Load())
	}
}
