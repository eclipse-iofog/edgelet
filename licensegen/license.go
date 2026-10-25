package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// detectLicense returns an SPDX expression for a license file body.
// An SPDX-License-Identifier line is used as written, so AND stays AND.
// Otherwise a file that contains more than one well-known grant is reported as OR.
// Unrecognized text becomes LicenseRef-<hash> so the identifier is stable.
func detectLicense(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if strings.TrimSpace(text) == "" {
		return "NOASSERTION"
	}
	if strings.Contains(text, "SQLite Is Public Domain") {
		return "LicenseRef-SQLite-PublicDomain"
	}
	if expr, ok := declaredSPDX(text); ok {
		return expr
	}
	var ids []string
	has := func(s string) bool { return strings.Contains(text, s) }
	lower := strings.ToLower(text)
	// MPL 2.0 section 1.12 names the GPL, LGPL, and AGPL. That sentence is not a grant.
	// Only the GPL document title, in capitals, counts as the GPL text itself.
	if strings.Contains(lower, "mozilla public license") && strings.Contains(lower, "2.0") {
		ids = append(ids, "MPL-2.0")
	}
	switch {
	case has("GNU LESSER GENERAL PUBLIC LICENSE"):
		ids = append(ids, gnuID("LGPL", text))
	case has("GNU GENERAL PUBLIC LICENSE"):
		ids = append(ids, gnuID("GPL", text))
	}
	if has("Apache License") && has("Version 2.0") {
		ids = append(ids, "Apache-2.0")
	}
	if has("Permission is hereby granted, free of charge") {
		ids = append(ids, "MIT")
	}
	if has("ISC License") || has("Permission to use, copy, modify, and/or distribute this software") {
		ids = append(ids, "ISC")
	}
	if has("Redistribution and use in source and binary forms") {
		if has("Neither the name") {
			ids = append(ids, "BSD-3-Clause")
		} else {
			ids = append(ids, "BSD-2-Clause")
		}
	}
	ids = uniqueStrings(ids)
	if len(ids) == 0 {
		sum := sha256.Sum256([]byte(text))
		return "LicenseRef-" + hex.EncodeToString(sum[:6])
	}
	return strings.Join(ids, " OR ")
}

func gnuID(kind, text string) string {
	ver := "2.0"
	switch {
	case strings.Contains(text, "Version 3"):
		ver = "3.0"
	case strings.Contains(text, "Version 2.1"):
		ver = "2.1"
	case strings.Contains(text, "Version 2"):
		ver = "2.0"
	}
	suffix := "-only"
	if strings.Contains(text, "any later version") {
		suffix = "-or-later"
	}
	return kind + "-" + ver + suffix
}

func licenseRef(id string) bool {
	return strings.HasPrefix(id, "LicenseRef-")
}

func declaredSPDX(text string) (string, bool) {
	const key = "spdx-license-identifier:"
	for _, line := range strings.Split(text, "\n") {
		i := strings.Index(strings.ToLower(line), key)
		if i < 0 {
			continue
		}
		expr := strings.TrimSpace(line[i+len(key):])
		expr = strings.Trim(expr, "`*_ ")
		expr = strings.TrimRight(expr, ".")
		expr = strings.TrimSpace(expr)
		if spdxExpr(expr) {
			return expr, true
		}
	}
	return "", false
}

func spdxExpr(expr string) bool {
	if expr == "" {
		return false
	}
	parts := strings.Fields(expr)
	if len(parts)%2 == 0 {
		return false
	}
	for i, part := range parts {
		if i%2 == 1 {
			if part != "AND" && part != "OR" && part != "WITH" {
				return false
			}
			continue
		}
		if !spdxID(part) {
			return false
		}
	}
	return true
}

func spdxID(id string) bool {
	if id == "" {
		return false
	}
	for i, r := range id {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case i > 0 && (r == '.' || r == '-' || r == '+'):
		default:
			return false
		}
	}
	return true
}

func expressionRefs(expr string) []string {
	var out []string
	for _, part := range strings.FieldsFunc(expr, func(r rune) bool {
		return r == ' ' || r == '(' || r == ')'
	}) {
		if part == "AND" || part == "OR" || part == "WITH" {
			continue
		}
		if licenseRef(part) {
			out = append(out, part)
		}
	}
	return out
}

func isLicenseFile(name string) bool {
	base := strings.ToLower(name)
	switch base {
	case "license", "licence", "copying", "notice", "unlicense",
		"license.md", "license.txt", "licence.md", "licence.txt",
		"copying.md", "copying.txt", "notice.md", "notice.txt":
		return true
	default:
		return strings.HasPrefix(base, "license-") && base != "license-3rd-party.md"
	}
}

func readLicenseText(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() || !isLicenseFile(e.Name()) {
			continue
		}
		names = append(names, e.Name())
	}
	if len(names) == 0 {
		return readmeLicense(dir, entries)
	}
	sort.Strings(names)
	var b strings.Builder
	for i, name := range names {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "", err
		}
		if i > 0 {
			b.WriteString("\n")
		}
		if len(names) > 1 {
			b.WriteString("--- ")
			b.WriteString(name)
			b.WriteString(" ---\n")
		}
		b.WriteString(strings.ReplaceAll(string(raw), "\r\n", "\n"))
		if !strings.HasSuffix(b.String(), "\n") {
			b.WriteByte('\n')
		}
	}
	return b.String(), nil
}

// readmeLicense reads a Readme only when the module has no license file.
// The text is kept when it contains a known license grant.
func readmeLicense(dir string, entries []os.DirEntry) (string, error) {
	var names []string
	for _, e := range entries {
		if e.IsDir() || !isReadme(e.Name()) {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return "", err
		}
		text := strings.ReplaceAll(string(raw), "\r\n", "\n")
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		id := detectLicense(text)
		if id == "NOASSERTION" || strings.HasPrefix(id, "LicenseRef-") {
			continue
		}
		return text, nil
	}
	return "", nil
}

func isReadme(name string) bool {
	base := strings.ToLower(name)
	switch base {
	case "readme", "readme.md", "readme.txt":
		return true
	default:
		return false
	}
}

func readSQLiteVersion(dir string) (string, error) {
	const needle = `const SQLITE_VERSION = "`
	var found string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || found != "" || d.IsDir() {
			return err
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(raw)
		i := strings.Index(text, needle)
		if i < 0 {
			return nil
		}
		rest := text[i+len(needle):]
		j := strings.IndexByte(rest, '"')
		if j <= 0 {
			return nil
		}
		found = rest[:j]
		return filepath.SkipAll
	})
	if err != nil && err != filepath.SkipAll {
		return "", err
	}
	if found == "" {
		return "", os.ErrNotExist
	}
	return found, nil
}

func uniqueStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	sort.Strings(in)
	out := in[:0:0]
	var prev string
	for i, s := range in {
		if i == 0 || s != prev {
			out = append(out, s)
			prev = s
		}
	}
	// copy so the caller can keep using the input
	cp := make([]string, len(out))
	copy(cp, out)
	return cp
}

func normalizeNewlines(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.TrimRightFunc(s, unicode.IsSpace)
	return s + "\n"
}
