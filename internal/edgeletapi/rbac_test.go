package edgeletapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eclipse-iofog/edgelet/internal/auth"
	"github.com/golang-jwt/jwt/v5"
)

func TestIsAuthorized_ReadsCanonicalRulesByGroup(t *testing.T) {
	claims := jwt.MapClaims{
		"tokenUse": "serviceaccount",
		"sub":      "system:serviceaccount:app:svc",
		"edgelet.iofog.org": map[string]any{
			"rbac": map[string]any{
				"version": "v1",
				"rulesByGroup": map[string]any{
					"edgelet.iofog.org/v1": []any{
						map[string]any{
							"resources": []any{"system/config"},
							"verbs":     []any{"get"},
						},
					},
				},
			},
		},
	}
	perm := rbacPermission{
		APIGroups: []string{"edgelet.iofog.org/v1", "edgelet.iofog.org/v1"},
		Resource:  "system/config",
		Verb:      "get",
	}
	if !isAuthorized(claims, perm) {
		t.Fatal("expected canonical rulesByGroup authorization to pass")
	}
}

func TestIsAuthorized_AcceptsVerbAliases(t *testing.T) {
	claims := jwt.MapClaims{
		"tokenUse": "serviceaccount",
		"sub":      "system:serviceaccount:app:svc",
		"edgelet.iofog.org": map[string]any{
			"rbac": map[string]any{
				"version": "v1",
				"rulesByGroup": map[string]any{
					"edgelet.iofog.org/v1": []any{
						map[string]any{
							"resources": []any{"system/config"},
							"verbs":     []any{"patch"},
						},
					},
				},
			},
		},
	}
	perm := rbacPermission{
		APIGroups: []string{"edgelet.iofog.org/v1"},
		Resource:  "system/config",
		Verb:      "update",
	}
	if !isAuthorized(claims, perm) {
		t.Fatal("expected patch->update alias to authorize")
	}
}

func TestIsAuthorized_ModelRoutesDenyAndAllow(t *testing.T) {
	denied := jwt.MapClaims{
		"tokenUse": "serviceaccount",
		"sub":      "system:serviceaccount:app:svc",
		"edgelet.iofog.org": map[string]any{
			"rbac": map[string]any{
				"rulesByGroup": map[string]any{
					"edgelet.iofog.org/v1": []any{
						map[string]any{
							"resources": []any{"images"},
							"verbs":     []any{"get"},
						},
					},
				},
			},
		},
	}
	perm := rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "models", Verb: "get"}
	if isAuthorized(denied, perm) {
		t.Fatal("expected models get to be denied without a models rule")
	}

	allowed := jwt.MapClaims{
		"tokenUse": "serviceaccount",
		"sub":      "system:serviceaccount:app:svc",
		"edgelet.iofog.org": map[string]any{
			"rbac": map[string]any{
				"rulesByGroup": map[string]any{
					"edgelet.iofog.org/v1": []any{
						map[string]any{
							"resources": []any{"models", "models/pull", "models/prune", "deploy/models"},
							"verbs":     []any{"get", "create", "delete"},
						},
					},
				},
			},
		},
	}
	for _, resource := range []string{"models", "models/pull", "models/prune", "deploy/models"} {
		p := rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: resource, Verb: "get"}
		if resource != "models" {
			p.Verb = "create"
		}
		if !isAuthorized(allowed, p) {
			t.Fatalf("expected %s to be allowed", resource)
		}
	}
	if isAuthorized(jwt.MapClaims{"tokenUse": "serviceaccount", "sub": "system:serviceaccount:app:svc"}, perm) {
		t.Fatal("expected missing token to stay unauthorized")
	}
}

