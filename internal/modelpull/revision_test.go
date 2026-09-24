package modelpull

import (
	"strings"
	"testing"
)

func TestResolveOCIRevision_Tag(t *testing.T) {
	got := ResolveOCIRevision("ai/gemma3", "4b-q8_0")
	if got.Reference != "4b-q8_0" {
		t.Fatalf("reference: got %q", got.Reference)
	}
	if got.Tag != "ai/gemma3:4b-q8_0" {
		t.Fatalf("tag: got %q", got.Tag)
	}
	if !got.Floating || got.Kind != RevisionKindTag {
		t.Fatalf("expected floating tag, got %+v", got)
	}
	if got.ResolvedRevision != "4b-q8_0" {
		t.Fatalf("resolved revision: got %q", got.ResolvedRevision)
	}
}

func TestResolveOCIRevision_Digest(t *testing.T) {
	digest := "sha256:1eca257ec64d465cf38f766561e28eddb3b764adfba703288106a31cd2fe84a8"
	got := ResolveOCIRevision("ai/gemma3", digest)
	if got.Reference != digest {
		t.Fatalf("reference: got %q", got.Reference)
	}
	if got.Tag != "ai/gemma3@"+digest {
		t.Fatalf("tag: got %q", got.Tag)
	}
	if got.Floating || got.Kind != RevisionKindDigest {
		t.Fatalf("expected pinned digest, got %+v", got)
	}
}

func TestResolveOCIRevision_EmptyLatest(t *testing.T) {
	got := ResolveOCIRevision("ai/gemma3", "")
	if got.Reference != "latest" || got.Tag != "ai/gemma3:latest" {
		t.Fatalf("expected latest tag, got %+v", got)
	}
	if !got.Floating || got.Kind != RevisionKindTag {
		t.Fatalf("expected floating latest, got %+v", got)
	}
	if got.ResolvedRevision != "latest" {
		t.Fatalf("resolved revision: got %q", got.ResolvedRevision)
	}
}

func TestIsOCIDigest(t *testing.T) {
	valid := "sha256:" + strings.Repeat("ab", 32)
	if !IsOCIDigest(valid) {
		t.Fatalf("expected %q to be a digest", valid)
	}
	if IsOCIDigest("sha256:abc") || IsOCIDigest("4b-q8_0") || IsOCIDigest("") {
		t.Fatal("expected non-digest revisions to be rejected")
	}
}

func TestResolveHFRevision_Commit(t *testing.T) {
	commit := "064fe43ea8c1e1f93477ef4a170bdc2b244ef02c"
	got := ResolveHFRevision(commit)
	if got.Revision != commit {
		t.Fatalf("revision: got %q", got.Revision)
	}
	if got.Floating || got.Kind != RevisionKindCommit {
		t.Fatalf("expected pinned commit, got %+v", got)
	}
	if got.Requested != commit {
		t.Fatalf("requested: got %q", got.Requested)
	}
}

func TestResolveHFRevision_EmptyMain(t *testing.T) {
	got := ResolveHFRevision("")
	if got.Revision != "main" {
		t.Fatalf("expected main, got %+v", got)
	}
	if !got.Floating || got.Kind != RevisionKindBranch {
		t.Fatalf("expected floating main, got %+v", got)
	}
	if got.Requested != "" {
		t.Fatalf("requested should stay empty, got %q", got.Requested)
	}
}

func TestResolveHFRevision_Branch(t *testing.T) {
	got := ResolveHFRevision("main")
	if got.Revision != "main" || !got.Floating || got.Kind != RevisionKindBranch {
		t.Fatalf("expected floating branch, got %+v", got)
	}
}

func TestIsHFCommit(t *testing.T) {
	valid := "064fe43ea8c1e1f93477ef4a170bdc2b244ef02c"
	if !IsHFCommit(valid) {
		t.Fatalf("expected %q to be a commit", valid)
	}
	if IsHFCommit("main") || IsHFCommit("064fe43") || IsHFCommit("") || IsHFCommit("sha256:abc") {
		t.Fatal("expected non-commit revisions to be rejected")
	}
}
