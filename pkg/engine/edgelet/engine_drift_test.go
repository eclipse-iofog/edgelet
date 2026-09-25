//go:build linux

package edgelet

import (
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/containerapply"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/workloadmeta"
	"github.com/eclipse-iofog/edgelet/pkg/imageref"
)

func TestMicroserviceImageMatches_DockerHubShortVsQualified(t *testing.T) {
	runtimeImage := "docker.io/emirhandurmus/frame-generator:v1.0.0"
	desiredImage := "emirhandurmus/frame-generator:v1.0.0"
	registry := models.NewRegistry(1, "https://docker.io/", true, "", "", "")
	registryURL, fromCache := imageref.MatchParamsOptional(registryURLFromRegistry(registry))
	if !microserviceImageMatches(runtimeImage, desiredImage, registryURL, fromCache) {
		t.Fatalf("expected %q and %q to match for drift check", runtimeImage, desiredImage)
	}
}

func TestMicroserviceImageMatches_QuayShortVsQualified(t *testing.T) {
	runtimeImage := "quay.io/org/app:v1"
	desiredImage := "org/app:v1"
	registry := models.NewRegistry(3, "quay.io", true, "", "", "")
	registryURL, fromCache := imageref.MatchParamsOptional(registryURLFromRegistry(registry))
	if !microserviceImageMatches(runtimeImage, desiredImage, registryURL, fromCache) {
		t.Fatalf("expected %q and %q to match with quay registry", runtimeImage, desiredImage)
	}
}

func TestMicroserviceImageMatches_QuayDoesNotMatchDockerHubAlias(t *testing.T) {
	runtimeImage := "docker.io/org/app:v1"
	desiredImage := "org/app:v1"
	registry := models.NewRegistry(3, "quay.io", true, "", "", "")
	registryURL, fromCache := imageref.MatchParamsOptional(registryURLFromRegistry(registry))
	if microserviceImageMatches(runtimeImage, desiredImage, registryURL, fromCache) {
		t.Fatalf("quay-bound %q must not match docker.io ref %q", desiredImage, runtimeImage)
	}
}

func TestMicroserviceImageMatches_DifferentTagsMismatch(t *testing.T) {
	runtimeImage := "docker.io/emirhandurmus/frame-generator:v1.0.0"
	desiredImage := "emirhandurmus/frame-generator:v2.0.0"
	registry := models.NewRegistry(1, "https://docker.io/", true, "", "", "")
	registryURL, fromCache := imageref.MatchParamsOptional(registryURLFromRegistry(registry))
	if microserviceImageMatches(runtimeImage, desiredImage, registryURL, fromCache) {
		t.Fatalf("expected tag mismatch between %q and %q", runtimeImage, desiredImage)
	}
}

func TestMatchParamsOptional_FromCacheRegistry(t *testing.T) {
	registry := models.NewRegistry(2, "from_cache", true, "", "", "")
	registryURL, fromCache := imageref.MatchParamsOptional(registryURLFromRegistry(registry))
	if registryURL != "from_cache" || !fromCache {
		t.Fatalf("from_cache registry: url=%q fromCache=%v", registryURL, fromCache)
	}
}

func TestNeedsOCISpec_HashPresentSkipsSpec(t *testing.T) {
	ms := models.NewMicroservice("ms-1", "alpine:3.19")
	env := []string{"FOO=bar"}
	label, err := containerapply.ApplyLabel(ms, env)
	if err != nil {
		t.Fatal(err)
	}
	if needsOCISpec(label, true) || needsOCISpec(label, false) {
		t.Fatal("a label with an environment digest must not read the OCI spec")
	}
}

func TestNeedsOCISpec_HashAbsentUsesSweep(t *testing.T) {
	ms := models.NewMicroservice("ms-1", "alpine:3.19")
	old, err := containerapply.Marshal(containerapply.FromMicroservice(ms))
	if err != nil {
		t.Fatal(err)
	}
	if needsOCISpec(old, false) {
		t.Fatal("fast path must not read the OCI spec when the digest is missing")
	}
	if !needsOCISpec(old, true) {
		t.Fatal("sweep must still read the OCI spec when the digest is missing")
	}
}

func TestMatchContainerInfo_MissingHashIsNotDrift(t *testing.T) {
	ms := models.NewMicroservice("ms-1", "alpine:3.19")
	old, err := containerapply.Marshal(containerapply.FromMicroservice(ms))
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{
		containerapply.LabelFingerprint: old,
		workloadmeta.LabelHostNetwork:   "false",
	}
	if !matchContainerInfo(ms, nil, "alpine:3.19", labels) {
		t.Fatal("a container without an environment digest must not be treated as drifted")
	}
}

func TestMatchContainerInfo_HashPresentEnvUnchanged(t *testing.T) {
	ms := models.NewMicroservice("ms-1", "alpine:3.19")
	desired := buildIofogContainerEnv(ms, nil)
	label, err := containerapply.ApplyLabel(ms, desired)
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{
		containerapply.LabelFingerprint: label,
		workloadmeta.LabelHostNetwork:   "false",
	}
	if !matchContainerInfo(ms, nil, "alpine:3.19", labels) {
		t.Fatal("info and labels must match when the digest is present and env is unchanged")
	}
	stored, ok := containerapply.EnvHashFromLabel(label)
	if !ok || stored != containerapply.HashEnv(desired) {
		t.Fatal("stored digest must match the desired environment")
	}
}
