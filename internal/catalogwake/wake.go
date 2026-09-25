// Package catalogwake tells the reconcile loop that a catalog item can change
// whether a workload is allowed to start. The process manager registers the mark.
package catalogwake

// Mark is set by the process manager. It records workloads whose catalog lists name.
var Mark func(name, source string)

// Notify marks workloads bound to a catalog item that became ready or failed.
func Notify(name, source string) {
	mark := Mark
	if mark == nil {
		return
	}
	mark(name, source)
}
