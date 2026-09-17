package edgeletapi

import (
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

type rbacPermission struct {
	APIGroups    []string
	Resource     string
	Verb         string
	ResourceName string
}

var localAPIAuthorizationGroups = []string{
	"edgelet.iofog.org/v1",
}

func mapRequestToPermission(r *http.Request) (rbacPermission, bool) {
	path := r.URL.Path
	method := strings.ToUpper(r.Method)
	verb := toRBACVerb(method)
	switch {
	case path == "/v1/system/status":
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "system/status", Verb: verb}, true
	case path == "/v1/system/info":
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "system/info", Verb: verb}, true
	case path == "/v1/system/version":
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "system/version", Verb: verb}, true
	case path == "/v1/system/provision":
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "system/provision", Verb: verb}, true
	case path == "/v1/system/reload":
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "system/reload", Verb: verb}, true
	case path == "/v1/runtime/drain":
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "runtime/drain", Verb: verb}, true
	case path == "/v1/system/prune":
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "system/prune", Verb: verb}, true
	case path == "/v1/system/logs":
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "system/logs", Verb: verb}, true
	case path == "/v1/system/logs:stream":
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "system/logs/stream", Verb: verb}, true
	case path == "/v1/system/config":
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "system/config", Verb: verb}, true
	case path == "/v1/system/controller/cert":
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "system/controller/cert", Verb: "update"}, true
	case path == "/v1/system/config/switch":
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "system/config/switch", Verb: "update"}, true
	case path == "/v1/system/gps":
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "system/gps", Verb: verb}, true
	case path == "/v1/system/controlplane" || path == "/v1/system/controlplane/manifest":
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "system/controlplane", Verb: verb}, true
	case path == "/v1/system/controlplane/restart":
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "system/controlplane", Verb: "create"}, true
	case path == "/v1/system/controller":
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "system/controller", Verb: verb}, true
	case path == "/v1/images":
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "images", Verb: verb}, true
	case path == "/v1/images:pull":
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "images/pull", Verb: verb}, true
	case strings.HasPrefix(path, "/v1/images:pull/"):
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "images/pull/status", Verb: verb}, true
	case path == "/v1/images:load":
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "images/load", Verb: verb}, true
	case strings.HasPrefix(path, "/v1/images:load/"):
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "images/load/status", Verb: verb}, true
	case path == "/v1/images:prune":
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "images/prune", Verb: verb}, true
	case path == "/v1/images:remove":
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "images/remove", Verb: verb}, true
	case path == "/v1/models":
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "models", Verb: verb}, true
	case path == "/v1/models:pull":
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "models/pull", Verb: verb}, true
	case strings.HasPrefix(path, "/v1/models:pull/"):
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "models/pull/status", Verb: verb}, true
	case path == "/v1/models:prune":
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "models/prune", Verb: verb}, true
	case strings.HasPrefix(path, "/v1/models/"):
		name := strings.TrimSpace(strings.TrimPrefix(path, "/v1/models/"))
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "models", Verb: verb, ResourceName: name}, true
	case path == "/v1/microservices/config":
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "microservices/config/self", Verb: verb}, true
	case path == "/v1/microservices/control":
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "microservices/control/self", Verb: verb}, true
	case path == "/v1/ms":
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "microservices", Verb: verb}, true
	case strings.HasPrefix(path, "/v1/ms/"):
		if id, ok := microserviceResourceName(path); ok {
			return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "microservices", Verb: verb, ResourceName: id}, true
		}
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "microservices", Verb: verb}, true
	case strings.HasPrefix(path, "/v1/deploy/microservices/"):
		id := strings.TrimSpace(strings.TrimPrefix(path, "/v1/deploy/microservices/"))
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "deploy/microservices", Verb: verb, ResourceName: id}, true
	case strings.HasPrefix(path, "/v1/deploy/microservices:apply/"):
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "deploy/microservices/apply/status", Verb: verb}, true
	case strings.HasPrefix(path, "/v1/deploy/registries/"):
		id := strings.TrimSpace(strings.TrimPrefix(path, "/v1/deploy/registries/"))
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "deploy/registries", Verb: verb, ResourceName: id}, true
	case strings.HasPrefix(path, "/v1/deploy/runtimeclasses/"):
		name := strings.TrimSpace(strings.TrimPrefix(path, "/v1/deploy/runtimeclasses/"))
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "deploy/runtimeclasses", Verb: verb, ResourceName: name}, true
	case strings.HasPrefix(path, "/v1/deploy/microservices"):
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "deploy/microservices", Verb: verb}, true
	case strings.HasPrefix(path, "/v1/deploy/registries"):
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "deploy/registries", Verb: verb}, true
	case strings.HasPrefix(path, "/v1/deploy/models"):
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "deploy/models", Verb: verb}, true
	case strings.HasPrefix(path, "/v1/deploy/runtimeclasses"):
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "deploy/runtimeclasses", Verb: verb}, true
	case strings.HasPrefix(path, "/v1/deploy/controlplane:apply/"):
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "deploy/controlplane/apply/status", Verb: verb}, true
	case strings.HasPrefix(path, "/v1/deploy/controlplane"):
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "deploy/controlplane", Verb: verb}, true
	case path == "/v1/auth/whoami":
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "auth/whoami", Verb: verb}, true
	case path == "/v1/auth/tokens" || path == "/v1/auth/tokens/revoke":
		return rbacPermission{APIGroups: localAPIAuthorizationGroups, Resource: "auth/tokens", Verb: verb}, true
	default:
		return rbacPermission{}, false
	}
}