func TestIsAuthorized_VolumeRoutesDenyAndAllow(t *testing.T) {
	denied := jwt.MapClaims{
		"tokenUse": "serviceaccount",
		"sub":      "system:serviceaccount:app:svc",
		"edgelet.iofog.org": map[string]any{
			"rbac": map[string]any{
				"rulesByGroup": map[string]any{
					"edgelet.iofog.org/v1": []any{
						map[string]any{
							"resources": []any{"models"},
							"verbs":     []any{"get"},
						},
					},
				},
			},
		},
	}
	destroy := rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "volumes", Verb: "delete"}
	if isAuthorized(denied, destroy) {
		t.Fatal("expected volumes delete to be denied without a volumes rule")
	}
	prune := rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "volumes/prune", Verb: "create"}
	if isAuthorized(denied, prune) {
		t.Fatal("expected volumes prune to be denied without a volumes/prune rule")
	}

	allowed := jwt.MapClaims{
		"tokenUse": "serviceaccount",
		"sub":      "system:serviceaccount:app:svc",
		"edgelet.iofog.org": map[string]any{
			"rbac": map[string]any{
				"rulesByGroup": map[string]any{
					"edgelet.iofog.org/v1": []any{
						map[string]any{
							"resources": []any{"volumes", "volumes/prune"},
							"verbs":     []any{"get", "delete", "create"},
						},
					},
				},
			},
		},
	}
	if !isAuthorized(allowed, destroy) {
		t.Fatal("expected volumes delete to be allowed")
	}
	if !isAuthorized(allowed, prune) {
		t.Fatal("expected volumes prune to be allowed")
	}
}

func TestMapRequestToPermission_ModelRoutesWithoutTokenStillMap(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	perm, ok := mapRequestToPermission(req)
	if !ok || perm.Resource != "models" {
		t.Fatalf("expected models mapping, got ok=%v perm=%+v", ok, perm)
	}
}

