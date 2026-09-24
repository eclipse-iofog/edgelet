package containerd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/eclipse-iofog/edgelet/internal/constants"
)

// wasmShimLogFile is the relative path opened by the wasm shim logger.
// containerd-shimkit calls FifoLogger::with_path("log") with create disabled,
// so the file must already exist in the process working directory.
const wasmShimLogFile = "log"

// UseWasmShimLogDir creates the shim log directory and makes it the working
// directory before the first wasm shim -info.
func UseWasmShimLogDir() error {
	return useWasmShimLogDir(constants.EdgeletWasmShimLogDir)
}

func useWasmShimLogDir(dir string) error {
	if err := prepareWasmShimLog(dir); err != nil {
		return err
	}
	if err := os.Chdir(dir); err != nil {
		return fmt.Errorf("chdir wasm shim log directory: %w", err)
	}
	return nil
}

func prepareWasmShimLog(dir string) error {
	info, err := os.Lstat(dir)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("wasm shim log directory %s is a symlink", dir)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat wasm shim log directory: %w", err)
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("create wasm shim log directory: %w", err)
	}
	info, err = os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("stat wasm shim log directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("wasm shim log path %s is not a directory", dir)
	}
	if err := os.Chmod(dir, 0o750); err != nil { // #nosec G302 -- shim log directory is 0750 so the runtime can traverse it
		return fmt.Errorf("chmod wasm shim log directory: %w", err)
	}
	logPath := filepath.Join(dir, wasmShimLogFile)
	logInfo, err := os.Lstat(logPath)
	if err == nil {
		if logInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("wasm shim log file %s is a symlink", logPath)
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("stat wasm shim log file: %w", err)
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600) // #nosec G304 -- path is the fixed shim log file under the prepared directory
	if err != nil {
		return fmt.Errorf("create wasm shim log file: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close wasm shim log file: %w", err)
	}
	return nil
}
