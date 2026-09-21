package runtimeapi

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/buildmeta"
	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/models"
)

func testLocalManifestYAML() string {
	return strings.TrimSpace(`
apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: router
spec:
  image: quay.io/skupper/skupper-router:latest
`) + "\n"
}

func testLocalManifestWithLabelsYAML() string {
	return strings.TrimSpace(`
apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: svc-a
  labels:
    team: edge
    owner: runtime
spec:
  image: nginx:latest
`) + "\n"
}

func testLocalManifestWithNameYAML(name string) string {
	return strings.TrimSpace(`
apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: `+name+`
spec:
  image: quay.io/skupper/skupper-router:latest
`) + "\n"
}

func testLocalManifestWithVolumeMountYAML() string {
	return strings.TrimSpace(`
apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: router
spec:
  image: quay.io/skupper/skupper-router:latest
  container:
    volumes:
      - hostDestination: router-secret
        containerDestination: /etc/secret
        type: VOLUME_MOUNT
`) + "\n"
}

func TestFacadePullImage_ValidatesRequiredImage(t *testing.T) {
	f := NewFacade()
	if _, err := f.PullImage("   ", nil, ""); err == nil || !strings.Contains(err.Error(), "image is required") {
		t.Fatalf("expected required image validation error, got: %v", err)
	}
}

func TestFacadePullImage_ValidatesPlatform(t *testing.T) {
	f := NewFacade()
	if _, err := f.PullImage("nginx:latest", nil, "bad-platform"); err == nil || !strings.Contains(err.Error(), "platform must follow") {
		t.Fatalf("expected platform validation error, got: %v", err)
	}
}

func TestFacadeLoadImageFromPath_ValidatesPath(t *testing.T) {
	f := NewFacade()
	if _, err := f.LoadImageFromPath("   "); err == nil || !strings.Contains(err.Error(), "path is required") {
		t.Fatalf("expected missing path validation error, got: %v", err)
	}
}

func TestFacadeLoadImageFromPath_RequiresRegularFile(t *testing.T) {
	f := NewFacade()
	if _, err := f.LoadImageFromPath(t.TempDir()); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("expected regular file validation error, got: %v", err)
	}
}

func TestFacadePullImage_RejectsFromCacheRegistry(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })
	if err := f.db.UpsertLocalRegistry(models.NewRegistry(99, "from_cache", true, "", "", "")); err != nil {
		t.Fatalf("failed to upsert registry: %v", err)
	}
	registryID := 99
	_, err := f.PullImage("nginx:latest", &registryID, "")
	if err == nil || !strings.Contains(err.Error(), "from_cache") {
		t.Fatalf("expected from_cache rejection, got: %v", err)
	}
}

func TestFacadePullImage_ResolvesRegistryHostWithRegistryID(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })
	if err := f.db.UpsertLocalRegistry(models.NewRegistry(98, "quay.io", true, "", "", "")); err != nil {
		t.Fatalf("failed to upsert registry: %v", err)
	}
	registryID := 98
	resolved, err := f.PullImage("skupper/skupper-router", &registryID, "")
	// In unit tests processmanager may not be initialized; resolved value should still be returned.
	if err == nil {
		t.Fatal("expected process manager init error in unit test")
	}
	if !strings.HasPrefix(resolved, "quay.io/") {
		t.Fatalf("expected resolved image ref with quay.io host, got: %q (err=%v)", resolved, err)
	}
}

func TestFacadePullImage_RejectsNonOCIRegistry(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })
	hf := models.NewRegistryBuilder().
		SetID(5).
		SetURL("https://huggingface.co").
		SetType(models.RegistryTypeHF).
		SetPassword("hf_token").
		Build()
	if err := f.db.UpsertLocalRegistry(hf); err != nil {
		t.Fatalf("failed to upsert hf registry: %v", err)
	}
	registryID := 5
	_, err := f.PullImage("org/weights:latest", &registryID, "")
	if err == nil || !strings.Contains(err.Error(), "type") || !strings.Contains(err.Error(), "oci") {
		t.Fatalf("expected oci type requirement error, got: %v", err)
	}
}

func TestFacadePullImage_RegistryNotFound(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })
	registryID := 123456
	_, err := f.PullImage("nginx:latest", &registryID, "")
	if err == nil {
		t.Fatal("expected error for missing registry")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not found style error, got: %v", err)
	}
}

