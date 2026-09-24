package containerapply

import (
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/models"
)

func TestNanoCPUsAndCPUQuota(t *testing.T) {
	if got := NanoCPUs(2.5); got != 2500000000 {
		t.Fatalf("NanoCPUs(2.5) = %d, want 2500000000", got)
	}
	if got := CPUQuota(2.5); got != 250000 {
		t.Fatalf("CPUQuota(2.5) = %d, want 250000", got)
	}
}

func TestFingerprintIgnoresCatalogItems(t *testing.T) {
	ms := models.NewMicroservice("ms-1", "alpine:3.19")
	ms.Models = &models.ModelCatalog{
		BindPath:    "/models",
		Permissions: "ro",
		Items:       []models.ModelCatalogItem{{Name: "a"}},
	}
	a := FromMicroservice(ms)
	ms.Models.Items = append(ms.Models.Items, models.ModelCatalogItem{Name: "b"})
	b := FromMicroservice(ms)
	aj, _ := Marshal(a)
	bj, _ := Marshal(b)
	if aj != bj {
		t.Fatalf("item membership must not change fingerprint:\n%s\n%s", aj, bj)
	}
	ms.Models.Permissions = "rw"
	if MatchesLabel(aj, ms) {
		t.Fatal("permissions change must not match")
	}
}

func TestFingerprintEmptyVsNonemptyCatalog(t *testing.T) {
	ms := models.NewMicroservice("ms-1", "alpine:3.19")
	empty, err := Marshal(FromMicroservice(ms))
	if err != nil {
		t.Fatal(err)
	}
	ms.Models = &models.ModelCatalog{
		BindPath:    "/models",
		Permissions: "ro",
		Items:       []models.ModelCatalogItem{{Name: "a"}},
	}
	if MatchesLabel(empty, ms) {
		t.Fatal("adding a catalog must recreate")
	}
	withCatalog, err := Marshal(FromMicroservice(ms))
	if err != nil {
		t.Fatal(err)
	}
	ms.Models.Items = nil
	if MatchesLabel(withCatalog, ms) {
		t.Fatal("removing the last catalog item must recreate")
	}
}

func TestMatchesLabelLegacyEmpty(t *testing.T) {
	ms := models.NewMicroservice("ms-1", "alpine:3.19")
	if !MatchesLabel("", ms) {
		t.Fatal("legacy container with no apply fields should match")
	}
	rw := "rw"
	ms.RunAsGroup = &rw
	if MatchesLabel("", ms) {
		t.Fatal("new apply field without stored fingerprint must recreate")
	}
}

func TestCommandArgsOmitVsSet(t *testing.T) {
	ms := models.NewMicroservice("ms-1", "alpine:3.19")
	if CommandArgs(ms) != nil {
		t.Fatalf("omit commands should leave image default, got %v", CommandArgs(ms))
	}
	empty := []string{}
	ms.Commands = &empty
	if CommandArgs(ms) != nil {
		t.Fatalf("empty commands should leave image default, got %v", CommandArgs(ms))
	}
	set := []string{"--serve"}
	ms.Commands = &set
	got := CommandArgs(ms)
	if len(got) != 1 || got[0] != "--serve" {
		t.Fatalf("set commands = %v", got)
	}
}

func TestDockerUserBothSet(t *testing.T) {
	u, g := "1000", "100"
	if got := DockerUser(&u, &g); got != "1000:100" {
		t.Fatalf("DockerUser = %q", got)
	}
	colon := "0:0"
	if got := DockerUser(&colon, &g); got != "0:0" {
		t.Fatalf("user with colon must not append group, got %q", got)
	}
}

func TestCatalogBindROVsRW(t *testing.T) {
	ms := models.NewMicroservice("ms-1", "alpine:3.19")
	ms.Models = &models.ModelCatalog{
		BindPath:    "/models",
		Permissions: "ro",
		Items:       []models.ModelCatalogItem{{Name: "m"}},
	}
	host, dest, ro, ok := CatalogBind(ms, "/var/lib/edgelet")
	if !ok || !ro || dest != "/models" || host == "" {
		t.Fatalf("ro bind: host=%q dest=%q ro=%v ok=%v", host, dest, ro, ok)
	}
	ms.Models.Permissions = "rw"
	_, _, ro, ok = CatalogBind(ms, "/var/lib/edgelet")
	if !ok || ro {
		t.Fatal("rw bind must not be read-only")
	}
}

