package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const (
	cdxSerialPlaceholder = "urn:uuid:00000000-0000-5000-8000-000000000000"
	spdxNSPlaceholder    = "https://github.com/eclipse-iofog/edgelet/sbom/0000000000000000000000000000000000000000000000000000000000000000"
	epoch                = "1970-01-01T00:00:00Z"
)

func renderCDX(subject Component, comps []Component) ([]byte, error) {
	doc := map[string]any{
		"bomFormat":    "CycloneDX",
		"specVersion":  "1.6",
		"serialNumber": cdxSerialPlaceholder,
		"version":      1,
		"metadata": map[string]any{
			"timestamp": epoch,
			"tools": map[string]any{
				"components": []any{
					map[string]any{"type": "application", "name": "edgelet-licensegen", "version": "1"},
				},
			},
			"component": cdxComponent(subject),
		},
		"components":   cdxComponents(comps),
		"dependencies": cdxDependencies(subject, comps),
	}
	raw, err := marshal(doc)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	id := uuidFrom(sum[:])
	return []byte(strings.Replace(string(raw), cdxSerialPlaceholder, id, 1)), nil
}

func renderSPDX(subject Component, comps []Component) ([]byte, error) {
	pkgs := make([]any, 0, len(comps)+1)
	pkgs = append(pkgs, spdxPackage(subject))
	seen := map[string]string{subject.spdxID(): subject.Ref}
	for _, c := range comps {
		id := c.spdxID()
		if prev, ok := seen[id]; ok {
			return nil, fmt.Errorf("SPDX ID collision %s for %s and %s", id, prev, c.Ref)
		}
		seen[id] = c.Ref
		pkgs = append(pkgs, spdxPackage(c))
	}
	doc := map[string]any{
		"spdxVersion":       "SPDX-2.3",
		"dataLicense":       "CC0-1.0",
		"SPDXID":            "SPDXRef-DOCUMENT",
		"name":              "edgelet",
		"documentNamespace": spdxNSPlaceholder,
		"creationInfo": map[string]any{
			"created":  epoch,
			"creators": []string{"Tool: edgelet-licensegen-1"},
		},
		"packages":                pkgs,
		"relationships":           spdxRelationships(subject, comps),
		"extractedLicensingInfos": spdxExtracted(subject, comps),
	}
	raw, err := marshal(doc)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	ns := "https://github.com/eclipse-iofog/edgelet/sbom/" + hexEncode(sum[:])
	return []byte(strings.Replace(string(raw), spdxNSPlaceholder, ns, 1)), nil
}

func cdxComponents(comps []Component) []any {
	out := make([]any, 0, len(comps))
	for _, c := range comps {
		out = append(out, cdxComponent(c))
	}
	return out
}

func cdxComponent(c Component) map[string]any {
	m := map[string]any{
		"type":    c.Type,
		"name":    c.Name,
		"bom-ref": c.Ref,
	}
	if c.Version != "" {
		m["version"] = c.Version
	}
	if c.Scope != "" {
		m["scope"] = c.Scope
	}
	if c.PURL != "" {
		m["purl"] = c.PURL
	}
	if c.Note != "" {
		m["description"] = c.Note
	}
	if c.License != "" {
		m["licenses"] = []any{map[string]any{"expression": c.License}}
	}
	if len(c.Hashes) > 0 {
		hashes := make([]any, 0, len(c.Hashes))
		for _, h := range c.Hashes {
			hashes = append(hashes, map[string]any{"alg": h.Alg, "content": h.Content})
		}
		m["hashes"] = hashes
	}
	if len(c.Props) > 0 || len(c.Closures) > 0 {
		props := append([]Prop(nil), c.Props...)
		if len(c.Closures) > 0 {
			props = append(props, prop("edgelet:closures", strings.Join(c.Closures, ",")))
		}
		sort.Slice(props, func(i, j int) bool {
			if props[i].Name == props[j].Name {
				return props[i].Value < props[j].Value
			}
			return props[i].Name < props[j].Name
		})
		arr := make([]any, 0, len(props))
		for _, p := range props {
			arr = append(arr, map[string]any{"name": p.Name, "value": p.Value})
		}
		m["properties"] = arr
	}
	return m
}

