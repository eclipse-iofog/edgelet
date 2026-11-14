// Package kubectl runs the Kubernetes client embedded in the fat runtime.
package kubectl

import (
	"os"
	"strings"

	"github.com/eclipse-iofog/edgelet-kube/internal/bundle"
	"github.com/eclipse-iofog/edgelet-kube/internal/config"
	"github.com/eclipse-iofog/edgelet-kube/internal/version"
	"k8s.io/kubectl/pkg/cmd"
)

// adminKubeconfig is the admin file used when kubectl is given no kubeconfig.
var adminKubeconfig = config.DefaultKubeconfig

// Run executes kubectl with args. Runtime flags that belong to this process
// are removed before the client sees them. When neither KUBECONFIG nor
// --kubeconfig is set, the admin file is used if it exists.
func Run(args []string) error {
	version.Pin()
	args = withoutRuntimeFlags(args)
	if err := useAdminKubeconfig(args); err != nil {
		return err
	}
	command := cmd.NewDefaultKubectlCommand()
	command.SetArgs(args)
	return command.Execute()
}

func useAdminKubeconfig(args []string) error {
	if os.Getenv("KUBECONFIG") != "" || hasKubeconfigFlag(args) {
		return nil
	}
	info, err := os.Stat(adminKubeconfig)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if info.IsDir() {
		return nil
	}
	return os.Setenv("KUBECONFIG", adminKubeconfig)
}

func hasKubeconfigFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "--kubeconfig" || strings.HasPrefix(arg, "--kubeconfig=") {
			return true
		}
	}
	return false
}

func withoutRuntimeFlags(args []string) []string {
	flag := "--" + bundle.DataDirFlag
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == flag:
			i++
		case strings.HasPrefix(arg, flag+"="):
		default:
			out = append(out, arg)
		}
	}
	return out
}
