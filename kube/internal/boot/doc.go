// Package boot starts containerd, imports images, and then opens the datastore.
// Image import finishes before Start returns. Kubelet is not started here.
package boot
