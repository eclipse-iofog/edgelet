//go:build linux && !cgo

package data

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/eclipse-iofog/edgelet/internal/constants"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/eclipse-iofog/edgelet/pkg/datadir"
	"github.com/eclipse-iofog/edgelet/pkg/dataverify"
	"github.com/eclipse-iofog/edgelet/pkg/flock"
	"github.com/eclipse-iofog/edgelet/pkg/untar"
)

var (
	dataLogger = logging.NewModuleLogger("Data")

	extractDir   string
	extractDirMu sync.RWMutex
)

// ExtractDir returns the directory containing extracted bundle content.
func ExtractDir() string {
	extractDirMu.RLock()
	defer extractDirMu.RUnlock()
	return extractDir
}

// ExtractBundle unpacks the embedded zstd bundle when needed and returns the content directory.
func ExtractBundle(dataDir string) (string, error) {
	return extract(dataDir)
}

// EnsureExtracted unpacks the embedded zstd bundle (if needed), verifies bin/,
// installs runtime auxiliaries to stable paths, and prepares CNI + pause image.
func EnsureExtracted() error {
	dir, err := extract("")
	if err != nil {
		return err
	}
	if err := installFromBundle(dir); err != nil {
		return err
	}
	if err := pinLegacyIptablesAux(filepath.Join(dir, "bin")); err != nil {
		return fmt.Errorf("pin legacy iptables aux: %w", err)
	}
	if err := prependRuntimePath(dir); err != nil {
		return fmt.Errorf("prepend runtime PATH: %w", err)
	}
	if err := loadKernelModules(dir); err != nil {
		dataLogger.Warnf("Failed to load kernel modules (non-fatal): %v", err)
	}
	pruneStaleBundles("")
	return nil
}

// getAssetAndDir returns the embedded zstd asset name and the authoritative bundle
// directory data/<sha256-prefix>/.
func getAssetAndDir(dataDir string) (string, string, error) {
	names := AssetNames()
	if len(names) == 0 {
		return "", "", errors.New("no embedded data bundle found (run scripts/package-data before building full profile)")
	}
	asset := names[len(names)-1]
	root, err := datadir.BundleRoot(dataDir)
	if err != nil {
		return "", "", err
	}
	hash := strings.SplitN(filepath.Base(asset), ".", 2)[0]
	dir := filepath.Join(root, hash)
	return asset, dir, nil
}

func resolveCurrentBundle(dataDir string) (string, error) {
	root, err := datadir.BundleRoot(dataDir)
	if err != nil {
		return "", err
	}
	current := filepath.Join(root, "current")
	target, err := os.Readlink(current)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	if !isBundleReady(target) {
		return "", fmt.Errorf("current bundle not ready at %s", target)
	}
	return target, nil
}

func installedBundleHash(dataDir string) (string, error) {
	dir, err := resolveCurrentBundle(dataDir)
	if err != nil {
		return "", err
	}
	return filepath.Base(filepath.Clean(dir)), nil
}

// bundleHashMatchesInstalled reports whether the current symlink targets wantHash.
// An empty wantHash means the running binary has no embedded bundle (tests/dev);
// in that case any ready current bundle is accepted.
func bundleHashMatchesInstalled(dataDir, wantHash string) bool {
	if wantHash == "" {
		return true
	}
	have, err := installedBundleHash(dataDir)
	if err != nil {
		return false
	}
	return have == wantHash
}

// promoteCurrentBundle rotates data/current into data/previous and symlinks current at dir
// (authoritative embed-hash tree). Refreshes stable data/cni symlinks.
func promoteCurrentBundle(dataDir, dir string) error {
	root, err := datadir.BundleRoot(dataDir)
	if err != nil {
		return err
	}
	currentSymlink := filepath.Join(root, "current")
	if target, err := os.Readlink(currentSymlink); err == nil {
		if !filepath.IsAbs(target) {
			target = filepath.Join(root, target)
		}
		absDir, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		if filepath.Clean(target) == filepath.Clean(absDir) {
			return setupStableCNIDir(root, dir)
		}
	}

	previousSymlink := filepath.Join(root, "previous")
	if _, err := os.Lstat(currentSymlink); err == nil {
		if err := os.Rename(currentSymlink, previousSymlink); err != nil {
			return fmt.Errorf("rotate current symlink: %w", err)
		}
	}
	if err := os.Symlink(dir, currentSymlink); err != nil {
		return fmt.Errorf("create current symlink: %w", err)
	}
	return setupStableCNIDir(root, dir)
}

