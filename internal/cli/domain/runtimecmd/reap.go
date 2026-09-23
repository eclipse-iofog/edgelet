package runtimecmd

import (
	"fmt"

	"github.com/eclipse-iofog/edgelet/internal/constants"
	"github.com/eclipse-iofog/edgelet/pkg/containerd"
)

var reapManagedShimsUntilClear = containerd.ReapManagedShimsUntilClear

// ReapOrphansResult carries the outcome of a local data-plane orphan reap.
type ReapOrphansResult struct {
	Human string
	Data  map[string]any
}

// ReapOrphans stops edgelet-scoped containerd shims and orphaned embedded containerd children
// after a verified data-plane drain. Incomplete drain must not reap shims.
func ReapOrphans() (*ReapOrphansResult, error) {
	if !containerd.HasDrainVerifiedMarker() {
		data := map[string]any{
			"socket":  constants.EdgeletContainerdSocket,
			"status":  "skipped",
			"message": "data-plane orphan reap skipped because drain did not verify",
		}
		return &ReapOrphansResult{
			Human: "Data-plane orphan reap skipped (drain did not verify).",
			Data:  data,
		}, nil
	}
	if err := reapManagedShimsUntilClear(constants.EdgeletContainerdSocket, containerd.DefaultShimReapBudgetCap); err != nil {
		return nil, err
	}
	data := map[string]any{
		"socket":  constants.EdgeletContainerdSocket,
		"status":  "complete",
		"message": "edgelet data-plane orphan reap complete",
	}
	return &ReapOrphansResult{
		Human: fmt.Sprintf("Data-plane orphan reap complete (%s).", constants.EdgeletContainerdSocket),
		Data:  data,
	}, nil
}
