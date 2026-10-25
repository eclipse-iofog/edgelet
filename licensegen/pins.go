package main

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

// Pins are the embed inputs that are not Go modules. versions come from
// scripts/version.sh so the SBOM cannot drift from the build.
type Pins struct {
	Go          string
	Crun        string
	CNI         string
	PauseRepo   string
	PauseDigest string
	K3sRoot     string
	K3sSHA      map[string]string
}

var (
	assignRE = regexp.MustCompile(`(?m)^([A-Z0-9_]+)="([^"]*)"$`)
	archRE   = regexp.MustCompile(`(?m)^[ \t]+(amd64|arm64|arm|riscv64)\)\n[ \t]+echo "([0-9a-f]{64})"`)
	digestRE = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

func parsePins(path string) (Pins, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Pins{}, err
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")
	vals := map[string]string{}
	for _, m := range assignRE.FindAllStringSubmatch(text, -1) {
		vals[m[1]] = m[2]
	}
	pins := Pins{
		Go:          vals["VERSION_GOLANG"],
		Crun:        vals["VERSION_CRUN"],
		CNI:         vals["VERSION_CNIPLUGINS"],
		PauseRepo:   vals["VERSION_PAUSE_REPO"],
		PauseDigest: vals["VERSION_PAUSE_DIGEST"],
		K3sRoot:     vals["VERSION_ROOT"],
		K3sSHA:      map[string]string{},
	}
	for _, m := range archRE.FindAllStringSubmatch(text, -1) {
		pins.K3sSHA[m[1]] = m[2]
	}
	if err := pins.validate(); err != nil {
		return Pins{}, fmt.Errorf("%s: %w", path, err)
	}
	return pins, nil
}

func (p Pins) validate() error {
	if p.Go == "" || p.Crun == "" || p.CNI == "" || p.K3sRoot == "" {
		return fmt.Errorf("missing VERSION_GOLANG, VERSION_CRUN, VERSION_CNIPLUGINS, or VERSION_ROOT")
	}
	if p.PauseRepo == "" {
		return fmt.Errorf("missing VERSION_PAUSE_REPO")
	}
	if !digestRE.MatchString(p.PauseDigest) {
		return fmt.Errorf("VERSION_PAUSE_DIGEST must be sha256:<64 hex>, got %q", p.PauseDigest)
	}
	for _, arch := range []string{"amd64", "arm64", "arm", "riscv64"} {
		sum := p.K3sSHA[arch]
		if len(sum) != 64 {
			return fmt.Errorf("missing k3s-root sha256 for %s", arch)
		}
	}
	return nil
}

func (p Pins) pausePURL() string {
	return "pkg:oci/pause@" + p.PauseDigest + "?repository_url=docker.io/" + p.PauseRepo
}
