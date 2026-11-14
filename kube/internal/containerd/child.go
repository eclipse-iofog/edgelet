package containerd

// MaybeRunChild serves containerd when args carry ChildArg.
// The process returns when containerd exits. It does not start the API server.
func MaybeRunChild(args []string) (bool, error) {
	if len(args) < 2 || args[1] != ChildArg {
		return false, nil
	}
	if err := serveChild(args); err != nil {
		return true, err
	}
	return true, nil
}
