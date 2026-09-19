package provision

import (
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/cli/run"
)

// DeprovisionResult carries deprovision outcome.
type DeprovisionResult struct {
	Human string
	Data  map[string]any
}

// DeprovisionRequest carries deprovision options.
type DeprovisionRequest struct {
	Scope        string
	PurgeVolumes bool
}

// Deprovision removes agent provisioning and optionally preserves local microservices.
func Deprovision(client run.EdgeletAPIClient, req DeprovisionRequest) (*DeprovisionResult, error) {
	if client == nil {
		return nil, run.NewCLIError(run.CodeInternal, "edgeletapi client is nil", nil)
	}
	scope := strings.ToLower(strings.TrimSpace(req.Scope))
	if scope == "" {
		scope = "all"
	}
	if scope != "all" && scope != "local" {
		return nil, run.NewCLIError(run.CodeInvalidArgument, "--scope requires all|local", nil)
	}
	path := "/v1/system/provision"
	query := make([]string, 0, 2)
	if scope != "all" {
		query = append(query, "scope="+scope)
	}
	if req.PurgeVolumes {
		query = append(query, "purgeVolumes=true")
	}
	if len(query) > 0 {
		path += "?" + strings.Join(query, "&")
	}
	data, err := client.Request("DELETE", path, nil)
	if err != nil {
		return nil, run.MapAPIError(err)
	}
	human := "agent deprovisioned successfully; started cleanup of managed and local microservices"
	if scope == "local" {
		human = "agent deprovisioned successfully; preserving local microservices"
	}
	if req.PurgeVolumes {
		human += "; workload persistent volumes purged"
	}
	return &DeprovisionResult{Human: human, Data: data}, nil
}