func logBundleHashMismatchIfNeeded(dataDir, wantHash string) {
	if wantHash == "" {
		return
	}
	have, err := installedBundleHash(dataDir)
	if err != nil || have == wantHash {
		return
	}
	dataLogger.Infof("Embedded bundle hash mismatch (installed=%s embedded=%s); re-extracting", have, wantHash)
}

// tryUseAuthoritativeBundle uses data/<embed-hash>/ when ready.
// It synchronizes current/previous/cni to that tree and returns it for exec/runtime resolution.
func tryUseAuthoritativeBundle(dataDir, wantHash, dir string) (string, bool, error) {
	if reason := bundleReadyReason(dir); reason != "" {
		return "", false, nil
	}
	logBundleHashMismatchIfNeeded(dataDir, wantHash)
	if err := promoteCurrentBundle(dataDir, dir); err != nil {
		return "", false, err
	}
	setExtractDir(dir)
	return dir, true, nil
}

// extract unpacks the embedded bundle when needed. Selection: the running thin
// binary's embed hash names the authoritative directory; data/current is updated on promote only.
func extract(dataDir string) (string, error) {
	wantHash := EmbeddedBundleHash()
	if wantHash == "" {
		if dir, err := resolveCurrentBundle(dataDir); err == nil {
			setExtractDir(dir)
			return dir, nil
		}
		return "", errors.New("no embedded data bundle found (run scripts/package-data before building full profile)")
	}

	asset, dir, err := getAssetAndDir(dataDir)
	if err != nil {
		return "", err
	}

	if bundleDir, ok, err := tryUseAuthoritativeBundle(dataDir, wantHash, dir); err != nil {
		return "", err
	} else if ok {
		return bundleDir, nil
	} else if reason := bundleReadyReason(dir); reason != "" {
		dataLogger.Infof("Embed bundle not ready at %s: %s", dir, reason)
	}

	logBundleHashMismatchIfNeeded(dataDir, wantHash)

	root, err := datadir.BundleRoot(dataDir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return "", fmt.Errorf("mkdir data root: %w", err)
	}

	lockFile := filepath.Join(root, ".lock")
	dataLogger.Infof("Acquiring lock file %s", lockFile)
	lock, err := flock.Acquire(lockFile)
	if err != nil {
		return "", fmt.Errorf("acquire extract lock: %w", err)
	}
	defer flock.Release(lock)

	if bundleDir, ok, err := tryUseAuthoritativeBundle(dataDir, wantHash, dir); err != nil {
		return "", err
	} else if ok {
		return bundleDir, nil
	}

	dataLogger.Infof("Preparing data dir %s", dir)

	names := AssetNames()
	if len(names) == 0 {
		return "", fmt.Errorf("embedded bundle unavailable and runtime not extracted at %s", dir)
	}

	content, err := Asset(asset)
	if err != nil {
		return "", fmt.Errorf("load embedded asset: %w", err)
	}

	tempDest := dir + "-tmp"
	_ = os.RemoveAll(tempDest)
	defer os.RemoveAll(tempDest)

	if err := untar.Untar(bytes.NewReader(content), tempDest); err != nil {
		return "", fmt.Errorf("untar bundle: %w", err)
	}
	if err := dataverify.Verify(filepath.Join(tempDest, "bin")); err != nil {
		return "", fmt.Errorf("verify extracted bin: %w", err)
	}

	if st, err := os.Stat(dir); err == nil && st.IsDir() {
		if isBundleReady(dir) {
			dataLogger.Debugf("Reusing ready embed bundle at %s", dir)
			if err := promoteCurrentBundle(dataDir, dir); err != nil {
				return "", err
			}
			setExtractDir(dir)
			return dir, nil
		}
		dataLogger.Infof("Replacing incomplete embed bundle at %s", dir)
		if err := os.RemoveAll(dir); err != nil {
			return "", fmt.Errorf("remove incomplete bundle dir: %w", err)
		}
	}

	if err := os.Rename(tempDest, dir); err != nil {
		return "", fmt.Errorf("rename extracted bundle: %w", err)
	}

	if err := promoteCurrentBundle(dataDir, dir); err != nil {
		return "", err
	}

	setExtractDir(dir)
	return dir, nil
}

