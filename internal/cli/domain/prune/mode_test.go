package prune

import (
	"strings"
	"testing"
)

func TestParseMode_RejectsInvalidMode(t *testing.T) {
	_, err := ParseMode([]string{"invalid"}, "usage")
	if err == nil || !strings.Contains(err.Error(), "dangling|containers|volumes|all") {
		t.Fatalf("expected invalid mode error, got: %v", err)
	}
}

func TestParseImageMode_RejectsNonDanglingMode(t *testing.T) {
	_, err := ParseImageMode([]string{"volumes"}, "usage")
	if err == nil || !strings.Contains(err.Error(), "supports only dangling mode") {
		t.Fatalf("expected dangling-only validation error, got: %v", err)
	}
}

func TestParseModelMode_RejectsNonDanglingMode(t *testing.T) {
	_, err := ParseModelMode([]string{"all"}, "usage")
	if err == nil || !strings.Contains(err.Error(), "model prune supports only dangling mode") {
		t.Fatalf("expected dangling-only validation error, got: %v", err)
	}
}

func TestParseKnowledgeMode_RejectsNonDanglingMode(t *testing.T) {
	_, err := ParseKnowledgeMode([]string{"all"}, "usage")
	if err == nil || !strings.Contains(err.Error(), "knowledge prune supports only dangling mode") {
		t.Fatalf("expected dangling-only validation error, got: %v", err)
	}
}
