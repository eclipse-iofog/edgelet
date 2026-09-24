package fieldagent

import (
	"sync/atomic"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/config"
)

func TestProcessChanges_PruneFlagInvokesImageAndModelPrune(t *testing.T) {
	var imageCalls, modelCalls, knowledgeCalls atomic.Int32
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
		pruneKnowledgeFn: func() error {
			knowledgeCalls.Add(1)
			return nil
		},
	}
	fa.state.SetInitialization(false)

	_ = fa.processChanges(map[string]any{"prune": true})
	if imageCalls.Load() != 1 || modelCalls.Load() != 1 || knowledgeCalls.Load() != 1 {
		t.Fatalf("expected image, model, and knowledge prune once, images=%d models=%d knowledge=%d", imageCalls.Load(), modelCalls.Load(), knowledgeCalls.Load())
	}
}

func TestProcessChanges_PruneFlagDoesNotPruneVolumes(t *testing.T) {
	var imageCalls, modelCalls, knowledgeCalls, volumeCalls atomic.Int32
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
		pruneKnowledgeFn: func() error {
			knowledgeCalls.Add(1)
			return nil
		},
		pruneVolumesFn: func() error {
			volumeCalls.Add(1)
			return nil
		},
	}
	fa.state.SetInitialization(false)

	_ = fa.processChanges(map[string]any{"prune": true})
	if imageCalls.Load() != 1 || modelCalls.Load() != 1 || knowledgeCalls.Load() != 1 {
		t.Fatalf("expected image, model, and knowledge prune once, images=%d models=%d knowledge=%d", imageCalls.Load(), modelCalls.Load(), knowledgeCalls.Load())
	}
	if volumeCalls.Load() != 0 {
		t.Fatalf("controller prune flag must not prune persistent volumes, got %d calls", volumeCalls.Load())
	}
}

func TestProcessChanges_PruneFlagSkippedDuringInitialization(t *testing.T) {
	var imageCalls, modelCalls, knowledgeCalls atomic.Int32
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
		pruneKnowledgeFn: func() error {
			knowledgeCalls.Add(1)
			return nil
		},
	}
	fa.state.SetInitialization(true)

	_ = fa.processChanges(map[string]any{"prune": true})
	if imageCalls.Load() != 0 || modelCalls.Load() != 0 || knowledgeCalls.Load() != 0 {
		t.Fatalf("expected prune skipped during initialization, images=%d models=%d knowledge=%d", imageCalls.Load(), modelCalls.Load(), knowledgeCalls.Load())
	}
}
