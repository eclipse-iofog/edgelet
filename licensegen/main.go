package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	root := flag.String("root", ".", "repository root")
	check := flag.Bool("check", false, "fail if committed root documents differ")
	release := flag.Bool("release", false, "write a per-binary SBOM")
	binary := flag.String("binary", "", "path to a built edgelet binary")
	stage := flag.String("stage", "", "embed stage directory (Linux release SBOMs)")
	version := flag.String("version", "dev", "release version stamped into a per-binary SBOM")
	outDir := flag.String("out", "", "directory for per-binary SBOM files (default: beside the binary)")
	flag.Parse()

	if *check && *release {
		fmt.Fprintln(os.Stderr, "-check and -release are different documents")
		os.Exit(2)
	}
	repo, err := filepath.Abs(*root)
	if err != nil {
		fail(err)
	}
	comps, subject, err := buildInventory(repo)
	if err != nil {
		fail(err)
	}
	if *release {
		if err := writeRelease(comps, *binary, *stage, *version, *outDir); err != nil {
			fail(err)
		}
		return
	}
	files, err := rootDocuments(subject, comps)
	if err != nil {
		fail(err)
	}
	if *check {
		if err := checkFiles(repo, files); err != nil {
			fail(err)
		}
		fmt.Println("sbom check passed")
		return
	}
	if err := writeFiles(repo, files); err != nil {
		fail(err)
	}
	fmt.Println("wrote SBOM.md, LICENSE-3RD-PARTY.md, sbom.cdx.json, sbom.spdx.json")
}

func rootDocuments(subject Component, comps []Component) (map[string][]byte, error) {
	cdx, err := renderCDX(subject, comps)
	if err != nil {
		return nil, err
	}
	spdx, err := renderSPDX(subject, comps)
	if err != nil {
		return nil, err
	}
	return map[string][]byte{
		"SBOM.md":              renderSBOM(subject, comps),
		"LICENSE-3RD-PARTY.md": renderNotices(comps),
		"sbom.cdx.json":        cdx,
		"sbom.spdx.json":       spdx,
	}, nil
}

func writeRelease(comps []Component, binary, stage, version, outDir string) error {
	if binary == "" {
		return fmt.Errorf("-binary is required")
	}
	info, err := os.Stat(binary)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", binary)
	}
	osName, arch, err := artifactName(binary)
	if err != nil {
		return err
	}
	sum, err := hashFile(binary)
	if err != nil {
		return err
	}
	subject, selected, err := selectArtifact(comps, osName, arch, version, sum)
	if err != nil {
		return err
	}
	if osName == "linux" {
		if stage == "" {
			return fmt.Errorf("-stage is required for a Linux release SBOM")
		}
		selected, err = applyStage(selected, stage, version, arch)
		if err != nil {
			return err
		}
	}
	cdx, err := renderCDX(subject, selected)
	if err != nil {
		return err
	}
	spdx, err := renderSPDX(subject, selected)
	if err != nil {
		return err
	}
	if outDir == "" {
		outDir = filepath.Dir(binary)
	}
	base := filepath.Base(binary)
	files := map[string][]byte{
		base + ".cdx.json":  cdx,
		base + ".spdx.json": spdx,
	}
	return writeFiles(outDir, files)
}

func writeFiles(dir string, files map[string][]byte) error {
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func checkFiles(dir string, files map[string][]byte) error {
	var mismatched []string
	for _, name := range []string{"SBOM.md", "LICENSE-3RD-PARTY.md", "sbom.cdx.json", "sbom.spdx.json"} {
		have, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		want := files[name]
		if !bytes.Equal(have, want) {
			mismatched = append(mismatched, fmt.Sprintf("%s\n%s", name, firstDiff(have, want)))
		}
	}
	if len(mismatched) == 0 {
		return nil
	}
	return fmt.Errorf("generated SBOM documents are stale; run make sbom\n%s", strings.Join(mismatched, "\n"))
}

func firstDiff(have, want []byte) string {
	h := bytes.Split(have, []byte("\n"))
	w := bytes.Split(want, []byte("\n"))
	n := len(h)
	if len(w) < n {
		n = len(w)
	}
	for i := 0; i < n; i++ {
		if !bytes.Equal(h[i], w[i]) {
			return fmt.Sprintf("line %d\n committed: %s\n generated: %s", i+1, trunc(h[i]), trunc(w[i]))
		}
	}
	return fmt.Sprintf("length committed %d generated %d", len(have), len(want))
}

func trunc(b []byte) string {
	if len(b) > 200 {
		b = b[:200]
	}
	return string(b)
}

func fail(err error) {
	fmt.Fprintf(os.Stderr, "licensegen: %v\n", err)
	os.Exit(1)
}
