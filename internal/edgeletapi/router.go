package edgeletapi

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/eclipse-iofog/edgelet/internal/edgeletapi/handlers"
	"github.com/eclipse-iofog/edgelet/internal/edgeletapi/websocket"
	"github.com/eclipse-iofog/edgelet/internal/utils/logging"
)

const (
	routerModuleName = "Edgelet API Router"
)

// Router handles HTTP routing
type Router struct {
	mux                *http.ServeMux
	statusHandler      *handlers.StatusHandler
	infoHandler        *handlers.InfoHandler
	healthLiveHandler  http.HandlerFunc
	healthReadyHandler http.HandlerFunc
	metricsHandler     http.HandlerFunc
	versionHandler     *handlers.VersionHandler
	authHandler        *handlers.AuthHandler
	apiHandler         *handlers.EdgeletAPIHandler
	controlWSHandler   *websocket.ControlHandler
}

// NewRouter creates a new router
func NewRouter() *Router {
	r := &Router{
		mux:                http.NewServeMux(),
		statusHandler:      &handlers.StatusHandler{},
		infoHandler:        &handlers.InfoHandler{},
		healthLiveHandler:  handlers.HealthLiveHandler,
		healthReadyHandler: handlers.HealthReadyHandler,
		metricsHandler:     handlers.MetricsHandler,
		versionHandler:     &handlers.VersionHandler{},
		authHandler:        handlers.NewAuthHandler(),
		apiHandler:         handlers.NewEdgeletAPIHandler(),
		controlWSHandler:   websocket.NewControlHandler(),
	}
	r.setupRoutes()
	return r
}

// ServeHTTP implements http.Handler
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	fields := map[string]any{
		"event":     "edgeletapi.debug",
		"method":    req.Method,
		"path":      req.URL.Path,
		"requestId": strings.TrimSpace(req.Header.Get(requestIDHeader)),
	}
	if authHeader := strings.TrimSpace(req.Header.Get("Authorization")); strings.HasPrefix(authHeader, "Bearer ") {
		for key, value := range safeTokenMeta(strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))) {
			fields[key] = value
		}
	}
	payload, _ := json.Marshal(fields)
	logging.LogDebug(routerModuleName, string(payload))
	r.mux.ServeHTTP(w, req)
}

// withActionLogging wraps a handler with "Start/Finished Processing..." info logs
func withActionLogging(actionName string, handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		moduleName := "Edgelet API : LocalApiServerHandler"
		logging.LogInfo(moduleName, "Start Processing "+actionName+" request")
		handler(w, req)
		logging.LogInfo(moduleName, "Finished Processing "+actionName+" request")
	}
}

