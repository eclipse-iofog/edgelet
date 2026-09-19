package cmd

import (
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/cli/client"
	"github.com/eclipse-iofog/edgelet/internal/cli/domain/microservice"
	"github.com/eclipse-iofog/edgelet/internal/cli/run"
	"github.com/spf13/cobra"
)

func newMSCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "ms",
		Short:   "Microservice operations",
		Long:    microservice.CommandLong(),
		Example: microservice.CommandExamples(),
	}

	cmd.AddCommand(
		newMSListCommand(),
		&cobra.Command{
			Use:    "ps",
			Hidden: true,
			RunE: func(cmd *cobra.Command, args []string) error {
				return run.NewCLIError(run.CodeInvalidArgument, "unknown ms subcommand \"ps\"; use \"edgelet ms ls\"", nil)
			},
		},
		newMSInspectCommand(),
		newMSLogsCommand(),
		newMSExecCommand(),
		newMSLifecycleCommand("start", "Start a microservice", "", microservice.Start),
		newMSLifecycleCommand("stop", "Stop a microservice", "", microservice.Stop),
		newMSLifecycleCommand("restart", "Restart a microservice", "", microservice.Restart),
		newMSLifecycleCommand("kill", "Kill a microservice", microservice.KillCommandLong(), microservice.Kill),
		newMSRemoveCommand(),
	)

	return cmd
}

func newMSListCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ls",
		Short: "List microservices",
		Args:  cobra.NoArgs,
		RunE:  runMSList,
	}
	cmd.Flags().String("source", "all", "Filter list: managed, local, controlplane, or all")
	registerSourceFlagCompletion(cmd)
	return cmd
}

func newMSInspectCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "inspect <id>",
		Short: "Inspect a microservice",
		Args:  cobra.ExactArgs(1),
		RunE:  runMSInspect,
	}
	cmd.Flags().Bool("summary", false, "Show summary output")
	registerMSInspectCompletions(cmd)
	return cmd
}

func newMSExecCommand() *cobra.Command {
	return &cobra.Command{
		Use:                "exec <id> [-- command...]",
		Short:              "Execute a command in a microservice",
		Args:               cobra.MinimumNArgs(1),
		DisableFlagParsing: true,
		RunE:               runMSExec,
	}
}

func registerMSInspectCompletions(cmd *cobra.Command) {
	_ = cmd.RegisterFlagCompletionFunc("summary", func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
		return []string{"true", "false"}, cobra.ShellCompDirectiveNoFileComp
	})
}

func newMSLogsCommand() *cobra.Command {
	var flags logsFlagValues
	cmd := &cobra.Command{
		Use:   "logs <id>",
		Short: "Stream microservice logs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if appCtx == nil {
				return run.NewCLIError(run.CodeInternal, "cli context is nil", nil)
			}
			if err := run.RequireDaemon(appCtx.Client); err != nil {
				return err
			}
			opts := flags.options()
			id := args[0]
			if opts.Follow {
				return microservice.StreamLogs(appCtx, concreteClient(appCtx), id, opts)
			}
			return microservice.FetchLogs(appCtx, appCtx.Client, id, opts)
		},
	}
	registerLogsFlags(cmd, &flags)
	return cmd
}

func newMSRemoveCommand() *cobra.Command {
	var cleanup bool
	cmd := &cobra.Command{
		Use:   "rm <id>",
		Short: "Remove a microservice",
		Long:  microservice.RemoveCommandLong(),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if appCtx == nil {
				return run.NewCLIError(run.CodeInternal, "cli context is nil", nil)
			}
			if err := run.RequireDaemon(appCtx.Client); err != nil {
				return err
			}
			id := args[0]
			var result *microservice.LifecycleResult
			err := run.WithSpinner(appCtx, msLifecycleSpinnerMessage("rm", id), func() error {
				var err error
				result, err = microservice.Remove(appCtx.Client, id, cleanup)
				return err
			})
			if err != nil {
				return err
			}
			return writeHumanMutationOrRoute(appCtx, result.Path, result.Human, result.Data)
		},
	}
	cmd.Flags().BoolVar(&cleanup, "cleanup", false, "Reserve a cleanup bit for later orphan prune; does not delete persistent VOLUME data now")
	return cmd
}

