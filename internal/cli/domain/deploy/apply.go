package deploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/cli/client"
	"github.com/eclipse-iofog/edgelet/internal/cli/domain/knowledge"
	"github.com/eclipse-iofog/edgelet/internal/cli/domain/model"
	"github.com/eclipse-iofog/edgelet/internal/cli/output"
	"github.com/eclipse-iofog/edgelet/internal/cli/run"
	"github.com/eclipse-iofog/edgelet/internal/cli/ui"
)

const runtimeClassApplyPollTimeout = 90 * time.Second

var (
	runtimeClassApplyPollInterval = time.Second
	startMultipartApply           = func(api run.EdgeletAPIClient, target Target, manifestPath string, fields map[string]string) (map[string]any, error) {
		return api.RequestMultipartFile("POST", target.applyPath(), "manifest", manifestPath, fields)
	}
	fetchApplyStatus = func(api run.EdgeletAPIClient, target Target, operationID string) (map[string]any, error) {
		return api.Request("GET", target.applyStatusPath(operationID), nil)
	}
)

// Request carries deploy -f options.
type Request struct {
	ManifestPath string
	SourceName   string
	DryRun       bool
}

// Result is the deploy command outcome.
type Result struct {
	Data   map[string]any
	Stages []string
	Human  string
}

// Execute runs deploy validate or apply for a manifest file.
func Execute(ctx context.Context, api run.EdgeletAPIClient, uiProgress *ui.UI, req Request) (*Result, error) {
	if api == nil {
		return nil, run.NewCLIError(run.CodeInternal, "edgeletapi client is nil", nil)
	}
	if strings.TrimSpace(req.ManifestPath) == "" {
		return nil, run.NewCLIError(run.CodeInvalidArgument, "usage: edgelet deploy -f <manifest.yaml>", nil)
	}

	docs, err := splitManifestDocuments(req.ManifestPath)
	if err != nil {
		return nil, run.NewCLIError(run.CodeInvalidArgument, err.Error(), err)
	}
	if mixed, mixedErr := modelThenMicroserviceDocs(docs); mixedErr != nil {
		return nil, run.NewCLIError(run.CodeInvalidArgument, mixedErr.Error(), mixedErr)
	} else if mixed != nil {
		return executeModelThenMicroservice(ctx, api, uiProgress, req, mixed.models, mixed.microservices)
	}

	target, err := DetectTargetFromManifest(req.ManifestPath)
	if err != nil {
		return nil, run.NewCLIError(run.CodeInvalidArgument, err.Error(), err)
	}

	fields := map[string]string{}
	if req.SourceName != "" {
		fields["sourceName"] = req.SourceName
	}
	if req.DryRun {
		fields["dryRun"] = "true"
	}

	switch target {
	case TargetControlPlane:
		result, err := applyAsync(ctx, api, uiProgress, target, req.ManifestPath, fields)
		if err != nil {
			if errors.Is(err, client.ErrPollTimeout) {
				return nil, err
			}
			return nil, err
		}
		if !req.DryRun {
			if err := verifyControlPlaneRunning(api); err != nil {
				return nil, err
			}
		}
		return result, nil
	case TargetMicroservices, TargetRuntimeClasses:
		if req.DryRun {
			data, err := api.RequestMultipartFile("POST", target.validatePath(), "manifest", req.ManifestPath, fields)
			if err != nil {
				return nil, run.MapAPIError(err)
			}
			return &Result{Data: data, Human: FormatValidateHuman(data)}, nil
		}
		fields["async"] = "true"
		return applyAsync(ctx, api, uiProgress, target, req.ManifestPath, fields)
	case TargetRegistries:
		var spin *ui.Spinner
		if uiProgress != nil {
			spin = uiProgress.StartSpinner(applySpinnerMessage(target))
			defer spin.Stop()
		}
		data, err := api.RequestMultipartFile("POST", target.applyPath(), "manifest", req.ManifestPath, fields)
		if err != nil {
			return nil, run.MapAPIError(err)
		}
		return &Result{Data: data, Human: FormatApplyHuman(data)}, nil
	case TargetModels:
		var spin *ui.Spinner
		if uiProgress != nil {
			spin = uiProgress.StartSpinner(applySpinnerMessage(target))
		}
		data, err := api.RequestMultipartFile("POST", target.applyPath(), "manifest", req.ManifestPath, fields)
		if spin != nil {
			spin.Stop()
		}
		if err != nil {
			return nil, run.MapAPIError(err)
		}
		result := &Result{Data: data, Human: FormatApplyHuman(data)}
		if req.DryRun {
			return result, nil
		}
		for _, name := range modelNamesFromApply(data) {
			if _, pullErr := model.Pull(ctx, api, uiProgress, model.PullRequest{Name: name}); pullErr != nil {
				return nil, pullErr
			}
		}
		return result, nil
	case TargetKnowledge:
		var spin *ui.Spinner
		if uiProgress != nil {
			spin = uiProgress.StartSpinner(applySpinnerMessage(target))
		}
		data, err := api.RequestMultipartFile("POST", target.applyPath(), "manifest", req.ManifestPath, fields)
		if spin != nil {
			spin.Stop()
		}
		if err != nil {
			return nil, run.MapAPIError(err)
		}
		result := &Result{Data: data, Human: FormatApplyHuman(data)}
		if req.DryRun {
			return result, nil
		}
		for _, name := range knowledgeNamesFromApply(data) {
			if _, pullErr := knowledge.Pull(ctx, api, uiProgress, knowledge.PullRequest{Name: name}); pullErr != nil {
				return nil, pullErr
			}
		}
		return result, nil
	default:
		return nil, run.NewCLIError(run.CodeInternal, "unsupported deploy target", nil)
	}
}

