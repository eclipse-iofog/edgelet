package fieldagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// ControllerAPIError represents a structured error response from the controller.
type ControllerAPIError struct {
	StatusCode int
	ErrorKind  string // JSON "error" field (e.g. "Unauthorized")
	Code       string
	Message    string
	Retryable  bool
	Body       string
	Legacy     bool // true when no code/retryable (name+message only)
}

func (e *ControllerAPIError) Error() string {
	switch {
	case e.Message != "":
		return fmt.Sprintf("controller API error: status %d: %s", e.StatusCode, e.Message)
	case e.Code != "":
		return fmt.Sprintf("controller API error: status %d: %s", e.StatusCode, e.Code)
	case e.Body != "":
		return fmt.Sprintf("controller API error: status %d: %s", e.StatusCode, e.Body)
	default:
		return fmt.Sprintf("controller API error: status %d", e.StatusCode)
	}
}

// ParseControllerAPIError parses a controller error response body.
func ParseControllerAPIError(status int, body string) error {
	body = strings.TrimSpace(body)
	apiErr := &ControllerAPIError{
		StatusCode: status,
		Body:       body,
	}

	if body != "" {
		var payload struct {
			Error     string `json:"error"`
			Code      string `json:"code"`
			Message   string `json:"message"`
			Retryable *bool  `json:"retryable"`
			Name      string `json:"name"`
		}
		if err := json.Unmarshal([]byte(body), &payload); err == nil {
			apiErr.ErrorKind = payload.Error
			apiErr.Code = payload.Code
			apiErr.Message = payload.Message
			if payload.Retryable != nil {
				apiErr.Retryable = *payload.Retryable
			} else if status == http.StatusServiceUnavailable {
				apiErr.Retryable = true
			}
			if payload.Name != "" && apiErr.Code == "" && payload.Retryable == nil {
				apiErr.Legacy = true
				if apiErr.ErrorKind == "" {
					apiErr.ErrorKind = payload.Name
				}
			}
		}
	} else if status == http.StatusServiceUnavailable {
		apiErr.Retryable = true
	}

	return apiErr
}

// IsControllerEndpointMissing reports a 404 for an unknown controller command path.
func IsControllerEndpointMissing(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "not found: controller endpoint not found")
}

// IsRetryableControllerError reports whether the error should be retried.
func IsRetryableControllerError(err error) bool {
	var apiErr *ControllerAPIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusServiceUnavailable || apiErr.Retryable
	}
	return false
}

// IsNonRetryableAgentAuthError reports structured agent 401 auth failures.
func IsNonRetryableAgentAuthError(err error) bool {
	var apiErr *ControllerAPIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusUnauthorized &&
			!apiErr.Retryable &&
			apiErr.Code != ""
	}
	return false
}

// IsLegacyControllerAuthError reports legacy 401 responses without structured codes.
func IsLegacyControllerAuthError(err error) bool {
	var apiErr *ControllerAPIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusUnauthorized && apiErr.Code == ""
	}
	return false
}

// IsControllerNotReady reports readiness probe 503 responses.
func IsControllerNotReady(err error) bool {
	var apiErr *ControllerAPIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusServiceUnavailable
	}
	return false
}
