//go:build !linux || !cgo

package images

import "errors"

func openStore(string) (Store, error) {
	return nil, errors.New("image import requires linux")
}
