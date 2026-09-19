package handlers

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eclipse-iofog/edgelet/internal/auth"
	"github.com/eclipse-iofog/edgelet/internal/config"
	"github.com/eclipse-iofog/edgelet/internal/containerexec"
	"github.com/eclipse-iofog/edgelet/internal/fieldagent"
	"github.com/eclipse-iofog/edgelet/internal/models"
	"github.com/eclipse-iofog/edgelet/internal/network"
	"github.com/eclipse-iofog/edgelet/internal/processmanager"
	"github.com/eclipse-iofog/edgelet/internal/runtimeapi"
	"github.com/eclipse-iofog/edgelet/internal/runtimestate"
	"github.com/eclipse-iofog/edgelet/internal/store"
	"github.com/eclipse-iofog/edgelet/internal/utils"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
	"github.com/eclipse-iofog/edgelet/pkg/engine"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	apiHandlerModuleName = "Edgelet API Handler"
)

// EdgeletAPIHandler handles Edgelet API v1 endpoint groups.
type EdgeletAPIHandler struct {
	facade                *runtimeapi.Facade
	execSessions          map[string]*localExecSession
	execMu                sync.RWMutex
	pullOps               map[string]*imagePullOperation
	pullMu                sync.RWMutex
	deployOps             map[string]*deployApplyOperation
	deployMu              sync.RWMutex
	controlPlaneApplyOps  map[string]*controlPlaneApplyOperation
	controlPlaneApplyMu   sync.RWMutex
	loadOps               map[string]*imageLoadOperation
	loadMu                sync.RWMutex
	runtimeClassApplyOps  map[string]*runtimeClassApplyOperation
	runtimeClassApplyMu   sync.RWMutex
	runtimeClassDeleteOps map[string]*runtimeClassDeleteOperation
	runtimeClassDeleteMu  sync.RWMutex
	resolveMicroservice   func(selector string) (string, error)
	streamMicroservicLog  func(microserviceUUID string, cfg *engine.TailConfig, handler engine.LogTailHandler) error
}

// NewEdgeletAPIHandler creates a new Edgelet API handler.
func NewEdgeletAPIHandler() *EdgeletAPIHandler {
	facade := runtimeapi.NewFacade()
	return &EdgeletAPIHandler{
		facade:                facade,
		execSessions:          make(map[string]*localExecSession),
		pullOps:               make(map[string]*imagePullOperation),
		deployOps:             make(map[string]*deployApplyOperation),
		controlPlaneApplyOps:  make(map[string]*controlPlaneApplyOperation),
		loadOps:               make(map[string]*imageLoadOperation),
		runtimeClassApplyOps:  make(map[string]*runtimeClassApplyOperation),
		runtimeClassDeleteOps: make(map[string]*runtimeClassDeleteOperation),
		resolveMicroservice: func(selector string) (string, error) {
			return facade.ResolveMicroserviceID(selector)
		},
		streamMicroservicLog: func(microserviceUUID string, cfg *engine.TailConfig, handler engine.LogTailHandler) error {
			return processmanager.GetInstance().StreamMicroserviceLogs(microserviceUUID, cfg, handler)
		},
	}
}

var edgeletAPIUpgrader = websocket.Upgrader{
	CheckOrigin: func(_ *http.Request) bool {
		return true
	},
}

type localExecSession struct {
	SessionID        string
	MicroserviceUUID string
	callback         *localExecCallback
	createdAt        time.Time
}

type localExecCallback struct {
	stdinR  *io.PipeReader
	stdinW  *io.PipeWriter
	stdoutR *io.PipeReader
	stdoutW *io.PipeWriter
	stderrR *io.PipeReader
	stderrW *io.PipeWriter
	done    chan struct{}
	mu      sync.Mutex
	closed  bool
	err     error
}

type imagePullOperation struct {
	OperationID   string
	Status        string
	Progress      int
	Image         string
	ResolvedImage string
	RegistryID    *int
	Platform      string
	Engine        string
	Error         string
	StartedAt     time.Time
	EndedAt       *time.Time
}

type imageLoadOperation struct {
	OperationID string
	Status      string
	Path        string
	Loaded      []map[string]any
	Count       int
	Engine      string
	Error       string
	StartedAt   time.Time
	EndedAt     *time.Time
}

type deployApplyOperation struct {
	OperationID  string
	Status       string
	Stage        string
	Kind         string
	Name         string
	Image        string
	DeploymentID string
	ErrorCode    string
	ErrorMessage string
	StartedAt    time.Time
	EndedAt      *time.Time
}

type runtimeClassApplyOperation struct {
	OperationID  string
	Status       string
	Stage        string
	Kind         string
	Name         string
	DryRun       bool
	StartedAt    time.Time
	EndedAt      *time.Time
	RuntimeClass *models.LocalRuntimeClass
	ErrorCode    string
	ErrorMessage string
	ErrorDetails map[string]any
}

type runtimeClassDeleteOperation struct {
	OperationID  string
	Status       string
	Stage        string
	Kind         string
	Name         string
	StartedAt    time.Time
	EndedAt      *time.Time
	RuntimeClass *models.LocalRuntimeClass
	ErrorCode    string
	ErrorMessage string
	ErrorDetails map[string]any
}

var runtimeClassApplySyncWaitTimeout = 8 * time.Second
var runtimeClassApplyRunner = func(facade *runtimeapi.Facade, manifest string, dryRun bool) (*models.LocalRuntimeClass, error) {
	return facade.ApplyLocalRuntimeClassManifest(manifest, dryRun)
}
var runtimeClassDeleteSyncWaitTimeout = 8 * time.Second
var runtimeClassDeletePreflightRunner = func(facade *runtimeapi.Facade, name string) (*models.LocalRuntimeClass, error) {
	return facade.ValidateRuntimeClassDelete(name)
}
var runtimeClassDeleteRunner = func(facade *runtimeapi.Facade, name string) error {
	return facade.DeleteRuntimeClass(name)
}

func newLocalExecCallback() *localExecCallback {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	return &localExecCallback{
		stdinR:  stdinR,
		stdinW:  stdinW,
		stdoutR: stdoutR,
		stdoutW: stdoutW,
		stderrR: stderrR,
		stderrW: stderrW,
		done:    make(chan struct{}),
	}
}

func (c *localExecCallback) GetStdinReader() io.Reader  { return c.stdinR }
func (c *localExecCallback) GetStdoutWriter() io.Writer { return c.stdoutW }
func (c *localExecCallback) GetStderrWriter() io.Writer { return c.stderrW }
func (c *localExecCallback) IsRunning() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.closed
}
func (c *localExecCallback) OnComplete() { c.close(nil) }
func (c *localExecCallback) OnError(err error) {
	c.close(err)
}
func (c *localExecCallback) close(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.closed = true
	c.err = err
	_ = c.stdinW.Close()
	_ = c.stdoutW.Close()
	_ = c.stderrW.Close()
	close(c.done)
}