func TestAuthMiddlewareV1_VolumeDestroyDeniedWithoutVolumesRule(t *testing.T) {
	origValidate := validateLocalJWTFn
	defer func() { validateLocalJWTFn = origValidate }()
	validateLocalJWTFn = func(string) (*auth.LocalJWTValidationResult, error) {
		return &auth.LocalJWTValidationResult{Claims: jwt.MapClaims{
			"tokenUse": "serviceaccount",
			"sub":      "system:serviceaccount:app:ms",
			"edgelet.iofog.org": map[string]any{
				"rbac": map[string]any{
					"rulesByGroup": map[string]any{
						"edgelet.iofog.org/v1": []any{
							map[string]any{
								"resources": []any{"models"},
								"verbs":     []any{"get"},
							},
						},
					},
				},
			},
		}}, nil
	}
	handler := authMiddlewareV1(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	cases := []struct {
		method string
		path   string
	}{
		{method: http.MethodDelete, path: "/v1/volumes/ms-uuid"},
		{method: http.MethodDelete, path: "/v1/volumes/shared/shared-config"},
		{method: http.MethodPost, path: "/v1/volumes:prune"},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.Header.Set("Authorization", "Bearer token")
		rr := httptest.NewRecorder()
		handler(rr, req)
		if rr.Code != http.StatusForbidden {
			t.Fatalf("%s %s: expected 403, got %d body=%s", tc.method, tc.path, rr.Code, rr.Body.String())
		}
	}
}

func TestAuthMiddlewareV1_ModelsDeniedWithoutToken(t *testing.T) {
	handler := authMiddlewareV1(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	rr := httptest.NewRecorder()
	handler(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without bearer token, got %d body=%s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "UNAUTHORIZED") {
		t.Fatalf("expected UNAUTHORIZED body, got %s", rr.Body.String())
	}
}

func TestMapRequestToPermission_ControlPlaneRoutes(t *testing.T) {
	tests := []struct {
		method   string
		path     string
		resource string
		verb     string
	}{
		{method: http.MethodGet, path: "/v1/system/controlplane", resource: "system/controlplane", verb: "get"},
		{method: http.MethodGet, path: "/v1/system/controlplane/manifest", resource: "system/controlplane", verb: "get"},
		{method: http.MethodDelete, path: "/v1/system/controlplane", resource: "system/controlplane", verb: "delete"},
		{method: http.MethodPost, path: "/v1/system/controlplane/restart", resource: "system/controlplane", verb: "create"},
		{method: http.MethodGet, path: "/v1/system/controller", resource: "system/controller", verb: "get"},
		{method: http.MethodPost, path: "/v1/deploy/controlplane:apply", resource: "deploy/controlplane", verb: "create"},
		{method: http.MethodGet, path: "/v1/deploy/controlplane:apply/op-123", resource: "deploy/controlplane/apply/status", verb: "get"},
		{method: http.MethodPost, path: "/v1/deploy/controlplane:validate", resource: "deploy/controlplane", verb: "create"},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(tt.method, tt.path, nil)
		perm, ok := mapRequestToPermission(req)
		if !ok {
			t.Fatalf("expected route %s to map", tt.path)
		}
		if perm.Resource != tt.resource || perm.Verb != tt.verb {
			t.Fatalf("unexpected mapping for %s: resource=%s verb=%s", tt.path, perm.Resource, perm.Verb)
		}
	}
}

func TestMapRequestToPermission_SystemSwitchAndCert(t *testing.T) {
	tests := []struct {
		method   string
		path     string
		resource string
		verb     string
	}{
		{method: http.MethodPost, path: "/v1/system/controller/cert", resource: "system/controller/cert", verb: "update"},
		{method: http.MethodPost, path: "/v1/system/config/switch", resource: "system/config/switch", verb: "update"},
		{method: http.MethodGet, path: "/v1/system/logs", resource: "system/logs", verb: "get"},
		{method: http.MethodGet, path: "/v1/system/logs:stream", resource: "system/logs/stream", verb: "get"},
		{method: http.MethodGet, path: "/v1/images", resource: "images", verb: "get"},
		{method: http.MethodPost, path: "/v1/images:pull", resource: "images/pull", verb: "create"},
		{method: http.MethodGet, path: "/v1/images:pull/abc", resource: "images/pull/status", verb: "get"},
		{method: http.MethodPost, path: "/v1/images:load", resource: "images/load", verb: "create"},
		{method: http.MethodGet, path: "/v1/images:load/abc", resource: "images/load/status", verb: "get"},
		{method: http.MethodPost, path: "/v1/images:prune", resource: "images/prune", verb: "create"},
		{method: http.MethodPost, path: "/v1/images:remove", resource: "images/remove", verb: "create"},
		{method: http.MethodGet, path: "/v1/models", resource: "models", verb: "get"},
		{method: http.MethodGet, path: "/v1/models/llama-2-7b-q2k", resource: "models", verb: "get"},
		{method: http.MethodDelete, path: "/v1/models/llama-2-7b-q2k", resource: "models", verb: "delete"},
		{method: http.MethodPost, path: "/v1/models:pull", resource: "models/pull", verb: "create"},
		{method: http.MethodGet, path: "/v1/models:pull/op-1", resource: "models/pull/status", verb: "get"},
		{method: http.MethodPost, path: "/v1/models:prune", resource: "models/prune", verb: "create"},
		{method: http.MethodGet, path: "/v1/volumes", resource: "volumes", verb: "get"},
		{method: http.MethodDelete, path: "/v1/volumes/ms-uuid", resource: "volumes", verb: "delete"},
		{method: http.MethodDelete, path: "/v1/volumes/shared/shared-config", resource: "volumes", verb: "delete"},
		{method: http.MethodPost, path: "/v1/volumes:prune", resource: "volumes/prune", verb: "create"},
		{method: http.MethodPost, path: "/v1/deploy/models:apply", resource: "deploy/models", verb: "create"},
		{method: http.MethodPost, path: "/v1/deploy/models:validate", resource: "deploy/models", verb: "create"},
		{method: http.MethodGet, path: "/v1/deploy/microservices:apply/op-123", resource: "deploy/microservices/apply/status", verb: "get"},
		{method: http.MethodPost, path: "/v1/deploy/runtimeclasses:apply", resource: "deploy/runtimeclasses", verb: "create"},
		{method: http.MethodPost, path: "/v1/deploy/runtimeclasses:validate", resource: "deploy/runtimeclasses", verb: "create"},
		{method: http.MethodGet, path: "/v1/deploy/runtimeclasses", resource: "deploy/runtimeclasses", verb: "get"},
		{method: http.MethodGet, path: "/v1/deploy/runtimeclasses/edgelet-wasmtime", resource: "deploy/runtimeclasses", verb: "get"},
		{method: http.MethodDelete, path: "/v1/deploy/runtimeclasses/edgelet-wasmtime", resource: "deploy/runtimeclasses", verb: "delete"},
	}
	for _, tt := range tests {
		req := httptest.NewRequest(tt.method, tt.path, nil)
		perm, ok := mapRequestToPermission(req)
		if !ok {
			t.Fatalf("expected route %s to map", tt.path)
		}
		if perm.Resource != tt.resource || perm.Verb != tt.verb {
			t.Fatalf("unexpected mapping for %s: resource=%s verb=%s", tt.path, perm.Resource, perm.Verb)
		}
	}
}
