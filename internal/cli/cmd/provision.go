package cmd

import (
	"github.com/eclipse-iofog/edgelet/internal/cli/domain/provision"
	"github.com/eclipse-iofog/edgelet/internal/cli/run"
	"github.com/spf13/cobra"
)

func newProvisionCommand() *cobra.Command {
	return &cobra.Command{
		Use:     "provision <provisioning-key>",
		Short:   "Provision the agent",
		Long:    provision.ProvisionLong(),
		Example: provision.ProvisionExamples(),
		Args:    cobra.ExactArgs(1),
		RunE:    runProvision,
	}
}

func newDeprovisionCommand() *cobra.Command {
	var scope string
	var keepLocal bool
	var purgeVolumes bool

	cmd := &cobra.Command{
		Use:     "deprovision",
		Short:   "Deprovision the agent",
		Long:    provision.DeprovisionLong(),
		Example: provision.DeprovisionExamples(),
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if appCtx == nil {
				return run.NewCLIError(run.CodeInternal, "cli context is nil", nil)
			}
			if err := run.RequireDaemon(appCtx.Client); err != nil {
				return err
			}
			scopeVal := scope
			if keepLocal {
				scopeVal = "local"
			}
			var result *provision.DeprovisionResult
			err := run.WithSpinner(appCtx, "Deprovisioning agent...", func() error {
				var err error
				result, err = provision.Deprovision(appCtx.Client, provision.DeprovisionRequest{
					Scope:        scopeVal,
					PurgeVolumes: purgeVolumes,
				})
				return err
			})
			if err != nil {
				return err
			}
			return writeHumanMutationOrData(appCtx, result.Human, result.Data)
		},
	}
	cmd.Flags().StringVar(&scope, "scope", "all", "Deprovision scope: all or local")
	cmd.Flags().BoolVar(&keepLocal, "keep-local", false, "Preserve local microservices (sets scope to local)")
	cmd.Flags().BoolVar(&purgeVolumes, "purge-volumes", false, "Destroy workload persistent VOLUME data after deprovision")
	registerDeprovisionScopeCompletion(cmd)
	return cmd
}

func runProvision(cmd *cobra.Command, args []string) error {
	if appCtx == nil {
		return run.NewCLIError(run.CodeInternal, "cli context is nil", nil)
	}
	if err := run.RequireDaemon(appCtx.Client); err != nil {
		return err
	}
	var result *provision.Result
	err := run.WithSpinner(appCtx, "Provisioning agent...", func() error {
		var err error
		result, err = provision.Provision(appCtx.Client, args[0])
		return err
	})
	if err != nil {
		return err
	}
	return writeHumanMutationOrData(appCtx, result.Human, result.Data)
}
