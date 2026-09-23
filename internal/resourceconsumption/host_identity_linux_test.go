//go:build linux

package resourceconsumption

import "testing"

func TestParseOSReleasePrettyName(t *testing.T) {
	const sample = `NAME="Ubuntu"
VERSION="24.04.4 LTS (Noble Numbat)"
PRETTY_NAME="Ubuntu 24.04.4 LTS"
ID=ubuntu
`
	got := parseOSReleasePrettyName(sample)
	if got != "Ubuntu 24.04.4 LTS" {
		t.Fatalf("got %q", got)
	}
}
