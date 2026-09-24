//go:build !linux

package cdidevices

// ListAvailable is empty on desktop platforms.
func ListAvailable(_ string) []string {
	return []string{}
}
