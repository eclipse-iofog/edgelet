package cmd

import (
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/branding"
	"github.com/eclipse-iofog/edgelet/internal/cli/client"
	"github.com/eclipse-iofog/edgelet/internal/cli/output"
	"github.com/eclipse-iofog/edgelet/internal/cli/run"
	"github.com/eclipse-iofog/edgelet/internal/cli/ui"
	"github.com/spf13/cobra"
)

var (
	rootCmd   *cobra.Command
	appCtx    *run.CLIContext
	newClient = run.DefaultClientFactory
)

const banner = "\n" + branding.EdgeletANSIShadow + "\n" +
	"  Edgelet\n" +
	"  Command Line Interface\n" +
	"  =====================\n"

// ShouldRunCLI reports whether argv should dispatch to the operator CLI instead
// of the daemon supervisor. Daemon-only invocation is "edgelet daemon"
// (used by systemd and service scripts).
func ShouldRunCLI(args []string) bool {
	if len(args) <= 1 {
		return true
	}
	switch args[1] {
	case "daemon", "runtime-bootstrap":
		return false
	}
	return true
}

// Execute runs the Cobra command tree and returns a process exit code.
func Execute() int {
	rootCmd = newRootCommand()
	if err := rootCmd.Execute(); err != nil {
		writeCommandError(err)
		return run.ExitCodeForError(err)
	}
	return run.ExitSuccess
}

func newRootCommand() *cobra.Command {
	var (
		outputFormat string
		quiet        bool
		verbose      bool
		debug        bool
		socket       string
		timeout      string
		noColor      bool
		showVersion  bool
	)

	cmd := &cobra.Command{
		Use:           "edgelet",
		Short:         "Local CLI for the Edgelet daemon",
		Long:          rootLongHelp(),
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			format, err := output.ParseFormat(outputFormat)
			if err != nil {
				return run.NewCLIError(run.CodeInvalidArgument, err.Error(), err)
			}
			opts := ui.Options{Quiet: quiet, NoColor: noColor}
			appCtx = run.NewCLIContext(opts, format)
			appCtx.Out = cmd.OutOrStdout()
			appCtx.ErrOut = cmd.ErrOrStderr()
			appCtx.UI = ui.NewWithWriters(appCtx.Out, appCtx.ErrOut, opts)
			appCtx.Verbose = verbose
			appCtx.Debug = debug
			appCtx.Socket = socket
			appCtx.Timeout = timeout
			appCtx.Version = Version
			appCtx.BuildTime = BuildTime
			appCtx.GitCommit = GitCommit
			appCtx.Client = newClient()
			if apiClient, ok := appCtx.Client.(*client.Client); ok {
				client.ConfigureCLI(apiClient, timeout)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if showVersion {
				return runVersion(appCtx)
			}
			return cmd.Help()
		},
	}

	cmd.PersistentFlags().StringVarP(&outputFormat, "output", "o", "human", "Output format: human, json, yaml")
	cmd.PersistentFlags().BoolVar(&quiet, "quiet", false, "Suppress interactive progress output")
	cmd.PersistentFlags().BoolVar(&verbose, "verbose", false, "Verbose logging")
	cmd.PersistentFlags().BoolVar(&debug, "debug", false, "Debug logging")
	cmd.PersistentFlags().StringVar(&socket, "socket", "", "Edgelet API unix socket path")
	cmd.PersistentFlags().StringVar(&timeout, "timeout", "", "Request timeout")
	cmd.PersistentFlags().BoolVar(&noColor, "no-color", false, "Disable color and interactive UX")
	cmd.Flags().BoolVar(&showVersion, "version", false, "Print CLI and daemon version")
	registerOutputFlagCompletion(cmd)
	cmd.SetHelpTemplate(cliHelpTemplate)
	cmd.SetHelpFunc(printCommandHelp)

	cmd.AddCommand(newVersionCommand())
	cmd.AddCommand(newDeployCommand())
	cmd.AddCommand(newSystemCommand())
	cmd.AddCommand(newMSCommand())
	cmd.AddCommand(newRegistryCommand())
	cmd.AddCommand(newControlPlaneCommand())
	cmd.AddCommand(newRuntimeClassCommand())
	cmd.AddCommand(newRuntimeCommand())
	cmd.AddCommand(newImageCommand())
	cmd.AddCommand(newModelCommand())
	cmd.AddCommand(newAuthCommand())
	cmd.AddCommand(newProvisionCommand())
	cmd.AddCommand(newDeprovisionCommand())
	cmd.AddCommand(newConfigCommand())
	cmd.AddCommand(newInitConfigCommand())
	cmd.AddCommand(newShutdownCommand())
	cmd.AddCommand(newCgroupPreflightCommand())
	cmd.AddCommand(newDeprecatedTopLevelCommand("cert", "use `edgelet config cert` instead of top-level cert"))
	cmd.AddCommand(newDeprecatedTopLevelCommand("switch", "use `edgelet config switch` instead of top-level switch"))
	cmd.AddCommand(newDeprecatedTopLevelCommand("start", "top-level start is removed; start the daemon with `edgelet daemon` or `systemctl start edgelet`"))
	cmd.AddCommand(newDeprecatedTopLevelCommand("stop", "use `edgelet system stop` instead of top-level stop"))
	cmd.AddCommand(newDeprecatedTopLevelCommand("prune", "use `edgelet system prune` instead of top-level prune"))

	cmd.AddCommand(newCompletionCommand(cmd))
	cmd.AddCommand(newDocumentationCommand(cmd))

	return cmd
}

func rootLongHelp() string {
	return strings.TrimSpace(`Local CLI for the Edgelet daemon.

Use "edgelet <command> --help" for command-specific usage.`)
}

func newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print edgelet version",
		RunE: func(_ *cobra.Command, _ []string) error {
			return runLocalVersion(contextOrBootstrap())
		},
	}
}
