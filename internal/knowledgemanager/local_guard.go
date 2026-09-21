package knowledgemanager

import (
	"errors"

	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/models"
)

// ErrLocalKnowledgeDisabled is returned when local Knowledge apply or pull is
// refused because watchdog is enabled.
var ErrLocalKnowledgeDisabled = errors.New("local knowledge is disabled while watchdog is enabled")

func localKnowledgeDisabled() bool {
	cfg := config.GetInstance()
	return cfg != nil && cfg.WatchdogEnabled
}

func refuseLocalKnowledgeApply() error {
	if localKnowledgeDisabled() {
		return ErrLocalKnowledgeDisabled
	}
	return nil
}

// RefuseLocalKnowledgeApply returns ErrLocalKnowledgeDisabled when watchdog is on.
func RefuseLocalKnowledgeApply() error {
	return refuseLocalKnowledgeApply()
}

func refuseLocalSourcePull(source string) error {
	if !localKnowledgeDisabled() {
		return nil
	}
	if source == models.KnowledgeSourceManaged {
		return nil
	}
	return ErrLocalKnowledgeDisabled
}
