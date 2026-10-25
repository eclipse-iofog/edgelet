package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePins(t *testing.T) {
	pins, err := parsePins("../scripts/version.sh")
	if err != nil {
		t.Fatal(err)
	}
	if pins.Go != "1.26.10" || pins.Crun != "1.28" || pins.CNI != "v1.9.1-k3s1" {
		t.Fatalf("unexpected pins: %+v", pins)
	}
	if pins.PauseRepo != "portainer/pause" {
		t.Fatalf("pause repo: %s", pins.PauseRepo)
	}
	if got := len(pins.K3sSHA); got != 4 {
		t.Fatalf("k3s arches: %d", got)
	}
}

func TestReadmeLicenseFallback(t *testing.T) {
	dir := t.TempDir()
	readme := "Package notes.\n\nPermission is hereby granted, free of charge, to any person\n"
	if err := os.WriteFile(filepath.Join(dir, "Readme"), []byte(readme), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readLicenseText(dir)
	if err != nil {
		t.Fatal(err)
	}
	if detectLicense(got) != "MIT" {
		t.Fatalf("readme license: %q", detectLicense(got))
	}
	plain := t.TempDir()
	if err := os.WriteFile(filepath.Join(plain, "Readme"), []byte("just a readme\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = readLicenseText(plain)
	if err != nil {
		t.Fatal(err)
	}
	if got != "" {
		t.Fatalf("plain readme kept: %q", got)
	}
	withLicense := t.TempDir()
	if err := os.WriteFile(filepath.Join(withLicense, "Readme"), []byte(readme), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(withLicense, "LICENSE"), []byte("Apache License\nVersion 2.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = readLicenseText(withLicense)
	if err != nil {
		t.Fatal(err)
	}
	if detectLicense(got) != "Apache-2.0" {
		t.Fatalf("license file lost: %q", detectLicense(got))
	}
}

func TestFenceLicense(t *testing.T) {
	got := fenceLicense("## COPYING ##\n```\nMPL\n```\n")
	if !strings.HasPrefix(got, "````\n") || !strings.HasSuffix(got, "\n````\n") {
		t.Fatalf("fence: %q", got)
	}
	if !strings.Contains(got, "## COPYING ##") || !strings.Contains(got, "```\nMPL\n```") {
		t.Fatalf("body changed: %q", got)
	}
	plain := fenceLicense("Apache License\n")
	if !strings.HasPrefix(plain, "```\n") || !strings.HasSuffix(plain, "\n```\n") {
		t.Fatalf("plain fence: %q", plain)
	}
}

func TestArtifactName(t *testing.T) {
	osName, arch, err := artifactName("build/edgelet-windows-amd64.exe")
	if err != nil {
		t.Fatal(err)
	}
	if osName != "windows" || arch != "amd64" {
		t.Fatalf("got %s %s", osName, arch)
	}
	osName, arch, err = artifactName("edgelet-linux-arm")
	if err != nil {
		t.Fatal(err)
	}
	if osName != "linux" || arch != "arm" {
		t.Fatalf("got %s %s", osName, arch)
	}
}

func TestDetectLicense(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"Apache License\nVersion 2.0, January 2004\n", "Apache-2.0"},
		{"Permission is hereby granted, free of charge, to any person\n", "MIT"},
		{"Apache License\nVersion 2.0\n\nPermission is hereby granted, free of charge\n", "Apache-2.0 OR MIT"},
		{"Redistribution and use in source and binary forms\nNeither the name of the authors\n", "BSD-3-Clause"},
		{"SQLite Is Public Domain\n", "LicenseRef-SQLite-PublicDomain"},
		{"GNU GENERAL PUBLIC LICENSE\nVersion 2, June 1991\n", "GPL-2.0-only"},
		{"GNU GENERAL PUBLIC LICENSE\nVersion 2\nany later version\n", "GPL-2.0-or-later"},
		{"Mozilla Public License Version 2.0\nmeans either the GNU General Public License, Version 2.0, or any later versions of those licenses.\n", "MPL-2.0"},
		{"Mozilla Public License, version 2.0\nmeans either the GNU General Public License, Version 2.0, the GNU Lesser General Public License, Version 2.1, or any later versions of those licenses.\n", "MPL-2.0"},
		{"`SPDX-License-Identifier: BSD-3-Clause AND MPL-2.0`\nRedistribution and use in source and binary forms\nNeither the name\nMozilla Public License Version 2.0\nGNU General Public License\n", "BSD-3-Clause AND MPL-2.0"},
	}
	for _, tc := range cases {
		if got := detectLicense(tc.text); got != tc.want {
			t.Errorf("detect(%q) = %s, want %s", tc.text, got, tc.want)
		}
	}
}