type modelThenMicroserviceSplit struct {
	models        []manifestDocument
	microservices []manifestDocument
}

func modelThenMicroserviceDocs(docs []manifestDocument) (*modelThenMicroserviceSplit, error) {
	hasModel := false
	hasMS := false
	for _, doc := range docs {
		switch {
		case strings.EqualFold(doc.Kind, "Model"):
			hasModel = true
		case strings.EqualFold(doc.Kind, "Microservice"):
			hasMS = true
		}
	}
	if !hasModel || !hasMS {
		return nil, nil
	}
	models, microservices, err := partitionManifestDocuments(docs)
	if err != nil {
		return nil, err
	}
	if len(models) == 0 || len(microservices) == 0 {
		return nil, nil
	}
	return &modelThenMicroserviceSplit{models: models, microservices: microservices}, nil
}

func executeModelThenMicroservice(ctx context.Context, api run.EdgeletAPIClient, uiProgress *ui.UI, req Request, modelDocs, msDocs []manifestDocument) (*Result, error) {
	modelPath, modelCleanup, err := writeTempManifest(joinManifestDocuments(modelDocs))
	if err != nil {
		return nil, run.NewCLIError(run.CodeInternal, err.Error(), err)
	}
	defer modelCleanup()
	msPath, msCleanup, err := writeTempManifest(joinManifestDocuments(msDocs))
	if err != nil {
		return nil, run.NewCLIError(run.CodeInternal, err.Error(), err)
	}
	defer msCleanup()

	modelResult, err := Execute(ctx, api, uiProgress, Request{
		ManifestPath: modelPath,
		SourceName:   req.SourceName,
		DryRun:       req.DryRun,
	})
	if err != nil {
		return nil, err
	}
	msReq := Request{
		ManifestPath: msPath,
		SourceName:   req.SourceName,
		DryRun:       req.DryRun,
	}
	msResult, err := Execute(ctx, api, uiProgress, msReq)
	if err != nil {
		return nil, err
	}
	human := strings.TrimSpace(strings.TrimSpace(modelResult.Human) + "\n" + strings.TrimSpace(msResult.Human))
	stages := append(append([]string{}, modelResult.Stages...), msResult.Stages...)
	return &Result{
		Data: map[string]any{
			"models":        modelResult.Data,
			"microservices": msResult.Data,
		},
		Stages: stages,
		Human:  human,
	}, nil
}

func writeTempManifest(content string) (string, func(), error) {
	f, err := os.CreateTemp("", "edgelet-deploy-*.yaml")
	if err != nil {
		return "", func() {}, err
	}
	path := f.Name()
	cleanup := func() { _ = os.Remove(path) }
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return path, cleanup, nil
}

func verifyControlPlaneRunning(api run.EdgeletAPIClient) error {
	data, err := api.Request("GET", "/v1/system/controlplane", nil)
	if err != nil {
		return run.MapAPIError(err)
	}
	state := strings.ToLower(strings.TrimSpace(output.MapValueAsString(data, "runtimeState")))
	if state == "running" {
		return nil
	}
	return run.NewCLIError(
		run.CodeInternal,
		fmt.Sprintf("control plane apply finished but runtimeState=%q (expected running); run edgelet controlplane get", state),
		nil,
	)
}

func controlPlanePollTimeoutError(operationID string) error {
	msg := "control plane deploy polling timed out; work may still be running on the daemon"
	msg += "; check edgelet controlplane get"
	if strings.TrimSpace(operationID) != "" {
		msg += fmt.Sprintf(" or GET /v1/deploy/controlplane:apply/%s", strings.TrimSpace(operationID))
	}
	msg += "; do not run another deploy immediately"
	return run.NewCLIError(run.CodeInternal, msg, client.ErrPollTimeout)
}

func applySpinnerMessage(target Target) string {
	switch target {
	case TargetControlPlane:
		return "Applying control plane manifest..."
	case TargetRegistries:
		return "Applying registry manifest..."
	case TargetModels:
		return "Applying model manifest..."
	case TargetKnowledge:
		return "Applying knowledge manifest..."
	default:
		return "Applying manifest..."
	}
}

