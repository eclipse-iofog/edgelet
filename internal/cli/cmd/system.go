package cmd

import (
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/cli/domain/prune"
	"github.com/eclipse-iofog/edgelet/internal/cli/domain/system"
	"github.com/eclipse-iofog/edgelet/internal/cli/output"
	"github.com/eclipse-iofog/edgelet/internal/cli/run"
	"github.com/spf13/cobra"
)

func newSystemCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "system",
		Short: "System operations",
		Long:  system.CommandLong(),
	}

	var systemPruneMode string

	cmd.AddCommand(
		&cobra.Command{
			Use:   "status",
			Short: "Show agent runtime status",
			RunE:  runGET("/v1/system/status"),
		},
		&cobra.Command{
			Use:   "info",
			Short: "Show agent configuration info",
			RunE:  runGET("/v1/system/info"),
		},
		&cobra.Command{
			Use:   "version",
			Short: "Show combined CLI and daemon version",
			RunE: func(cmd *cobra.Command, args []string) error {
				return runVersion(appCtx)
			},
		},
		&cobra.Command{
			Use:   "reload",
			Short: "Reload daemon configuration",
			RunE:  runSystemReload,
		},
		&cobra.Command{
			Use:   "stop",
			Short: "Gracefully stop the daemon",
			Long:  system.StopCommandLong(),
			RunE:  runSystemStop,
		},
		func() *cobra.Command {
			pruneCmd := &cobra.Command{
				Use:       "prune [dangling|containers|volumes|all]",
				Short:     "Prune unused resources",
				Long:      "Prune unused resources. Default mode is dangling images. volumes does not destroy persistent VOLUME data; use edgelet volume prune.",
				Args:      cobra.MaximumNArgs(1),
				ValidArgs: []string{"dangling", "containers", "volumes", "all"},
				Example: strings.Join([]string{
					"edgelet system prune",
					"edgelet system prune all",
					"edgelet system prune --mode all",
				}, "\n"),
				RunE: runSystemPrune,
			}
			pruneCmd.Flags().StringVarP(&systemPruneMode, "mode", "m", "", "Prune mode: dangling|containers|volumes|all")
			registerSystemPruneModeCompletion(pruneCmd)
			return pruneCmd
		}(),
		newSystemLogsCommand(),
	)

	return cmd
}

func newSystemLogsCommand() *cobra.Command {
	var flags logsFlagValues
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Stream daemon logs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if appCtx == nil {
				return run.NewCLIError(run.CodeInternal, "cli context is nil", nil)
			}
			if err := run.RequireDaemon(appCtx.Client); err != nil {
				return err
			}
			opts := flags.options()
			if opts.Follow {
				return system.StreamLogs(appCtx, concreteClient(appCtx), opts)
			}
			return system.FetchLogs(appCtx, appCtx.Client, opts)
		},
	}
	registerLogsFlags(cmd, &flags)
	return cmd
}

func runSystemStop(cmd *cobra.Command, args []string) error {
	if appCtx == nil {
		return run.NewCLIError(run.CodeInternal, "cli context is nil", nil)
	}
	if err := run.RequireDaemon(appCtx.Client); err != nil {
		return err
	}
	var result *system.StopResult
	err := run.WithSpinner(appCtx, "Stopping daemon...", func() error {
		var err error
		result, err = system.Stop(appCtx.Client)
		return err
	})
	if err != nil {
		return err
	}
	return writeHumanOrRoute(appCtx, "/v1/system/stop", result.Human, result.Data)
}

func runSystemReload(cmd *cobra.Command, args []string) error {
	if appCtx == nil {
		return run.NewCLIError(run.CodeInternal, "cli context is nil", nil)
	}
	if err := run.RequireDaemon(appCtx.Client); err != nil {
		return err
	}
	path := "/v1/system/reload"
	var data map[string]any
	err := run.WithSpinner(appCtx, "Reloading configuration...", func() error {
		var reqErr error
		data, reqErr = appCtx.Client.Request("POST", path, nil)
		return run.MapAPIError(reqErr)
	})
	if err != nil {
		return err
	}
	return writeHumanOrRoute(appCtx, path, output.FormatEdgeletAPIHuman(path, data), data)
}

func runSystemPrune(cmd *cobra.Command, args []string) error {
	if appCtx == nil {
		return run.NewCLIError(run.CodeInternal, "cli context is nil", nil)
	}
	if err := run.RequireDaemon(appCtx.Client); err != nil {
		return err
	}
	modeArg, _ := cmd.Flags().GetString("mode")
	parseArgs := make([]string, 0, len(args)+2)
	if strings.TrimSpace(modeArg) != "" {
		parseArgs = append(parseArgs, "--mode", modeArg)
	}
	parseArgs = append(parseArgs, args...)
	mode, err := prune.ParseMode(parseArgs, "Usage: edgelet system prune [dangling|containers|volumes|all]")
	if err != nil {
		return err
	}
	if mode == "volumes" {
		return run.NewCLIError(run.CodeInvalidArgument, "system prune volumes does not destroy persistent VOLUME data; use edgelet volume prune", nil)
	}
	path := "/v1/system/prune"
	if mode != "" {
		path += "?mode=" + mode
	}
	if appCtx.Format.IsStructured() {
		data, reqErr := appCtx.Client.Request("POST", path, nil)
		if reqErr != nil {
			return run.MapAPIError(reqErr)
		}
		return run.WriteRouteData(appCtx, path, data)
	}
	spin := appCtx.UI.StartSpinner("Pruning resources...")
	data, err := appCtx.Client.Request("POST", path, nil)
	spin.Stop()
	if err != nil {
		return run.MapAPIError(err)
	}
	human := output.FormatEdgeletAPIHuman(path, data)
	if strings.TrimSpace(human) == "" {
		return run.WriteRouteData(appCtx, path, data)
	}
	return run.WriteHumanSuccess(appCtx, human)
}

func runGET(path string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if appCtx == nil {
			return run.NewCLIError(run.CodeInternal, "cli context is nil", nil)
		}
		if err := run.RequireDaemon(appCtx.Client); err != nil {
			return err
		}
		data, err := appCtx.Client.Request("GET", path, nil)
		if err != nil {
			return run.MapAPIError(err)
		}
		return run.WriteRouteData(appCtx, path, data)
	}
}

func runPOST(path string, payload any) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if appCtx == nil {
			return run.NewCLIError(run.CodeInternal, "cli context is nil", nil)
		}
		if err := run.RequireDaemon(appCtx.Client); err != nil {
			return err
		}
		data, err := appCtx.Client.Request("POST", path, payload)
		if err != nil {
			return run.MapAPIError(err)
		}
		return run.WriteRouteData(appCtx, path, data)
	}
}
