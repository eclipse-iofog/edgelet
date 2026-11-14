//go:build !linux || !cgo

package containerd

import "errors"

func serveChild(_ []string) error {
	return errors.New("containerd child requires linux")
}