func TestFacadeApplyLocalManifest_ProgressStages_DryRun(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })
	stages := make([]string, 0)
	id, _, err := f.ApplyLocalManifest(testLocalManifestYAML(), "cli", true, func(stage string, _ string) {
		stages = append(stages, strings.TrimSpace(stage))
	})
	if err != nil {
		t.Fatalf("expected dry-run success, got: %v", err)
	}
	if strings.TrimSpace(id) == "" {
		t.Fatal("expected non-empty deployment id")
	}
	if len(stages) < 2 {
		t.Fatalf("expected at least parsing and done stages, got: %v", stages)
	}
	if stages[0] != DeployStageParsing {
		t.Fatalf("expected first stage %q, got %q (all=%v)", DeployStageParsing, stages[0], stages)
	}
	if stages[len(stages)-1] != DeployStageDone {
		t.Fatalf("expected final stage %q, got %q (all=%v)", DeployStageDone, stages[len(stages)-1], stages)
	}
}

func TestFacadeApplyLocalManifest_ProgressIncludesPersisting_OnFailure(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })
	stages := make([]string, 0)
	_, _, err := f.ApplyLocalManifest(testLocalManifestYAML(), "cli", false, func(stage string, _ string) {
		stages = append(stages, strings.TrimSpace(stage))
	})
	if err == nil {
		t.Fatal("expected runtime failure when DB/engine are not fully initialized")
	}
	if len(stages) == 0 {
		t.Fatal("expected progress stages before failure")
	}
	if stages[0] != DeployStageParsing {
		t.Fatalf("expected first stage %q, got %q (all=%v)", DeployStageParsing, stages[0], stages)
	}
	foundPersisting := false
	for _, stage := range stages {
		if stage == DeployStagePersisting {
			foundPersisting = true
			break
		}
	}
	if !foundPersisting {
		t.Fatalf("expected %q stage before failure, got: %v", DeployStagePersisting, stages)
	}
}

func TestFacadeApplyLocalManifest_RejectsVolumeMountType(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })

	_, _, err := f.ApplyLocalManifest(testLocalManifestWithVolumeMountYAML(), "cli", false, nil)
	if err == nil {
		t.Fatal("expected validation error for VOLUME_MOUNT")
	}
	if !strings.Contains(err.Error(), "not supported for local manifests") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestManifestToMicroservice_PropagatesMetadataLabels(t *testing.T) {
	f := NewFacade()
	doc, err := f.ParseAndValidateLocalManifest(testLocalManifestWithLabelsYAML())
	if err != nil {
		t.Fatalf("expected manifest to parse: %v", err)
	}

	ms := manifestToMicroservice(doc, "dep-1", "nginx:latest")
	if got := ms.Labels["team"]; got != "edge" {
		t.Fatalf("expected propagated label team=edge, got %q", got)
	}
	if got := ms.Labels["owner"]; got != "runtime" {
		t.Fatalf("expected propagated label owner=runtime, got %q", got)
	}

	// Ensure microservice labels are decoupled from manifest map mutations.
	doc.Metadata.Labels["team"] = "mutated"
	if got := ms.Labels["team"]; got != "edge" {
		t.Fatalf("expected copied labels map, got team=%q", got)
	}
}

func TestFacadeListRuntimeMicroservices_SuppressesStaleManagedEntries(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() {
		_ = f.db.Close()
		f.sr.ResetProcessManagerStatus()
	})
	f.fa.Clear()
	f.sr.ResetProcessManagerStatus()
	f.sr.UpdateProcessManagerStatus(func(pm *models.ProcessManagerStatus) {
		pm.SetMicroservicesState("stale-ms", models.MicroserviceStateDeleted)
	})

	items := f.ListRuntimeMicroservices()
	if len(items) != 0 {
		t.Fatalf("expected stale managed entries to be suppressed, got: %#v", items)
	}
}

