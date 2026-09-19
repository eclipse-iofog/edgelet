package volume

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/cli/output"
	"github.com/eclipse-iofog/edgelet/internal/cli/run"
)

// RemoveResult carries volume remove outcome.
type RemoveResult struct {
	Human string
	Data  map[string]any
	Path  string
}

// PruneResult carries volume prune outcome.
type PruneResult struct {
	Human string
	Data  map[string]any
}

// RemovePrivate deletes a private persistent VOLUME by UUID.
func RemovePrivate(client run.EdgeletAPIClient, uuid, name string, force bool) (*RemoveResult, error) {
	if client == nil {
		return nil, run.NewCLIError(run.CodeInternal, "edgeletapi client is nil", nil)
	}
	uuid = strings.TrimSpace(uuid)
	if uuid == "" {
		return nil, run.NewCLIError(run.CodeInvalidArgument, "uuid is required", nil)
	}
	path := "/v1/volumes/" + url.PathEscape(uuid)
	query := url.Values{}
	if strings.TrimSpace(name) != "" {
		query.Set("name", strings.TrimSpace(name))
	}
	if force {
		query.Set("force", "true")
	}
	if encoded := query.Encode(); encoded != "" {
		path += "?" + encoded
	}
	data, err := client.Request("DELETE", path, nil)
	if err != nil {
		return nil, run.MapAPIError(err)
	}
	return &RemoveResult{
		Human: output.FormatEdgeletAPIHuman(path, data),
		Data:  data,
		Path:  path,
	}, nil
}

// RemoveShared deletes a shared persistent VOLUME by name.
func RemoveShared(client run.EdgeletAPIClient, name string, force bool) (*RemoveResult, error) {
	if client == nil {
		return nil, run.NewCLIError(run.CodeInternal, "edgeletapi client is nil", nil)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, run.NewCLIError(run.CodeInvalidArgument, "shared volume name is required", nil)
	}
	path := "/v1/volumes/shared/" + url.PathEscape(name)
	if force {
		path += "?force=true"
	}
	data, err := client.Request("DELETE", path, nil)
	if err != nil {
		return nil, run.MapAPIError(err)
	}
	return &RemoveResult{
		Human: output.FormatEdgeletAPIHuman(path, data),
		Data:  data,
		Path:  path,
	}, nil
}

// PruneOptions controls volume orphan prune.
type PruneOptions struct {
	Orphans bool
	Yes     bool
	Force   bool
}

// Prune lists or destroys unreferenced persistent VOLUME claims.
func Prune(client run.EdgeletAPIClient, opts PruneOptions) (*PruneResult, error) {
	if client == nil {
		return nil, run.NewCLIError(run.CodeInternal, "edgeletapi client is nil", nil)
	}
	query := url.Values{}
	query.Set("orphans", "true")
	if opts.Yes {
		query.Set("yes", "true")
		query.Set("dryRun", "false")
	} else {
		query.Set("dryRun", "true")
	}
	if opts.Force {
		query.Set("force", "true")
	}
	path := "/v1/volumes:prune?" + query.Encode()
	data, err := client.Request("POST", path, nil)
	if err != nil {
		return nil, run.MapAPIError(err)
	}
	human := output.FormatEdgeletAPIHuman("/v1/volumes:prune", data)
	if strings.TrimSpace(human) == "" {
		human = formatPruneFallback(data, opts.Yes)
	}
	return &PruneResult{Human: human, Data: data}, nil
}

func formatPruneFallback(data map[string]any, confirm bool) string {
	if confirm {
		return fmt.Sprintf("pruned persistent volumes: deleted=%s", output.MapValueAsString(data, "deleted"))
	}
	return "volume prune dry-run; pass --yes to destroy"
}