func (h *EdgeletAPIHandler) HandleSystemProvision(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var req struct {
			ProvisioningKey string `json:"provisioningKey"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "invalid JSON body", nil)
			return
		}
		if strings.TrimSpace(req.ProvisioningKey) == "" {
			writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "provisioningKey is required", nil)
			return
		}
		if err := h.facade.Provision(req.ProvisioningKey); err != nil {
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
			return
		}
		cfg := config.GetInstance()
		writeSuccess(w, http.StatusOK, map[string]any{
			"status":    "ok",
			"agentUuid": strings.TrimSpace(cfg.IOFogUUID),
		})
	case http.MethodDelete:
		scope := strings.TrimSpace(r.URL.Query().Get("scope"))
		purgeVolumes, err := parseBooleanFormValue(r.URL.Query().Get("purgeVolumes"), "purgeVolumes")
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
			return
		}
		if r.Body != nil {
			var body struct {
				Scope        *string `json:"scope"`
				PurgeVolumes *bool   `json:"purgeVolumes"`
			}
			dec := json.NewDecoder(r.Body)
			if decodeErr := dec.Decode(&body); decodeErr == nil {
				if body.Scope != nil && scope == "" {
					scope = strings.TrimSpace(*body.Scope)
				}
				if body.PurgeVolumes != nil && strings.TrimSpace(r.URL.Query().Get("purgeVolumes")) == "" {
					purgeVolumes = *body.PurgeVolumes
				}
			} else if !errors.Is(decodeErr, io.EOF) && r.ContentLength > 0 {
				writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "invalid JSON body", nil)
				return
			}
		}
		if err := h.facade.Deprovision(scope, purgeVolumes); err != nil {
			if strings.Contains(strings.ToLower(err.Error()), "invalid deprovision scope") {
				writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
				return
			}
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
			return
		}
		writeSuccess(w, http.StatusOK, map[string]any{"status": "ok"})
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
	}
}

func (h *EdgeletAPIHandler) HandleSystemReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	if err := config.GetInstance().TriggerReloadCallback(); err != nil {
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
		return
	}
	writeSuccess(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (h *EdgeletAPIHandler) HandleRuntimeDrain(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	if !runtimestate.GetState().EngineReady() && !processmanager.RuntimeEngineAvailable() {
		writeAPIError(w, http.StatusServiceUnavailable, ErrCodeInternal, "runtime engine is not ready", nil)
		return
	}

	timeout := time.Duration(config.GetInstance().ShutdownDrainTimeout()) * time.Second
	var body struct {
		TimeoutSeconds *int `json:"timeoutSeconds"`
	}
	if r.Body != nil {
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "invalid JSON body", nil)
			return
		}
	}
	if body.TimeoutSeconds != nil && *body.TimeoutSeconds >= 0 {
		timeout = time.Duration(*body.TimeoutSeconds) * time.Second
	}

	if err := processmanager.DrainRuntimeForDataPlaneStop(timeout); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "timed out") {
			writeAPIError(w, http.StatusGatewayTimeout, ErrCodeInternal, err.Error(), nil)
			return
		}
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
		return
	}
	writeSuccess(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (h *EdgeletAPIHandler) HandleSystemPrune(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	result, err := h.facade.Prune(r.URL.Query().Get("mode"))
	if err != nil {
		if errors.Is(err, runtimeapi.ErrSystemPruneVolumes) {
			writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
			return
		}
		if strings.Contains(strings.ToLower(err.Error()), "invalid prune mode") {
			writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
			return
		}
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
		return
	}
	writeSuccess(w, http.StatusOK, result)
}

func (h *EdgeletAPIHandler) HandleSystemLogs(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/system/logs:stream" {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
			return
		}
		h.handleSystemLogsStreamWS(w, r)
		return
	}
	if r.URL.Path != "/v1/system/logs" {
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "not found", nil)
		return
	}
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}

	tailLines := 100
	tailRaw := strings.TrimSpace(r.URL.Query().Get("tailLines"))
	if tailRaw == "" {
		tailRaw = strings.TrimSpace(r.URL.Query().Get("tail"))
	}
	if tailRaw != "" {
		parsed, err := strconv.Atoi(tailRaw)
		if err != nil || parsed <= 0 {
			writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "tailLines must be a positive integer", nil)
			return
		}
		tailLines = parsed
	}

	cfg := config.GetInstance()
	iofogUUID := strings.TrimSpace(cfg.IOFogUUID)
	if iofogUUID == "" {
		iofogUUID = "local-agent"
	}
	readerHandler := newSystemLogsCollectHandler()
	reader := utils.NewLocalLogReader(
		fmt.Sprintf("edgeletapi-system-logs-%d", time.Now().UnixNano()),
		iofogUUID,
		cfg.LogDirectory,
		&utils.TailConfig{
			Follow: false,
			Lines:  tailLines,
			Since:  strings.TrimSpace(r.URL.Query().Get("since")),
			Until:  strings.TrimSpace(r.URL.Query().Get("until")),
		},
		readerHandler,
	)
	reader.Start()
	select {
	case <-readerHandler.done:
	case <-time.After(5 * time.Second):
	}
	reader.Stop()
	if readerHandler.err != nil {
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, readerHandler.err.Error(), nil)
		return
	}
	writeSuccess(w, http.StatusOK, map[string]any{
		"iofogUuid": iofogUUID,
		"entries":   readerHandler.entries,
	})
}

func (h *EdgeletAPIHandler) HandleImages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	if body, _ := io.ReadAll(r.Body); len(bytes.TrimSpace(body)) > 0 {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "request body is not allowed", nil)
		return
	}
	items, err := h.facade.ListImages()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
		return
	}
	writeSuccess(w, http.StatusOK, map[string]any{
		"items": items,
		"count": len(items),
	})
}

func (h *EdgeletAPIHandler) HandleImagePull(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	var req struct {
		Image      string `json:"image"`
		RegistryID *int   `json:"registryId,omitempty"`
		Platform   string `json:"platform,omitempty"`
		Async      bool   `json:"async,omitempty"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "invalid JSON body", nil)
		return
	}
	if strings.TrimSpace(req.Image) == "" {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "image is required", nil)
		return
	}
	logging.LogInfo(apiHandlerModuleName, fmt.Sprintf("local image pull requested image=%s async=%v", strings.TrimSpace(req.Image), req.Async))
	if req.Async {
		op := &imagePullOperation{
			OperationID: uuid.NewString(),
			Status:      "running",
			Progress:    0,
			Image:       strings.TrimSpace(req.Image),
			RegistryID:  req.RegistryID,
			Platform:    strings.TrimSpace(req.Platform),
			Engine:      strings.ToLower(strings.TrimSpace(config.GetInstance().ContainerEngine)),
			StartedAt:   time.Now().UTC(),
		}
		h.pullMu.Lock()
		h.pullOps[op.OperationID] = op
		h.pullMu.Unlock()
		logging.LogInfo(apiHandlerModuleName, fmt.Sprintf("local image pull operation started operationId=%s image=%s engine=%s", op.OperationID, op.Image, op.Engine))

		accepted := snapshotImagePullOperation(op)
		go func(operationID string) {
			resolvedImage, err := h.facade.PullImageWithProgress(req.Image, req.RegistryID, req.Platform, func(progress float32) {
				h.pullMu.Lock()
				defer h.pullMu.Unlock()
				current, ok := h.pullOps[operationID]
				if !ok {
					return
				}
				if progress < 0 {
					progress = 0
				}
				if progress > 100 {
					progress = 100
				}
				if int(progress) > current.Progress {
					current.Progress = int(progress)
				}
			})
			h.pullMu.Lock()
			defer h.pullMu.Unlock()
			current, ok := h.pullOps[operationID]
			if !ok {
				return
			}
			now := time.Now().UTC()
			current.EndedAt = &now
			current.ResolvedImage = strings.TrimSpace(resolvedImage)
			if err != nil {
				current.Status = "failed"
				current.Error = err.Error()
				logging.LogWarn(apiHandlerModuleName, fmt.Sprintf("local image pull failed operationId=%s image=%s err=%v", operationID, strings.TrimSpace(req.Image), err))
				return
			}
			current.Progress = 100
			current.Status = "succeeded"
			logging.LogInfo(apiHandlerModuleName, fmt.Sprintf("local image pull succeeded operationId=%s image=%s resolvedImage=%s", operationID, strings.TrimSpace(req.Image), current.ResolvedImage))
		}(op.OperationID)

		writeSuccess(w, http.StatusAccepted, accepted)
		return
	}

	resolvedImage, err := h.facade.PullImage(req.Image, req.RegistryID, req.Platform)
	if err != nil {
		if strings.Contains(err.Error(), "registryId") || strings.Contains(err.Error(), "platform") || strings.Contains(err.Error(), "required") {
			writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
			return
		}
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
		return
	}
	logging.LogInfo(apiHandlerModuleName, fmt.Sprintf("local image pull succeeded image=%s resolvedImage=%s", strings.TrimSpace(req.Image), strings.TrimSpace(resolvedImage)))
	payload := map[string]any{
		"status":        "ok",
		"image":         strings.TrimSpace(req.Image),
		"resolvedImage": strings.TrimSpace(resolvedImage),
		"platform":      strings.TrimSpace(req.Platform),
		"engine":        strings.ToLower(strings.TrimSpace(config.GetInstance().ContainerEngine)),
		"message":       "image pulled successfully",
	}
	if req.RegistryID != nil {
		payload["registryId"] = *req.RegistryID
	}
	writeSuccess(w, http.StatusOK, payload)
}

func (h *EdgeletAPIHandler) HandleImagePullStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	operationID := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/v1/images:pull/"))
	if operationID == "" {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "missing operation id", nil)
		return
	}
	h.pullMu.RLock()
	op, ok := h.pullOps[operationID]
	if !ok {
		h.pullMu.RUnlock()
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "pull operation not found", nil)
		return
	}
	response := snapshotImagePullOperation(op)
	h.pullMu.RUnlock()
	writeSuccess(w, http.StatusOK, response)
}

func (h *EdgeletAPIHandler) HandleImageLoad(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "invalid JSON body", nil)
		return
	}
	path := strings.TrimSpace(req.Path)
	if path == "" {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "path is required", nil)
		return
	}
	engineName := strings.ToLower(strings.TrimSpace(config.GetInstance().ContainerEngine))
	op := &imageLoadOperation{
		OperationID: uuid.NewString(),
		Status:      "running",
		Path:        path,
		Engine:      engineName,
		StartedAt:   time.Now().UTC(),
	}
	h.loadMu.Lock()
	h.loadOps[op.OperationID] = op
	h.loadMu.Unlock()
	logging.LogInfo(apiHandlerModuleName, fmt.Sprintf("image load operation started operationId=%s path=%s engine=%s", op.OperationID, path, engineName))

	accepted := snapshotImageLoadOperation(op)
	go func(operationID, loadPath string) {
		loaded, err := h.facade.LoadImageFromPath(loadPath)
		h.loadMu.Lock()
		defer h.loadMu.Unlock()
		current, ok := h.loadOps[operationID]
		if !ok {
			return
		}
		now := time.Now().UTC()
		current.EndedAt = &now
		if err != nil {
			current.Status = "failed"
			current.Error = err.Error()
			logging.LogWarn(apiHandlerModuleName, fmt.Sprintf("image load failed operationId=%s path=%s err=%v", operationID, loadPath, err))
			return
		}
		items := make([]map[string]any, 0, len(loaded))
		for _, item := range loaded {
			items = append(items, map[string]any{
				"name": item.Name,
				"id":   item.ID,
			})
		}
		current.Status = "succeeded"
		current.Loaded = items
		current.Count = len(items)
		logging.LogInfo(apiHandlerModuleName, fmt.Sprintf("image load succeeded operationId=%s path=%s count=%d", operationID, loadPath, current.Count))
	}(op.OperationID, path)

	writeSuccess(w, http.StatusAccepted, accepted)
}

func (h *EdgeletAPIHandler) HandleImageLoadStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	operationID := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/v1/images:load/"))
	if operationID == "" {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "missing operation id", nil)
		return
	}
	h.loadMu.RLock()
	op, ok := h.loadOps[operationID]
	if !ok {
		h.loadMu.RUnlock()
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "image load operation not found", nil)
		return
	}
	response := snapshotImageLoadOperation(op)
	h.loadMu.RUnlock()
	writeSuccess(w, http.StatusOK, response)
}

func (h *EdgeletAPIHandler) HandleImagePrune(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	mode := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("mode")))
	if mode != "" && mode != runtimeapi.PruneModeDangling {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "image prune supports only mode=dangling", nil)
		return
	}
	result, err := h.facade.Prune(runtimeapi.PruneModeDangling)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "invalid prune mode") {
			writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
			return
		}
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
		return
	}
	writeSuccess(w, http.StatusOK, result)
}

