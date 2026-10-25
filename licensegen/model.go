package main

import "strings"

const (
	scopeRequired = "required"
	scopeOptional = "optional"
	scopeExcluded = "excluded"

	relDepends  = "depends"
	relContains = "contains"
	relTest     = "test"
	relExcluded = "excluded"
)

// Component is one SBOM entry. Ref is the CycloneDX bom-ref and must be unique.
type Component struct {
	Ref         string
	Type        string
	Name        string
	Version     string
	PURL        string
	Scope       string
	License     string
	LicenseText string
	Closures    []string
	Hashes      []Hash
	Props       []Prop
	Note        string
	Relation    string
	ContainedBy string
}

type Hash struct {
	Alg     string
	Content string
}

type Prop struct {
	Name  string
	Value string
}

func (c Component) spdxID() string {
	var b strings.Builder
	b.WriteString("SPDXRef-")
	for _, r := range c.Ref {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

func prop(name, value string) Prop {
	return Prop{Name: name, Value: value}
}
