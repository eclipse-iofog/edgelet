package edgeguard

import (
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/auth"
)

func TestEvaluateSignatureLinkDirection(t *testing.T) {
	lockedUp := []auth.EdgeGuardLink{{Name: "eth0", MAC: "aa:bb:cc:dd:ee:ff", LinkState: "1"}}
	lockedDown := []auth.EdgeGuardLink{{Name: "eth0", MAC: "aa:bb:cc:dd:ee:ff", LinkState: "0"}}
	lockedUnknown := []auth.EdgeGuardLink{{Name: "eth0", MAC: "aa:bb:cc:dd:ee:ff", LinkState: "unknown"}}
	liveDown := []auth.EdgeGuardLink{{Name: "eth0", MAC: "aa:bb:cc:dd:ee:ff", LinkState: "0"}}
	liveUnknown := []auth.EdgeGuardLink{{Name: "eth0", MAC: "aa:bb:cc:dd:ee:ff", LinkState: "unknown"}}
	liveUp := []auth.EdgeGuardLink{{Name: "eth0", MAC: "aa:bb:cc:dd:ee:ff", LinkState: "1"}}

	action, persist := evaluateSignature(true, "hash", "hash", lockedUp, liveDown)
	if action != signatureRefreshLock {
		t.Fatalf("1 -> 0 action = %d, want refresh", action)
	}
	if len(persist) != 1 || persist[0].LinkState != "1" {
		t.Fatalf("1 -> 0 persisted lock = %+v, want linkState 1", persist)
	}

	action, _ = evaluateSignature(true, "hash", "hash", lockedUp, liveUnknown)
	if action != signatureRefreshLock {
		t.Fatalf("1 -> unknown action = %d, want refresh", action)
	}

	action, persist = evaluateSignature(true, "hash", "hash", persist, liveUp)
	if action != signatureRefreshLock {
		t.Fatalf("1 -> 0 -> 1 action = %d, want refresh", action)
	}
	if len(persist) != 1 || persist[0].LinkState != "1" {
		t.Fatalf("recovery persisted lock = %+v, want linkState 1", persist)
	}

	action, _ = evaluateSignature(true, "hash", "hash", lockedDown, liveUp)
	if action != signatureDeprovision {
		t.Fatalf("0 -> 1 action = %d, want deprovision", action)
	}
	action, _ = evaluateSignature(true, "hash", "hash", lockedUnknown, liveUp)
	if action != signatureDeprovision {
		t.Fatalf("unknown -> 1 action = %d, want deprovision", action)
	}

	action, _ = evaluateSignature(true, "hash", "hash", lockedDown, liveUnknown)
	if action != signatureRefreshLock {
		t.Fatalf("0 -> unknown action = %d, want refresh", action)
	}
	action, _ = evaluateSignature(true, "hash", "hash", lockedUnknown, liveDown)
	if action != signatureRefreshLock {
		t.Fatalf("unknown -> 0 action = %d, want refresh", action)
	}

	action, _ = evaluateSignature(true, "stored", "live", lockedUp, liveDown)
	if action != signatureDeprovision {
		t.Fatalf("identity mismatch action = %d, want deprovision", action)
	}

	action, gotLive := evaluateSignature(false, "stored", "different", lockedUp, liveDown)
	if action != signatureRebaseline {
		t.Fatalf("missing link lock action = %d, want rebaseline", action)
	}
	if len(gotLive) != 1 || gotLive[0].LinkState != "0" {
		t.Fatalf("rebaseline links = %+v, want live linkState 0", gotLive)
	}
}

func TestDownwardLinkLogOncePerTransition(t *testing.T) {
	locked := []auth.EdgeGuardLink{{Name: "eth0", MAC: "aa:bb:cc:dd:ee:ff", LinkState: "1"}}
	liveDown := []auth.EdgeGuardLink{{Name: "eth0", MAC: "aa:bb:cc:dd:ee:ff", LinkState: "0"}}
	liveUp := []auth.EdgeGuardLink{{Name: "eth0", MAC: "aa:bb:cc:dd:ee:ff", LinkState: "1"}}

	first, observed := applyDownwardObservations(nil, locked, liveDown)
	if len(first) != 1 {
		t.Fatalf("first downward notices = %d, want 1", len(first))
	}
	second, observed := applyDownwardObservations(observed, locked, liveDown)
	if len(second) != 0 {
		t.Fatalf("repeated downward notices = %d, want 0", len(second))
	}
	_, observed = applyDownwardObservations(observed, locked, liveUp)
	third, _ := applyDownwardObservations(observed, locked, liveDown)
	if len(third) != 1 {
		t.Fatalf("downward after recovery notices = %d, want 1", len(third))
	}
}
