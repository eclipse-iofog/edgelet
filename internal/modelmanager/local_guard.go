package modelmanager

import (
	"errors"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/models"
)

// ErrLocalModelsDisabled is returned when local Model apply or pull is refused
// because watchdog is enabled.
var ErrLocalModelsDisabled = errors.New("local models are disabled while watchdog is enabled")

func localModelsDisabled() bool {
	cfg := config.GetInstance()
	return cfg != nil && cfg.WatchdogEnabled
}

func refuseLocalModelApply() error {
	if localModelsDisabled() {
		return ErrLocalModelsDisabled
	}
	return nil
}

// RefuseLocalModelApply returns ErrLocalModelsDisabled when watchdog is on.
func RefuseLocalModelApply() error {
	return refuseLocalModelApply()
}

func refuseLocalSourcePull(source string) error {
	if !localModelsDisabled() {
		return nil
	}
	if source == models.ModelSourceManaged {
		return nil
	}
	return ErrLocalModelsDisabled
}