func TestFacadeListRuntimeMicroservices_KeepingActiveManagedEntries(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() {
		_ = f.db.Close()
		f.sr.ResetProcessManagerStatus()
	})
	f.fa.Clear()
	f.sr.ResetProcessManagerStatus()
	f.sr.UpdateProcessManagerStatus(func(pm *models.ProcessManagerStatus) {
		pm.SetMicroservicesState("active-ms", models.MicroserviceStateRunning)
	})

	items := f.ListRuntimeMicroservices()
	if len(items) != 1 {
		t.Fatalf("expected running managed entry to remain visible, got: %#v", items)
	}
	if got := items[0]["uuid"]; got != "active-ms" {
		t.Fatalf("expected uuid active-ms, got: %v", got)
	}
}

func TestFacadeListRuntimeMicroservices_DoesNotDuplicateLocalUUIDAsManaged(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() {
		_ = f.db.Close()
		f.sr.ResetProcessManagerStatus()
	})
	f.fa.Clear()
	f.sr.ResetProcessManagerStatus()

	local := &models.LocalDeployedMicroservice{
		LocalUUID:        "local-dup-1",
		ApplicationName:  "edgelet",
		MicroserviceName: "router",
		SourceName:       "local-cli",
		ManifestYAML:     "kind: Microservice",
		ImageName:        "nginx:latest",
		State:            "running",
		RuntimeState:     "running",
	}
	if err := f.db.UpsertLocalWorkload(local); err != nil {
		t.Fatalf("failed to upsert local deployment: %v", err)
	}
	f.sr.UpdateProcessManagerStatus(func(pm *models.ProcessManagerStatus) {
		pm.SetMicroservicesState("local-dup-1", models.MicroserviceStateDeleted)
	})

	items := f.ListRuntimeMicroservices()
	if len(items) != 1 {
		t.Fatalf("expected single local entry, got %#v", items)
	}
	if got := items[0]["type"]; got != "local" {
		t.Fatalf("expected local type, got %v", got)
	}
}

func TestFacadeListRuntimeMicroservices_StaleRunningAbsentAfterPrune(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() {
		_ = f.db.Close()
		f.sr.ResetProcessManagerStatus()
	})
	f.fa.Clear()
	f.sr.ResetProcessManagerStatus()
	f.sr.UpdateProcessManagerStatus(func(pm *models.ProcessManagerStatus) {
		pm.SetMicroservicesState("stale-running-ms", models.MicroserviceStateRunning)
	})
	f.sr.PruneProcessManagerStatus(func(uuid string, _ *models.MicroserviceStatus) bool {
		return uuid == "stale-running-ms"
	})

	items := f.ListRuntimeMicroservices()
	if len(items) != 0 {
		t.Fatalf("expected stale running entry to be absent after prune, got: %#v", items)
	}
}

func TestFacadeApplyLocalManifest_NormalizesLocalLifecycleFields(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })

	deploymentID, _, err := f.ApplyLocalManifest(testLocalManifestYAML(), "cli", false, nil)
	if err == nil {
		t.Fatal("expected runtime failure in unit test")
	}
	if strings.TrimSpace(deploymentID) != "" {
		t.Fatalf("expected empty deployment id on failure, got %q", deploymentID)
	}

	items, listErr := f.ListLocalDeployments()
	if listErr != nil {
		t.Fatalf("failed to list local deployments: %v", listErr)
	}
	if len(items) != 1 {
		t.Fatalf("expected one local deployment row, got %d", len(items))
	}

	item := items[0]
	if item.ApplicationName != "edgelet" {
		t.Fatalf("expected application_name edgelet, got %q", item.ApplicationName)
	}
	if item.DesiredState != "running" {
		t.Fatalf("expected desired_state running, got %q", item.DesiredState)
	}
	if item.RuntimeState != "failed" {
		t.Fatalf("expected runtime_state failed after launch failure, got %q", item.RuntimeState)
	}
	if item.Generation != 1 {
		t.Fatalf("expected generation 1, got %d", item.Generation)
	}
}

func TestResolveMicroserviceID_LocalDottedSelector(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })

	item := &models.LocalDeployedMicroservice{
		LocalUUID:        "local-uuid-1",
		ApplicationName:  "edgelet",
		MicroserviceName: "router",
		SourceName:       "local-cli",
		ManifestYAML:     "kind: Microservice",
		ImageName:        "nginx:latest",
		State:            "running",
	}
	if err := f.db.UpsertLocalWorkload(item); err != nil {
		t.Fatalf("failed to upsert local deployment: %v", err)
	}

	id, err := f.ResolveMicroserviceID("edgelet.router")
	if err != nil {
		t.Fatalf("expected dotted local selector to resolve, got: %v", err)
	}
	if id != "local-uuid-1" {
		t.Fatalf("expected local-uuid-1, got %q", id)
	}
}

