// Package main writes the zstd bundle the thin binary embeds.
// The bundle contains the fat runtime and the aux binaries. It contains no OCI image.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/eclipse-iofog/edgelet-kube/internal/bundle"
)

func main() {
	stage := flag.String("stage", "", "directory whose bin tree is packed")
	out := flag.String("out", "", "directory for the zstd bundle")
	flag.Parse()
	if err := pack(*stage, *out); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "pack: %v\n", err)
		os.Exit(1)
	}
}

func pack(stage, out string) error {
	if stage == "" || out == "" {
		return errors.New("stage and out are required")
	}
	packed, err := bundle.Pack(stage)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(packed)
	name := hex.EncodeToString(sum[:]) + ".tar.zst"
	if err := os.MkdirAll(out, 0o755); err != nil {
		return fmt.Errorf("mkdir bundle dir: %w", err)
	}
	old, err := filepath.Glob(filepath.Join(out, "*.tar.zst"))
	if err != nil {
		return fmt.Errorf("list bundles: %w", err)
	}
	for _, path := range old {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove %s: %w", path, err)
		}
	}
	dest := filepath.Join(out, name)
	if err := os.WriteFile(dest, packed, 0o644); err != nil {
		return fmt.Errorf("write bundle: %w", err)
	}
	fmt.Println(dest)
	return nil
}
