// Package datastore opens kine for the runtime database.
// An empty endpoint uses SQLite. Any other endpoint is handed to kine.
// Secrets encryption writes its config only when the flag is set on a first boot.
// Kube-apiserver is not started.
package datastore