func cdxDependencies(subject Component, comps []Component) []any {
	deps := map[string][]string{}
	add := func(from, to string) {
		for _, have := range deps[from] {
			if have == to {
				return
			}
		}
		deps[from] = append(deps[from], to)
	}
	for _, c := range comps {
		if c.Scope == scopeRequired {
			add(subject.Ref, c.Ref)
		}
		for _, p := range c.Props {
			if p.Name == "edgelet:contained-by" {
				add(p.Value, c.Ref)
			}
		}
	}
	keys := make([]string, 0, len(deps))
	for k := range deps {
		keys = append(keys, k)
		sort.Strings(deps[k])
	}
	sort.Strings(keys)
	out := make([]any, 0, len(keys))
	for _, k := range keys {
		out = append(out, map[string]any{"ref": k, "dependsOn": deps[k]})
	}
	return out
}

func spdxPackage(c Component) map[string]any {
	ver := c.Version
	if ver == "" {
		ver = "NOASSERTION"
	}
	m := map[string]any{
		"SPDXID":           c.spdxID(),
		"name":             c.Name,
		"versionInfo":      ver,
		"downloadLocation": "NOASSERTION",
		"filesAnalyzed":    false,
		"licenseConcluded": emptyLicense(c.License),
		"licenseDeclared":  emptyLicense(c.License),
		"copyrightText":    "NOASSERTION",
	}
	if c.PURL != "" {
		m["externalRefs"] = []any{map[string]any{
			"referenceCategory": "PACKAGE-MANAGER",
			"referenceType":     "purl",
			"referenceLocator":  c.PURL,
		}}
	}
	if c.Note != "" {
		m["description"] = c.Note
	}
	if len(c.Hashes) > 0 {
		checksums := make([]any, 0, len(c.Hashes))
		for _, h := range c.Hashes {
			checksums = append(checksums, map[string]any{"algorithm": h.Alg, "checksumValue": h.Content})
		}
		m["checksums"] = checksums
	}
	return m
}

func spdxRelationships(subject Component, comps []Component) []any {
	type rel struct{ a, kind, b string }
	var rows []rel
	rows = append(rows, rel{"SPDXRef-DOCUMENT", "DESCRIBES", subject.spdxID()})
	for _, c := range comps {
		switch c.Relation {
		case relTest:
			rows = append(rows, rel{c.spdxID(), "TEST_DEPENDENCY_OF", subject.spdxID()})
		case relExcluded:
			rows = append(rows, rel{c.spdxID(), "OPTIONAL_DEPENDENCY_OF", subject.spdxID()})
		default:
			contained := false
			for _, p := range c.Props {
				if p.Name == "edgelet:contained-by" {
					rows = append(rows, rel{parentSPDX(p.Value, subject, comps), "CONTAINS", c.spdxID()})
					contained = true
				}
			}
			if !contained {
				rows = append(rows, rel{subject.spdxID(), "DEPENDS_ON", c.spdxID()})
			} else {
				rows = append(rows, rel{subject.spdxID(), "DEPENDS_ON", c.spdxID()})
			}
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].a != rows[j].a {
			return rows[i].a < rows[j].a
		}
		if rows[i].kind != rows[j].kind {
			return rows[i].kind < rows[j].kind
		}
		return rows[i].b < rows[j].b
	})
	out := make([]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{
			"spdxElementId":      r.a,
			"relationshipType":   r.kind,
			"relatedSpdxElement": r.b,
		})
	}
	return out
}

func parentSPDX(ref string, subject Component, comps []Component) string {
	if ref == subject.Ref {
		return subject.spdxID()
	}
	for _, c := range comps {
		if c.Ref == ref {
			return c.spdxID()
		}
	}
	return subject.spdxID()
}

func spdxExtracted(subject Component, comps []Component) []any {
	texts := map[string]string{}
	add := func(c Component) {
		for _, id := range expressionRefs(c.License) {
			if _, ok := texts[id]; ok {
				continue
			}
			if c.LicenseText != "" {
				texts[id] = c.LicenseText
			}
		}
	}
	add(subject)
	for _, c := range comps {
		add(c)
	}
	if len(texts) == 0 {
		return []any{}
	}
	ids := make([]string, 0, len(texts))
	for id := range texts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]any, 0, len(ids))
	for _, id := range ids {
		out = append(out, map[string]any{
			"licenseId":     id,
			"extractedText": texts[id],
		})
	}
	return out
}

