package models

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/workloadmeta"
	"gopkg.in/yaml.v3"
)

func TestBuildMicroserviceFromLocalManifestUsesLocalApplicationScope(t *testing.T) {
	doc := validLocalDeployManifestForTest("local-svc")
	ms := BuildMicroserviceFromLocalManifest(doc, "dep-1", "nginx:latest")

	if ms.ApplicationName != workloadmeta.LocalDeployApplicationName {
		t.Fatalf("expected application %q, got %q", workloadmeta.LocalDeployApplicationName, ms.ApplicationName)
	}

	if got := workloadmeta.ResolveScope(ms.ApplicationName, ms.HostNetworkMode); got != workloadmeta.ScopeLocal {
		t.Fatalf("expected local scope for local deploy, got %q", got)
	}
}

func TestBuildMicroserviceFromLocalManifestHostNetworkBypassesLocalScope(t *testing.T) {
	doc := validLocalDeployManifestForTest("local-hostnet")
	doc.Spec.Container.HostNetworkMode = true
	ms := BuildMicroserviceFromLocalManifest(doc, "dep-2", "nginx:latest")

	if got := workloadmeta.ResolveScope(ms.ApplicationName, ms.HostNetworkMode); got != workloadmeta.ScopeManaged {
		t.Fatalf("expected managed scope for host-network local deploy, got %q", got)
	}
}

func TestBuildMicroserviceFromLocalManifestConvertsMemoryLimitMiBToBytes(t *testing.T) {
	doc := validLocalDeployManifestForTest("local-mem")
	doc.Spec.Container.MemoryLimit = 512
	ms := BuildMicroserviceFromLocalManifest(doc, "dep-3", "nginx:latest")

	if ms.MemoryLimit == nil {
		t.Fatal("expected memory limit to be set")
	}
	want := int64(512 * 1024 * 1024)
	if *ms.MemoryLimit != want {
		t.Fatalf("MemoryLimit=%d want %d", *ms.MemoryLimit, want)
	}
}

func TestBuildMicroserviceFromLocalManifest_EntrypointCommandsOmitVsEmptyVsSet(t *testing.T) {
	t.Run("omitted", func(t *testing.T) {
		doc := validLocalDeployManifestForTest("omit-argv")
		ms := BuildMicroserviceFromLocalManifest(doc, "dep-omit", "nginx:latest")
		if !UsesImageDefault(ms.Entrypoint) || ms.Entrypoint != nil {
			t.Fatalf("omitted entrypoint must stay nil, got %#v", ms.Entrypoint)
		}
		if !UsesImageDefault(ms.Commands) || ms.Commands != nil {
			t.Fatalf("omitted commands must stay nil, got %#v", ms.Commands)
		}
		if len(ms.Args) != 0 {
			t.Fatalf("omitted commands must not copy args, got %v", ms.Args)
		}
	})
	t.Run("empty lists", func(t *testing.T) {
		doc := validLocalDeployManifestForTest("empty-argv")
		empty := []string{}
		doc.Spec.Container.Entrypoint = &empty
		doc.Spec.Container.Commands = &empty
		ms := BuildMicroserviceFromLocalManifest(doc, "dep-empty", "nginx:latest")
		if ms.Entrypoint == nil || ms.Commands == nil {
			t.Fatal("explicit empty lists must be non-nil")
		}
		if !UsesImageDefault(ms.Entrypoint) || !UsesImageDefault(ms.Commands) {
			t.Fatal("explicit empty lists must use image defaults")
		}
		if len(ms.Args) != 0 {
			t.Fatalf("empty commands must not copy args, got %v", ms.Args)
		}
	})
	t.Run("non-empty", func(t *testing.T) {
		doc := validLocalDeployManifestForTest("set-argv")
		entry := []string{"/app/start"}
		cmds := []string{"--serve", "--port=8080"}
		doc.Spec.Container.Entrypoint = &entry
		doc.Spec.Container.Commands = &cmds
		ms := BuildMicroserviceFromLocalManifest(doc, "dep-set", "nginx:latest")
		if UsesImageDefault(ms.Entrypoint) || (*ms.Entrypoint)[0] != "/app/start" {
			t.Fatalf("entrypoint = %#v", ms.Entrypoint)
		}
		if UsesImageDefault(ms.Commands) || (*ms.Commands)[0] != "--serve" {
			t.Fatalf("commands = %#v", ms.Commands)
		}
		if len(ms.Args) != 2 || ms.Args[1] != "--port=8080" {
			t.Fatalf("args = %v", ms.Args)
		}
	})
}