func (h *EdgeletAPIHandler) HandleImageRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	var req struct {
		Selector string `json:"selector"`
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "invalid JSON body", nil)
		return
	}
	if strings.TrimSpace(req.Selector) == "" {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "selector is required", nil)
		return
	}
	removed, err := h.facade.RemoveImage(req.Selector)
	if err != nil {
		if strings.Contains(err.Error(), "ambiguous") || strings.Contains(err.Error(), "required") {
			writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
			return
		}
		if strings.Contains(err.Error(), "not found") {
			writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, err.Error(), nil)
			return
		}
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
		return
	}
	writeSuccess(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"selector": strings.TrimSpace(req.Selector),
		"removed":  removed,
		"engine":   strings.ToLower(strings.TrimSpace(config.GetInstance().ContainerEngine)),
		"message":  "image removed successfully",
	})
}

func (h *EdgeletAPIHandler) HandleSystemGPS(w http.ResponseWriter, r *http.Request) {
	cfg := config.GetInstance()
	switch r.Method {
	case http.MethodGet:
		lat, lon := "0", "0"
		coords := strings.TrimSpace(cfg.GPSCoordinates)
		if coords != "" {
			parts := strings.Split(coords, ",")
			if len(parts) == 2 {
				lat = strings.TrimSpace(parts[0])
				lon = strings.TrimSpace(parts[1])
			}
		}
		writeSuccess(w, http.StatusOK, map[string]any{
			"status":    "okay",
			"timestamp": time.Now().UnixMilli(),
			"lat":       lat,
			"lon":       lon,
		})
	case http.MethodPost:
		var req struct {
			Lat any `json:"lat"`
			Lon any `json:"lon"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "invalid JSON body", nil)
			return
		}
		lat, err := gpsValueToString(req.Lat)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "lat is required", nil)
			return
		}
		lon, err := gpsValueToString(req.Lon)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "lon is required", nil)
			return
		}
		normalizedCoordinates, err := normalizeGPSLatLon(lat, lon)
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
			return
		}
		config.SuppressReloadForInProcessMutation()
		defer config.RestoreReloadAfterInProcessMutation()

		errorsMap := cfg.SetConfig(map[string]any{
			"gps":  "manual",
			"gpsc": normalizedCoordinates,
		})
		if len(errorsMap) > 0 {
			writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "invalid GPS configuration update", map[string]any{"errorMap": errorsMap})
			return
		}
		if err := cfg.TriggerReloadCallback(); err != nil {
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
			return
		}
		if err := cfg.TriggerGPSConfigCallback(); err != nil {
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
			return
		}
		writeSuccess(w, http.StatusOK, map[string]any{"status": "okay"})
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
	}
}

func (h *EdgeletAPIHandler) HandleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		cfg := config.GetInstance()
		writeSuccess(w, http.StatusOK, map[string]any{
			"controllerUrl":          cfg.ControllerURL,
			"controllerCert":         cfg.ControllerCert,
			"containerEngine":        cfg.ContainerEngine,
			"containerEngineUrl":     cfg.ContainerEngineURL,
			"networkInterface":       cfg.NetworkInterface,
			"diskLimitGiB":           cfg.DiskLimit,
			"diskDirectory":          cfg.DiskDirectory,
			"memoryLimitMiB":         cfg.MemoryLimit,
			"cpuLimitPercent":        cfg.CPULimit,
			"logLimitGiB":            cfg.LogLimit,
			"logDirectory":           cfg.LogDirectory,
			"logFileCount":           cfg.LogFileCount,
			"logLevel":               cfg.LogLevel,
			"statusFrequencySeconds": cfg.StatusFrequency,
			"changeFrequencySeconds": cfg.ChangeFrequency,
			"watchdogEnabled":        cfg.WatchdogEnabled,
			"edgeGuardFrequency":     cfg.EdgeGuardFrequency,
			"gpsMode":                cfg.GPSMode,
			"gpsCoordinates":         cfg.GPSCoordinates,
			"gpsDevice":              cfg.GPSDevice,
			"gpsScanFrequency":       cfg.GPSScanFrequency,
			"arch":                   cfg.Arch,
			"secureMode":             cfg.SecureMode,
			"pruningFrequency":       cfg.PruningFrequency,
			"availableDiskThreshold": cfg.AvailableDiskThreshold,
			"upgradeScanFrequency":   cfg.UpgradeScanFrequency,
			"devMode":                cfg.DevMode,
			"timezone":               cfg.TimeZone,
		})
	case http.MethodPatch:
		var req struct {
			Set map[string]any `json:"set"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "invalid JSON body", nil)
			return
		}
		if len(req.Set) == 0 {
			writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "missing set payload", nil)
			return
		}

		configMap := make(map[string]any)
		for key, value := range req.Set {
			if short, ok := configKeyToShortCode(key); ok {
				configMap[short] = fmt.Sprintf("%v", value)
			}
		}
		if len(configMap) == 0 {
			writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "no supported config keys provided", nil)
			return
		}
		cfg := config.GetInstance()
		if err := validateNetworkInterfaceUpdate(cfg, configMap); err != nil {
			writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), map[string]any{
				"field": "networkInterface",
			})
			return
		}
		cfg.SnapshotForReload()
		config.SuppressReloadForInProcessMutation()
		defer config.RestoreReloadAfterInProcessMutation()

		errorsMap := cfg.SetConfig(configMap)
		if len(errorsMap) == 0 {
			if err := cfg.TriggerReloadCallback(); err != nil {
				writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
				return
			}
		}
		resp := map[string]any{
			"status":   "ok",
			"errorMap": errorsMap,
		}
		if runtimestate.GetState().PendingRestart() {
			resp["pendingRestart"] = true
			resp["message"] = "containerEngine change persisted; restart edgelet.service to activate the new engine"
		}
		writeSuccess(w, http.StatusOK, resp)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
	}
}

func validateNetworkInterfaceUpdate(cfg *config.Config, configMap map[string]any) error {
	rawNetworkInterface, hasNetworkInterface := configMap["n"]
	if !hasNetworkInterface {
		return nil
	}
	networkInterfaceValue := strings.TrimSpace(fmt.Sprintf("%v", rawNetworkInterface))
	controllerURL := strings.TrimSpace(cfg.ControllerURL)
	if rawControllerURL, hasControllerURL := configMap["a"]; hasControllerURL {
		controllerURL = strings.TrimSpace(fmt.Sprintf("%v", rawControllerURL))
	}
	if err := network.GetInstance().ValidateNetworkInterfaceConfig(controllerURL, networkInterfaceValue); err != nil {
		return fmt.Errorf("invalid networkInterface %q: %w", networkInterfaceValue, err)
	}
	return nil
}

func (h *EdgeletAPIHandler) HandleSystemControllerCert(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	var req struct {
		Certificate string `json:"certificate"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "invalid JSON body", nil)
		return
	}
	cert := strings.TrimSpace(req.Certificate)
	if cert == "" {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "certificate is required", nil)
		return
	}
	decoded, err := base64.StdEncoding.DecodeString(cert)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "certificate must be a valid base64-encoded PEM certificate", nil)
		return
	}
	if _, err := auth.LoadCertificateFromPEM(decoded); err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "decoded certificate must be valid PEM", nil)
		return
	}

	cfg := config.GetInstance()
	certPath := strings.TrimSpace(cfg.ControllerCert)
	if certPath == "" {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "controllerCert path is not configured; use edgelet config -ac <path>", nil)
		return
	}
	certDir := filepath.Dir(certPath)
	if certDir != "" && certDir != "." {
		if err := os.MkdirAll(certDir, 0o755); err != nil { // #nosec G301 -- cert dir must be traversable for daemon user
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, fmt.Sprintf("failed to prepare certificate directory: %v", err), nil)
			return
		}
	}
	if err := os.WriteFile(certPath, decoded, 0o600); err != nil {
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, fmt.Sprintf("failed to write certificate file: %v", err), nil)
		return
	}

	config.SuppressReloadForInProcessMutation()
	defer config.RestoreReloadAfterInProcessMutation()

	errorsMap := cfg.SetConfig(map[string]any{"sec": "on"})
	if len(errorsMap) > 0 {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "failed to enable secure mode", map[string]any{"errorMap": errorsMap})
		return
	}
	if err := cfg.TriggerReloadCallback(); err != nil {
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
		return
	}
	writeSuccess(w, http.StatusOK, map[string]any{
		"status":               "ok",
		"updatedKey":           "controllerCert",
		"certificatePath":      certPath,
		"secureMode":           "on",
		"certificateInstalled": true,
	})
}

func (h *EdgeletAPIHandler) HandleSystemConfigSwitch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	var req struct {
		Profile string `json:"profile"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "invalid JSON body", nil)
		return
	}
	profileInput := strings.TrimSpace(req.Profile)
	if profileInput == "" {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "profile is required", nil)
		return
	}
	profile, err := utils.ParseConfigSwitcherState(strings.ToLower(profileInput))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "profile must be one of dev|prod|def", nil)
		return
	}
	cfg := config.GetInstance()
	oldProfile := cfg.GetCurrentProfile().FullValue()

	config.SuppressReloadForInProcessMutation()
	defer config.RestoreReloadAfterInProcessMutation()

	if err := cfg.SwitchProfile(profile); err != nil {
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
		return
	}
	if err := cfg.TriggerReloadCallback(); err != nil {
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
		return
	}
	writeSuccess(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"oldProfile": oldProfile,
		"profile":    profile.FullValue(),
	})
}