func TestResolveMicroserviceID_LocalDottedSelectorDuplicateRejected(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })

	items := []*models.LocalDeployedMicroservice{
		{
			LocalUUID:        "local-uuid-1",
			ApplicationName:  "edgelet",
			MicroserviceName: "router",
			SourceName:       "local-cli",
			ManifestYAML:     "kind: Microservice",
			ImageName:        "nginx:latest",
			State:            "running",
		},
		{
			LocalUUID:        "local-uuid-2",
			ApplicationName:  "edgelet",
			MicroserviceName: "router",
			SourceName:       "local-cli",
			ManifestYAML:     "kind: Microservice",
			ImageName:        "nginx:latest",
			State:            "running",
		},
	}
	if err := f.db.UpsertLocalWorkload(items[0]); err != nil {
		t.Fatalf("failed to upsert local deployment %s: %v", items[0].LocalUUID, err)
	}
	if err := f.db.UpsertLocalWorkload(items[1]); err == nil {
		t.Fatal("expected duplicate local dotted selector insert to fail")
	}

	id, err := f.ResolveMicroserviceID("edgelet.router")
	if err != nil {
		t.Fatalf("expected local dotted selector to still resolve existing entry, got: %v", err)
	}
	if id != items[0].LocalUUID {
		t.Fatalf("expected %s, got %s", items[0].LocalUUID, id)
	}
}

func TestFacadeApplyLocalManifest_IdempotentPatchReusesUUID(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })

	_, _, firstErr := f.ApplyLocalManifest(testLocalManifestYAML(), "cli", false, nil)
	if firstErr == nil {
		t.Fatal("expected runtime failure in unit test")
	}
	itemsAfterFirst, err := f.ListLocalDeployments()
	if err != nil {
		t.Fatalf("failed to list local deployments after first apply: %v", err)
	}
	if len(itemsAfterFirst) != 1 {
		t.Fatalf("expected one local deployment after first apply, got %d", len(itemsAfterFirst))
	}
	first := itemsAfterFirst[0]

	_, _, secondErr := f.ApplyLocalManifest(testLocalManifestWithNameYAML("router"), "cli", false, nil)
	if secondErr == nil {
		t.Fatal("expected runtime failure in unit test")
	}
	itemsAfterSecond, err := f.ListLocalDeployments()
	if err != nil {
		t.Fatalf("failed to list local deployments after second apply: %v", err)
	}
	if len(itemsAfterSecond) != 1 {
		t.Fatalf("expected one local deployment after idempotent patch apply, got %d", len(itemsAfterSecond))
	}
	second := itemsAfterSecond[0]
	if second.LocalUUID != first.LocalUUID {
		t.Fatalf("expected same uuid to be reused, first=%s second=%s", first.LocalUUID, second.LocalUUID)
	}
	if second.Generation != first.Generation+1 {
		t.Fatalf("expected generation increment on patch apply, first=%d second=%d", first.Generation, second.Generation)
	}
}

func TestFacadeApplyLocalManifest_RejectsManagedCatalogName(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })
	row := &models.LocalModel{
		Name:       "fleet-model",
		Source:     models.ModelSourceManaged,
		Repo:       "org/model",
		RegistryID: 1,
		State:      models.ModelStateReady,
	}
	if err := f.db.UpsertLocalModel(row); err != nil {
		t.Fatalf("seed managed model: %v", err)
	}
	manifest := strings.TrimSpace(`
apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: infer
spec:
  image: nginx:latest
  models:
    bindPath: /models
    items:
      - name: fleet-model
`) + "\n"
	_, _, err := f.ApplyLocalManifest(manifest, "cli", true, nil)
	if err == nil {
		t.Fatal("expected catalog source error")
	}
	if !strings.Contains(err.Error(), "managed") && !strings.Contains(err.Error(), "local") {
		t.Fatalf("expected local-only bind error, got %v", err)
	}
}

