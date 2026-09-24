package cmd

import (
	"context"
	"net/url"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/cli/domain/model"
	"github.com/eclipse-iofog/edgelet/internal/cli/output"
	"github.com/eclipse-iofog/edgelet/internal/cli/run"
	"github.com/eclipse-iofog/edgelet/internal/cli/ui"
	"github.com/spf13/cobra"
)

func newModelCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "model",
		Short:   "Model operations",
		Long:    model.CommandLong(),
		Example: model.CommandExamples(),
	}

	var modelPruneMode string
	cmd.AddCommand(
		newModelPullCommand(),
		&cobra.Command{
			Use:   "ls",
			Short: "List models",
			RunE:  runGET("/v1/models"),
		},
		&cobra.Command{
			Use:   "inspect <name>",
			Short: "Inspect a model",
			Args:  cobra.ExactArgs(1),
			RunE:  runModelInspect,
		},
		func() *cobra.Command {
			pruneCmd := &cobra.Command{
				Use:       "prune [dangling]",
				Short:     "Prune unreferenced models",
				Long:      "Prune dangling models only (on-disk artifacts with no deployed row or workload reference).",
				Args:      cobra.MaximumNArgs(1),
				ValidArgs: []string{"dangling"},
				Example: strings.Join([]string{
					"edgelet model prune",
					"edgelet model prune dangling",
					"edgelet model prune --mode dangling",
				}, "\n"),
				RunE: runModelPrune,
			}
			pruneCmd.Flags().StringVarP(&modelPruneMode, "mode", "m", "", "Prune mode (only: dangling)")
			registerModelPruneModeCompletion(pruneCmd)
			return pruneCmd
		}(),
		&cobra.Command{
			Use:   "rm <name>",
			Short: "Remove a model",
			Long:  "Remove a deployed model by metadata.name and delete its on-disk artifacts.",
			Args:  cobra.ExactArgs(1),
			RunE:  runModelRemove,
		},
	)
	return cmd
}

func newModelPullCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pull <name>",
		Short: "Pull a model",
		Long:  model.PullCommandLong(),
		Example: strings.Join([]string{
			"edgelet model pull llama-2-7b-q2k",
			"edgelet model pull tiny-gpt2 --repo hf-internal-testing/tiny-random-gpt2 --revision 71034c5d8bde858ff824298bdedc65515b97d2b9 --registry 3 --files config.json --format unknown",
		}, "\n"),
		Args: cobra.ExactArgs(1),
		RunE: runModelPull,
	}
	cmd.Flags().String("repo", "", "Repository path without host")
	cmd.Flags().String("revision", "", "Tag, digest, branch, or commit")
	cmd.Flags().IntP("registry", "r", 0, "Registry id")
	cmd.Flags().StringArray("files", nil, "HF file path or glob (repeatable)")
	cmd.Flags().String("format", "", "Format hint (gguf, safetensors, onnx, pytorch, tensorrt, unknown)")
	registerModelFormatCompletion(cmd)
	return cmd
}

func runModelPull(cmd *cobra.Command, args []string) error {
	if appCtx == nil {
		return run.NewCLIError(run.CodeInternal, "cli context is nil", nil)
	}
	if err := run.RequireDaemon(appCtx.Client); err != nil {
		return err
	}
	repo, _ := cmd.Flags().GetString("repo")
	revision, _ := cmd.Flags().GetString("revision")
	registryID, _ := cmd.Flags().GetInt("registry")
	files, _ := cmd.Flags().GetStringArray("files")
	format, _ := cmd.Flags().GetString("format")
	var uiProgress *ui.UI
	if !appCtx.Format.IsStructured() {
		uiProgress = appCtx.UI
	}
	result, err := model.Pull(context.Background(), appCtx.Client, uiProgress, model.PullRequest{
		Name:       strings.TrimSpace(args[0]),
		Repo:       strings.TrimSpace(repo),
		Revision:   strings.TrimSpace(revision),
		RegistryID: registryID,
		Files:      files,
		Format:     strings.TrimSpace(format),
	})
	if err != nil {
		return err
	}
	if appCtx.Format.IsStructured() {
		return run.WriteValue(appCtx, result.Data)
	}
	return run.WriteHumanSuccess(appCtx, result.Human)
}

func runModelInspect(cmd *cobra.Command, args []string) error {
	if appCtx == nil {
		return run.NewCLIError(run.CodeInternal, "cli context is nil", nil)
	}
	if err := run.RequireDaemon(appCtx.Client); err != nil {
		return err
	}
	path := "/v1/models/" + url.PathEscape(args[0])
	data, err := appCtx.Client.Request("GET", path, nil)
	if err != nil {
		return run.MapAPIError(err)
	}
	return run.WriteRouteData(appCtx, path, data)
}

func runModelPrune(cmd *cobra.Command, args []string) error {
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

	if appCtx.Format.IsStructured() {
		result, err := model.Prune(appCtx.Client, parseArgs)
		if err != nil {
			return err
		}
		return run.WriteValue(appCtx, result.Data)
	}

	spin := appCtx.UI.StartSpinner("Pruning dangling models...")
	result, err := model.Prune(appCtx.Client, parseArgs)
	spin.Stop()
	if err != nil {
		return err
	}
	human := strings.TrimSpace(result.Human)
	if human == "" {
		human = strings.TrimSpace(output.FormatEdgeletAPIHuman("/v1/models:prune", result.Data))
	}
	if human == "" {
		return writeHumanOrRoute(appCtx, "/v1/models:prune", result.Human, result.Data)
	}
	return run.WriteHumanSuccess(appCtx, human)
}

func runModelRemove(cmd *cobra.Command, args []string) error {
	if appCtx == nil {
		return run.NewCLIError(run.CodeInternal, "cli context is nil", nil)
	}
	if err := run.RequireDaemon(appCtx.Client); err != nil {
		return err
	}
	name := args[0]
	var result *model.RemoveResult
	err := run.WithSpinner(appCtx, "Removing model "+name+"...", func() error {
		var err error
		result, err = model.Remove(appCtx.Client, name)
		return err
	})
	if err != nil {
		return err
	}
	return writeHumanOrRoute(appCtx, "/v1/models/"+name, result.Human, result.Data)
}
