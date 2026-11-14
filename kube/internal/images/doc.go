// Package images imports archives into the containerd k8s.io namespace and pins them.
// An empty images directory pulls the pinned list. A tar added later is imported
// without a process restart.
package images
