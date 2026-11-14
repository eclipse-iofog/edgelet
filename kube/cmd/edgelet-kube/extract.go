//go:build linux && !cgo

package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/eclipse-iofog/edgelet-kube/internal/bundle"
	"github.com/eclipse-iofog/edgelet/pkg/datadir"
	"github.com/eclipse-iofog/edgelet/pkg/dataverify"
	"github.com/eclipse-iofog/edgelet/pkg/flock"
	"github.com/eclipse-iofog/edgelet/pkg/untar"
)

// extract unpacks this binary's embedded runtime with the shared untar and lock helpers.
func extract(dataDir string) (string, error) {
	asset, err := embeddedBundle()
	if err != nil {
		return "", err
	}
	root, err := datadir.BundleRoot(dataDir)
	if err != nil {
		return "", err
	}
	hash := strings.TrimSuffix(filepath.Base(asset), ".tar.zst")
	if !validHash(hash) {
		return "", fmt.Errorf("embedded bundle %s has no hash name", asset)
	}
	dir := filepath.Join(root, hash)
	if bundleReady(dir) {
		return dir, nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil { // #nosec G301 -- data root must be traversable
		return "", fmt.Errorf("mkdir data root: %w", err)
	}
	lock, err := flock.Acquire(filepath.Join(root, ".lock"))
	if err != nil {
		return "", fmt.Errorf("acquire extract lock: %w", err)
	}
	defer func() { _ = flock.Release(lock) }()
	if bundleReady(dir) {
		return dir, nil
	}
	return unpack(asset, dir)
}

func unpack(asset, dir string) (out string, err error) {
	content, err := assetBytes(asset)
	if err != nil {
		return "", fmt.Errorf("load embedded runtime: %w", err)
	}
	tempDest := dir + "-tmp"
	if err = os.RemoveAll(tempDest); err != nil {
		return "", fmt.Errorf("remove extract temp dir: %w", err)
	}
	defer func() {
		if err != nil {
			_ = os.RemoveAll(tempDest)
		}
	}()
	if err = untar.Untar(bytes.NewReader(content), tempDest); err != nil {
		return "", fmt.Errorf("untar runtime bundle: %w", err)
	}
	fat := filepath.Join(tempDest, "bin", bundle.FatName)
	ok, readErr := dataverify.IsELF(fat)
	if readErr != nil {
		return "", fmt.Errorf("read runtime: %w", readErr)
	}
	if !ok {
		return "", fmt.Errorf("runtime is not an ELF binary")
	}
	if err = os.Chmod(fat, 0o755); err != nil { // #nosec G302 -- runtime binary must be executable
		return "", fmt.Errorf("chmod runtime: %w", err)
	}
	if err = os.RemoveAll(dir); err != nil {
		return "", fmt.Errorf("remove incomplete bundle: %w", err)
	}
	if err = os.Rename(tempDest, dir); err != nil {
		return "", fmt.Errorf("rename extracted bundle: %w", err)
	}
	return dir, nil
}

func bundleReady(dir string) bool {
	ok, err := dataverify.IsELF(filepath.Join(dir, "bin", bundle.FatName))
	return err == nil && ok
}

func validHash(hash string) bool {
	if len(hash) != 64 {
		return false
	}
	for _, c := range hash {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
