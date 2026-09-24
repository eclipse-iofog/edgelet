//go:build linux

package edgelet

import (
	"context"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/processmanager"
	"github.com/eclipse-iofog/edgelet/pkg/engine/edgelet/cri"
	runtimeapi "k8s.io/cri-api/pkg/apis/runtime/v1"
)

func (e *Engine) preflightReleasePodSandbox(ctx context.Context, msUUID string) {
	if e == nil || e.criClient == nil {
		return
	}
	msUUID = strings.TrimSpace(msUUID)
	if msUUID == "" {
		return
	}
	sandboxes, err := e.criClient.ListPodSandboxes(ctx, nil)
	if err != nil {
		return
	}
	for _, sb := range sandboxes {
		if sb == nil || sb.Metadata == nil {
			continue
		}
		if strings.TrimSpace(sb.Metadata.Uid) != msUUID {
			continue
		}
		_ = e.criClient.StopPodSandbox(ctx, sb.Id)
		_ = e.criClient.RemovePodSandbox(ctx, sb.Id)
	}
}

func (e *Engine) runPodSandboxWithRecovery(ctx context.Context, podConfig *runtimeapi.PodSandboxConfig, runtimeHandler, msUUID string) (string, error) {
	if err := processmanager.ErrIfDataPlaneDrainHold(); err != nil {
		return "", err
	}
	e.preflightReleasePodSandbox(ctx, msUUID)
	sandboxID, err := e.criClient.RunPodSandbox(ctx, podConfig, runtimeHandler)
	if err == nil {
		return sandboxID, nil
	}
	if cri.IsPodSandboxNameReserved(err) {
		if reservedID := cri.ReservedPodSandboxID(err); reservedID != "" {
			_ = e.criClient.StopPodSandbox(ctx, reservedID)
			_ = e.criClient.RemovePodSandbox(ctx, reservedID)
		}
		e.preflightReleasePodSandbox(ctx, msUUID)
		if err := processmanager.ErrIfDataPlaneDrainHold(); err != nil {
			return "", err
		}
		return e.criClient.RunPodSandbox(ctx, podConfig, runtimeHandler)
	}
	return "", err
}