func bundleReadyReason(dir string) string {
	binDir := filepath.Join(dir, "bin")
	if err := dataverify.VerifyFatRuntime(filepath.Join(binDir, dataverify.FatRuntimeName)); err != nil {
		return fmt.Sprintf("fat runtime: %v", err)
	}
	if _, err := os.Stat(filepath.Join(binDir, "containerd-shim-runc-v2")); err != nil {
		return "missing containerd-shim-runc-v2"
	}
	if err := dataverify.VerifyNetAux(binDir); err != nil {
		return fmt.Sprintf("net aux: %v", err)
	}
	return ""
}

func isBundleReady(dir string) bool {
	return bundleReadyReason(dir) == ""
}

const legacyIptablesTarget = "xtables-legacy-multi"

func pinLegacyIptablesAux(binDir string) error {
	legacyBin := filepath.Join(binDir, "aux", legacyIptablesTarget)
	if _, err := os.Stat(legacyBin); err != nil {
		return fmt.Errorf("stat %s: %w", legacyIptablesTarget, err)
	}
	iptablesLink := filepath.Join(binDir, "aux", "iptables")
	currentTarget, err := os.Readlink(iptablesLink)
	if err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("read aux/iptables symlink: %w", err)
		}
	} else if currentTarget == legacyIptablesTarget {
		return nil
	}
	dataLogger.Infof("Repairing aux/iptables symlink (was %q, want %s)", currentTarget, legacyIptablesTarget)
	if err := os.Remove(iptablesLink); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove aux/iptables: %w", err)
	}
	if err := os.Symlink(legacyIptablesTarget, iptablesLink); err != nil {
		return fmt.Errorf("symlink aux/iptables: %w", err)
	}
	return nil
}

func setExtractDir(dir string) {
	extractDirMu.Lock()
	extractDir = dir
	extractDirMu.Unlock()
}

func setupStableCNIDir(root, dir string) error {
	cniPath := filepath.Join(root, "cni")
	cniBin := filepath.Join(dir, "bin", "cni")
	if err := os.MkdirAll(cniPath, 0755); err != nil {
		return fmt.Errorf("mkdir stable cni dir: %w", err)
	}
	_ = os.Remove(filepath.Join(cniPath, "cni"))
	if err := os.Symlink(cniBin, filepath.Join(cniPath, "cni")); err != nil {
		return fmt.Errorf("symlink cni multicall: %w", err)
	}

	ents, err := os.ReadDir(filepath.Join(dir, "bin"))
	if err != nil {
		return fmt.Errorf("read extracted bin: %w", err)
	}
	for _, ent := range ents {
		info, err := ent.Info()
		if err != nil || info.Mode()&fs.ModeSymlink == 0 {
			continue
		}
		target, err := os.Readlink(filepath.Join(dir, "bin", ent.Name()))
		if err != nil || target != "cni" {
			continue
		}
		src := filepath.Join(cniPath, ent.Name())
		if info, err := os.Lstat(src); err == nil && info.Mode()&fs.ModeSymlink == 0 {
			continue
		}
		_ = os.Remove(src)
		if err := os.Symlink(cniBin, src); err != nil {
			return fmt.Errorf("symlink cni plugin %s: %w", ent.Name(), err)
		}
	}
	return nil
}

func installFromBundle(dir string) error {
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(constants.EdgeletContainerdBinDir, 0755); err != nil {
		return fmt.Errorf("create bin dir: %w", err)
	}

	binaries := []struct {
		name    string
		symlink bool
	}{
		{"containerd-shim-runc-v2", true},
		{"crun", true},
	}
	for _, b := range binaries {
		src := filepath.Join(binDir, b.name)
		dest := filepath.Join(constants.EdgeletContainerdBinDir, b.name)
		if err := installBinary(src, dest); err != nil {
			return fmt.Errorf("install %s: %w", b.name, err)
		}
		if b.symlink {
			symlinkShimToPath(dest, b.name)
		}
	}

	if err := installCNIPlugins(binDir); err != nil {
		return err
	}
	if err := loadCNIConfig(); err != nil {
		return err
	}
	return installPauseImage(dir)
}

func installCNIPlugins(binDir string) error {
	if err := os.MkdirAll(constants.EdgeletCNIPluginsDir, 0755); err != nil {
		return fmt.Errorf("create CNI plugins dir: %w", err)
	}
	plugins := []string{"bridge", "host-local", "portmap", "loopback"}
	for _, name := range plugins {
		src := filepath.Join(binDir, name)
		dest := filepath.Join(constants.EdgeletCNIPluginsDir, name)
		if err := installPluginLink(src, dest); err != nil {
			return fmt.Errorf("install CNI plugin %s: %w", name, err)
		}
	}
	return nil
}