func (h *EdgeletAPIHandler) HandleMicroserviceConfigSelf(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(authHeader, "Bearer ") {
		writeAPIError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "missing bearer token", nil)
		return
	}
	token := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
	result, err := auth.ValidateEdgeletAPIJWT(token)
	if err != nil {
		writeAPIError(w, http.StatusUnauthorized, ErrCodeUnauthorized, "invalid JWT token", nil)
		return
	}
	microserviceUUID, ok := claimMicroserviceUUID(result.Claims)
	if !ok {
		writeAPIError(w, http.StatusForbidden, ErrCodeForbidden, "microservice identity claim is required", nil)
		return
	}

	configString, exists := fieldagent.GetInstance().GetContainerConfig(microserviceUUID)
	if !exists || strings.TrimSpace(configString) == "" {
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "microservice config not found", nil)
		return
	}

	var payload any
	if err := json.Unmarshal([]byte(configString), &payload); err != nil {
		// Keep v2-aligned arbitrary payload behavior even when payload is not strict JSON.
		payload = configString
	}
	writeSuccess(w, http.StatusOK, map[string]any{
		"microserviceUuid": microserviceUUID,
		"config":           payload,
	})
}

func (h *EdgeletAPIHandler) HandleMicroservices(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/ms" {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
			return
		}
		source := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("source")))
		if source == "" {
			source = "all"
		}
		if source != "all" && source != "managed" && source != "local" && source != "controlplane" {
			writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "source must be one of managed|local|controlplane|all", nil)
			return
		}
		items := h.facade.ListRuntimeMicroservices()
		filtered := make([]map[string]any, 0, len(items))
		for _, item := range items {
			itemType := strings.ToLower(fmt.Sprintf("%v", item["type"]))
			itemSource := strings.ToLower(fmt.Sprintf("%v", item["source"]))
			if source == "all" || source == itemType || source == itemSource {
				filtered = append(filtered, item)
			}
		}
		writeSuccess(w, http.StatusOK, map[string]any{
			"items": filtered,
		})
		return
	}

	rest := strings.TrimPrefix(r.URL.Path, "/v1/ms/")
	rest = strings.TrimSpace(rest)
	if rest == "" {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "missing microservice id", nil)
		return
	}
	parts := strings.Split(rest, "/")
	id := strings.TrimSpace(parts[0])
	if id == "" {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "missing microservice id", nil)
		return
	}

	if len(parts) == 1 {
		switch r.Method {
		case http.MethodGet:
			item, err := h.facade.GetRuntimeMicroservice(id)
			if err != nil {
				writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, err.Error(), nil)
				return
			}
			if summary := strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("summary")), "true"); summary {
				payload := map[string]any{
					"uuid":         item["uuid"],
					"name":         item["name"],
					"application":  item["application"],
					"source":       item["source"],
					"type":         item["type"],
					"state":        item["state"],
					"containerId":  item["containerId"],
					"image":        item["image"],
					"healthStatus": item["healthStatus"],
				}
				if podID, ok := item["podId"]; ok && strings.TrimSpace(fmt.Sprintf("%v", podID)) != "" && podID != nil {
					payload["podId"] = podID
				}
				writeSuccess(w, http.StatusOK, payload)
				return
			}
			writeSuccess(w, http.StatusOK, item)
		case http.MethodDelete:
			cleanup, parseErr := parseBooleanFormValue(r.URL.Query().Get("cleanup"), "cleanup")
			if parseErr != nil {
				writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, parseErr.Error(), nil)
				return
			}
			uuid, err := h.facade.RemoveRuntimeMicroserviceWithCleanup(id, cleanup)
			if err != nil {
				writeMicroserviceLifecycleError(w, err)
				return
			}
			payload := map[string]any{
				"status":           "ok",
				"microserviceUuid": uuid,
				"warning":          "if microservice is controller-managed, reconcile may recreate it",
			}
			if cleanup {
				payload["cleanupReserved"] = true
			}
			writeSuccess(w, http.StatusOK, payload)
		default:
			writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		}
		return
	}

	action := strings.TrimSpace(strings.Join(parts[1:], "/"))
	switch action {
	case "start":
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
			return
		}
		uuid, err := h.facade.StartRuntimeMicroservice(id)
		if err != nil {
			writeMicroserviceLifecycleError(w, err)
			return
		}
		writeSuccess(w, http.StatusOK, map[string]any{
			"status":           "ok",
			"microserviceUuid": uuid,
			"warning":          "if microservice is controller-managed, reconcile may recreate or restart it",
		})
	case "stop":
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
			return
		}
		uuid, err := h.facade.StopRuntimeMicroservice(id)
		if err != nil {
			writeMicroserviceLifecycleError(w, err)
			return
		}
		writeSuccess(w, http.StatusOK, map[string]any{
			"status":           "ok",
			"microserviceUuid": uuid,
			"warning":          "if microservice is controller-managed, reconcile may recreate or restart it",
		})
	case "restart":
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
			return
		}
		uuid, err := h.facade.RestartRuntimeMicroservice(id)
		if err != nil {
			writeMicroserviceLifecycleError(w, err)
			return
		}
		writeSuccess(w, http.StatusOK, map[string]any{
			"status":           "ok",
			"microserviceUuid": uuid,
			"warning":          "if microservice is controller-managed, reconcile may recreate or restart it",
		})
	case "kill":
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
			return
		}
		uuid, err := h.facade.KillRuntimeMicroservice(id)
		if err != nil {
			writeMicroserviceLifecycleError(w, err)
			return
		}
		writeSuccess(w, http.StatusOK, map[string]any{
			"status":           "ok",
			"microserviceUuid": uuid,
			"warning":          "if microservice is controller-managed, reconcile may recreate or restart it",
		})
	case "logs":
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
			return
		}
		tailLines := 100
		tailRaw := strings.TrimSpace(r.URL.Query().Get("tailLines"))
		if tailRaw == "" {
			tailRaw = strings.TrimSpace(r.URL.Query().Get("tail"))
		}
		if tailRaw != "" {
			parsed, err := strconv.Atoi(tailRaw)
			if err != nil || parsed <= 0 {
				writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "tailLines must be a positive integer", nil)
				return
			}
			tailLines = parsed
		}
		uuid, entries, err := h.facade.GetRuntimeMicroserviceLogs(id, tailLines, strings.TrimSpace(r.URL.Query().Get("since")), strings.TrimSpace(r.URL.Query().Get("until")))
		if err != nil {
			writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
			return
		}
		writeSuccess(w, http.StatusOK, map[string]any{
			"microserviceUuid": uuid,
			"entries":          entries,
		})
	case "logs:stream":
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
			return
		}
		h.handleLogsStreamWS(w, r, id)
	case "exec/sessions":
		if r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
			return
		}
		h.handleCreateExecSession(w, r, id)
	default:
		if strings.HasPrefix(action, "exec/sessions/") {
			sessionPart := strings.TrimPrefix(action, "exec/sessions/")
			if strings.HasSuffix(sessionPart, ":attach") {
				if r.Method != http.MethodGet {
					writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
					return
				}
				sessionID := strings.TrimSuffix(sessionPart, ":attach")
				h.handleAttachExecSessionWS(w, r, id, sessionID)
				return
			}
			sessionID := strings.TrimSpace(sessionPart)
			if sessionID == "" {
				writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "missing exec session id", nil)
				return
			}
			switch r.Method {
			case http.MethodGet:
				h.handleGetExecSessionStatus(w, id, sessionID)
			case http.MethodDelete:
				h.handleStopExecSession(w, id, sessionID)
			default:
				writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
			}
			return
		}
		writeAPIError(w, http.StatusNotImplemented, ErrCodeNotImplemented, "not implemented", nil)
	}
}

func (h *EdgeletAPIHandler) HandleDeployMicroservicesApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	manifest, sourceName, dryRun, err := parseManifestMultipartRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
		return
	}
	asyncRaw := strings.TrimSpace(strings.ToLower(r.FormValue("async")))
	switch asyncRaw {
	case "", "false", "0", "no", "true", "1", "yes":
	default:
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "async must be a boolean", nil)
		return
	}

	manifestDoc, err := h.facade.ParseAndValidateLocalManifest(manifest)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
		return
	}
	op := &deployApplyOperation{
		OperationID: uuid.NewString(),
		Status:      "running",
		Stage:       runtimeapi.DeployStageParsing,
		Kind:        strings.TrimSpace(manifestDoc.Kind),
		Name:        strings.TrimSpace(manifestDoc.Metadata.Name),
		Image:       manifestDoc.ManifestImage(),
		StartedAt:   time.Now().UTC(),
	}

	h.deployMu.Lock()
	h.deployOps[op.OperationID] = op
	h.deployMu.Unlock()
	logging.LogInfo(apiHandlerModuleName, fmt.Sprintf("local deploy apply operation started operationId=%s source=%s dryRun=%v name=%s image=%s", op.OperationID, strings.TrimSpace(sourceName), dryRun, op.Name, op.Image))

	accepted := snapshotDeployApplyAccepted(op)
	go func(operationID string, manifestText string, source string, applyDryRun bool) {
		deploymentID, _, applyErr := h.facade.ApplyLocalManifest(manifestText, source, applyDryRun, func(stage string, _ string) {
			normalized := strings.TrimSpace(strings.ToLower(stage))
			if normalized == "" {
				return
			}
			h.deployMu.Lock()
			defer h.deployMu.Unlock()
			current, ok := h.deployOps[operationID]
			if !ok {
				return
			}
			current.Stage = normalized
		})

		h.deployMu.Lock()
		defer h.deployMu.Unlock()
		current, ok := h.deployOps[operationID]
		if !ok {
			return
		}
		now := time.Now().UTC()
		current.EndedAt = &now
		if applyErr != nil {
			current.Status = "failed"
			current.ErrorCode = ErrCodeInternal
			current.ErrorMessage = applyErr.Error()
			logging.LogWarn(apiHandlerModuleName, fmt.Sprintf("local deploy apply failed operationId=%s source=%s err=%v", operationID, strings.TrimSpace(source), applyErr))
			return
		}
		current.Status = "succeeded"
		current.Stage = runtimeapi.DeployStageDone
		current.DeploymentID = strings.TrimSpace(deploymentID)
		logging.LogInfo(apiHandlerModuleName, fmt.Sprintf("local deploy apply succeeded operationId=%s deploymentId=%s stage=%s", operationID, current.DeploymentID, current.Stage))
	}(op.OperationID, manifest, sourceName, dryRun)

	writeSuccess(w, http.StatusAccepted, accepted)
}