func TestBuildMicroserviceFromLocalManifest_HealthCheckAndAnnotations(t *testing.T) {
	doc := validLocalDeployManifestForTest("hc-ann")
	doc.Spec.Container.HealthCheck.Test = []string{"CMD", "curl", "-f", "http://localhost:1880/"}
	doc.Spec.Container.HealthCheck.Interval = 30
	doc.Spec.Container.HealthCheck.Timeout = 5
	doc.Spec.Container.HealthCheck.StartPeriod = 10
	doc.Spec.Container.HealthCheck.Retries = 3
	doc.Spec.Container.Annotations = map[string]any{
		"iofog.network": "true",
		"count":         2,
	}
	ms := BuildMicroserviceFromLocalManifest(doc, "dep-hc", "nginx:latest")
	if ms.Healthcheck == nil {
		t.Fatal("expected healthcheck on runtime model")
	}
	if len(ms.Healthcheck.Test) != 4 || ms.Healthcheck.Test[0] != "CMD" {
		t.Fatalf("healthcheck test = %v", ms.Healthcheck.Test)
	}
	if ms.Healthcheck.Interval == nil || *ms.Healthcheck.Interval != 30 {
		t.Fatalf("interval = %v", ms.Healthcheck.Interval)
	}
	if ms.Healthcheck.Timeout == nil || *ms.Healthcheck.Timeout != 5 {
		t.Fatalf("timeout = %v", ms.Healthcheck.Timeout)
	}
	if ms.Healthcheck.StartPeriod == nil || *ms.Healthcheck.StartPeriod != 10 {
		t.Fatalf("startPeriod = %v", ms.Healthcheck.StartPeriod)
	}
	if ms.Healthcheck.Retries == nil || *ms.Healthcheck.Retries != 3 {
		t.Fatalf("retries = %v", ms.Healthcheck.Retries)
	}
	if ms.Annotations == nil {
		t.Fatal("expected annotations JSON on runtime model")
	}
	var ann map[string]string
	if err := json.Unmarshal([]byte(*ms.Annotations), &ann); err != nil {
		t.Fatalf("annotations JSON: %v", err)
	}
	if ann["iofog.network"] != "true" || ann["count"] != "2" {
		t.Fatalf("annotations = %v", ann)
	}
}

func TestBuildMicroserviceFromLocalManifest_CatalogAndConfigNotInjected(t *testing.T) {
	doc := validLocalDeployManifestForTest("cat-cfg")
	doc.Spec.Models = &ModelCatalog{
		BindPath:    "/models",
		Permissions: ModelCatalogPermRO,
		Items:       []ModelCatalogItem{{Name: "test-model"}},
	}
	doc.Spec.Knowledge = &KnowledgeCatalog{
		BindPath:    "/knowledge",
		Permissions: KnowledgeCatalogPermRO,
		Items:       []KnowledgeCatalogItem{{Name: "product-docs"}},
	}
	doc.Spec.Config = map[string]any{"myKey": "value"}
	ms := BuildMicroserviceFromLocalManifest(doc, "dep-cat", "nginx:latest")
	if !ms.Models.HasItems() || ms.Models.BindPath != "/models" || ms.Models.Items[0].Name != "test-model" {
		t.Fatalf("catalog = %#v", ms.Models)
	}
	if !ms.Knowledge.HasItems() || ms.Knowledge.BindPath != "/knowledge" || ms.Knowledge.Items[0].Name != "product-docs" {
		t.Fatalf("knowledge catalog = %#v", ms.Knowledge)
	}
	if ms.Config != nil {
		t.Fatalf("spec.config must not be injected, got %v", ms.Config)
	}
}

