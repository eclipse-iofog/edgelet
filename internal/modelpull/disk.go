package modelpull

import (
	"fmt"
	"strings"
)

// DiskGuard rejects a pull when the estimated artifact would leave too little free space.
type DiskGuard interface {
	Ensure(needed int64) error
}

// EnsureDiskSpace fails when needed bytes cannot be stored while keeping
// thresholdPercent of total capacity free under location.
func EnsureDiskSpace(needed, free, total, thresholdPercent int64, location string) error {
	if needed <= 0 {
		return nil
	}
	if thresholdPercent < 0 {
		thresholdPercent = 0
	}
	if thresholdPercent > 100 {
		thresholdPercent = 100
	}
	if free < needed {
		return fmt.Errorf(
			"not enough disk space under %s: need %d bytes, have %d bytes free",
			displayPath(location), needed, free,
		)
	}
	if total <= 0 {
		return nil
	}
	remain := free - needed
	minFree := total * thresholdPercent / 100
	if remain < minFree {
		return fmt.Errorf(
			"not enough disk space under %s: need %d bytes, have %d bytes free; available-disk threshold is %d%% so at least %d bytes must remain free",
			displayPath(location), needed, free, thresholdPercent, minFree,
		)
	}
	return nil
}

func displayPath(location string) string {
	location = strings.TrimSpace(location)
	if location == "" {
		return "disk directory"
	}
	return location
}
