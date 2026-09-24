//go:build !linux

package processmanager

// ListVolumeTreeHolders is only implemented on Linux (/proc fd scan).
func ListVolumeTreeHolders(string) ([]int, error) {
	return nil, nil
}

// ListVolumePathHolders is only implemented on Linux (/proc fd scan).
func ListVolumePathHolders([]string) ([]int, error) {
	return nil, nil
}
