//go:build linux && !cgo

// Package main is the thin edgelet-kube binary. It extracts the embedded runtime and execs it.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"github.com/eclipse-iofog/edgelet-kube/internal/bundle"
	"github.com/eclipse-iofog/edgelet-kube/internal/dispatch"
	"github.com/eclipse-iofog/edgelet-kube/internal/paths"
)

func main() {
	if err := run(os.Args); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "edgelet-kube: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cmd, err := dispatch.Parse(args, os.Getenv(bundle.DataDirEnv))
	if err != nil {
		return err
	}
	dir, err := extract(cmd.DataDir)
	if err != nil {
		return err
	}
	if _, err := paths.Place(dir); err != nil {
		return err
	}
	fat := filepath.Join(dir, "bin", bundle.FatName)
	if err := syscall.Exec(fat, cmd.Args, os.Environ()); err != nil { // #nosec G204 -- fat is the extracted runtime
		return fmt.Errorf("exec runtime: %w", err)
	}
	return nil
}