func TestFacadeApplyLocalManifest_CatalogItemChangeKeepsContainer(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })
	for _, name := range []string{"test-model", "qwen3-8-27b"} {
		row := &models.LocalModel{
			Name:        name,
			Source:      models.ModelSourceLocal,
			Repo:        "org/" + name,
			RegistryID:  1,
			State:       models.ModelStateReady,
			ContentPath: t.TempDir(),
		}
		if err := f.db.UpsertLocalModel(row); err != nil {
			t.Fatalf("seed model %s: %v", name, err)
		}
	}
	existingYAML := strings.TrimSpace(`
apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: router
spec:
  image: nginx:latest
  models:
    bindPath: /models
    permissions: ro
    items:
      - name: test-model
`) + "\n"
	existing := &models.LocalDeployedMicroservice{
		LocalUUID:          "local-keep-1",
		ApplicationName:    "edgelet",
		MicroserviceName:   "router",
		SourceName:         "local-cli",
		ManifestYAML:       existingYAML,
		ImageName:          "nginx:latest",
		ContainerID:        "ctr-keep-1",
		State:              "running",
		DesiredState:       "running",
		RuntimeState:       "running",
		Generation:         1,
		ObservedGeneration: 1,
	}
	if err := f.db.UpsertLocalWorkload(existing); err != nil {
		t.Fatalf("seed local workload: %v", err)
	}
	nextYAML := strings.TrimSpace(`
apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: router
spec:
  image: nginx:latest
  models:
    bindPath: /models
    permissions: ro
    items:
      - name: test-model
      - name: qwen3-8-27b
`) + "\n"
	id, _, err := f.ApplyLocalManifest(nextYAML, "cli", false, nil)
	if err != nil {
		t.Fatalf("expected in-place catalog update, got: %v", err)
	}
	if id != "local-keep-1" {
		t.Fatalf("expected reused uuid, got %q", id)
	}
	got, err := f.db.GetLocalWorkload("local-keep-1")
	if err != nil {
		t.Fatalf("load workload: %v", err)
	}
	if got.ContainerID != "ctr-keep-1" {
		t.Fatalf("container must be kept, got %q", got.ContainerID)
	}
	if got.RuntimeState != "running" {
		t.Fatalf("expected running, got %q", got.RuntimeState)
	}
}

func TestFacadeStartRuntimeMicroservice_LocalPersistsDesiredState(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })

	item := &models.LocalDeployedMicroservice{
		LocalUUID:        "local-start-1",
		ApplicationName:  "edgelet",
		MicroserviceName: "router",
		SourceName:       "local-cli",
		ManifestYAML:     testLocalManifestYAML(),
		ImageName:        "nginx:latest",
		State:            "failed",
		DesiredState:     "stopped",
		RuntimeState:     "failed",
		Generation:       1,
	}
	if err := f.db.UpsertLocalWorkload(item); err != nil {
		t.Fatalf("failed to seed local deployment: %v", err)
	}

	_, _ = f.StartRuntimeMicroservice("local-start-1")

	got, err := f.db.GetLocalWorkload("local-start-1")
	if err != nil {
		t.Fatalf("failed to load local deployment: %v", err)
	}
	if got.DesiredState != "running" {
		t.Fatalf("expected desired_state running, got %q", got.DesiredState)
	}
	if got.RuntimeState != "starting" {
		t.Fatalf("expected runtime_state starting, got %q", got.RuntimeState)
	}
	if got.Generation != 2 {
		t.Fatalf("expected generation increment to 2, got %d", got.Generation)
	}
}

func TestFacadeStopRuntimeMicroservice_LocalPersistsDesiredState(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })

	item := &models.LocalDeployedMicroservice{
		LocalUUID:        "local-stop-1",
		ApplicationName:  "edgelet",
		MicroserviceName: "router",
		SourceName:       "local-cli",
		ManifestYAML:     testLocalManifestYAML(),
		ImageName:        "nginx:latest",
		State:            "running",
		DesiredState:     "running",
		RuntimeState:     "running",
		Generation:       2,
	}
	if err := f.db.UpsertLocalWorkload(item); err != nil {
		t.Fatalf("failed to seed local deployment: %v", err)
	}

	_, _ = f.StopRuntimeMicroservice("local-stop-1")

	got, err := f.db.GetLocalWorkload("local-stop-1")
	if err != nil {
		t.Fatalf("failed to load local deployment: %v", err)
	}
	if got.DesiredState != "stopped" {
		t.Fatalf("expected desired_state stopped, got %q", got.DesiredState)
	}
	if got.RuntimeState != "stopping" {
		t.Fatalf("expected runtime_state stopping, got %q", got.RuntimeState)
	}
	if got.Generation != 3 {
		t.Fatalf("expected generation increment to 3, got %d", got.Generation)
	}
}

