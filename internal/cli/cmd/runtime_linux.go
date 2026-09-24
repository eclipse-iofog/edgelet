//go:build linux

package cmd

import (
	"errors"
	"fmt"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/cli/domain/runtimecmd"
	"github.com/eclipse-iofog/edgelet/internal/cli/run"
	"github.com/spf13/cobra"
)

const defaultRuntimeDrainTimeoutSec = 90

func newRuntimeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "runtime",
		Short: "Embedded runtime data-plane operations",
		Long: strings.TrimSpace(`Operations for the embedded containerd data plane.

Used by edgelet-containerd.service stop hooks and operators during maintenance.`),
	}

	drainCmd := &cobra.Command{
		Use:   "drain",
		Short: "Drain labeled microservice containers before data-plane stop",
		Long: strings.TrimSpace(`Stops labeled microservice containers while the CRI socket is still up.

Default path uses the control-plane API. --direct quiesces through the embedded
runtime without the control-plane API, then force-kills leftovers and volume
holders and verifies before success.

Default timeout follows shutdownGracePeriodSeconds (90s). Exit 0 when drain completes;
exit 1 on timeout or verify failure.`),
		Args: cobra.NoArgs,
		RunE: runRuntimeDrain,
	}
	drainCmd.Flags().Int("timeout", defaultRuntimeDrainTimeoutSec, "Drain budget in seconds (0 = server default)")
	drainCmd.Flags().Bool("direct", false, "Drain through the embedded runtime without the control-plane API")
	cmd.AddCommand(drainCmd)

	reapCmd := &cobra.Command{
		Use:   "reap-orphans",
		Short: "Reap orphaned edgelet data-plane shims and containerd children",
		Long: strings.TrimSpace(`Last-resort cleanup for edgelet-scoped containerd-shim and --edgelet-containerd-child
processes bound to /run/edgelet/containerd.sock. Used by edgelet-containerd stop hooks and operators during recovery.`),
		Args: cobra.NoArgs,
		RunE: runRuntimeReapOrphans,
	}
	cmd.AddCommand(reapCmd)

	return cmd
}

func runRuntimeDrain(cmd *cobra.Command, args []string) error {
	if appCtx == nil {
		return run.NewCLIError(run.CodeInternal, "cli context is nil", nil)
	}

	timeoutSec, err := cmd.Flags().GetInt("timeout")
	if err != nil {
		return run.NewCLIError(run.CodeInvalidArgument, fmt.Sprintf("invalid --timeout: %v", err), err)
	}
	direct, err := cmd.Flags().GetBool("direct")
	if err != nil {
		return run.NewCLIError(run.CodeInvalidArgument, fmt.Sprintf("invalid --direct: %v", err), err)
	}

	if direct {
		if err := runtimecmd.ExecDirectDrain(timeoutSec); err != nil {
			return run.NewCLIError(run.CodeInternal, err.Error(), err)
		}
		return nil
	}

	if err := run.RequireDaemon(appCtx.Client); err != nil {
		return err
	}

	var result *runtimecmd.DrainResult
	reqErr := run.WithSpinner(appCtx, "Draining microservice containers...", func() error {
		var err error
		result, err = runtimecmd.Drain(appCtx.Client, timeoutSec)
		return err
	})
	if reqErr != nil {
		var apiErr *run.CLIError
		if errors.As(reqErr, &apiErr) && apiErr != nil {
			if strings.Contains(strings.ToLower(apiErr.Message), "timed out") {
				return run.NewCLIError(run.CodeInternal, apiErr.Message, reqErr)
			}
		}
		return reqErr
	}
	return writeHumanOrRoute(appCtx, "/v1/runtime/drain", result.Human, result.Data)
}

func runRuntimeReapOrphans(cmd *cobra.Command, args []string) error {
	if appCtx == nil {
		return run.NewCLIError(run.CodeInternal, "cli context is nil", nil)
	}

	var result *runtimecmd.ReapOrphansResult
	reqErr := run.WithSpinner(appCtx, "Reaping data-plane orphans...", func() error {
		var err error
		result, err = runtimecmd.ReapOrphans()
		return err
	})
	if reqErr != nil {
		return reqErr
	}
	return writeHumanOrRoute(appCtx, "/v1/runtime/reap-orphans", result.Human, result.Data)
}
