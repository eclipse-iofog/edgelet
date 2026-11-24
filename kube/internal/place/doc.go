// Package place assigns pods to this node.
// The admission plugin sets the node name when the pod can run here.
// A loop binds the pod later, through the Pod Binding subresource, when a
// temporary reason has cleared.
package place
