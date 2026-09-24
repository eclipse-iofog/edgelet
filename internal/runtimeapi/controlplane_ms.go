package runtimeapi

import (
	"errors"
	"fmt"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/models"
)

// ErrControlPlaneLifecycleBlocked indicates ms rm/stop/kill/start/restart on the controller UUID is forbidden.
type ErrControlPlaneLifecycleBlocked struct {
	Operation string
}

func (e *ErrControlPlaneLifecycleBlocked) Error() string {
	op := strings.TrimSpace(e.Operation)
	if op == "" {
		op = "mutation"
	}
	if op == "restart" {
		return "controller microservice cannot be restarted via ms restart while agent is provisioned; use edgelet controlplane restart"
	}
	return fmt.Sprintf(
		"controller microservice cannot be %s while agent is provisioned; deprovision the agent or use edgelet controlplane delete when unprovisioned",
		op,
	)
}

// ErrControlPlaneDeleteBlocked indicates DELETE /v1/system/controlplane is forbidden while provisioned.
type ErrControlPlaneDeleteBlocked struct{}

func (e *ErrControlPlaneDeleteBlocked) Error() string {
	return "control plane delete is not allowed while agent is provisioned; deprovision the agent first"
}

func (f *Facade) controlPlaneDeploymentRow() (*models.ControlPlaneDeployment, bool) {
	if f == nil || f.db == nil || f.db.Conn() == nil {
		return nil, false
	}
	item, found, err := f.db.GetSystemControlPlane()
	if err != nil || !found || item == nil {
		return nil, false
	}
	if strings.EqualFold(strings.TrimSpace(item.DesiredState), "deleted") {
		return nil, false
	}
	return item, true
}

func (f *Facade) guardControlPlaneMicroserviceMutation(uuid, operation string) error {
	if f.fa.NotProvisioned() {
		return nil
	}
	uuid = strings.TrimSpace(uuid)
	if uuid == "" {
		return nil
	}
	if ms := f.fa.FindLatestMicroserviceByUUID(uuid); ms != nil && ms.IsController {
		return &ErrControlPlaneLifecycleBlocked{Operation: operation}
	}
	item, ok := f.controlPlaneDeploymentRow()
	if !ok {
		return nil
	}
	if strings.TrimSpace(item.ControllerUUID) != uuid {
		return nil
	}
	return &ErrControlPlaneLifecycleBlocked{Operation: operation}
}

func controlPlaneRuntimeListEntry(item *models.ControlPlaneDeployment) map[string]any {
	if item == nil {
		return nil
	}
	item.NormalizeDefaults()
	state := strings.ToLower(strings.TrimSpace(item.RuntimeState))
	if state == "" {
		state = strings.ToLower(strings.TrimSpace(item.State))
	}
	entry := map[string]any{
		"uuid":        item.ControllerUUID,
		"name":        item.Name,
		"application": item.Namespace,
		"source":      "controlplane",
		"type":        "controller",
		"state":       state,
		"containerId": item.ContainerID,
		"image":       item.Image,
	}
	if podID := models.PodIDForEngine(currentEngineName(config.GetInstance()), item.ContainerID, ""); podID != "" {
		entry["podId"] = podID
	}
	return entry
}

// IsControlPlaneDeleteBlocked reports whether err blocks control-plane delete while provisioned.
func IsControlPlaneDeleteBlocked(err error) bool {
	var blocked *ErrControlPlaneDeleteBlocked
	return errors.As(err, &blocked)
}

// IsControlPlaneLifecycleBlocked reports whether err blocks control-plane ms lifecycle.
func IsControlPlaneLifecycleBlocked(err error) bool {
	var blocked *ErrControlPlaneLifecycleBlocked
	return errors.As(err, &blocked)
}

// IsControlPlaneRestartBlocked reports whether err blocks operator control-plane restart.
func IsControlPlaneRestartBlocked(err error) bool {
	return errors.Is(err, ErrControlPlaneRestartBlocked)
}