func microserviceResourceName(path string) (string, bool) {
	rest := strings.TrimPrefix(path, "/v1/ms/")
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) == 0 {
		return "", false
	}
	id := strings.TrimSpace(parts[0])
	if id == "" {
		return "", false
	}
	return id, true
}

func isAuthorized(claims jwt.MapClaims, p rbacPermission) bool {
	tokenUse, ok := claims["tokenUse"].(string)
	if !ok {
		tokenUse = ""
	}
	sub, ok := claims["sub"].(string)
	if !ok {
		sub = ""
	}
	if strings.TrimSpace(tokenUse) == "edgeletapi" && (strings.HasPrefix(sub, "system:edgeletadmin:") || sub == "system:edgeletadmin:bootstrap") {
		return true
	}

	iofogRaw, ok := claims["edgelet.iofog.org"].(map[string]any)
	if !ok {
		return false
	}
	rbacRaw, ok := iofogRaw["rbac"].(map[string]any)
	if !ok {
		return false
	}
	rulesRaw, ok := rbacRaw["rulesByGroup"].(map[string]any)
	if !ok {
		return false
	}
	return rulesMatchAnyGroup(rulesRaw, p.APIGroups, p.Resource, p.Verb, p.ResourceName)
}

func rulesMatchAnyGroup(groups map[string]any, apiGroups []string, resource, verb, resourceName string) bool {
	for _, group := range apiGroups {
		if rulesMatch(groups, group, resource, verb, resourceName) {
			return true
		}
	}
	return false
}

func rulesMatch(groups map[string]any, group, resource, verb, resourceName string) bool {
	rulesRaw, exists := groups[group]
	if !exists {
		rulesRaw, exists = groups["*"]
		if !exists {
			return false
		}
	}
	rulesSlice, ok := rulesRaw.([]any)
	if !ok {
		return false
	}
	for _, ruleRaw := range rulesSlice {
		rule, ok := ruleRaw.(map[string]any)
		if !ok {
			continue
		}
		if !ruleListMatch(rule["resources"], resource) {
			continue
		}
		if !ruleVerbMatch(rule["verbs"], verb) {
			continue
		}
		if resourceName != "" {
			if rawNames, exists := rule["resourceNames"]; exists && !ruleListMatch(rawNames, resourceName) {
				continue
			}
		}
		return true
	}
	return false
}

func ruleVerbMatch(raw any, expected string) bool {
	values, ok := raw.([]any)
	if !ok {
		return false
	}
	expected = canonicalVerb(expected)
	for _, value := range values {
		s, ok := value.(string)
		if !ok {
			continue
		}
		if s == "*" || canonicalVerb(s) == expected {
			return true
		}
	}
	return false
}

func ruleListMatch(raw any, expected string) bool {
	if expected == "" {
		return true
	}
	values, ok := raw.([]any)
	if !ok {
		return false
	}
	for _, value := range values {
		s, ok := value.(string)
		if !ok {
			continue
		}
		if s == "*" || strings.EqualFold(s, expected) {
			return true
		}
	}
	return false
}

func toRBACVerb(method string) string {
	switch strings.ToUpper(method) {
	case http.MethodGet:
		return "get"
	case http.MethodPost:
		return "create"
	case http.MethodPatch, http.MethodPut:
		return "update"
	case http.MethodDelete:
		return "delete"
	default:
		return canonicalVerb(strings.ToLower(method))
	}
}

func canonicalVerb(verb string) string {
	switch strings.ToLower(strings.TrimSpace(verb)) {
	case "patch", "put":
		return "update"
	case "post":
		return "create"
	case "remove":
		return "delete"
	default:
		return strings.ToLower(strings.TrimSpace(verb))
	}
}