func TestBuildMicroserviceFromLocalManifest_YAMLHealthCheckRoundTrip(t *testing.T) {
	raw := []byte(`
apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: nodered-demo
spec:
  image: nodered/node-red:latest
  container:
    annotations:
      team: demo
    healthCheck:
      test: ["CMD", "curl", "-f", "http://localhost:1880/"]
      interval: 30
      timeout: 5
      startPeriod: 10
      retries: 3
    entrypoint: []
    commands: ["--serve"]
    memoryLimit: 512
    memoryReservation: 128
`)
	doc := &LocalDeployManifest{}
	if err := yaml.Unmarshal(raw, doc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ms := BuildMicroserviceFromLocalManifest(doc, "dep-yaml", doc.ManifestImage())
	if ms.Healthcheck == nil || ms.Healthcheck.Timeout == nil || *ms.Healthcheck.Timeout != 5 {
		t.Fatalf("healthcheck = %#v", ms.Healthcheck)
	}
	if ms.Annotations == nil || !strings.Contains(*ms.Annotations, `"team":"demo"`) {
		t.Fatalf("annotations = %v", ms.Annotations)
	}
	if !UsesImageDefault(ms.Entrypoint) {
		t.Fatal("empty entrypoint must use image default")
	}
	if UsesImageDefault(ms.Commands) || (*ms.Commands)[0] != "--serve" {
		t.Fatalf("commands = %#v", ms.Commands)
	}
	if ms.MemoryLimit == nil || *ms.MemoryLimit != 512*1024*1024 {
		t.Fatalf("memoryLimit bytes = %v", ms.MemoryLimit)
	}
	if ms.MemoryReservation == nil || *ms.MemoryReservation != 128*1024*1024 {
		t.Fatalf("memoryReservation bytes = %v", ms.MemoryReservation)
	}
}

func TestLocalDeployManifestValidateCatalogApply_ManagedName(t *testing.T) {
	doc := validLocalDeployManifestForTest("bind-managed")
	doc.Spec.Models = &ModelCatalog{
		BindPath: "/models",
		Items:    []ModelCatalogItem{{Name: "fleet-model"}},
	}
	err := doc.ValidateCatalogApply(statusLookup(map[string]ModelStatusInfo{
		"fleet-model": {Source: ModelSourceManaged, State: ModelStateReady, Exists: true},
	}))
	var scope *ErrModelSourceScope
	if !errors.As(err, &scope) || scope.Name != "fleet-model" || scope.Want != ModelSourceLocal {
		t.Fatalf("expected local-only source error, got %v", err)
	}
}

func TestLocalDeployNeedsRecreate_CatalogItemsOnly(t *testing.T) {
	prevDoc := validLocalDeployManifestForTest("keep")
	prevDoc.Spec.Models = &ModelCatalog{
		BindPath:    "/models",
		Permissions: ModelCatalogPermRO,
		Items:       []ModelCatalogItem{{Name: "a"}},
	}
	nextDoc := validLocalDeployManifestForTest("keep")
	nextDoc.Spec.Models = &ModelCatalog{
		BindPath:    "/models",
		Permissions: ModelCatalogPermRO,
		Items:       []ModelCatalogItem{{Name: "a"}, {Name: "b"}},
	}
	prev := BuildMicroserviceFromLocalManifest(prevDoc, "dep-1", "nginx:latest")
	next := BuildMicroserviceFromLocalManifest(nextDoc, "dep-1", "nginx:latest")
	if LocalDeployNeedsRecreate(prev, next) {
		t.Fatal("catalog item membership must not recreate")
	}
	next.Models.BindPath = "/other"
	if !LocalDeployNeedsRecreate(prev, next) {
		t.Fatal("bindPath change must recreate")
	}
}

func TestLocalDeployManifestValidateKnowledgeCatalogApply_ManagedName(t *testing.T) {
	doc := validLocalDeployManifestForTest("bind-managed-knowledge")
	doc.Spec.Knowledge = &KnowledgeCatalog{
		BindPath: "/knowledge",
		Items:    []KnowledgeCatalogItem{{Name: "fleet-docs"}},
	}
	err := doc.ValidateKnowledgeCatalogApply(knowledgeStatusLookup(map[string]KnowledgeStatusInfo{
		"fleet-docs": {Source: KnowledgeSourceManaged, State: KnowledgeStateReady, Exists: true},
	}))
	var scope *ErrKnowledgeSourceScope
	if !errors.As(err, &scope) || scope.Name != "fleet-docs" || scope.Want != KnowledgeSourceLocal {
		t.Fatalf("expected local-only knowledge source error, got %v", err)
	}
}

func TestLocalDeployNeedsRecreate_KnowledgeCatalogItemsOnly(t *testing.T) {
	prevDoc := validLocalDeployManifestForTest("keep-knowledge")
	prevDoc.Spec.Knowledge = &KnowledgeCatalog{
		BindPath:    "/knowledge",
		Permissions: KnowledgeCatalogPermRO,
		Items:       []KnowledgeCatalogItem{{Name: "a"}},
	}
	nextDoc := validLocalDeployManifestForTest("keep-knowledge")
	nextDoc.Spec.Knowledge = &KnowledgeCatalog{
		BindPath:    "/knowledge",
		Permissions: KnowledgeCatalogPermRO,
		Items:       []KnowledgeCatalogItem{{Name: "a"}, {Name: "b"}},
	}
	prev := BuildMicroserviceFromLocalManifest(prevDoc, "dep-1", "nginx:latest")
	next := BuildMicroserviceFromLocalManifest(nextDoc, "dep-1", "nginx:latest")
	if LocalDeployNeedsRecreate(prev, next) {
		t.Fatal("knowledge catalog item membership must not recreate")
	}
	next.Knowledge.BindPath = "/corpus"
	if !LocalDeployNeedsRecreate(prev, next) {
		t.Fatal("knowledge bindPath change must recreate")
	}
}

func TestLocalDeployNeedsRecreate_ImageChange(t *testing.T) {
	prev := BuildMicroserviceFromLocalManifest(validLocalDeployManifestForTest("img"), "dep-1", "nginx:1")
	next := BuildMicroserviceFromLocalManifest(validLocalDeployManifestForTest("img"), "dep-1", "nginx:2")
	if !LocalDeployNeedsRecreate(prev, next) {
		t.Fatal("image change must recreate")
	}
}