func TestKnowledgeCatalogBindROVsRW(t *testing.T) {
	ms := models.NewMicroservice("ms-1", "alpine:3.19")
	ms.Knowledge = &models.KnowledgeCatalog{
		BindPath:    "/knowledge",
		Permissions: "ro",
		Items:       []models.KnowledgeCatalogItem{{Name: "product-docs"}},
	}
	host, dest, ro, ok := KnowledgeCatalogBind(ms, "/var/lib/edgelet")
	if !ok || !ro || dest != "/knowledge" || !strings.Contains(host, "/volumes/microservices/ms-1/knowledge") {
		t.Fatalf("ro bind: host=%q dest=%q ro=%v ok=%v", host, dest, ro, ok)
	}
	ms.Knowledge.Permissions = "rw"
	_, _, ro, ok = KnowledgeCatalogBind(ms, "/var/lib/edgelet")
	if !ok || ro {
		t.Fatal("rw knowledge bind must not be read-only")
	}
}

func TestFingerprintIgnoresKnowledgeCatalogItems(t *testing.T) {
	ms := models.NewMicroservice("ms-1", "alpine:3.19")
	ms.Knowledge = &models.KnowledgeCatalog{
		BindPath:    "/knowledge",
		Permissions: "ro",
		Items:       []models.KnowledgeCatalogItem{{Name: "a"}},
	}
	a := FromMicroservice(ms)
	ms.Knowledge.Items = append(ms.Knowledge.Items, models.KnowledgeCatalogItem{Name: "b"})
	b := FromMicroservice(ms)
	aj, _ := Marshal(a)
	bj, _ := Marshal(b)
	if aj != bj {
		t.Fatalf("knowledge item membership must not change fingerprint:\n%s\n%s", aj, bj)
	}
	ms.Knowledge.BindPath = "/corpus"
	if MatchesLabel(aj, ms) {
		t.Fatal("knowledge bindPath change must not match")
	}
}

func TestApplyLabelIncludesEnvHash(t *testing.T) {
	ms := models.NewMicroservice("ms-1", "alpine:3.19")
	env := []string{"FOO=bar", "BAZ=qux"}
	label, err := ApplyLabel(ms, env)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := EnvHashFromLabel(label)
	if !ok {
		t.Fatal("new apply label must include an environment digest")
	}
	if got != HashEnv(env) {
		t.Fatalf("env hash = %q want %q", got, HashEnv(env))
	}
	if HashEnv([]string{"BAZ=qux", "FOO=bar"}) != HashEnv(env) {
		t.Fatal("environment digest must be order-independent")
	}
	if !MatchesLabel(label, ms) {
		t.Fatal("digest on the label must still match apply fields")
	}
}

func TestMatchesLabelIgnoresMissingEnvHash(t *testing.T) {
	ms := models.NewMicroservice("ms-1", "alpine:3.19")
	ms.Models = &models.ModelCatalog{
		BindPath:    "/models",
		Permissions: "ro",
		Items:       []models.ModelCatalogItem{{Name: "a"}},
	}
	old, err := Marshal(FromMicroservice(ms))
	if err != nil {
		t.Fatal(err)
	}
	if HasEnvHash(old) {
		t.Fatal("legacy label must not report an environment digest")
	}
	if !MatchesLabel(old, ms) {
		t.Fatal("legacy label without a digest must still match apply fields")
	}
}

func TestPOSIXRlimitsUnlimited(t *testing.T) {
	limits := map[string]models.Ulimit{
		"nofile":  {Soft: 65536, Hard: 65536},
		"memlock": {Soft: -1, Hard: -1},
	}
	got := POSIXRlimits(limits)
	if len(got) != 2 {
		t.Fatalf("rlimits = %d", len(got))
	}
	var sawUnlimited bool
	for _, r := range got {
		if r.Type == "RLIMIT_MEMLOCK" {
			sawUnlimited = r.Hard == ^uint64(0) && r.Soft == ^uint64(0)
		}
	}
	if !sawUnlimited {
		t.Fatal("expected unlimited memlock")
	}
}
