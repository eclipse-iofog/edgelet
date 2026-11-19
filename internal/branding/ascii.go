package branding

import "strings"

// ReleaseLabel returns the linked version for the startup banner.
// A git tag already starts with v, so the banner must not add a second one.
func ReleaseLabel(version string) string {
	version = strings.TrimSpace(version)
	if version == "" {
		return "vdev"
	}
	if strings.HasPrefix(version, "v") || strings.HasPrefix(version, "V") {
		return version
	}
	return "v" + version
}

// EdgeletANSIShadow is the EDGELET logo in ANSI Shadow figlet style.
const EdgeletANSIShadow = "" +
	"███████╗██████╗  ██████╗ ███████╗██╗     ███████╗████████╗\n" +
	"██╔════╝██╔══██╗██╔════╝ ██╔════╝██║     ██╔════╝╚══██╔══╝\n" +
	"█████╗  ██║  ██║██║  ███╗█████╗  ██║     █████╗     ██║   \n" +
	"██╔══╝  ██║  ██║██║   ██║██╔══╝  ██║     ██╔══╝     ██║   \n" +
	"███████╗██████╔╝╚██████╔╝███████╗███████╗███████╗   ██║   \n" +
	"╚══════╝╚═════╝  ╚═════╝ ╚══════╝╚══════╝╚══════╝   ╚═╝   \n"
