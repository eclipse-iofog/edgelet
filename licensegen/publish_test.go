package main

import (
	"strings"
	"testing"
)

func TestPublishedComponentsOmitExcludedAndVersionless(t *testing.T) {
	in := []Component{
		{Ref: "pkg:golang/keep@v1.0.0", Name: "keep", Version: "v1.0.0", Scope: scopeRequired, PURL: "pkg:golang/keep@v1.0.0"},
		{Ref: "pkg:golang/unused@v1.2.3", Name: "unused", Version: "v1.2.3", Scope: scopeExcluded, PURL: "pkg:golang/unused@v1.2.3"},
		{Ref: "pkg:generic/util-linux?vendored-by=k3s-root", Name: "util-linux", Scope: scopeRequired, PURL: "pkg:generic/util-linux?vendored-by=k3s-root"},
		{Ref: "pkg:golang/testonly@v1.2.3", Name: "testonly", Version: "v1.2.3", Scope: scopeOptional, PURL: "pkg:golang/testonly@v1.2.3"},
	}
	got := publishedComponents(in)
	if len(got) != 2 || got[0].Name != "keep" || got[1].Name != "testonly" {
		t.Fatalf("published = %+v", names(got))
	}

	subject := Component{Ref: "pkg:golang/github.com/eclipse-iofog/edgelet", Name: "edgelet", Version: "NOASSERTION", Type: "application"}
	files, err := rootDocuments(subject, in)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"sbom.cdx.json", "sbom.spdx.json"} {
		body := string(files[name])
		for _, hidden := range []string{"unused", "util-linux", "pkg:golang/unused@v1.2.3"} {
			if strings.Contains(body, hidden) {
				t.Fatalf("%s contains %q", name, hidden)
			}
		}
		if !strings.Contains(body, "pkg:golang/keep@v1.0.0") {
			t.Fatalf("%s dropped a versioned required component", name)
		}
	}
	md := string(files["SBOM.md"])
	for _, visible := range []string{"unused", "util-linux", "keep"} {
		if !strings.Contains(md, visible) {
			t.Fatalf("SBOM.md missing %q", visible)
		}
	}
	if !strings.Contains(string(files["LICENSE-3RD-PARTY.md"]), "util-linux") {
		t.Fatal("LICENSE-3RD-PARTY.md missing util-linux")
	}
}

func names(comps []Component) []string {
	out := make([]string, len(comps))
	for i, c := range comps {
		out[i] = c.Name
	}
	return out
}
