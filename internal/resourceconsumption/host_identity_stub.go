//go:build !linux

package resourceconsumption

func hostOSReleasePrettyName() string {
	return ""
}
