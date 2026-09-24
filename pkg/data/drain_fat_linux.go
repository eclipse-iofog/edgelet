//go:build linux && !cgo

package data

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/eclipse-iofog/edgelet/pkg/datadir"
	"github.com/eclipse-iofog/edgelet/pkg/dataverify"
	"github.com/eclipse-iofog/edgelet/pkg/untar"
)

// ReadyCurrentRuntime returns data/current/bin/edgelet when that bundle is ready
// and its directory name equals embedHash. A missing or different current bundle
// reports ok=false without changing the tree.
func ReadyCurrentRuntime(dataDir, embedHash string) (string, bool, error) {
	embedHash = filepath.Clean(embedHash)
	if embedHash == "" || embedHash == "." {
		return "", false, nil
	}
	root, err := datadir.BundleRoot(dataDir)
	if err != nil {
		return "", false, err
	}
	currentLink := filepath.Join(root, "current")
	target, err := os.Readlink(currentLink)
	if err != nil {
		return "", false, nil
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(root, target)
	}
	if filepath.Base(filepath.Clean(target)) != embedHash {
		return "", false, nil
	}
	if !isBundleReady(target) {
		return "", false, nil
	}
	bin := filepath.Join(currentLink, "bin", dataverify.FatRuntimeName)
	if err := dataverify.VerifyFatRuntime(bin); err != nil {
		return "", false, nil
	}
	return bin, true, nil
}

// StageDrainFatELF unpacks only the fat runtime ELF from the embedded bundle into dir.
// It does not extract the rest of the bundle and does not promote data/current.
func StageDrainFatELF(dir string) (string, error) {
	names := AssetNames()
	if len(names) == 0 {
		return "", errors.New("no embedded data bundle found")
	}
	asset := names[len(names)-1]
	content, err := Asset(asset)
	if err != nil {
		return "", fmt.Errorf("load embedded runtime: %w", err)
	}
	dest := filepath.Join(dir, dataverify.FatRuntimeName)
	if err := stageFatELFFromBundle(bytes.NewReader(content), dest); err != nil {
		return "", err
	}
	return dest, nil
}

func stageFatELFFromBundle(r io.Reader, dest string) error {
	if err := untar.ExtractRegularFile(r, "bin/"+dataverify.FatRuntimeName, dest); err != nil {
		_ = os.Remove(dest)
		return err
	}
	if err := dataverify.VerifyFatRuntime(dest); err != nil {
		_ = os.Remove(dest)
		return err
	}
	return nil
}