func TestFacadeDeprovision_RejectsInvalidScope(t *testing.T) {
	f := NewFacade()
	err := f.Deprovision("bad", false)
	if err == nil {
		t.Fatal("expected invalid scope error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "invalid deprovision scope") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFacadePrune_RejectsInvalidMode(t *testing.T) {
	f := NewFacade()
	_, err := f.Prune("bad")
	if err == nil {
		t.Fatal("expected invalid mode error")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "invalid prune mode") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFacadePrune_AllModeReturnsPartialOnStepFailures(t *testing.T) {
	f := NewFacade()
	result, err := f.Prune("all")
	if err != nil {
		t.Fatalf("expected no hard error for all mode best-effort prune, got: %v", err)
	}
	status, ok := result["status"].(string)
	if !ok {
		t.Fatal("type assertion failed for status")
	}
	if status != "partial" {
		t.Fatalf("expected partial status, got: %v", result)
	}
	rawErrors, ok := result["errors"].(map[string]string)
	if !ok {
		if generic, genericOK := result["errors"].(map[string]any); genericOK {
			rawErrors = make(map[string]string, len(generic))
			for k, v := range generic {
				rawErrors[k] = fmt.Sprintf("%v", v)
			}
			ok = true
		}
	}
	if !ok {
		t.Fatalf("expected step error map, got: %#v", result["errors"])
	}
	if len(rawErrors) == 0 {
		t.Fatal("expected partial error details, got none")
	}
	if _, ok := result["modelsRemoved"]; !ok {
		t.Fatalf("expected unused local model prune in all mode, got %#v", result)
	}
	if _, ok := result["knowledgeRemoved"]; ok {
		t.Fatalf("system prune must not delete knowledge, got %#v", result)
	}
	if fmt.Sprintf("%v", result["volumesDeletedCount"]) != "0" {
		t.Fatalf("expected no persistent VOLUME destroy, got %#v", result)
	}
}

func TestFacadePrune_VolumesModeRefusesPersistentData(t *testing.T) {
	f := NewFacade()
	_, err := f.Prune("volumes")
	if err == nil || !errors.Is(err, ErrSystemPruneVolumes) {
		t.Fatalf("expected volumes mode refusal, got %v", err)
	}
}

func TestApplyRuntimeClassManifest_MetadataOnlyPathDoesNotDependOnRuntimeCallback(t *testing.T) {
	f := NewFacade()
	cfg := config.GetInstance()
	cfg.ContainerEngine = "edgelet"
	embedded := true
	buildmeta.SetHasEmbeddedEngineForTest(&embedded)
	t.Cleanup(func() { buildmeta.SetHasEmbeddedEngineForTest(nil) })

	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })

	item, err := f.ApplyLocalRuntimeClassManifest(`
apiVersion: edgelet.iofog.org/v1
kind: RuntimeClass
metadata:
  name: edgelet-wasmtime
handler: edgelet-wasmtime
`, false)
	if err != nil {
		t.Fatalf("expected apply success for metadata-only runtimeclass path, got: %v", err)
	}
	if item == nil || strings.TrimSpace(item.Name) != "edgelet-wasmtime" {
		t.Fatalf("expected applied runtimeclass edgelet-wasmtime, got: %#v", item)
	}
	if _, getErr := f.db.GetLocalRuntimeClass("edgelet-wasmtime"); getErr != nil {
		t.Fatalf("expected runtimeclass persisted, got: %v", getErr)
	}
}

func TestDeleteRuntimeClass_MetadataOnlyPathDoesNotDependOnRuntimeCallback(t *testing.T) {
	f := NewFacade()
	cfg := config.GetInstance()
	cfg.ContainerEngine = "edgelet"
	embedded := true
	buildmeta.SetHasEmbeddedEngineForTest(&embedded)
	t.Cleanup(func() { buildmeta.SetHasEmbeddedEngineForTest(nil) })

	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })

	if _, err := f.ApplyLocalRuntimeClassManifest(`
apiVersion: edgelet.iofog.org/v1
kind: RuntimeClass
metadata:
  name: edgelet-wasmtime
handler: edgelet-wasmtime
`, false); err != nil {
		t.Fatalf("failed to seed runtimeclass: %v", err)
	}

	err := f.DeleteRuntimeClass("edgelet-wasmtime")
	if err != nil {
		t.Fatalf("expected delete success for metadata-only runtimeclass path, got: %v", err)
	}
	if _, getErr := f.db.GetLocalRuntimeClass("edgelet-wasmtime"); !errors.Is(getErr, sql.ErrNoRows) {
		t.Fatalf("expected runtimeclass deleted, got: %v", getErr)
	}
}

