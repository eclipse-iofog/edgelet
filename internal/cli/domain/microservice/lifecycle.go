package microservice

import (
	"github.com/eclipse-iofog/edgelet/internal/cli/output"
	"github.com/eclipse-iofog/edgelet/internal/cli/run"
)

// LifecycleResult carries a microservice lifecycle operation outcome.
type LifecycleResult struct {
	Human string
	Data  map[string]any
	Path  string
}

func lifecycle(client run.EdgeletAPIClient, method, path string) (*LifecycleResult, error) {
	if client == nil {
		return nil, run.NewCLIError(run.CodeInternal, "edgeletapi client is nil", nil)
	}
	data, err := client.Request(method, path, nil)
	if err != nil {
		return nil, run.MapAPIError(err)
	}
	return &LifecycleResult{
		Human: output.FormatEdgeletAPIHuman(path, data),
		Data:  data,
		Path:  path,
	}, nil
}

// Start starts a microservice.
func Start(client run.EdgeletAPIClient, id string) (*LifecycleResult, error) {
	return lifecycle(client, "POST", "/v1/ms/"+id+"/start")
}

// Stop stops a microservice.
func Stop(client run.EdgeletAPIClient, id string) (*LifecycleResult, error) {
	return lifecycle(client, "POST", "/v1/ms/"+id+"/stop")
}

// Restart restarts a microservice.
func Restart(client run.EdgeletAPIClient, id string) (*LifecycleResult, error) {
	return lifecycle(client, "POST", "/v1/ms/"+id+"/restart")
}

// Kill kills a microservice.
func Kill(client run.EdgeletAPIClient, id string) (*LifecycleResult, error) {
	return lifecycle(client, "POST", "/v1/ms/"+id+"/kill")
}

// Remove deletes a microservice. Persistent VOLUME data is retained.
func Remove(client run.EdgeletAPIClient, id string, cleanup bool) (*LifecycleResult, error) {
	path := "/v1/ms/" + id
	if cleanup {
		path += "?cleanup=true"
	}
	return lifecycle(client, "DELETE", path)
}