// setupRoutes registers all routes
func (r *Router) setupRoutes() {
	// Health and metrics (no auth — used by orchestrators and monitoring)
	r.mux.HandleFunc("/health/live", r.healthLiveHandler)
	r.mux.HandleFunc("/health/ready", r.healthReadyHandler)
	r.mux.HandleFunc("/metrics", r.metricsHandler)

	// EdgeletAPI v1 routes (prefix /v1)
	r.mux.HandleFunc("/v1/system/status", chainMiddleware(withRoute("/v1/system/status", r.statusHandler.HandleStatus), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/system/info", chainMiddleware(withRoute("/v1/system/info", r.infoHandler.HandleInfo), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/system/version", chainMiddleware(withRoute("/v1/system/version", r.versionHandler.HandleVersion), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/system/provision", chainMiddleware(withRoute("/v1/system/provision", r.apiHandler.HandleSystemProvision), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/system/reload", chainMiddleware(withRoute("/v1/system/reload", r.apiHandler.HandleSystemReload), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/runtime/drain", chainMiddleware(withRoute("/v1/runtime/drain", r.apiHandler.HandleRuntimeDrain), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/system/prune", chainMiddleware(withRoute("/v1/system/prune", r.apiHandler.HandleSystemPrune), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/system/logs", chainMiddleware(withRoute("/v1/system/logs", r.apiHandler.HandleSystemLogs), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/system/logs:stream", chainMiddleware(withRoute("/v1/system/logs:stream", r.apiHandler.HandleSystemLogs), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/system/gps", chainMiddleware(withRoute("/v1/system/gps", r.apiHandler.HandleSystemGPS), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/system/config", chainMiddleware(withRoute("/v1/system/config", r.apiHandler.HandleConfig), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/system/controller/cert", chainMiddleware(withRoute("/v1/system/controller/cert", r.apiHandler.HandleSystemControllerCert), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/system/config/switch", chainMiddleware(withRoute("/v1/system/config/switch", r.apiHandler.HandleSystemConfigSwitch), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/system/controlplane", chainMiddleware(withRoute("/v1/system/controlplane", r.apiHandler.HandleSystemControlPlane), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/system/controlplane/manifest", chainMiddleware(withRoute("/v1/system/controlplane/manifest", r.apiHandler.HandleSystemControlPlane), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/system/controlplane/restart", chainMiddleware(withRoute("/v1/system/controlplane/restart", r.apiHandler.HandleSystemControlPlaneRestart), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/system/controller", chainMiddleware(withRoute("/v1/system/controller", r.apiHandler.HandleSystemControllerStatus), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/images", chainMiddleware(withRoute("/v1/images", r.apiHandler.HandleImages), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/images:pull", chainMiddleware(withRoute("/v1/images:pull", r.apiHandler.HandleImagePull), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/images:pull/", chainMiddleware(withRoute("/v1/images:pull/", r.apiHandler.HandleImagePullStatus), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/images:load", chainMiddleware(withRoute("/v1/images:load", r.apiHandler.HandleImageLoad), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/images:load/", chainMiddleware(withRoute("/v1/images:load/", r.apiHandler.HandleImageLoadStatus), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/images:prune", chainMiddleware(withRoute("/v1/images:prune", r.apiHandler.HandleImagePrune), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/images:remove", chainMiddleware(withRoute("/v1/images:remove", r.apiHandler.HandleImageRemove), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/models", chainMiddleware(withRoute("/v1/models", r.apiHandler.HandleModels), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/models/", chainMiddleware(withRoute("/v1/models/", r.apiHandler.HandleModels), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/models:pull", chainMiddleware(withRoute("/v1/models:pull", r.apiHandler.HandleModelPull), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/models:pull/", chainMiddleware(withRoute("/v1/models:pull/", r.apiHandler.HandleModelPullStatus), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/models:prune", chainMiddleware(withRoute("/v1/models:prune", r.apiHandler.HandleModelPrune), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/volumes:prune", chainMiddleware(withRoute("/v1/volumes:prune", r.apiHandler.HandleVolumePrune), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/volumes/shared/", chainMiddleware(withRoute("/v1/volumes/shared/", r.apiHandler.HandleVolumeShared), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/volumes/shared", chainMiddleware(withRoute("/v1/volumes/shared", r.apiHandler.HandleVolumeShared), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/volumes/", chainMiddleware(withRoute("/v1/volumes/", r.apiHandler.HandleVolumes), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/volumes", chainMiddleware(withRoute("/v1/volumes", r.apiHandler.HandleVolumes), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))

	r.mux.HandleFunc("/v1/microservices/config", chainMiddleware(withRoute("/v1/microservices/config", r.apiHandler.HandleMicroserviceConfigSelf), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/microservices/control", r.controlWSHandler.Handle)

	r.mux.HandleFunc("/v1/ms", chainMiddleware(withRoute("/v1/ms", r.apiHandler.HandleMicroservices), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/ms/", chainMiddleware(withRoute("/v1/ms/", r.apiHandler.HandleMicroservices), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/deploy/microservices:apply", chainMiddleware(withRoute("/v1/deploy/microservices:apply", r.apiHandler.HandleDeployMicroservicesApply), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/deploy/microservices:apply/", chainMiddleware(withRoute("/v1/deploy/microservices:apply/", r.apiHandler.HandleDeployMicroservicesApplyStatus), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/deploy/microservices:validate", chainMiddleware(withRoute("/v1/deploy/microservices:validate", r.apiHandler.HandleDeployMicroservicesValidate), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/deploy/microservices", chainMiddleware(withRoute("/v1/deploy/microservices", r.apiHandler.HandleDeployMicroservices), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/deploy/microservices/", chainMiddleware(withRoute("/v1/deploy/microservices/", r.apiHandler.HandleDeployMicroservices), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/deploy/registries:apply", chainMiddleware(withRoute("/v1/deploy/registries:apply", r.apiHandler.HandleDeployRegistriesApply), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/deploy/registries:validate", chainMiddleware(withRoute("/v1/deploy/registries:validate", r.apiHandler.HandleDeployRegistriesValidate), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/deploy/models:apply", chainMiddleware(withRoute("/v1/deploy/models:apply", r.apiHandler.HandleDeployModelsApply), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/deploy/models:validate", chainMiddleware(withRoute("/v1/deploy/models:validate", r.apiHandler.HandleDeployModelsValidate), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/deploy/registries", chainMiddleware(withRoute("/v1/deploy/registries", r.apiHandler.HandleDeployRegistries), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/deploy/registries/", chainMiddleware(withRoute("/v1/deploy/registries/", r.apiHandler.HandleDeployRegistries), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/deploy/runtimeclasses:apply", chainMiddleware(withRoute("/v1/deploy/runtimeclasses:apply", r.apiHandler.HandleDeployRuntimeClassesApply), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/deploy/runtimeclasses:apply/", chainMiddleware(withRoute("/v1/deploy/runtimeclasses:apply/", r.apiHandler.HandleDeployRuntimeClassesApplyStatus), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/deploy/runtimeclasses:delete/", chainMiddleware(withRoute("/v1/deploy/runtimeclasses:delete/", r.apiHandler.HandleDeployRuntimeClassesDeleteStatus), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/deploy/runtimeclasses:validate", chainMiddleware(withRoute("/v1/deploy/runtimeclasses:validate", r.apiHandler.HandleDeployRuntimeClassesValidate), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/deploy/runtimeclasses", chainMiddleware(withRoute("/v1/deploy/runtimeclasses", r.apiHandler.HandleDeployRuntimeClasses), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/deploy/runtimeclasses/", chainMiddleware(withRoute("/v1/deploy/runtimeclasses/", r.apiHandler.HandleDeployRuntimeClasses), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/deploy/controlplane:apply", chainMiddleware(withRoute("/v1/deploy/controlplane:apply", r.apiHandler.HandleDeployControlPlaneApply), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/deploy/controlplane:apply/", chainMiddleware(withRoute("/v1/deploy/controlplane:apply/", r.apiHandler.HandleDeployControlPlaneApplyStatus), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/deploy/controlplane:validate", chainMiddleware(withRoute("/v1/deploy/controlplane:validate", r.apiHandler.HandleDeployControlPlaneValidate), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/auth/whoami", chainMiddleware(withRoute("/v1/auth/whoami", r.authHandler.HandleWhoAmI), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/auth/tokens", chainMiddleware(withRoute("/v1/auth/tokens", r.apiHandler.HandleAuthTokens), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
	r.mux.HandleFunc("/v1/auth/tokens/revoke", chainMiddleware(withRoute("/v1/auth/tokens/revoke", r.apiHandler.HandleAuthTokensRevoke), authMiddlewareV1, accessLoggingMiddleware, requestIDMiddleware))
}