func (h *EdgeletAPIHandler) HandleDeployMicroservicesApplyStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	operationID := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/v1/deploy/microservices:apply/"))
	if operationID == "" {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "missing operation id", nil)
		return
	}
	h.deployMu.RLock()
	op, ok := h.deployOps[operationID]
	if !ok {
		h.deployMu.RUnlock()
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "deploy apply operation not found", nil)
		return
	}
	response := snapshotDeployApplyOperation(op)
	h.deployMu.RUnlock()
	writeSuccess(w, http.StatusOK, response)
}

func (h *EdgeletAPIHandler) HandleDeployMicroservicesValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	manifest, _, _, err := parseManifestMultipartRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
		return
	}
	doc, err := h.facade.ParseAndValidateLocalManifest(manifest)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
		return
	}
	writeSuccess(w, http.StatusOK, map[string]any{
		"valid":      true,
		"kind":       doc.Kind,
		"name":       doc.Metadata.Name,
		"apiVersion": doc.APIVersion,
		"image":      doc.ManifestImage(),
	})
}

func (h *EdgeletAPIHandler) HandleDeployMicroservices(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/deploy/microservices" {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
			return
		}
		list, err := h.facade.ListLocalDeployments()
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
			return
		}
		writeSuccess(w, http.StatusOK, map[string]any{"items": list})
		return
	}

	id := strings.TrimPrefix(r.URL.Path, "/v1/deploy/microservices/")
	if id == "" {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "missing deployment id", nil)
		return
	}
	switch r.Method {
	case http.MethodGet:
		item, err := h.facade.GetLocalDeployment(id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "not found", nil)
				return
			}
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
			return
		}
		writeSuccess(w, http.StatusOK, item)
	case http.MethodDelete:
		if err := h.facade.DeleteLocalDeployment(id); err != nil {
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
			return
		}
		writeSuccess(w, http.StatusOK, map[string]any{"status": "ok"})
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
	}
}

func (h *EdgeletAPIHandler) HandleDeployRegistriesApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	manifest, _, dryRun, err := parseManifestMultipartRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
		return
	}
	logging.LogInfo(apiHandlerModuleName, fmt.Sprintf("local registry apply requested dryRun=%v", dryRun))
	reg, err := h.facade.ApplyLocalRegistryManifest(manifest, dryRun)
	if err != nil {
		logging.LogWarn(apiHandlerModuleName, fmt.Sprintf("local registry apply failed dryRun=%v err=%v", dryRun, err))
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
		return
	}
	logging.LogInfo(apiHandlerModuleName, fmt.Sprintf("local registry apply succeeded id=%d url=%s type=%s dryRun=%v", reg.ID, strings.TrimSpace(reg.URL), reg.NormalizedType(), dryRun))
	writeSuccess(w, http.StatusOK, map[string]any{
		"accepted": true,
		"dryRun":   dryRun,
		"kind":     "Registry",
		"registry": reg,
	})
}

func (h *EdgeletAPIHandler) HandleDeployRegistriesValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	manifest, _, _, err := parseManifestMultipartRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
		return
	}
	doc, err := h.facade.ParseAndValidateLocalRegistryManifest(manifest)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
		return
	}
	writeSuccess(w, http.StatusOK, map[string]any{
		"valid":      true,
		"apiVersion": doc.APIVersion,
		"kind":       doc.Kind,
		"url":        doc.Spec.URL,
		"private":    doc.Spec.Private,
		"type":       doc.Spec.Type,
		"insecure":   doc.Spec.Insecure,
	})
}

func (h *EdgeletAPIHandler) HandleDeployRegistries(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/deploy/registries" {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
			return
		}
		items, err := h.facade.ListRegistries()
		if err != nil {
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
			return
		}
		writeSuccess(w, http.StatusOK, map[string]any{"items": items})
		return
	}

	idRaw := strings.TrimPrefix(r.URL.Path, "/v1/deploy/registries/")
	idRaw = strings.TrimSpace(idRaw)
	id, err := strconv.Atoi(idRaw)
	if err != nil || id <= 0 {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "invalid registry id", nil)
		return
	}
	switch r.Method {
	case http.MethodGet:
		item, err := h.facade.GetRegistry(id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "not found", nil)
				return
			}
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
			return
		}
		writeSuccess(w, http.StatusOK, item)
	case http.MethodDelete:
		if err := h.facade.DeleteRegistry(id); err != nil {
			writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
			return
		}
		writeSuccess(w, http.StatusOK, map[string]any{"status": "ok", "id": id})
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
	}
}

func (h *EdgeletAPIHandler) HandleDeployRuntimeClassesApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	manifest, _, dryRun, err := parseManifestMultipartRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
		return
	}
	async, err := parseBooleanFormValue(r.FormValue("async"), "async")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
		return
	}

	doc, err := h.facade.ParseAndValidateLocalRuntimeClassManifest(manifest)
	if err != nil {
		writeRuntimeClassManifestError(w, err)
		return
	}

	op := &runtimeClassApplyOperation{
		OperationID: uuid.NewString(),
		Status:      "queued",
		Stage:       runtimeapi.RuntimeClassStageWriteConfig,
		Kind:        strings.TrimSpace(doc.Kind),
		Name:        strings.TrimSpace(doc.Metadata.Name),
		DryRun:      dryRun,
		StartedAt:   time.Now().UTC(),
	}
	h.runtimeClassApplyMu.Lock()
	h.runtimeClassApplyOps[op.OperationID] = op
	h.runtimeClassApplyMu.Unlock()

	accepted := snapshotRuntimeClassApplyOperation(op)
	applyRunner := runtimeClassApplyRunner
	syncWaitTimeout := runtimeClassApplySyncWaitTimeout
	done := make(chan struct{})
	go func(operationID string, manifestText string, applyDryRun bool) {
		h.runtimeClassApplyMu.Lock()
		current, ok := h.runtimeClassApplyOps[operationID]
		if ok {
			current.Status = "running"
			current.Stage = runtimeapi.RuntimeClassStageWriteConfig
		}
		h.runtimeClassApplyMu.Unlock()

		item, applyErr := applyRunner(h.facade, manifestText, applyDryRun)

		h.runtimeClassApplyMu.Lock()
		defer h.runtimeClassApplyMu.Unlock()
		current, ok = h.runtimeClassApplyOps[operationID]
		if !ok {
			close(done)
			return
		}
		now := time.Now().UTC()
		current.EndedAt = &now
		if applyErr != nil {
			current.Status = "failed"
			current.Stage = runtimeClassErrorStage(applyErr, runtimeapi.RuntimeClassStageWriteConfig)
			current.ErrorCode = ErrCodeInternal
			if errors.Is(applyErr, runtimeapi.ErrRuntimeClassUnsupported) {
				current.ErrorCode = ErrCodeInvalidArgument
			}
			var managedErr *runtimeapi.ErrRuntimeClassManagedName
			if errors.As(applyErr, &managedErr) {
				current.ErrorCode = ErrCodeConflict
			}
			current.ErrorMessage = applyErr.Error()
			current.ErrorDetails = runtimeClassErrorDetails(applyErr)
			close(done)
			return
		}
		current.RuntimeClass = item
		current.Status = "succeeded"
		current.Stage = runtimeapi.RuntimeClassStageDone
		close(done)
	}(op.OperationID, manifest, dryRun)

	if async {
		writeSuccess(w, http.StatusAccepted, accepted)
		return
	}

	select {
	case <-done:
		h.runtimeClassApplyMu.RLock()
		current, ok := h.runtimeClassApplyOps[op.OperationID]
		if !ok {
			h.runtimeClassApplyMu.RUnlock()
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, "runtimeclass apply operation missing", nil)
			return
		}
		if strings.TrimSpace(current.ErrorMessage) != "" {
			statusCode := http.StatusInternalServerError
			if current.ErrorCode == ErrCodeInvalidArgument {
				statusCode = http.StatusBadRequest
			}
			if current.ErrorCode == ErrCodeConflict {
				statusCode = http.StatusConflict
			}
			details := current.ErrorDetails
			if details == nil {
				details = map[string]any{}
			}
			if _, hasStage := details["stage"]; !hasStage {
				details["stage"] = current.Stage
			}
			details["operationId"] = current.OperationID
			errCode := current.ErrorCode
			errMsg := current.ErrorMessage
			stage := current.Stage
			operationID := current.OperationID
			h.runtimeClassApplyMu.RUnlock()
			writeAPIError(w, statusCode, errCode, errMsg, map[string]any{
				"operationId": operationID,
				"stage":       stage,
				"error": map[string]any{
					"code":    errCode,
					"message": errMsg,
					"details": details,
				},
			})
			return
		}
		response := snapshotRuntimeClassApplyOperation(current)
		h.runtimeClassApplyMu.RUnlock()
		writeSuccess(w, http.StatusOK, response)
	case <-time.After(syncWaitTimeout):
		h.runtimeClassApplyMu.RLock()
		current, ok := h.runtimeClassApplyOps[op.OperationID]
		var response map[string]any
		if ok {
			response = snapshotRuntimeClassApplyOperation(current)
		} else {
			response = accepted
		}
		h.runtimeClassApplyMu.RUnlock()
		writeSuccess(w, http.StatusAccepted, response)
	}
}

func (h *EdgeletAPIHandler) HandleDeployRuntimeClassesApplyStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	operationID := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/v1/deploy/runtimeclasses:apply/"))
	if operationID == "" {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "missing operation id", nil)
		return
	}
	h.runtimeClassApplyMu.RLock()
	op, ok := h.runtimeClassApplyOps[operationID]
	if !ok {
		h.runtimeClassApplyMu.RUnlock()
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "runtimeclass apply operation not found", nil)
		return
	}
	response := snapshotRuntimeClassApplyOperation(op)
	h.runtimeClassApplyMu.RUnlock()
	writeSuccess(w, http.StatusOK, response)
}