func TestDeleteRuntimeClass_RejectsReservedName(t *testing.T) {
	f := NewFacade()
	cfg := config.GetInstance()
	cfg.ContainerEngine = "edgelet"
	embedded := true
	buildmeta.SetHasEmbeddedEngineForTest(&embedded)
	t.Cleanup(func() { buildmeta.SetHasEmbeddedEngineForTest(nil) })

	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })

	err := f.DeleteRuntimeClass("crun")
	if err == nil {
		t.Fatal("expected reserved runtime delete rejection")
	}
	var reservedErr *ErrReservedRuntimeClassDelete
	if !errors.As(err, &reservedErr) {
		t.Fatalf("expected reserved delete error type, got: %T %v", err, err)
	}
}

func TestDeleteRuntimeClass_RejectsWhenRuntimeInUse(t *testing.T) {
	f := NewFacade()
	cfg := config.GetInstance()
	cfg.ContainerEngine = "edgelet"
	embedded := true
	buildmeta.SetHasEmbeddedEngineForTest(&embedded)
	t.Cleanup(func() { buildmeta.SetHasEmbeddedEngineForTest(nil) })

	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })

	if _, err := f.ApplyLocalRuntimeClassManifest(`
apiVersion: edgelet.iofog.org/v1
kind: RuntimeClass
metadata:
  name: edgelet-wasmtime
handler: edgelet-wasmtime
`, false); err != nil {
		t.Fatalf("failed to seed runtimeclass: %v", err)
	}

	ms := &models.LocalDeployedMicroservice{
		LocalUUID:        "11111111-1111-1111-1111-111111111111",
		ApplicationName:  "edgelet",
		MicroserviceName: "runtime-edgelet-ms",
		SourceName:       "local-cli",
		ManifestYAML: `apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: runtime-edgelet-ms
spec:
  container:
    runtime: edgelet-wasmtime
`,
		ImageName:    "ghcr.io/containerd/runwasi/wasi-demo-app:latest",
		State:        "running",
		RuntimeState: "running",
		DesiredState: "running",
	}
	if err := f.db.UpsertLocalWorkload(ms); err != nil {
		t.Fatalf("failed to seed local deployed microservice: %v", err)
	}

	err := f.DeleteRuntimeClass("edgelet-wasmtime")
	if err == nil {
		t.Fatal("expected runtime-in-use delete rejection")
	}
	var inUseErr *ErrRuntimeClassInUse
	if !errors.As(err, &inUseErr) {
		t.Fatalf("expected runtime-in-use error type, got: %T %v", err, err)
	}
	if len(inUseErr.BlockingMicroserviceUuids) == 0 || inUseErr.BlockingMicroserviceUuids[0] != ms.LocalUUID {
		t.Fatalf("expected blocking uuid in error, got=%v", inUseErr.BlockingMicroserviceUuids)
	}
}

func TestNormalizeRuntimeClassOperationStage(t *testing.T) {
	tests := map[string]string{
		"write_config":  RuntimeClassStageWriteConfig,
		"persisting":    RuntimeClassStageWriteConfig,
		"reconfiguring": RuntimeClassStageStopRuntime,
		"done":          RuntimeClassStageDone,
		"unknown":       "",
	}
	for input, expected := range tests {
		if got := NormalizeRuntimeClassOperationStage(input); got != expected {
			t.Fatalf("NormalizeRuntimeClassOperationStage(%q) = %q, expected %q", input, got, expected)
		}
	}
}

