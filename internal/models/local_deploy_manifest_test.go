package models

import (
	"strings"
	"testing"
)

func validLocalDeployManifestForTest(name string) *LocalDeployManifest {
	doc := &LocalDeployManifest{}
	doc.APIVersion = "edgelet.iofog.org/v1"
	doc.Kind = "Microservice"
	doc.Metadata.Name = name
	doc.Spec.Image = "nginx:latest"
	return doc
}

func TestLocalDeployManifestValidate_RequiresSpecImage(t *testing.T) {
	doc := validLocalDeployManifestForTest("img-required")
	doc.Spec.Image = ""
	if err := doc.Validate(); err == nil || !strings.Contains(err.Error(), "spec.image is required") {
		t.Fatalf("expected spec.image required error, got: %v", err)
	}
}

func TestLocalDeployManifestValidate_NameDNS1123(t *testing.T) {
	valid := validLocalDeployManifestForTest("router-1")
	if err := valid.Validate(); err != nil {
		t.Fatalf("expected valid name to pass, got: %v", err)
	}

	invalidNames := []string{
		"Router",
		"router_1",
		"-router",
		"router-",
		"router.service",
	}
	for _, name := range invalidNames {
		doc := validLocalDeployManifestForTest(name)
		if err := doc.Validate(); err == nil {
			t.Fatalf("expected invalid name %q to fail validation", name)
		}
	}
}

