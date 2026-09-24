//go:build linux

package resourceconsumption

import (
	"os"
	"strings"
)

func hostOSReleasePrettyName() string {
	data, err := os.ReadFile("/etc/os-release") // #nosec G703 -- standard host identity path
	if err != nil {
		return ""
	}
	return parseOSReleasePrettyName(string(data))
}

func parseOSReleasePrettyName(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || key != "PRETTY_NAME" {
			continue
		}
		return strings.Trim(value, `"`)
	}
	return ""
}
