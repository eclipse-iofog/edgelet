// Command zstdcmd compresses an image tar, or prints the names stored in one.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/eclipse-iofog/edgelet-kube/scripts/airgap"
)

func main() {
	list := flag.String("list", "", "archive whose image names are printed")
	in := flag.String("in", "", "tar to compress")
	out := flag.String("out", "", "zstd archive to write")
	images := flag.String("images", "", "print the pin list for this architecture")
	flag.Parse()
	if err := run(*list, *in, *out, *images); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "airgap: %v\n", err)
		os.Exit(1)
	}
}

func run(list, in, out, arch string) error {
	if arch != "" {
		if list != "" || in != "" || out != "" {
			return errors.New("images is exclusive")
		}
		for _, ref := range airgap.ImagesFor(arch) {
			fmt.Println(ref)
		}
		return nil
	}
	if list != "" {
		names, err := airgap.ListFile(list)
		if err != nil {
			return err
		}
		for _, name := range names {
			fmt.Println(name)
		}
		return nil
	}
	if in == "" || out == "" {
		return errors.New("in and out are required")
	}
	file, err := os.Open(in)
	if err != nil {
		return err
	}
	defer func() {
		_ = file.Close()
	}()
	return airgap.Compress(out, file)
}
