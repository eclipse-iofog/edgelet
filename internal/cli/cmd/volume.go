package cmd

import (
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/cli/domain/volume"
	"github.com/eclipse-iofog/edgelet/internal/cli/run"
	"github.com/spf13/cobra"
)

func newVolumeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "volume",
		Short:   "Persistent volume operations",
		Long:    volume.CommandLong(),
		Example: volume.CommandExamples(),
	}
	cmd.AddCommand(
		&cobra.Command{
			Use:   "ls",
			Short: "List persistent volumes",
			RunE:  runGET("/v1/volumes"),
		},
		newVolumeRemoveCommand(),
		newVolumePruneCommand(),
	)
	return cmd
}

func newVolumeRemoveCommand() *cobra.Command {
	var sharedName string
	var force bool
	cmd := &cobra.Command{
		Use:   "rm [<uuid> [<name>]]",
		Short: "Remove a persistent volume",
		Long:  volume.RemoveCommandLong(),
		Args:  cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVolumeRemove(cmd, args, sharedName, force)
		},
	}
	cmd.Flags().StringVar(&sharedName, "shared", "", "Destroy a shared volume by name")
	cmd.Flags().BoolVar(&force, "force", false, "Bypass the desired-state gate when unmounted")
	return cmd
}

func newVolumePruneCommand() *cobra.Command {
	var orphans, yes, force bool
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Prune unreferenced persistent volumes",
		Long:  volume.PruneCommandLong(),
		Args:  cobra.NoArgs,
		Example: strings.Join([]string{
			"edgelet volume prune",
			"edgelet volume prune --orphans --yes",
			"edgelet volume prune --orphans --yes --force",
		}, "\n"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runVolumePrune(orphans, yes, force)
		},
	}
	cmd.Flags().BoolVar(&orphans, "orphans", false, "Select unreferenced persistent volumes (default for this command)")
	cmd.Flags().BoolVar(&yes, "yes", false, "Destroy listed orphans; without this flag prune is a dry-run")
	cmd.Flags().BoolVar(&force, "force", false, "Bypass the 24h grace window when unmounted")
	return cmd
}

func runVolumeRemove(_ *cobra.Command, args []string, sharedName string, force bool) error {
	if appCtx == nil {
		return run.NewCLIError(run.CodeInternal, "cli context is nil", nil)
	}
	if err := run.RequireDaemon(appCtx.Client); err != nil {
		return err
	}
	sharedName = strings.TrimSpace(sharedName)
	if sharedName != "" {
		if len(args) > 0 {
			return run.NewCLIError(run.CodeInvalidArgument, "volume rm --shared does not take a uuid argument", nil)
		}
		var result *volume.RemoveResult
		err := run.WithSpinner(appCtx, "Removing shared volume "+sharedName+"...", func() error {
			var err error
			result, err = volume.RemoveShared(appCtx.Client, sharedName, force)
			return err
		})
		if err != nil {
			return err
		}
		return writeHumanOrRoute(appCtx, result.Path, result.Human, result.Data)
	}
	if len(args) == 0 {
		return run.NewCLIError(run.CodeInvalidArgument, "volume rm requires a uuid or --shared <name>", nil)
	}
	uuid := strings.TrimSpace(args[0])
	name := ""
	if len(args) > 1 {
		name = strings.TrimSpace(args[1])
	}
	var result *volume.RemoveResult
	err := run.WithSpinner(appCtx, "Removing volume "+uuid+"...", func() error {
		var err error
		result, err = volume.RemovePrivate(appCtx.Client, uuid, name, force)
		return err
	})
	if err != nil {
		return err
	}
	return writeHumanOrRoute(appCtx, result.Path, result.Human, result.Data)
}

func runVolumePrune(_, yes, force bool) error {
	if appCtx == nil {
		return run.NewCLIError(run.CodeInternal, "cli context is nil", nil)
	}
	if err := run.RequireDaemon(appCtx.Client); err != nil {
		return err
	}
	if appCtx.Format.IsStructured() {
		result, err := volume.Prune(appCtx.Client, volume.PruneOptions{Orphans: true, Yes: yes, Force: force})
		if err != nil {
			return err
		}
		return run.WriteValue(appCtx, result.Data)
	}
	message := "Listing persistent volume orphans..."
	if yes {
		message = "Pruning persistent volume orphans..."
	}
	spin := appCtx.UI.StartSpinner(message)
	result, err := volume.Prune(appCtx.Client, volume.PruneOptions{Orphans: true, Yes: yes, Force: force})
	spin.Stop()
	if err != nil {
		return err
	}
	return writeHumanOrRoute(appCtx, "/v1/volumes:prune", result.Human, result.Data)
}
