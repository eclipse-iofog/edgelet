package model

import (
	"net/url"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/cli/output"
	"github.com/eclipse-iofog/edgelet/internal/cli/run"
)

// RemoveResult carries model remove outcome.
type RemoveResult struct {
	Human string
	Data  map[string]any
}

// Remove deletes one deployed model by metadata.name.
func Remove(client run.EdgeletAPIClient, name string) (*RemoveResult, error) {
	if client == nil {
		return nil, run.NewCLIError(run.CodeInternal, "edgeletapi client is nil", nil)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, run.NewCLIError(run.CodeInvalidArgument, "model name is required", nil)
	}
	path := "/v1/models/" + url.PathEscape(name)
	data, err := client.Request("DELETE", path, nil)
	if err != nil {
		return nil, run.MapAPIError(err)
	}
	return &RemoveResult{
		Human: output.FormatEdgeletAPIHuman(path, data),
		Data:  data,
	}, nil
}