func emptyLicense(id string) string {
	if id == "" {
		return "NOASSERTION"
	}
	return id
}

func renderSBOM(subject Component, comps []Component) []byte {
	var b strings.Builder
	b.WriteString("# Software Bill of Materials\n\n")
	b.WriteString("Generated by `licensegen`. Do not edit by hand. Regenerate with `make sbom`.\n\n")
	b.WriteString("This file describes the **source tree**: which components this commit links and embeds, at which versions, under which licenses. It is not the SBOM of a built binary. GitHub Releases ship one CycloneDX file and one SPDX file beside each `edgelet-<os>-<arch>` binary. Those files add the binary hash and, on Linux, the staged embed file hashes.\n\n")
	b.WriteString("`sbom.cdx.json` and `sbom.spdx.json` list required and optional components that have a version. They omit excluded modules, which are not compiled into a shipped binary. They also omit contained programs that have no upstream version of their own (BusyBox, iproute2, iptables, nftables, ebtables, and util-linux). Those programs stay in the inventory below and in `LICENSE-3RD-PARTY.md`. They are shipped inside the pinned k3s-root tarball.\n\n")
	b.WriteString("Container image attestations are not in this document.\n\n")
	b.WriteString("## Scopes\n\n")
	b.WriteString("| Scope | Meaning |\n| --- | --- |\n")
	b.WriteString("| required | Linked into a shipped binary, or staged into the Linux embed bundle |\n")
	b.WriteString("| optional | Compiled into tests only |\n")
	b.WriteString("| excluded | Present in `go list -m all` and compiled into nothing here |\n\n")
	counts := map[string]int{}
	for _, c := range comps {
		counts[c.Scope]++
	}
	fmt.Fprintf(&b, "Required %d, optional %d, excluded %d.\n\n", counts[scopeRequired], counts[scopeOptional], counts[scopeExcluded])
	b.WriteString("## Inventory\n\n")
	for _, scope := range []string{scopeRequired, scopeOptional, scopeExcluded} {
		n := 0
		for _, c := range comps {
			if c.Scope == scope {
				n++
			}
		}
		if n == 0 {
			continue
		}
		fmt.Fprintf(&b, "### %s\n\n", scope)
		b.WriteString("| Component | Version | License | Where |\n| --- | --- | --- | --- |\n")
		for _, c := range comps {
			if c.Scope != scope {
				continue
			}
			where := strings.Join(c.Closures, ", ")
			if where == "" {
				where = "—"
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", mdCell(c.Name), mdCell(showVersion(c.Version)), mdCell(c.License), mdCell(where))
		}
		b.WriteString("\n")
	}
	b.WriteString("## Embed bundle\n\n")
	b.WriteString("Linux thin binaries embed one zstd bundle. The bundle contains the fat runtime, `containerd-shim-runc-v2`, crun, the CNI multicall, a staged subset of k3s-root (`bin/busybox`, `bin/ip`, `bin/aux`), and `images/pause.tar.gz`. SQLite is linked into that fat runtime and into the desktop binaries. The Linux thin ELF does not link it.\n\n")
	b.WriteString("k3s-root hashes in this document are the upstream tarball pins from `scripts/version.sh`. They are not hashes of the staged subset. The k3s-root source repository is Apache-2.0 for its build scripts and Buildroot config. That license does not cover the programs in the tarball; those are the GPL components listed above. The pause hash is the multi-arch image index digest. The archive is retagged `portainer/pause:latest` so the CRI sandbox ref stays stable. The pause binary is Kubernetes `build/pause/linux/pause.c`, which is Apache-2.0. portainer/kubesolo compiles that source; the MIT license of the kubesolo repository does not cover the image.\n\n")
	b.WriteString("Corresponding source for GPL binaries: [crun](https://github.com/containers/crun) at the pinned tag, and [k3s-root](https://github.com/k3s-io/k3s-root) at the pinned tag (buildroot config in that tag builds BusyBox, iproute2, iptables, nftables, ebtables, and util-linux).\n\n")
	b.WriteString("Full license texts are in `LICENSE-3RD-PARTY.md`.\n\n")
	b.WriteString("## What this document does not claim\n\n")
	b.WriteString("- No hash of a built `edgelet` binary. That hash is on the release SBOM.\n")
	b.WriteString("- No file-level analysis of Go module contents. SPDX `filesAnalyzed` is false.\n")
	b.WriteString("- The subject version is `NOASSERTION` because this file describes the tree, not a release tag.\n")
	b.WriteString("- Timestamps are the Unix epoch so a regeneration of an unchanged tree is byte-identical.\n")
	_ = subject
	return []byte(b.String())
}

func renderNotices(comps []Component) []byte {
	type item struct {
		id  string
		ref string
	}
	type group struct {
		items []item
		text  string
	}
	var order []string
	groups := map[string]*group{}
	for _, c := range comps {
		key := "id:" + c.License
		if c.LicenseText != "" {
			key = "text:" + hashBytes([]byte(c.LicenseText))
		}
		g, ok := groups[key]
		if !ok {
			g = &group{text: c.LicenseText}
			groups[key] = g
			order = append(order, key)
		}
		g.items = append(g.items, item{id: c.License, ref: c.Name + "@" + showVersion(c.Version)})
	}
	var b strings.Builder
	b.WriteString("# Third-party notices\n\n")
	b.WriteString("Generated by `licensegen`. Do not edit by hand. Regenerate with `make sbom`.\n\n")
	b.WriteString("This file is the license text for components in `SBOM.md`. A GitHub Release copies it next to the binaries.\n\n")
	b.WriteString("GPL components are BusyBox, iproute2, iptables, nftables, ebtables, util-linux (staged from the pinned k3s-root tag), and crun (pinned tag). Their corresponding source is the k3s-root tag and the crun tag named in `SBOM.md`.\n\n")
	for _, key := range order {
		g := groups[key]
		sort.Slice(g.items, func(i, j int) bool {
			if g.items[i].ref == g.items[j].ref {
				return g.items[i].id < g.items[j].id
			}
			return g.items[i].ref < g.items[j].ref
		})
		ids := map[string]struct{}{}
		var idList []string
		for _, it := range g.items {
			if _, ok := ids[it.id]; ok {
				continue
			}
			ids[it.id] = struct{}{}
			idList = append(idList, it.id)
		}
		sort.Strings(idList)
		fmt.Fprintf(&b, "## %s\n\n", mdHeading(strings.Join(idList, ", ")))
		b.WriteString("Components:\n\n")
		for _, it := range g.items {
			fmt.Fprintf(&b, "- %s (%s)\n", it.ref, it.id)
		}
		b.WriteByte('\n')
		if g.text == "" {
			if len(idList) == 1 && idList[0] == "NOASSERTION" {
				b.WriteString("No license text was found for these components.\n\n")
			} else {
				b.WriteString("Standard SPDX identifier. Module copies of this license are included under their own text when the module ships a LICENSE file.\n\n")
			}
			continue
		}
		b.WriteString(fenceLicense(g.text))
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func showVersion(v string) string {
	if v == "" {
		return "—"
	}
	return v
}

func mdCell(s string) string {
	return strings.ReplaceAll(s, "|", "\\|")
}

func mdHeading(s string) string {
	if s == "" {
		return "NOASSERTION"
	}
	return strings.ReplaceAll(s, "\n", " ")
}

// fenceLicense wraps license text so Markdown in an upstream file stays literal.
// The fence is longer than any backtick run in the text.
func fenceLicense(text string) string {
	n := 3
	run := 0
	for _, r := range text {
		if r == '`' {
			run++
			if run >= n {
				n = run + 1
			}
			continue
		}
		run = 0
	}
	bar := strings.Repeat("`", n)
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return bar + "\n" + text + bar + "\n"
}

func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func uuidFrom(sum []byte) string {
	b := make([]byte, 16)
	copy(b, sum[:16])
	b[6] = (b[6] & 0x0f) | 0x50
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("urn:uuid:%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func hexEncode(b []byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexdigits[v>>4]
		out[i*2+1] = hexdigits[v&0x0f]
	}
	return string(out)
}
