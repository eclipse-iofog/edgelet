package knowledge

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/cli/client"
	"github.com/eclipse-iofog/edgelet/internal/cli/output"
	"github.com/eclipse-iofog/edgelet/internal/cli/run"
	"github.com/eclipse-iofog/edgelet/internal/cli/ui"
)

// PullRequest carries knowledge pull options.
type PullRequest struct {
	Name       string
	Repo       string
	Revision   string
	RegistryID int
	Files      []string
	Format     string
}

func (req PullRequest) hasSpec() bool {
	return strings.TrimSpace(req.Repo) != "" ||
		strings.TrimSpace(req.Revision) != "" ||
		req.RegistryID > 0 ||
		len(req.Files) > 0 ||
		strings.TrimSpace(req.Format) != ""
}

// PullResult is the knowledge pull command outcome.
type PullResult struct {
	Data  map[string]any
	Human string
}

// Pull starts an async knowledge pull and waits until it finishes.
func Pull(ctx context.Context, api run.EdgeletAPIClient, uiProgress *ui.UI, req PullRequest) (*PullResult, error) {
	if api == nil {
		return nil, run.NewCLIError(run.CodeInternal, "edgeletapi client is nil", nil)
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, run.NewCLIError(run.CodeInvalidArgument, "knowledge name is required", nil)
	}
	if req.hasSpec() && (strings.TrimSpace(req.Repo) == "" || req.RegistryID <= 0) {
		return nil, run.NewCLIError(run.CodeInvalidArgument, "repo and --registry are required when specifying a knowledge", nil)
	}

	body := map[string]any{"name": name}
	if repo := strings.TrimSpace(req.Repo); repo != "" {
		body["repo"] = repo
	}
	if revision := strings.TrimSpace(req.Revision); revision != "" {
		body["revision"] = revision
	}
	if req.RegistryID > 0 {
		body["registryId"] = req.RegistryID
	}
	if len(req.Files) > 0 {
		body["files"] = req.Files
	}
	if format := strings.TrimSpace(req.Format); format != "" {
		body["format"] = format
	}

	startResult, err := api.Request("POST", "/v1/knowledge:pull", body)
	if err != nil {
		return nil, run.MapAPIError(err)
	}
	operationID := client.OperationIDFromStart(startResult)
	if operationID == "" || operationID == "<unknown>" {
		return nil, run.NewCLIError(run.CodeInternal, "missing knowledge pull operationId in response", nil)
	}

	progress := client.PollProgress{
		UI:           uiProgress,
		PercentLabel: "pulling knowledge",
	}
	if uiProgress != nil {
		spin := uiProgress.StartSpinner("Pulling knowledge...")
		defer spin.Stop()
		progress.Spinner = spin
	}

	final, _, err := client.PollAsyncOperation(ctx, client.PollConfig{
		Interval: 500 * time.Millisecond,
		Timeout:  client.PollTimeoutFor("knowledge-pull"),
	}, func() (map[string]any, error) {
		return api.Request("GET", "/v1/knowledge:pull/"+operationID, nil)
	}, progress)
	if err != nil {
		return nil, run.MapAPIError(err)
	}

	status := strings.ToLower(strings.TrimSpace(output.MapValueAsString(final, "status")))
	if status == "failed" {
		errMsg := strings.TrimSpace(output.MapValueAsString(final, "error"))
		if errMsg == "" || errMsg == "<unknown>" {
			errMsg = "knowledge pull failed"
		}
		return nil, run.NewCLIError(run.CodeInternal, errMsg, nil)
	}

	return &PullResult{Data: final, Human: formatPullHuman(final)}, nil
}

func formatPullHuman(result map[string]any) string {
	return fmt.Sprintf(
		"knowledge pulled successfully: %s",
		output.ValueOrDefault(output.MapValueAsString(result, "name"), "<unknown>"),
	)
}
