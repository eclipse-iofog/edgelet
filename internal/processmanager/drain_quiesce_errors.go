package processmanager

import "errors"

var (
	errVolumeHoldersRemain = errors.New("volume tree still has open file holders")
	errLabeledTasksRemain  = errors.New("labeled workload tasks still present")
	errLabeledTasksUnknown = errors.New("cannot verify labeled workload tasks")
)
