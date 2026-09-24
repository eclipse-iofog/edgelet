package handlers

import (
	"strings"
	"unicode"
)

func normalizeReportKey(raw string) string {
	key := strings.TrimSpace(strings.ToLower(raw))
	switch key {
	case "gps-coordinates(lat,lon)":
		return "gpsCoordinates"
	case "developer's-mode":
		return "developerMode"
	}

	replacer := strings.NewReplacer("-", " ", "_", " ", "/", " ", "(", " ", ")", " ", ",", " ", "'", "")
	key = replacer.Replace(key)
	parts := strings.Fields(key)
	if len(parts) == 0 {
		return ""
	}
	camel := parts[0]
	for _, p := range parts[1:] {
		if p == "" {
			continue
		}
		runes := []rune(p)
		runes[0] = unicode.ToUpper(runes[0])
		camel += string(runes)
	}
	return fixStatusReportKeyAcronyms(camel)
}

func fixStatusReportKeyAcronyms(key string) string {
	switch key {
	case "agentCpuPercent":
		return "agentCpu"
	case "runtimeCpuPercent":
		return "runtimeCpu"
	case "agentMemoryMib":
		return "agentMemory"
	case "runtimeMemoryMib":
		return "runtimeMemory"
	case "edgeletTotalCpuPercent":
		return "edgeletStackCpu"
	case "edgeletTotalMemoryMib", "edgeletStackMemoryMib":
		return "edgeletStackMemory"
	default:
		return key
	}
}
