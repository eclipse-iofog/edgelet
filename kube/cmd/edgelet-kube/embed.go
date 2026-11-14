//go:build linux && !cgo

package main

import (
	"embed"
	"fmt"
	"strings"

	"github.com/eclipse-iofog/edgelet/pkg/data/bindata"
)

//go:embed embed/*
var embedFS embed.FS

var embedded = bindata.Bindata{FS: &embedFS, Prefix: "embed"}

func embeddedBundle() (string, error) {
	var match string
	for _, name := range embedded.AssetNames() {
		if strings.HasSuffix(name, ".tar.zst") {
			match = name
		}
	}
	if match == "" {
		return "", fmt.Errorf("no embedded runtime bundle")
	}
	return match, nil
}

func assetBytes(name string) ([]byte, error) {
	return embedded.Asset(name)
}
