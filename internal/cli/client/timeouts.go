package client

import (
	"strings"
	"time"
)

const (
	// DefaultRequestTimeout is the per-request HTTP budget when --timeout is unset.
	DefaultRequestTimeout = 60 * time.Second

	defaultControlPlanePollTimeout  = 15 * time.Minute
	defaultLongOperationPollTimeout = 30 * time.Minute
)

var pollTimeoutOverride time.Duration

// ConfigureCLI applies CLI transport settings from the global --timeout flag.
// When set, --timeout overrides async poll totals only; per-request HTTP stays at DefaultRequestTimeout.
func ConfigureCLI(c *Client, timeoutFlag string) {
	if c == nil {
		return
	}
	c.SetRequestTimeout(DefaultRequestTimeout)
	pollTimeoutOverride = 0
	if d, ok := ParseDurationFlag(timeoutFlag); ok {
		pollTimeoutOverride = d
	}
}

// ParseDurationFlag parses a Go duration string from the CLI --timeout flag.
func ParseDurationFlag(raw string) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, false
	}
	return d, true
}

// PollTimeoutFor returns the poll budget for an operation class, honoring --timeout when set.
func PollTimeoutFor(kind string) time.Duration {
	if pollTimeoutOverride > 0 {
		return pollTimeoutOverride
	}
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "controlplane", "control-plane", "cp":
		return defaultControlPlanePollTimeout
	case "model-pull", "models-pull", "knowledge-pull":
		return 6 * time.Hour
	default:
		return defaultLongOperationPollTimeout
	}
}
