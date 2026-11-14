// Package bundle names the runtime file stored in the thin binary's zstd bundle.
package bundle

const (
	// FatName is the fat runtime file name.
	FatName = "edgelet-kube"
	// FatPath is the fat runtime path inside the bundle.
	FatPath = "bin/" + FatName

	// DefaultDataDir is the data directory when no flag or environment variable is set.
	DefaultDataDir = "/var/lib/edgelet-kube"
	// DataDirEnv is the environment variable for the data directory.
	DataDirEnv = "EDGELET_KUBE_DATA_DIR"
	// DataDirFlag is the command-line flag for the data directory.
	DataDirFlag = "data-dir"

	// ServerCommand extracts the bundle and runs the fat runtime.
	ServerCommand = "server"
)

// Client reports whether name is a multicall tool dispatched to the fat runtime.
func Client(name string) bool {
	switch name {
	case "kubectl", "ctr", "crictl":
		return true
	default:
		return false
	}
}