func (h *EdgeletAPIHandler) HandleDeployRuntimeClassesValidate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	manifest, _, _, err := parseManifestMultipartRequest(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
		return
	}
	doc, err := h.facade.ParseAndValidateLocalRuntimeClassManifest(manifest)
	if err != nil {
		writeRuntimeClassManifestError(w, err)
		return
	}
	writeSuccess(w, http.StatusOK, map[string]any{
		"valid":       true,
		"apiVersion":  doc.APIVersion,
		"kind":        doc.Kind,
		"name":        doc.Metadata.Name,
		"handler":     doc.Handler,
		"runtimeName": doc.Metadata.Name,
	})
}

func (h *EdgeletAPIHandler) HandleDeployRuntimeClasses(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/v1/deploy/runtimeclasses" {
		if r.Method != http.MethodGet {
			writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
			return
		}
		items, err := h.facade.ListRuntimeClasses()
		if err != nil {
			if errors.Is(err, runtimeapi.ErrRuntimeClassUnsupported) {
				writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
				return
			}
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
			return
		}
		writeSuccess(w, http.StatusOK, map[string]any{"items": items})
		return
	}

	name := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/v1/deploy/runtimeclasses/"))
	if name == "" {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "missing runtime class name", nil)
		return
	}

	switch r.Method {
	case http.MethodGet:
		item, err := h.facade.GetRuntimeClass(name)
		if err != nil {
			if errors.Is(err, runtimeapi.ErrRuntimeClassUnsupported) {
				writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
				return
			}
			if errors.Is(err, sql.ErrNoRows) {
				writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "not found", nil)
				return
			}
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
			return
		}
		writeSuccess(w, http.StatusOK, item)
	case http.MethodDelete:
		h.handleDeployRuntimeClassDelete(w, r, name)
	default:
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
	}
}

func (h *EdgeletAPIHandler) HandleDeployRuntimeClassesDeleteStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	operationID := strings.TrimSpace(strings.TrimPrefix(r.URL.Path, "/v1/deploy/runtimeclasses:delete/"))
	if operationID == "" {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "missing operation id", nil)
		return
	}
	h.runtimeClassDeleteMu.RLock()
	op, ok := h.runtimeClassDeleteOps[operationID]
	if !ok {
		h.runtimeClassDeleteMu.RUnlock()
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "runtimeclass delete operation not found", nil)
		return
	}
	response := snapshotRuntimeClassDeleteOperation(op)
	h.runtimeClassDeleteMu.RUnlock()
	writeSuccess(w, http.StatusOK, response)
}

func (h *EdgeletAPIHandler) handleDeployRuntimeClassDelete(w http.ResponseWriter, r *http.Request, name string) {
	async, err := parseBooleanFormValue(r.URL.Query().Get("async"), "async")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
		return
	}

	target, err := runtimeClassDeletePreflightRunner(h.facade, name)
	if err != nil {
		statusCode, code := runtimeClassDeleteStatusAndCode(err)
		writeAPIError(w, statusCode, code, err.Error(), runtimeClassErrorDetails(err))
		return
	}

	op := &runtimeClassDeleteOperation{
		OperationID:  uuid.NewString(),
		Status:       "queued",
		Stage:        runtimeapi.RuntimeClassStageWriteConfig,
		Kind:         "RuntimeClassDelete",
		Name:         strings.TrimSpace(strings.ToLower(name)),
		StartedAt:    time.Now().UTC(),
		RuntimeClass: target,
	}
	h.runtimeClassDeleteMu.Lock()
	h.runtimeClassDeleteOps[op.OperationID] = op
	h.runtimeClassDeleteMu.Unlock()

	accepted := snapshotRuntimeClassDeleteOperation(op)
	deleteRunner := runtimeClassDeleteRunner
	syncWaitTimeout := runtimeClassDeleteSyncWaitTimeout
	done := make(chan struct{})
	go func(operationID, runtimeClassName string) {
		h.runtimeClassDeleteMu.Lock()
		current, ok := h.runtimeClassDeleteOps[operationID]
		if ok {
			current.Status = "running"
			current.Stage = runtimeapi.RuntimeClassStageWriteConfig
		}
		h.runtimeClassDeleteMu.Unlock()

		deleteErr := deleteRunner(h.facade, runtimeClassName)

		h.runtimeClassDeleteMu.Lock()
		defer h.runtimeClassDeleteMu.Unlock()
		current, ok = h.runtimeClassDeleteOps[operationID]
		if !ok {
			close(done)
			return
		}
		now := time.Now().UTC()
		current.EndedAt = &now
		if deleteErr != nil {
			current.Status = "failed"
			current.Stage = runtimeClassErrorStage(deleteErr, runtimeapi.RuntimeClassStageWriteConfig)
			_, current.ErrorCode = runtimeClassDeleteStatusAndCode(deleteErr)
			current.ErrorMessage = deleteErr.Error()
			current.ErrorDetails = runtimeClassErrorDetails(deleteErr)
			close(done)
			return
		}
		current.Status = "succeeded"
		current.Stage = runtimeapi.RuntimeClassStageDone
		close(done)
	}(op.OperationID, name)

	if async {
		writeSuccess(w, http.StatusAccepted, accepted)
		return
	}

	select {
	case <-done:
		h.runtimeClassDeleteMu.RLock()
		current, ok := h.runtimeClassDeleteOps[op.OperationID]
		if !ok {
			h.runtimeClassDeleteMu.RUnlock()
			writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, "runtimeclass delete operation missing", nil)
			return
		}
		if strings.TrimSpace(current.ErrorMessage) != "" {
			statusCode := http.StatusInternalServerError
			switch current.ErrorCode {
			case ErrCodeInvalidArgument:
				statusCode = http.StatusBadRequest
			case ErrCodeNotFound:
				statusCode = http.StatusNotFound
			}
			details := current.ErrorDetails
			if details == nil {
				details = map[string]any{}
			}
			if _, hasStage := details["stage"]; !hasStage {
				details["stage"] = current.Stage
			}
			details["operationId"] = current.OperationID
			errCode := current.ErrorCode
			errMsg := current.ErrorMessage
			h.runtimeClassDeleteMu.RUnlock()
			writeAPIError(w, statusCode, errCode, errMsg, details)
			return
		}
		response := snapshotRuntimeClassDeleteOperation(current)
		h.runtimeClassDeleteMu.RUnlock()
		writeSuccess(w, http.StatusOK, response)
	case <-time.After(syncWaitTimeout):
		h.runtimeClassDeleteMu.RLock()
		current, ok := h.runtimeClassDeleteOps[op.OperationID]
		var response map[string]any
		if ok {
			response = snapshotRuntimeClassDeleteOperation(current)
		} else {
			response = accepted
		}
		h.runtimeClassDeleteMu.RUnlock()
		writeSuccess(w, http.StatusAccepted, response)
	}
}

func runtimeClassDeleteStatusAndCode(err error) (int, string) {
	switch {
	case errors.Is(err, runtimeapi.ErrRuntimeClassUnsupported):
		return http.StatusBadRequest, ErrCodeInvalidArgument
	case errors.Is(err, sql.ErrNoRows):
		return http.StatusNotFound, ErrCodeNotFound
	default:
		var reservedErr *runtimeapi.ErrReservedRuntimeClassDelete
		if errors.As(err, &reservedErr) {
			return http.StatusBadRequest, ErrCodeInvalidArgument
		}
		var inUseErr *runtimeapi.ErrRuntimeClassInUse
		if errors.As(err, &inUseErr) {
			return http.StatusBadRequest, ErrCodeInvalidArgument
		}
		var managedErr *runtimeapi.ErrRuntimeClassManagedName
		if errors.As(err, &managedErr) {
			return http.StatusConflict, ErrCodeConflict
		}
		return http.StatusInternalServerError, ErrCodeInternal
	}
}

func writeRuntimeClassManifestError(w http.ResponseWriter, err error) {
	if errors.Is(err, runtimeapi.ErrRuntimeClassUnsupported) {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
		return
	}
	var managedErr *runtimeapi.ErrRuntimeClassManagedName
	if errors.As(err, &managedErr) {
		writeAPIError(w, http.StatusConflict, ErrCodeConflict, err.Error(), managedErr.Details())
		return
	}
	writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
}

func runtimeClassErrorDetails(err error) map[string]any {
	type detailsProvider interface {
		Details() map[string]any
	}
	var provider detailsProvider
	if errors.As(err, &provider) {
		return provider.Details()
	}
	return nil
}

func runtimeClassErrorStage(err error, fallback string) string {
	fallback = runtimeapi.NormalizeRuntimeClassOperationStage(fallback)
	if fallback == "" {
		fallback = runtimeapi.RuntimeClassStageWriteConfig
	}
	details := runtimeClassErrorDetails(err)
	if len(details) == 0 {
		return fallback
	}
	raw, ok := details["stage"]
	if !ok {
		return fallback
	}
	stage := runtimeapi.NormalizeRuntimeClassOperationStage(fmt.Sprintf("%v", raw))
	if stage == "" {
		return fallback
	}
	return stage
}

func parseManifestMultipartRequest(r *http.Request) (manifest, sourceName string, dryRun bool, err error) {
	if err := r.ParseMultipartForm(16 << 20); err != nil { // #nosec G120 -- multipart capped at 16 MiB
		return "", "", false, errors.New("invalid multipart form body")
	}
	file, _, ferr := r.FormFile("manifest")
	if ferr != nil {
		return "", "", false, errors.New("manifest file part is required")
	}
	defer file.Close()
	raw, rerr := io.ReadAll(file)
	if rerr != nil {
		return "", "", false, errors.New("failed to read manifest")
	}
	manifest = strings.TrimSpace(string(raw))
	if manifest == "" {
		return "", "", false, errors.New("manifest file is empty")
	}
	sourceName = strings.TrimSpace(r.FormValue("sourceName"))
	dryRaw := strings.TrimSpace(strings.ToLower(r.FormValue("dryRun")))
	switch dryRaw {
	case "", "false", "0", "no":
		dryRun = false
	case "true", "1", "yes":
		dryRun = true
	default:
		return "", "", false, errors.New("dryRun must be a boolean")
	}
	return manifest, sourceName, dryRun, nil
}