func installPluginLink(src, dest string) error {
	_ = os.Remove(dest)
	return os.Symlink(src, dest)
}

func loadCNIConfig() error {
	for _, dir := range []string{
		constants.EdgeletCNIConfDir,
		constants.DefaultSystemCNIConfDir,
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("create CNI conf dir %s: %w", dir, err)
		}
	}
	if err := writeAndSymlinkCNIConfig(
		generateManagedCNIConfig(),
		constants.EdgeletCNIConfigFile,
		filepath.Join(constants.DefaultSystemCNIConfDir, constants.DefaultCNIConfigName),
	); err != nil {
		return err
	}
	dataLogger.Infof("Single-bridge CNI config written")
	return nil
}

func writeAndSymlinkCNIConfig(cfg map[string]any, targetPath, systemLink string) error {
	cniConfig, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal CNI config: %w", err)
	}
	if err := os.WriteFile(targetPath, cniConfig, 0644); err != nil {
		return fmt.Errorf("write CNI config file: %w", err)
	}
	if systemLink != "" {
		_ = os.Remove(systemLink)
		if err := os.Symlink(targetPath, systemLink); err != nil && !os.IsExist(err) {
			return fmt.Errorf("symlink CNI config %s: %w", systemLink, err)
		}
	}
	return nil
}

func installPauseImage(dir string) error {
	src := filepath.Join(dir, "images", "pause.tar.gz")
	if _, err := os.Stat(src); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("stat pause image: %w", err)
	}
	if err := os.MkdirAll(constants.EdgeletContainerdImagesDir, 0755); err != nil {
		return fmt.Errorf("create images dir: %w", err)
	}
	dest := filepath.Join(constants.EdgeletContainerdImagesDir, "pause.tar.gz")
	return copyFileIfChanged(src, dest, 0644)
}

func installBinary(src, dest string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	return extractBinary(data, dest)
}

func symlinkShimToPath(src, name string) {
	const pathDir = "/usr/local/bin"
	link := filepath.Join(pathDir, name)
	_ = os.Remove(link)
	if err := os.Symlink(src, link); err != nil {
		dataLogger.Warnf("Could not symlink %s to %s (non-fatal): %v", name, pathDir, err)
	} else {
		dataLogger.Debugf("Linked %s -> %s", link, src)
	}
}

func loadKernelModules(bundleDir string) error {
	modules := []string{
		"overlay",
		"br_netfilter",
		"ip_tables",
		"iptable_filter",
		"iptable_nat",
		"nf_conntrack",
	}

	modprobe := "modprobe"
	if bundled := bundleModprobePath(bundleDir); bundled != "" {
		modprobe = bundled
	}

	var firstErr error
	for _, mod := range modules {
		if err := exec.Command(modprobe, mod).Run(); err != nil {
			dataLogger.Debugf("modprobe %s: %v", mod, err)
			if firstErr == nil {
				firstErr = fmt.Errorf("modprobe %s: %w", mod, err)
			}
		}
	}

	if err := os.WriteFile("/proc/sys/net/ipv4/ip_forward", []byte("1"), 0644); err != nil {
		dataLogger.Debugf("Failed to enable IP forwarding: %v", err)
	}

	return firstErr
}

func extractBinary(data []byte, destFile string) error {
	if existing, err := os.ReadFile(destFile); err == nil {
		if bytes.Equal(existing, data) {
			dataLogger.Debugf("Binary already up-to-date, skipping rewrite: %s", destFile)
			return nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read existing %s: %w", destFile, err)
	}

	tmpFile := destFile + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0755); err != nil { // #nosec G306 -- binaries need execute permission
		return fmt.Errorf("write temp %s: %w", tmpFile, err)
	}
	if err := os.Rename(tmpFile, destFile); err != nil {
		_ = os.Remove(tmpFile)
		return fmt.Errorf("replace %s with temp binary: %w", destFile, err)
	}
	return nil
}

func copyFileIfChanged(src, dest string, perm os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	if existing, err := os.ReadFile(dest); err == nil && bytes.Equal(existing, data) {
		return nil
	}
	tmp := dest + ".tmp"
	if err := os.WriteFile(tmp, data, perm); err != nil {
		return fmt.Errorf("write temp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace %s: %w", dest, err)
	}
	return nil
}
