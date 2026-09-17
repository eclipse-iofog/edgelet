//go:build linux

package edgelet

import (
	"errors"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/registrytls"
)

func TestPullImage_HTTPWithoutInsecure(t *testing.T) {
	e := &Engine{}
	blocked := models.NewRegistryBuilder().
		SetURL("http://registry.internal:5000").
		SetType(models.RegistryTypeOCI).
		Build()
	err := e.PullImage("registry.internal/app:v1", blocked, nil)
	if !errors.Is(err, registrytls.ErrHTTPRequiresInsecure) {
		t.Fatalf("expected http without insecure to fail, got: %v", err)
	}
}