func TestLocalDeployManifestValidate_LocalVolumePolicies(t *testing.T) {
	t.Run("bind explicit valid absolute path", func(t *testing.T) {
		doc := validLocalDeployManifestForTest("bind-ok")
		doc.Spec.Container.Volumes = []struct {
			HostDestination      string `yaml:"hostDestination" json:"hostDestination"`
			ContainerDestination string `yaml:"containerDestination" json:"containerDestination"`
			AccessMode           string `yaml:"accessMode,omitempty" json:"accessMode,omitempty"`
			Type                 string `yaml:"type,omitempty" json:"type,omitempty"`
			Scope                string `yaml:"scope,omitempty" json:"scope,omitempty"`
		}{
			{
				HostDestination:      "/var/lib/data",
				ContainerDestination: "/data",
				Type:                 "BIND",
			},
		}
		if err := doc.Validate(); err != nil {
			t.Fatalf("expected explicit bind volume to pass, got: %v", err)
		}
	})

	t.Run("volume explicit valid name", func(t *testing.T) {
		doc := validLocalDeployManifestForTest("volume-ok")
		doc.Spec.Container.Volumes = []struct {
			HostDestination      string `yaml:"hostDestination" json:"hostDestination"`
			ContainerDestination string `yaml:"containerDestination" json:"containerDestination"`
			AccessMode           string `yaml:"accessMode,omitempty" json:"accessMode,omitempty"`
			Type                 string `yaml:"type,omitempty" json:"type,omitempty"`
			Scope                string `yaml:"scope,omitempty" json:"scope,omitempty"`
		}{
			{
				HostDestination:      "nodered_data-1",
				ContainerDestination: "/data",
				Type:                 "VOLUME",
			},
		}
		if err := doc.Validate(); err != nil {
			t.Fatalf("expected explicit volume mapping to pass, got: %v", err)
		}
	})

	t.Run("empty type defaults to bind", func(t *testing.T) {
		doc := validLocalDeployManifestForTest("empty-type-bind")
		doc.Spec.Container.Volumes = []struct {
			HostDestination      string `yaml:"hostDestination" json:"hostDestination"`
			ContainerDestination string `yaml:"containerDestination" json:"containerDestination"`
			AccessMode           string `yaml:"accessMode,omitempty" json:"accessMode,omitempty"`
			Type                 string `yaml:"type,omitempty" json:"type,omitempty"`
			Scope                string `yaml:"scope,omitempty" json:"scope,omitempty"`
		}{
			{
				HostDestination:      "/opt/agent/data",
				ContainerDestination: "/data",
				Type:                 "",
			},
		}
		if err := doc.Validate(); err != nil {
			t.Fatalf("expected empty type to default to bind, got: %v", err)
		}
		if got := doc.Spec.Container.Volumes[0].Type; got != "BIND" {
			t.Fatalf("expected type normalized to BIND, got %q", got)
		}
	})

	t.Run("bind requires absolute host path", func(t *testing.T) {
		doc := validLocalDeployManifestForTest("bind-relative")
		doc.Spec.Container.Volumes = []struct {
			HostDestination      string `yaml:"hostDestination" json:"hostDestination"`
			ContainerDestination string `yaml:"containerDestination" json:"containerDestination"`
			AccessMode           string `yaml:"accessMode,omitempty" json:"accessMode,omitempty"`
			Type                 string `yaml:"type,omitempty" json:"type,omitempty"`
			Scope                string `yaml:"scope,omitempty" json:"scope,omitempty"`
		}{
			{
				HostDestination:      "relative/path",
				ContainerDestination: "/data",
				Type:                 "BIND",
			},
		}
		if err := doc.Validate(); err == nil {
			t.Fatal("expected relative bind path to fail")
		}
	})

	t.Run("volume requires valid name", func(t *testing.T) {
		doc := validLocalDeployManifestForTest("volume-invalid")
		doc.Spec.Container.Volumes = []struct {
			HostDestination      string `yaml:"hostDestination" json:"hostDestination"`
			ContainerDestination string `yaml:"containerDestination" json:"containerDestination"`
			AccessMode           string `yaml:"accessMode,omitempty" json:"accessMode,omitempty"`
			Type                 string `yaml:"type,omitempty" json:"type,omitempty"`
			Scope                string `yaml:"scope,omitempty" json:"scope,omitempty"`
		}{
			{
				HostDestination:      "/tmp/host-path",
				ContainerDestination: "/data",
				Type:                 "VOLUME",
			},
		}
		err := doc.Validate()
		if err == nil {
			t.Fatal("expected invalid volume name to fail")
		}
		if got := err.Error(); got == "" || !strings.Contains(got, "use type: bind") {
			t.Fatalf("expected guidance to use type: bind, got %q", got)
		}
	})

	t.Run("volume mount forbidden", func(t *testing.T) {
		doc := validLocalDeployManifestForTest("volume-mount-invalid")
		doc.Spec.Container.Volumes = []struct {
			HostDestination      string `yaml:"hostDestination" json:"hostDestination"`
			ContainerDestination string `yaml:"containerDestination" json:"containerDestination"`
			AccessMode           string `yaml:"accessMode,omitempty" json:"accessMode,omitempty"`
			Type                 string `yaml:"type,omitempty" json:"type,omitempty"`
			Scope                string `yaml:"scope,omitempty" json:"scope,omitempty"`
		}{
			{
				HostDestination:      "router-secret",
				ContainerDestination: "/etc/secret",
				Type:                 "VOLUME_MOUNT",
			},
		}
		err := doc.Validate()
		if err == nil {
			t.Fatal("expected VOLUME_MOUNT to fail")
		}
		if got := err.Error(); got == "" || !strings.Contains(got, "not supported for local manifests") {
			t.Fatalf("unexpected error: %q", got)
		}
	})

	t.Run("unknown volume scope does not fail apply", func(t *testing.T) {
		doc := validLocalDeployManifestForTest("scope-unknown")
		doc.Spec.Container.Volumes = []struct {
			HostDestination      string `yaml:"hostDestination" json:"hostDestination"`
			ContainerDestination string `yaml:"containerDestination" json:"containerDestination"`
			AccessMode           string `yaml:"accessMode,omitempty" json:"accessMode,omitempty"`
			Type                 string `yaml:"type,omitempty" json:"type,omitempty"`
			Scope                string `yaml:"scope,omitempty" json:"scope,omitempty"`
		}{
			{
				HostDestination:      "nodered_data-1",
				ContainerDestination: "/data",
				Type:                 "VOLUME",
				Scope:                "SHARED",
			},
		}
		if err := doc.Validate(); err != nil {
			t.Fatalf("unknown scope must not fail apply, got: %v", err)
		}
		if got := doc.Spec.Container.Volumes[0].Scope; got != VolumeScopePrivate {
			t.Fatalf("expected unknown scope stored private, got %q", got)
		}
	})

	t.Run("bind ignores shared scope", func(t *testing.T) {
		doc := validLocalDeployManifestForTest("bind-scope")
		doc.Spec.Container.Volumes = []struct {
			HostDestination      string `yaml:"hostDestination" json:"hostDestination"`
			ContainerDestination string `yaml:"containerDestination" json:"containerDestination"`
			AccessMode           string `yaml:"accessMode,omitempty" json:"accessMode,omitempty"`
			Type                 string `yaml:"type,omitempty" json:"type,omitempty"`
			Scope                string `yaml:"scope,omitempty" json:"scope,omitempty"`
		}{
			{
				HostDestination:      "/var/lib/data",
				ContainerDestination: "/data",
				Type:                 "BIND",
				Scope:                "shared",
			},
		}
		if err := doc.Validate(); err != nil {
			t.Fatalf("BIND with scope must not fail apply, got: %v", err)
		}
		if got := doc.Spec.Container.Volumes[0].Scope; got != VolumeScopePrivate {
			t.Fatalf("expected BIND scope ignored to private, got %q", got)
		}
	})
}