type msLifecycleFn func(run.EdgeletAPIClient, string) (*microservice.LifecycleResult, error)

func msLifecycleSpinnerMessage(name, id string) string {
	switch name {
	case "start":
		return "Starting microservice " + id + "..."
	case "stop":
		return "Stopping microservice " + id + "..."
	case "restart":
		return "Restarting microservice " + id + "..."
	case "kill":
		return "Killing microservice " + id + "..."
	case "rm":
		return "Removing microservice " + id + "..."
	default:
		return "Updating microservice " + id + "..."
	}
}

func newMSLifecycleCommand(name, short, long string, fn msLifecycleFn) *cobra.Command {
	return &cobra.Command{
		Use:   name + " <id>",
		Short: short,
		Long:  long,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if appCtx == nil {
				return run.NewCLIError(run.CodeInternal, "cli context is nil", nil)
			}
			if err := run.RequireDaemon(appCtx.Client); err != nil {
				return err
			}
			id := args[0]
			var result *microservice.LifecycleResult
			err := run.WithSpinner(appCtx, msLifecycleSpinnerMessage(name, id), func() error {
				var err error
				result, err = fn(appCtx.Client, id)
				return err
			})
			if err != nil {
				return err
			}
			return writeHumanMutationOrRoute(appCtx, result.Path, result.Human, result.Data)
		},
	}
}

func runMSList(cmd *cobra.Command, args []string) error {
	if appCtx == nil {
		return run.NewCLIError(run.CodeInternal, "cli context is nil", nil)
	}
	if err := run.RequireDaemon(appCtx.Client); err != nil {
		return err
	}
	source, err := cmd.Flags().GetString("source")
	if err != nil {
		return run.NewCLIError(run.CodeInternal, err.Error(), err)
	}
	source = strings.ToLower(strings.TrimSpace(source))
	if source == "" {
		source = "all"
	}
	if source != "managed" && source != "local" && source != "controlplane" && source != "all" {
		return run.NewCLIError(run.CodeInvalidArgument, "--source requires managed|local|controlplane|all", nil)
	}
	path := "/v1/ms?source=" + source
	data, err := appCtx.Client.Request("GET", path, nil)
	if err != nil {
		return run.MapAPIError(err)
	}
	return run.WriteRouteData(appCtx, "/v1/ms", data)
}

func runMSInspect(cmd *cobra.Command, args []string) error {
	if appCtx == nil {
		return run.NewCLIError(run.CodeInternal, "cli context is nil", nil)
	}
	if err := run.RequireDaemon(appCtx.Client); err != nil {
		return err
	}
	summary, err := cmd.Flags().GetBool("summary")
	if err != nil {
		return run.NewCLIError(run.CodeInternal, err.Error(), err)
	}
	path := "/v1/ms/" + args[0]
	if summary {
		path += "?summary=true"
	}
	data, err := appCtx.Client.Request("GET", path, nil)
	if err != nil {
		return run.MapAPIError(err)
	}
	return run.WriteRouteData(appCtx, path, data)
}

func runMSExec(cmd *cobra.Command, args []string) error {
	if appCtx == nil {
		return run.NewCLIError(run.CodeInternal, "cli context is nil", nil)
	}
	if err := run.RequireDaemon(appCtx.Client); err != nil {
		return err
	}
	if appCtx.Format.IsStructured() {
		return run.NewCLIError(run.CodeInvalidArgument, "exec output supports human format only", nil)
	}
	id, command, err := microservice.ParseExecArgs(args)
	if err != nil {
		return err
	}
	return microservice.Exec(concreteClient(appCtx), id, command)
}

func concreteClient(ctx *run.CLIContext) *client.Client {
	if ctx == nil {
		return client.New()
	}
	if c, ok := ctx.Client.(*client.Client); ok {
		return c
	}
	return client.New()
}
