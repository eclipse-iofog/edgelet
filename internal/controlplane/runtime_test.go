package controlplane

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/workloadmeta"
)

func validControlPlaneManifestForRuntimeTest() *models.ControlPlaneManifest {
	doc := &models.ControlPlaneManifest{}
	doc.APIVersion = "edgelet.iofog.org/v1"
	doc.Kind = "ControlPlane"
	doc.Metadata.Name = "pot"
	doc.Metadata.Namespace = "default"
	doc.Spec.Controller.Image = controlPlaneTestImage
	doc.Spec.Auth = models.ValidEmbeddedAuthForTest()
	return doc
}

func TestBuildMicroserviceFromControlPlaneLaunchSpec(t *testing.T) {
	doc := validControlPlaneManifestForRuntimeTest()
	port := 51121
	console := 8008
	doc.Spec.Controller.Port = &port
	doc.Spec.Console.Port = &console

	ms, err := BuildMicroserviceFromControlPlane(doc, "cp-uuid-1", doc.ManifestControllerImage())
	if err != nil {
		t.Fatalf("build microservice: %v", err)
	}

	if ms.MicroserviceUUID != "cp-uuid-1" {
		t.Fatalf("expected uuid cp-uuid-1, got %q", ms.MicroserviceUUID)
	}
	if ms.ApplicationName != "default" || ms.MicroserviceName != "pot" {
		t.Fatalf("expected application=default name=pot, got application=%q name=%q", ms.ApplicationName, ms.MicroserviceName)
	}
	if !ms.IsController || !ms.IsSystem {
		t.Fatal("expected controller system workload flags")
	}
	if ms.HostNetworkMode || ms.IsPrivileged {
		t.Fatal("expected bridge non-privileged controller container")
	}

	if len(ms.PortMappings) != 2 {
		t.Fatalf("expected 2 port mappings, got %d", len(ms.PortMappings))
	}
	if ms.PortMappings[0].Outside != HostAPIPort || ms.PortMappings[0].Inside != port {
		t.Fatalf("unexpected API port mapping: %+v", ms.PortMappings[0])
	}
	if ms.PortMappings[1].Outside != HostConsolePort || ms.PortMappings[1].Inside != console {
		t.Fatalf("unexpected console port mapping: %+v", ms.PortMappings[1])
	}

	if len(ms.VolumeMappings) != 2 {
		t.Fatalf("expected 2 named volumes, got %d", len(ms.VolumeMappings))
	}
	if ms.VolumeMappings[0].HostDestination != VolumeDBName {
		t.Fatalf("expected db volume %q, got %q", VolumeDBName, ms.VolumeMappings[0].HostDestination)
	}
	if ms.VolumeMappings[0].Type != models.VolumeMappingTypeVolume || ms.VolumeMappings[0].EffectiveVolumeScope() != models.VolumeScopePrivate {
		t.Fatalf("control-plane db volume must be private VOLUME, got type=%q scope=%q", ms.VolumeMappings[0].Type, ms.VolumeMappings[0].EffectiveVolumeScope())
	}
	if ms.VolumeMappings[1].Type != models.VolumeMappingTypeVolume || ms.VolumeMappings[1].EffectiveVolumeScope() != models.VolumeScopePrivate {
		t.Fatalf("control-plane log volume must be private VOLUME, got type=%q scope=%q", ms.VolumeMappings[1].Type, ms.VolumeMappings[1].EffectiveVolumeScope())
	}

	hasNetRaw := false
	for _, cap := range ms.CapAdd {
		if strings.EqualFold(cap, "NET_RAW") {
			hasNetRaw = true
		}
	}
	for _, drop := range ms.CapDrop {
		if strings.EqualFold(drop, "NET_RAW") {
			t.Fatal("NET_RAW must not be dropped for control plane")
		}
	}
	if !hasNetRaw {
		t.Fatal("expected NET_RAW capability")
	}

	env := map[string]string{}
	for _, item := range ms.EnvVars {
		env[item.Key] = item.Value
	}
	if env["CONTROL_PLANE"] != "Remote" || env["CONTROLLER_UUID"] != "cp-uuid-1" {
		t.Fatalf("unexpected controller env: %#v", env)
	}

	in := workloadmeta.BuildInput{
		MicroserviceUUID: ms.MicroserviceUUID,
		MicroserviceName: ms.MicroserviceName,
		ApplicationName:  ms.ApplicationName,
		NodeUUID:         "node-1",
		RuntimeEngine:    workloadmeta.RuntimeEngineEdgelet,
		IsController:     ms.IsController,
		IsSystem:         ms.IsSystem,
	}
	labels := workloadmeta.BuildLabels(in)
	if labels[workloadmeta.LabelAppPartOf] != "default" {
		t.Fatalf("expected part-of default, got %q", labels[workloadmeta.LabelAppPartOf])
	}
	if labels[workloadmeta.LabelAppName] != "pot" {
		t.Fatalf("expected name pot, got %q", labels[workloadmeta.LabelAppName])
	}
	if labels[workloadmeta.LabelRole] != workloadmeta.RoleController {
		t.Fatalf("expected role controller, got %q", labels[workloadmeta.LabelRole])
	}
	if labels[workloadmeta.LabelSystem] != "true" {
		t.Fatalf("expected system=true, got %q", labels[workloadmeta.LabelSystem])
	}
	if labels[workloadmeta.LabelScope] != workloadmeta.ScopeLocal {
		t.Fatalf("expected scope local, got %q", labels[workloadmeta.LabelScope])
	}
}

