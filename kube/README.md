# edgelet-kube

`kube/` is the module [`github.com/eclipse-iofog/edgelet-kube`](go.mod). It builds **edgelet-kube**, a single-node Kubernetes distribution for a branch or site server.

This tree is still in development and is not published.

edgelet-kube is not the edgelet agent. It has its own binary, its own data directory, and its own systemd unit. It does not replace `edgelet`, and it does not speak the controller protocol.
