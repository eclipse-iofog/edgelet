package edgeguard

import (
	"fmt"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/auth"
)

type signatureAction int

const (
	signatureRebaseline signatureAction = iota
	signatureDeprovision
	signatureRefreshLock
)

func linkLockFromPayload(payload FingerprintPayload) []auth.EdgeGuardLink {
	normalized := normalizeFingerprintPayload(payload)
	links := make([]auth.EdgeGuardLink, 0, len(normalized.PrimaryNICs))
	for _, nic := range normalized.PrimaryNICs {
		links = append(links, auth.EdgeGuardLink{
			Name:      nic.Name,
			MAC:       nic.MAC,
			LinkState: normalizeLinkState(nic.LinkState),
		})
	}
	return links
}

func evaluateSignature(linksPresent bool, storedHash, newHash string, locked, live []auth.EdgeGuardLink) (signatureAction, []auth.EdgeGuardLink) {
	if !linksPresent {
		return signatureRebaseline, live
	}
	if storedHash != newHash || linkLockBroken(locked, live) {
		return signatureDeprovision, nil
	}
	return signatureRefreshLock, locked
}

func linkLockBroken(locked, live []auth.EdgeGuardLink) bool {
	liveByKey := indexLinks(live)
	for _, lock := range locked {
		current, ok := liveByKey[linkKey(lock)]
		if !ok {
			continue
		}
		lockedState := normalizeLinkState(lock.LinkState)
		liveState := normalizeLinkState(current.LinkState)
		if (lockedState == "0" || lockedState == "unknown") && liveState == "1" {
			return true
		}
	}
	return false
}

type downwardNotice struct {
	message string
}

// applyDownwardObservations returns a log line for each locked-up NIC whose
// live carrier moved to 0 or unknown since the previous observation. The
// returned map is the observation state for the next check. Recovery to 1
// clears that NIC so a later drop is logged again.
func applyDownwardObservations(previous map[string]string, locked, live []auth.EdgeGuardLink) ([]downwardNotice, map[string]string) {
	next := make(map[string]string, len(previous))
	for key, state := range previous {
		next[key] = state
	}
	liveByKey := indexLinks(live)
	notices := make([]downwardNotice, 0)
	for _, lock := range locked {
		if normalizeLinkState(lock.LinkState) != "1" {
			continue
		}
		current, ok := liveByKey[linkKey(lock)]
		key := linkKey(lock)
		if !ok {
			delete(next, key)
			continue
		}
		liveState := normalizeLinkState(current.LinkState)
		if liveState == "1" {
			delete(next, key)
			continue
		}
		if liveState != "0" && liveState != "unknown" {
			continue
		}
		if previous[key] == liveState {
			continue
		}
		notices = append(notices, downwardNotice{
			message: fmt.Sprintf(
				"NIC %s (%s) link state 1 -> %s; node stays provisioned",
				lock.Name, lock.MAC, liveState,
			),
		})
		next[key] = liveState
	}
	return notices, next
}

func indexLinks(links []auth.EdgeGuardLink) map[string]auth.EdgeGuardLink {
	indexed := make(map[string]auth.EdgeGuardLink, len(links))
	for _, link := range links {
		indexed[linkKey(link)] = link
	}
	return indexed
}

func linkKey(link auth.EdgeGuardLink) string {
	return normalizeString(link.MAC) + "|" + normalizeString(link.Name)
}

func normalizeLinkState(state string) string {
	state = strings.ToLower(strings.TrimSpace(state))
	if state == "" {
		return "unknown"
	}
	return state
}