func TestBuildMicroserviceFromControlPlaneTLSPathMount(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{models.ControlPlaneTLSCertFilename, models.ControlPlaneTLSKeyFilename} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("test"), 0o600); err != nil {
			t.Fatalf("write cert file: %v", err)
		}
	}

	doc := validControlPlaneManifestForRuntimeTest()
	doc.Spec.TLS = &models.ControlPlaneTLSConfig{
		Path: dir,
	}

	ms, err := BuildMicroserviceFromControlPlane(doc, "cp-uuid-2", doc.ManifestControllerImage())
	if err != nil {
		t.Fatalf("build microservice: %v", err)
	}
	if len(ms.VolumeMappings) != 3 {
		t.Fatalf("expected db+log+cert mounts, got %d", len(ms.VolumeMappings))
	}
	certMount := ms.VolumeMappings[2]
	if certMount.Type != models.VolumeMappingTypeBind {
		t.Fatalf("expected bind mount for cert path, got %q", certMount.Type)
	}
	if certMount.HostDestination != filepath.Clean(dir) || certMount.ContainerDestination != ContainerCertMountPath {
		t.Fatalf("unexpected cert mount: %+v", certMount)
	}
	if certMount.AccessMode != "ro" {
		t.Fatalf("expected read-only cert mount, got %q", certMount.AccessMode)
	}
}

func TestBuildMicroserviceFromControlPlaneTLSPathRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{models.ControlPlaneTLSCertFilename, models.ControlPlaneTLSKeyFilename} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("test"), 0o600); err != nil {
			t.Fatalf("write cert file: %v", err)
		}
	}
	doc := validControlPlaneManifestForRuntimeTest()
	doc.Spec.TLS = &models.ControlPlaneTLSConfig{
		Path: dir + string(filepath.Separator) + ".." + string(filepath.Separator) + filepath.Base(dir),
	}
	if _, err := BuildMicroserviceFromControlPlane(doc, "cp-uuid-3", doc.ManifestControllerImage()); err == nil ||
		!strings.Contains(err.Error(), "traversal") {
		t.Fatalf("expected traversal rejection, got: %v", err)
	}
}

func TestMergeControlPlaneCapabilitiesAlwaysAddsNetRaw(t *testing.T) {
	add, drop := mergeControlPlaneCapabilities(nil, []string{"NET_RAW", "SYS_ADMIN"})
	if len(drop) != 1 || drop[0] != "SYS_ADMIN" {
		t.Fatalf("expected NET_RAW stripped from drop, got %#v", drop)
	}
	found := false
	for _, cap := range add {
		if strings.EqualFold(cap, "NET_RAW") {
			found = true
		}
	}
	if !found {
		t.Fatal("expected NET_RAW in cap add")
	}
}