func parseBooleanFormValue(raw string, fieldName string) (bool, error) {
	switch strings.TrimSpace(strings.ToLower(raw)) {
	case "", "false", "0", "no":
		return false, nil
	case "true", "1", "yes":
		return true, nil
	default:
		return false, fmt.Errorf("%s must be a boolean", fieldName)
	}
}

func runtimeClassApplyOperationResponse(op *runtimeClassApplyOperation) map[string]any {
	response := map[string]any{
		"operationId": op.OperationID,
		"status":      op.Status,
		"startedAt":   op.StartedAt.Format(time.RFC3339Nano),
		"kind":        op.Kind,
		"name":        op.Name,
		"dryRun":      op.DryRun,
	}
	if strings.TrimSpace(op.Stage) != "" {
		response["stage"] = op.Stage
	}
	if op.EndedAt != nil {
		response["endedAt"] = op.EndedAt.Format(time.RFC3339Nano)
	}
	if op.RuntimeClass != nil {
		response["runtimeClass"] = op.RuntimeClass
	}
	if strings.TrimSpace(op.ErrorMessage) != "" {
		code := strings.TrimSpace(op.ErrorCode)
		if code == "" {
			code = ErrCodeInternal
		}
		errorPayload := map[string]any{
			"code":    code,
			"message": op.ErrorMessage,
		}
		if len(op.ErrorDetails) > 0 {
			errorPayload["details"] = op.ErrorDetails
		}
		response["error"] = errorPayload
	}
	return response
}

func runtimeClassDeleteOperationResponse(op *runtimeClassDeleteOperation) map[string]any {
	response := map[string]any{
		"operationId": op.OperationID,
		"status":      op.Status,
		"startedAt":   op.StartedAt.Format(time.RFC3339Nano),
		"kind":        op.Kind,
		"name":        op.Name,
	}
	if strings.TrimSpace(op.Stage) != "" {
		response["stage"] = op.Stage
	}
	if op.EndedAt != nil {
		response["endedAt"] = op.EndedAt.Format(time.RFC3339Nano)
	}
	if op.RuntimeClass != nil {
		response["runtimeClass"] = op.RuntimeClass
	}
	if strings.TrimSpace(op.ErrorMessage) != "" {
		code := strings.TrimSpace(op.ErrorCode)
		if code == "" {
			code = ErrCodeInternal
		}
		errorPayload := map[string]any{
			"code":    code,
			"message": op.ErrorMessage,
		}
		if len(op.ErrorDetails) > 0 {
			errorPayload["details"] = op.ErrorDetails
		}
		response["error"] = errorPayload
	}
	return response
}

func (h *EdgeletAPIHandler) HandleAuthTokens(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	items, err := store.GetInstance().ListServiceAccountTokens()
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
		return
	}
	writeSuccess(w, http.StatusOK, map[string]any{"items": items})
}

func (h *EdgeletAPIHandler) HandleAuthTokensRevoke(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, ErrCodeMethodNotAllowed, "method not allowed", nil)
		return
	}
	var req struct {
		JTI string `json:"jti"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "invalid JSON body", nil)
		return
	}
	if strings.TrimSpace(req.JTI) == "" {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "jti is required", nil)
		return
	}
	if err := store.GetInstance().RevokeServiceAccountToken(req.JTI, time.Now().Unix()); err != nil {
		logging.LogError(apiHandlerModuleName, "Failed to revoke token", err)
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
		return
	}
	writeSuccess(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (h *EdgeletAPIHandler) handleCreateExecSession(w http.ResponseWriter, r *http.Request, selector string) {
	var req struct {
		Command []string `json:"command"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, "invalid JSON body", nil)
		return
	}
	if len(req.Command) == 0 {
		req.Command = containerexec.ShellCommandInteractive()
	}
	if len(req.Command) == 1 && strings.TrimSpace(req.Command[0]) == "" {
		req.Command = containerexec.ShellCommandInteractive()
	}
	msUUID, err := h.facade.ResolveMicroserviceID(selector)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
		return
	}
	sessionID := uuid.NewString()
	callback := newLocalExecCallback()
	pm := processmanager.GetInstance()
	if err := pm.CreateLocalExecSession(sessionID, msUUID, req.Command, callback); err != nil {
		if errors.Is(err, processmanager.ErrExecStartTimeout) {
			writeAPIError(w, http.StatusGatewayTimeout, ErrCodeExecStartTimeout, err.Error(), nil)
			return
		}
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
		return
	}
	status := "PENDING"
	if rec, ok := pm.GetSession(sessionID); ok && rec.Started {
		status = "ACTIVE"
	}
	h.execMu.Lock()
	h.execSessions[sessionID] = &localExecSession{
		SessionID:        sessionID,
		MicroserviceUUID: msUUID,
		callback:         callback,
		createdAt:        time.Now().UTC(),
	}
	h.execMu.Unlock()
	writeSuccess(w, http.StatusOK, map[string]any{
		"sessionId": sessionID,
		"status":    status,
	})
}

func (h *EdgeletAPIHandler) handleGetExecSessionStatus(w http.ResponseWriter, selector, sessionID string) {
	uuid, err := h.facade.ResolveMicroserviceID(selector)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
		return
	}
	rec, ok := h.lookupLocalExecSession(uuid, sessionID)
	if !ok {
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "exec session not found", nil)
		return
	}
	pm := processmanager.GetInstance()
	running, err := pm.GetExecSessionStatus(rec.RuntimeExecID)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
		return
	}
	writeSuccess(w, http.StatusOK, map[string]any{
		"sessionId":        sessionID,
		"microserviceUuid": uuid,
		"running":          running,
		"exitCode": func() any {
			code, codeErr := pm.GetExecSessionExitCode(rec.RuntimeExecID)
			if codeErr != nil {
				return nil
			}
			return code
		}(),
	})
}

func (h *EdgeletAPIHandler) handleStopExecSession(w http.ResponseWriter, selector, sessionID string) {
	uuid, err := h.facade.ResolveMicroserviceID(selector)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
		return
	}
	if _, ok := h.lookupLocalExecSession(uuid, sessionID); !ok {
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "exec session not found", nil)
		return
	}
	if err := processmanager.GetInstance().ReleaseExecSession(sessionID, processmanager.ExecOwnerLocal); err != nil {
		writeAPIError(w, http.StatusInternalServerError, ErrCodeInternal, err.Error(), nil)
		return
	}
	h.execMu.Lock()
	delete(h.execSessions, sessionID)
	h.execMu.Unlock()
	writeSuccess(w, http.StatusOK, map[string]any{"status": "ok", "sessionId": sessionID})
}

func (h *EdgeletAPIHandler) lookupLocalExecSession(msUUID, sessionID string) (*processmanager.ExecSessionRecord, bool) {
	rec, ok := processmanager.GetInstance().GetSession(sessionID)
	if !ok || rec.Owner != processmanager.ExecOwnerLocal || rec.MSUUID != msUUID {
		return nil, false
	}
	return rec, true
}

func (h *EdgeletAPIHandler) handleAttachExecSessionWS(w http.ResponseWriter, r *http.Request, selector, sessionID string) {
	uuid, err := h.facade.ResolveMicroserviceID(selector)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, ErrCodeInvalidArgument, err.Error(), nil)
		return
	}
	rec, ok := h.lookupLocalExecSession(uuid, sessionID)
	if !ok {
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "exec session not found", nil)
		return
	}
	h.execMu.RLock()
	session, ok := h.execSessions[sessionID]
	h.execMu.RUnlock()
	if !ok || session.MicroserviceUUID != uuid {
		writeAPIError(w, http.StatusNotFound, ErrCodeNotFound, "exec session not found", nil)
		return
	}
	conn, err := edgeletAPIUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}

	pm := processmanager.GetInstance()
	var wg sync.WaitGroup
	var writeMu sync.Mutex
	wg.Add(3)
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, readErr := session.callback.stdoutR.Read(buf)
			if n > 0 {
				writeMu.Lock()
				writeErr := conn.WriteJSON(map[string]any{"stream": "stdout", "line": string(buf[:n])})
				writeMu.Unlock()
				if writeErr != nil {
					return
				}
			}
			if readErr != nil {
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		buf := make([]byte, 4096)
		for {
			n, readErr := session.callback.stderrR.Read(buf)
			if n > 0 {
				writeMu.Lock()
				writeErr := conn.WriteJSON(map[string]any{"stream": "stderr", "line": string(buf[:n])})
				writeMu.Unlock()
				if writeErr != nil {
					return
				}
			}
			if readErr != nil {
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for {
			_, msg, readErr := conn.ReadMessage()
			if readErr != nil {
				_ = session.callback.stdinW.Close()
				return
			}
			var ctrl struct {
				Type string `json:"type"`
				Cols uint32 `json:"cols"`
				Rows uint32 `json:"rows"`
			}
			if json.Unmarshal(msg, &ctrl) == nil && strings.EqualFold(strings.TrimSpace(ctrl.Type), "resize") {
				_ = pm.ResizeExecSession(rec.RuntimeExecID, ctrl.Cols, ctrl.Rows)
				continue
			}
			_, _ = session.callback.stdinW.Write(msg)
		}
	}()
	select {
	case <-session.callback.done:
	case <-time.After(10 * time.Minute):
	}
	exitCode, _ := pm.GetExecSessionExitCode(rec.RuntimeExecID)
	_ = pm.ReleaseExecSession(sessionID, processmanager.ExecOwnerLocal)
	h.execMu.Lock()
	delete(h.execSessions, sessionID)
	h.execMu.Unlock()
	writeMu.Lock()
	_ = conn.WriteJSON(map[string]any{
		"stream":   "control",
		"line":     "session closed",
		"exitCode": exitCode,
	})
	writeMu.Unlock()
	_ = conn.Close()
	wg.Wait()
}

func (h *EdgeletAPIHandler) handleLogsStreamWS(w http.ResponseWriter, r *http.Request, selector string) {
	conn, err := edgeletAPIUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	uuid, err := h.resolveMicroservice(selector)
	if err != nil {
		_ = conn.WriteJSON(map[string]any{"error": err.Error()})
		return
	}

	tailLines := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("tail")); raw != "" {
		n, parseErr := strconv.Atoi(raw)
		if parseErr != nil || n <= 0 {
			_ = conn.WriteJSON(map[string]any{"error": "invalid tail parameter"})
			return
		}
		tailLines = n
	}
	since := strings.TrimSpace(r.URL.Query().Get("since"))
	until := strings.TrimSpace(r.URL.Query().Get("until"))

	tailHandler := newWSLogTailHandler(conn)
	cfg := &engine.TailConfig{
		Follow: true,
		Lines:  tailLines,
		Since:  since,
		Until:  until,
	}
	if err := h.streamMicroservicLog(uuid, cfg, tailHandler); err != nil {
		_ = conn.WriteJSON(map[string]any{"error": err.Error()})
		return
	}

	// Keep one read loop so websocket close is detected and stream can terminate.
	go func() {
		for {
			if _, _, readErr := conn.ReadMessage(); readErr != nil {
				tailHandler.OnError("ws", readErr)
				return
			}
		}
	}()

	<-tailHandler.done
}

