//go:build linux && cgo

package containerd

import (
	// Register the in-process containerd plugins: CRI, the overlay snapshotter,
	// and the gRPC server. There is no standalone containerd executable.
	_ "github.com/containerd/containerd/v2/core/runtime/v2"                  // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/content/local/plugin"     // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/cri"                      // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/cri/images"               // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/cri/runtime"              // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/diff/walking/plugin"      // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/events"                   // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/gc"                       // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/imageverifier"            // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/leases"                   // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/metadata"                 // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/mount"                    // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/nri"                      // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/restart"                  // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/sandbox"                  // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/server/debug"             // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/server/grpc"              // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/server/metrics"           // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/server/ttrpc"             // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/services/containers"      // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/services/content"         // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/services/diff"            // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/services/events"          // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/services/healthcheck"     // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/services/images"          // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/services/introspection"   // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/services/leases"          // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/services/mounts"          // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/services/namespaces"      // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/services/opt"             // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/services/sandbox"         // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/services/snapshots"       // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/services/streaming"       // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/services/tasks"           // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/services/transfer"        // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/services/version"         // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/services/warning"         // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/snapshots/overlay/plugin" // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/streaming"                // containerd plugin
	_ "github.com/containerd/containerd/v2/plugins/transfer"                 // containerd plugin
)
