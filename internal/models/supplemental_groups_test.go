package models

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func localManifestWithGroups(t *testing.T, groupsYAML string) *LocalDeployManifest {
	t.Helper()
	raw := fmt.Sprintf(`
apiVersion: edgelet.iofog.org/v1
kind: Microservice
metadata:
  name: groups
spec:
  image: nginx:latest
  container:
    supplementalGroups: %s
`, groupsYAML)
	doc := &LocalDeployManifest{}
	dec := yaml.NewDecoder(strings.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(doc); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	return doc
}

func TestLocalManifestSupplementalGroupsSorted(t *testing.T) {
	doc := localManifestWithGroups(t, "[993, 44]")
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	ms := BuildMicroserviceFromLocalManifest(doc, "dep-groups", doc.ManifestImage())
	if !slices.Equal(ms.SupplementalGroups, []int64{44, 993}) {
		t.Fatalf("groups=%v", ms.SupplementalGroups)
	}
}

func TestLocalManifestSupplementalGroupsKeepsZero(t *testing.T) {
	doc := localManifestWithGroups(t, "[993, 0]")
	if err := doc.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	ms := BuildMicroserviceFromLocalManifest(doc, "dep-zero", doc.ManifestImage())
	if !slices.Equal(ms.SupplementalGroups, []int64{0, 993}) {
		t.Fatalf("groups=%v", ms.SupplementalGroups)
	}
}

func TestLocalManifestSupplementalGroupsRejectsInvalid(t *testing.T) {
	tooMany := make([]string, 17)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("%d", i+1)
	}
	cases := []struct {
		name  string
		value string
	}{
		{name: "name", value: "[render]"},
		{name: "negative string", value: `["-1"]`},
		{name: "negative", value: "[-1]"},
		{name: "duplicate", value: "[44, 44]"},
		{name: "too many", value: "[" + strings.Join(tooMany, ", ") + "]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := localManifestWithGroups(t, tc.value)
			err := doc.Validate()
			if err == nil || !strings.Contains(err.Error(), "supplementalGroups") {
				t.Fatalf("expected supplementalGroups validate error, got %v", err)
			}
		})
	}
}

func TestMicroserviceSupplementalGroupsCanonicalized(t *testing.T) {
	ms := NewMicroservice("ms-groups", "nginx:latest")
	ms.SupplementalGroups = []int64{993, 0, 44}
	if err := ms.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !slices.Equal(ms.SupplementalGroups, []int64{0, 44, 993}) {
		t.Fatalf("groups=%v", ms.SupplementalGroups)
	}

	ms.SupplementalGroups = []int64{44, 44}
	if err := ms.Validate(); err == nil {
		t.Fatal("expected duplicate group ID to fail")
	}
}

func TestNormalizeSupplementalGroupsEmpty(t *testing.T) {
	for _, raw := range []any{nil, []any(nil), []int64(nil), []any{}} {
		groups, err := NormalizeSupplementalGroups(raw)
		if err != nil {
			t.Fatalf("empty %T: %v", raw, err)
		}
		if len(groups) != 0 {
			t.Fatalf("empty %T produced %v", raw, groups)
		}
	}
}