func (h *EdgeletAPIHandler) handleSystemLogsStreamWS(w http.ResponseWriter, r *http.Request) {
	conn, err := edgeletAPIUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	tailLines := 100
	if raw := strings.TrimSpace(r.URL.Query().Get("tailLines")); raw != "" {
		n, parseErr := strconv.Atoi(raw)
		if parseErr != nil || n <= 0 {
			_ = conn.WriteJSON(map[string]any{"error": "invalid tailLines parameter"})
			return
		}
		tailLines = n
	}
	since := strings.TrimSpace(r.URL.Query().Get("since"))
	until := strings.TrimSpace(r.URL.Query().Get("until"))

	cfg := config.GetInstance()
	iofogUUID := strings.TrimSpace(cfg.IOFogUUID)
	if iofogUUID == "" {
		iofogUUID = "local-agent"
	}
	tailHandler := newWSSystemLogTailHandler(conn)
	reader := utils.NewLocalLogReader(
		fmt.Sprintf("edgeletapi-system-log-stream-%d", time.Now().UnixNano()),
		iofogUUID,
		cfg.LogDirectory,
		&utils.TailConfig{
			Follow: true,
			Lines:  tailLines,
			Since:  since,
			Until:  until,
		},
		tailHandler,
	)
	reader.Start()
	defer reader.Stop()

	go func() {
		for {
			if _, _, readErr := conn.ReadMessage(); readErr != nil {
				tailHandler.OnError("ws", readErr)
				return
			}
		}
	}()
	<-tailHandler.done
}

type wsLogTailHandler struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
	done    chan struct{}
	once    sync.Once
}

func newWSLogTailHandler(conn *websocket.Conn) *wsLogTailHandler {
	return &wsLogTailHandler{
		conn: conn,
		done: make(chan struct{}),
	}
}

func (h *wsLogTailHandler) OnLogLine(_, _ string, line []byte, st engine.StreamType) {
	stream := "stdout"
	if st == engine.Stderr {
		stream = "stderr"
	}
	event := map[string]any{
		"ts":     time.Now().UTC().Format(time.RFC3339Nano),
		"stream": stream,
		"line":   string(line),
	}
	h.writeMu.Lock()
	err := h.conn.WriteJSON(event)
	h.writeMu.Unlock()
	if err != nil {
		h.once.Do(func() { close(h.done) })
	}
}

func (h *wsLogTailHandler) OnComplete(_ string) {
	h.once.Do(func() { close(h.done) })
}

func (h *wsLogTailHandler) OnError(_ string, err error) {
	if err != nil && !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
		h.writeMu.Lock()
		_ = h.conn.WriteJSON(map[string]any{"error": err.Error()})
		h.writeMu.Unlock()
	}
	h.once.Do(func() { close(h.done) })
}

type systemLogsCollectHandler struct {
	mu      sync.Mutex
	entries []map[string]any
	err     error
	done    chan struct{}
	once    sync.Once
}

func newSystemLogsCollectHandler() *systemLogsCollectHandler {
	return &systemLogsCollectHandler{
		entries: make([]map[string]any, 0),
		done:    make(chan struct{}),
	}
}

func (h *systemLogsCollectHandler) OnLogLine(_, _ string, line string) {
	h.mu.Lock()
	h.entries = append(h.entries, map[string]any{
		"ts":     time.Now().UTC().Format(time.RFC3339Nano),
		"stream": "stdout",
		"line":   line,
	})
	h.mu.Unlock()
}

func (h *systemLogsCollectHandler) OnComplete(_ string) {
	h.once.Do(func() { close(h.done) })
}

func (h *systemLogsCollectHandler) OnError(_ string, err error) {
	h.mu.Lock()
	h.err = err
	h.mu.Unlock()
	h.once.Do(func() { close(h.done) })
}

type wsSystemLogTailHandler struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
	done    chan struct{}
	once    sync.Once
}

func newWSSystemLogTailHandler(conn *websocket.Conn) *wsSystemLogTailHandler {
	return &wsSystemLogTailHandler{
		conn: conn,
		done: make(chan struct{}),
	}
}

func (h *wsSystemLogTailHandler) OnLogLine(_, _ string, line string) {
	event := map[string]any{
		"ts":     time.Now().UTC().Format(time.RFC3339Nano),
		"stream": "stdout",
		"line":   line,
	}
	h.writeMu.Lock()
	err := h.conn.WriteJSON(event)
	h.writeMu.Unlock()
	if err != nil {
		h.once.Do(func() { close(h.done) })
	}
}

func (h *wsSystemLogTailHandler) OnComplete(_ string) {
	h.once.Do(func() { close(h.done) })
}

func (h *wsSystemLogTailHandler) OnError(_ string, err error) {
	if err != nil && !websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
		h.writeMu.Lock()
		_ = h.conn.WriteJSON(map[string]any{"error": err.Error()})
		h.writeMu.Unlock()
	}
	h.once.Do(func() { close(h.done) })
}

func configKeyToShortCode(key string) (string, bool) {
	normalized := strings.TrimSpace(key)
	normalized = strings.TrimPrefix(normalized, "--")
	normalized = strings.TrimPrefix(normalized, "-")

	switch normalized {
	case "a", "controllerUrl":
		return "a", true
	case "ac", "controllerCert":
		return "ac", true
	case "ce", "containerEngine":
		return "ce", true
	case "cu", "containerEngineUrl":
		return "cu", true
	case "n", "networkInterface":
		return "n", true
	case "d", "diskLimitGiB":
		return "d", true
	case "dl", "diskDirectory":
		return "dl", true
	case "m", "memoryLimitMiB":
		return "m", true
	case "p", "cpuLimitPercent":
		return "p", true
	case "l", "logLimitGiB":
		return "l", true
	case "ld", "logDirectory":
		return "ld", true
	case "lc", "logFileCount":
		return "lc", true
	case "ll", "logLevel":
		return "ll", true
	case "sf", "statusFrequencySeconds":
		return "sf", true
	case "cf", "changeFrequencySeconds":
		return "cf", true
	case "wd", "watchdogEnabled":
		return "wd", true
	case "egf", "edgeGuardFrequency":
		return "egf", true
	case "gps", "gpsMode":
		return "gps", true
	case "gpsc", "gpsCoordinates":
		return "gpsc", true
	case "gpsd", "gpsDevice":
		return "gpsd", true
	case "gpsf", "gpsScanFrequency":
		return "gpsf", true
	case "ft", "arch":
		return "ft", true
	case "sec", "secureMode":
		return "sec", true
	case "pf", "pruningFrequency":
		return "pf", true
	case "dt", "availableDiskThreshold":
		return "dt", true
	case "uf", "upgradeScanFrequency":
		return "uf", true
	case "dev", "devMode":
		return "dev", true
	case "tz", "timezone":
		return "tz", true
	default:
		return "", false
	}
}

func gpsValueToString(value any) (string, error) {
	switch v := value.(type) {
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return "", errors.New("empty")
		}
		return s, nil
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	case int:
		return strconv.Itoa(v), nil
	default:
		return "", errors.New("unsupported")
	}
}

func normalizeGPSLatLon(lat, lon string) (string, error) {
	latValue, err := strconv.ParseFloat(strings.TrimSpace(lat), 64)
	if err != nil {
		return "", errors.New("lat must be a valid number")
	}
	lonValue, err := strconv.ParseFloat(strings.TrimSpace(lon), 64)
	if err != nil {
		return "", errors.New("lon must be a valid number")
	}
	if latValue < -90 || latValue > 90 {
		return "", errors.New("lat must be between -90 and 90")
	}
	if lonValue < -180 || lonValue > 180 {
		return "", errors.New("lon must be between -180 and 180")
	}
	return fmt.Sprintf("%.5f,%.5f", latValue, lonValue), nil
}

func claimMicroserviceUUID(claims map[string]any) (string, bool) {
	iofog, ok := claims["edgelet.iofog.org"].(map[string]any)
	if !ok {
		return "", false
	}
	ms, ok := iofog["microservice"].(map[string]any)
	if !ok {
		return "", false
	}
	uuid, ok := ms["uuid"].(string)
	if !ok {
		uuid = ""
	}
	uuid = strings.TrimSpace(uuid)
	return uuid, uuid != ""
}
