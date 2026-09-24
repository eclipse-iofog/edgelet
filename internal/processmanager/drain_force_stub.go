//go:build !unix

package processmanager

import "errors"

func killProcess(int) error {
	return errors.New("forced leftover process stop is only supported on unix")
}