func TestRemoveRuntimeMicroservice_LocalRowGoneAndIdempotent(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })

	item := &models.LocalDeployedMicroservice{
		LocalUUID:        "local-rm-1",
		ApplicationName:  "edgelet",
		MicroserviceName: "rm-target",
		SourceName:       "local-cli",
		ManifestYAML:     "kind: Microservice",
		ImageName:        "nginx:latest",
		DesiredState:     "running",
		RuntimeState:     "running",
		State:            "running",
	}
	if err := f.db.UpsertLocalWorkload(item); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if _, err := f.RemoveRuntimeMicroservice("local-rm-1"); err != nil {
		t.Fatalf("first rm: %v", err)
	}
	got, err := f.db.GetLocalWorkload("local-rm-1")
	if err == nil && got != nil {
		t.Fatalf("expected local row deleted after rm, got %+v", got)
	}
	if _, err := f.RemoveRuntimeMicroservice("local-rm-1"); err != nil {
		t.Fatalf("second rm should be idempotent, got: %v", err)
	}
}

func TestListRuntimeMicroservices_HidesLocalTombstone(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() {
		_ = f.db.Close()
		f.sr.ResetProcessManagerStatus()
	})
	f.fa.Clear()
	deletedAtSec := time.Now().Unix()
	item := &models.LocalDeployedMicroservice{
		LocalUUID:        "local-tombstone",
		ApplicationName:  "edgelet",
		MicroserviceName: "gone",
		SourceName:       "local-cli",
		ManifestYAML:     "kind: Microservice",
		ImageName:        "nginx:latest",
		DesiredState:     "deleted",
		RuntimeState:     "deleted",
		State:            "deleted",
		DeletedAt:        &deletedAtSec,
	}
	if err := f.db.UpsertLocalWorkload(item); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	items := f.ListRuntimeMicroservices()
	for _, entry := range items {
		if entry["uuid"] == "local-tombstone" {
			t.Fatalf("expected tombstone hidden from ms ls, got %#v", items)
		}
	}
}

func TestApplyLocalManifest_SkipsGoneTombstoneAndAllocatesNewUUID(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })
	deletedAtSec := time.Now().Unix()
	tombstone := &models.LocalDeployedMicroservice{
		LocalUUID:        "old-router-uuid",
		ApplicationName:  "edgelet",
		MicroserviceName: "router",
		SourceName:       "local-cli",
		ManifestYAML:     testLocalManifestYAML(),
		ImageName:        "nginx:latest",
		DesiredState:     "deleted",
		RuntimeState:     "deleted",
		State:            "deleted",
		DeletedAt:        &deletedAtSec,
	}
	if err := f.db.UpsertLocalWorkload(tombstone); err != nil {
		t.Fatalf("upsert tombstone: %v", err)
	}
	_, _, applyErr := f.ApplyLocalManifest(testLocalManifestYAML(), "cli", false, nil)
	if applyErr == nil {
		t.Fatal("expected runtime failure in unit test")
	}
	items, err := f.ListLocalDeployments()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var live *models.LocalDeployedMicroservice
	for _, item := range items {
		if item != nil && !item.IsGone() {
			live = item
			break
		}
	}
	if live == nil {
		t.Fatal("expected a new live local row after apply over tombstone")
	}
	if live.LocalUUID == "old-router-uuid" {
		t.Fatal("expected apply to allocate a new uuid instead of patching the tombstone")
	}
}

func TestApplyLocalManifest_RejectsDeletingName(t *testing.T) {
	f := NewFacade()
	if err := f.db.Open(t.TempDir()); err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	t.Cleanup(func() { _ = f.db.Close() })
	deletedAtSec := time.Now().Unix()
	item := &models.LocalDeployedMicroservice{
		LocalUUID:        "deleting-router",
		ApplicationName:  "edgelet",
		MicroserviceName: "router",
		SourceName:       "local-cli",
		ManifestYAML:     testLocalManifestYAML(),
		ImageName:        "nginx:latest",
		DesiredState:     "deleted",
		RuntimeState:     "deleting",
		State:            "deleting",
		ContainerID:      "still-here",
		DeletedAt:        &deletedAtSec,
	}
	if err := f.db.UpsertLocalWorkload(item); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	_, _, err := f.ApplyLocalManifest(testLocalManifestYAML(), "cli", true, nil)
	if err == nil || !strings.Contains(err.Error(), "is deleting") {
		t.Fatalf("expected deleting name to be rejected, got: %v", err)
	}
}