func applyAsync(ctx context.Context, api run.EdgeletAPIClient, uiProgress *ui.UI, target Target, manifestPath string, fields map[string]string) (*Result, error) {
	startResult, err := startMultipartApply(api, target, manifestPath, fields)
	if err != nil {
		return nil, run.MapAPIError(err)
	}

	startStatus := normalizeStatus(output.MapValueAsString(startResult, "status"))
	if startStatus == "succeeded" {
		return finalizeApply(startResult, nil)
	}
	if startStatus == "failed" {
		code, message := ApplyError(startResult)
		return nil, run.NewCLIError(code, message, nil)
	}

	operationID := client.OperationIDFromStart(startResult)
	if operationID == "" || operationID == "<unknown>" {
		return finalizeApply(startResult, nil)
	}

	pollCfg := client.PollConfig{Interval: 500 * time.Millisecond}
	switch target {
	case TargetRuntimeClasses:
		pollCfg.Interval = runtimeClassApplyPollInterval
		pollCfg.Timeout = runtimeClassApplyPollTimeout
	case TargetControlPlane:
		pollCfg.Timeout = client.PollTimeoutFor("controlplane")
	default:
		pollCfg.Timeout = client.PollTimeoutFor("microservices")
	}

	stageFormatter := ui.FormatDeployStageLine
	baseMessage := "Applying microservice manifest..."
	switch target {
	case TargetRuntimeClasses:
		stageFormatter = ui.FormatRuntimeClassStageLine
		baseMessage = "Applying runtimeclass manifest..."
	case TargetControlPlane:
		stageFormatter = ui.FormatControlPlaneStageLine
		baseMessage = "Applying control plane manifest..."
	}

	progress := client.PollProgress{
		UI:             uiProgress,
		StageFormatter: stageFormatter,
	}
	if uiProgress != nil {
		spin := uiProgress.StartSpinner(baseMessage)
		defer spin.Stop()
		progress.Spinner = spin
	}

	final, stages, err := client.PollAsyncOperation(ctx, pollCfg, func() (map[string]any, error) {
		return fetchApplyStatus(api, target, operationID)
	}, progress)
	if err != nil {
		if errors.Is(err, client.ErrPollTimeout) && target == TargetRuntimeClasses {
			human := FormatRuntimeClassInProgress(operationID, "running", lastStageFrom(stages))
			data := map[string]any{
				"operationId": operationID,
				"status":      "running",
			}
			return &Result{Data: WithStages(data, stages), Stages: stages, Human: human}, nil
		}
		if errors.Is(err, client.ErrPollTimeout) && target == TargetControlPlane {
			return nil, controlPlanePollTimeoutError(operationID)
		}
		return nil, run.MapAPIError(err)
	}

	status := normalizeStatus(output.MapValueAsString(final, "status"))
	if status == "failed" {
		code, message := ApplyError(final)
		return nil, run.NewCLIError(code, message, nil)
	}
	return finalizeApply(final, stages)
}

func lastStageFrom(stages []string) string {
	if len(stages) == 0 {
		return ""
	}
	return stages[len(stages)-1]
}

func modelNamesFromApply(data map[string]any) []string {
	if data == nil {
		return nil
	}
	var names []string
	seen := map[string]bool{}
	appendName := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || name == "<unknown>" || seen[name] {
			return
		}
		seen[name] = true
		names = append(names, name)
	}
	switch items := data["models"].(type) {
	case []any:
		for _, item := range items {
			if m, ok := item.(map[string]any); ok {
				appendName(output.MapValueAsString(m, "name"))
			}
		}
	case []map[string]any:
		for _, m := range items {
			appendName(output.MapValueAsString(m, "name"))
		}
	}
	if len(names) == 0 {
		appendName(output.MapValueAsString(data, "name"))
	}
	return names
}

func knowledgeNamesFromApply(data map[string]any) []string {
	if data == nil {
		return nil
	}
	var names []string
	seen := map[string]bool{}
	appendName := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || name == "<unknown>" || seen[name] {
			return
		}
		seen[name] = true
		names = append(names, name)
	}
	switch items := data["knowledge"].(type) {
	case []any:
		for _, item := range items {
			if m, ok := item.(map[string]any); ok {
				appendName(output.MapValueAsString(m, "name"))
			}
		}
	case []map[string]any:
		for _, m := range items {
			appendName(output.MapValueAsString(m, "name"))
		}
	}
	if len(names) == 0 {
		appendName(output.MapValueAsString(data, "name"))
	}
	return names
}

func finalizeApply(data map[string]any, stages []string) (*Result, error) {
	human := FormatApplyHuman(data)
	if strings.HasPrefix(human, "Error[") {
		code, message := ApplyError(data)
		return nil, run.NewCLIError(code, message, nil)
	}
	return &Result{
		Data:   WithStages(data, stages),
		Stages: stages,
		Human:  human,
	}, nil
}
