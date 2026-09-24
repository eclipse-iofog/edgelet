package cmd

import (
	"context"
	"net/url"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/cli/domain/knowledge"
	"github.com/eclipse-iofog/edgelet/internal/cli/output"
	"github.com/eclipse-iofog/edgelet/internal/cli/run"
	"github.com/eclipse-iofog/edgelet/internal/cli/ui"
	"github.com/spf13/cobra"
)

func newKnowledgeCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "knowledge",
		Short:   "Knowledge operations",
		Long:    knowledge.CommandLong(),
		Example: knowledge.CommandExamples(),
	}

	var knowledgePruneMode string
	cmd.AddCommand(
		newKnowledgePullCommand(),
		&cobra.Command{
			Use:   "ls",
			Short: "List knowledge",
			RunE:  runGET("/v1/knowledge"),
		},
		&cobra.Command{
			Use:   "inspect <name>",
			Short: "Inspect a knowledge",
			Args:  cobra.ExactArgs(1),
			RunE:  runKnowledgeInspect,
		},
		func() *cobra.Command {
			pruneCmd := &cobra.Command{
				Use:       "prune [dangling]",
				Short:     "Prune unreferenced knowledge",
				Long:      "Prune dangling knowledge only (on-disk artifacts with no deployed row or workload reference).",
				Args:      cobra.MaximumNArgs(1),
				ValidArgs: []string{"dangling"},
				Example: strings.Join([]string{
					"edgelet knowledge prune",
					"edgelet knowledge prune dangling",
					"edgelet knowledge prune --mode dangling",
				}, "\n"),
				RunE: runKnowledgePrune,
			}
			pruneCmd.Flags().StringVarP(&knowledgePruneMode, "mode", "m", "", "Prune mode (only: dangling)")
			registerKnowledgePruneModeCompletion(pruneCmd)
			return pruneCmd
		}(),
		&cobra.Command{
			Use:   "rm <name>",
			Short: "Remove a knowledge",
			Long:  "Remove a deployed knowledge by metadata.name and delete its on-disk artifacts.",
			Args:  cobra.ExactArgs(1),
			RunE:  runKnowledgeRemove,
		},
	)
	return cmd
}

func newKnowledgePullCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pull <name>",
		Short: "Pull a knowledge",
		Long:  knowledge.PullCommandLong(),
		Example: strings.Join([]string{
			"edgelet knowledge pull product-docs",
			"edgelet knowledge pull wiki-faiss --repo acme/wiki --revision 9f3c111122223333444455556666777788889999 --registry 3 --files data/**/*.jsonl --format jsonl",
		}, "\n"),
		Args: cobra.ExactArgs(1),
		RunE: runKnowledgePull,
	}
	cmd.Flags().String("repo", "", "Repository path without host")
	cmd.Flags().String("revision", "", "Tag, digest, branch, or commit")
	cmd.Flags().IntP("registry", "r", 0, "Registry id")
	cmd.Flags().StringArray("files", nil, "HF file path or glob (repeatable)")
	cmd.Flags().String("format", "", "Format hint (markdown, pdf, jsonl, parquet, arrow, sqlite, faiss, chroma, lance, unknown)")
	registerKnowledgeFormatCompletion(cmd)
	return cmd
}

func runKnowledgePull(cmd *cobra.Command, args []string) error {
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
	result, err := knowledge.Pull(context.Background(), appCtx.Client, uiProgress, knowledge.PullRequest{
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

func runKnowledgeInspect(cmd *cobra.Command, args []string) error {
	if appCtx == nil {
		return run.NewCLIError(run.CodeInternal, "cli context is nil", nil)
	}
	if err := run.RequireDaemon(appCtx.Client); err != nil {
		return err
	}
	path := "/v1/knowledge/" + url.PathEscape(args[0])
	data, err := appCtx.Client.Request("GET", path, nil)
	if err != nil {
		return run.MapAPIError(err)
	}
	return run.WriteRouteData(appCtx, path, data)
}

func runKnowledgePrune(cmd *cobra.Command, args []string) error {
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
		result, err := knowledge.Prune(appCtx.Client, parseArgs)
		if err != nil {
			return err
		}
		return run.WriteValue(appCtx, result.Data)
	}

	spin := appCtx.UI.StartSpinner("Pruning dangling knowledge...")
	result, err := knowledge.Prune(appCtx.Client, parseArgs)
	spin.Stop()
	if err != nil {
		return err
	}
	human := strings.TrimSpace(result.Human)
	if human == "" {
		human = strings.TrimSpace(output.FormatEdgeletAPIHuman("/v1/knowledge:prune", result.Data))
	}
	if human == "" {
		return writeHumanOrRoute(appCtx, "/v1/knowledge:prune", result.Human, result.Data)
	}
	return run.WriteHumanSuccess(appCtx, human)
}

func runKnowledgeRemove(cmd *cobra.Command, args []string) error {
	if appCtx == nil {
		return run.NewCLIError(run.CodeInternal, "cli context is nil", nil)
	}
	if err := run.RequireDaemon(appCtx.Client); err != nil {
		return err
	}
	name := args[0]
	var result *knowledge.RemoveResult
	err := run.WithSpinner(appCtx, "Removing knowledge "+name+"...", func() error {
		var err error
		result, err = knowledge.Remove(appCtx.Client, name)
		return err
	})
	if err != nil {
		return err
	}
	return writeHumanOrRoute(appCtx, "/v1/knowledge/"+name, result.Human, result.Data)
}
