// Package modelpull pulls and stores AI model artifacts on the node.
package modelpull

import (
	"regexp"
	"strings"
)

const (
	RevisionKindTag    = "tag"
	RevisionKindDigest = "digest"
	RevisionKindCommit = "commit"
	RevisionKindBranch = "branch"

	defaultOCITag     = "latest"
	defaultHFRevision = "main"
)

var (
	sha256DigestPattern = regexp.MustCompile(`(?i)^sha256:[a-f0-9]{64}$`)
	hfCommitPattern     = regexp.MustCompile(`(?i)^[a-f0-9]{40}$`)
)

// OCIRef is a resolved OCI artifact reference derived from spec.repo and spec.revision.
type OCIRef struct {
	// Repo is the repository path without a registry host.
	Repo string
	// Reference is the tag or digest passed to the registry (e.g. "latest" or "sha256:…").
	Reference string
	// Tag is the store index tag (repo:tag or repo@sha256:…).
	Tag string
	// Floating is true for tags (including latest) and false for digest pins.
	Floating bool
	// Kind is "tag" or "digest".
	Kind string
	// Requested is the original revision string after trim.
	Requested string
	// ResolvedRevision is the revision recorded after resolve (latest when empty).
	ResolvedRevision string
}

// ResolveOCIRevision maps a model revision onto an OCI tag or digest reference.
// An empty revision becomes "latest". A sha256:<64 hex> revision is a digest pin.
func ResolveOCIRevision(repo, revision string) OCIRef {
	repo = strings.TrimSpace(repo)
	revision = strings.TrimSpace(revision)
	ref := OCIRef{
		Repo:      repo,
		Requested: revision,
	}
	if IsOCIDigest(revision) {
		digest := strings.ToLower(revision)
		ref.Reference = digest
		ref.Tag = repo + "@" + digest
		ref.Floating = false
		ref.Kind = RevisionKindDigest
		ref.ResolvedRevision = digest
		return ref
	}
	if revision == "" {
		revision = defaultOCITag
	}
	ref.Reference = revision
	ref.Tag = repo + ":" + revision
	ref.Floating = true
	ref.Kind = RevisionKindTag
	ref.ResolvedRevision = revision
	return ref
}

// IsOCIDigest reports whether revision is a sha256 digest pin.
func IsOCIDigest(revision string) bool {
	return sha256DigestPattern.MatchString(strings.TrimSpace(revision))
}

// HFRef is a resolved Hugging Face Hub revision derived from spec.revision.
type HFRef struct {
	// Revision is the value sent to the Hub (main, branch, tag, or commit).
	Revision string
	// Floating is true for branches/tags (including the empty→main default).
	Floating bool
	// Kind is "commit" or "branch".
	Kind string
	// Requested is the original revision string after trim.
	Requested string
}

// ResolveHFRevision maps a model revision onto a Hub revision parameter.
// An empty revision becomes "main". A 40-character hex string is a commit pin.
func ResolveHFRevision(revision string) HFRef {
	revision = strings.TrimSpace(revision)
	ref := HFRef{Requested: revision}
	if IsHFCommit(revision) {
		ref.Revision = strings.ToLower(revision)
		ref.Floating = false
		ref.Kind = RevisionKindCommit
		return ref
	}
	if revision == "" {
		revision = defaultHFRevision
	}
	ref.Revision = revision
	ref.Floating = true
	ref.Kind = RevisionKindBranch
	return ref
}

// IsHFCommit reports whether revision is a 40-character git commit pin.
func IsHFCommit(revision string) bool {
	return hfCommitPattern.MatchString(strings.TrimSpace(revision))
}
