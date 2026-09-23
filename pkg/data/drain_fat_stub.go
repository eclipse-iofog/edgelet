//go:build !linux || cgo

package data

import "errors"

// ReadyCurrentRuntime is unavailable when this build has no embedded bundle.
func ReadyCurrentRuntime(string, string) (string, bool, error) {
	return "", false, nil
}

// StageDrainFatELF is unavailable when this build has no embedded bundle.
func StageDrainFatELF(string) (string, error) {
	return "", errors.New("no embedded data bundle found")
}
