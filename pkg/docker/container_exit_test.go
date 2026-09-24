package docker

import (
	"strings"
	"testing"
)

func TestFormatContainerExitMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    string
		exitCode  int
		oomKilled bool
		errText   string
		want      string
	}{
		{
			name:     "exited with error text",
			status:   "exited",
			exitCode: 1,
			errText:  "config missing",
			want:     "exitCode=1 oomKilled=false error=config missing",
		},
		{
			name:      "oom killed",
			status:    "exited",
			exitCode:  137,
			oomKilled: true,
			errText:   "killed",
			want:      "exitCode=137 oomKilled=true error=killed",
		},
		{
			name:      "oom while docker still reports running",
			status:    "running",
			exitCode:  137,
			oomKilled: true,
			want:      "exitCode=137 oomKilled=true",
		},
		{
			name:     "empty error still has exitCode and oomKilled",
			status:   "exited",
			exitCode: 1,
			want:     "exitCode=1 oomKilled=false",
		},
		{
			name:     "successful exit still reports finished details",
			status:   "exited",
			exitCode: 0,
			want:     "exitCode=0 oomKilled=false",
		},
		{
			name:     "dead container",
			status:   "dead",
			exitCode: 2,
			errText:  "  device vanished  ",
			want:     "exitCode=2 oomKilled=false error=device vanished",
		},
		{
			name:     "created with recorded exit",
			status:   "created",
			exitCode: 127,
			errText:  "executable not found",
			want:     "exitCode=127 oomKilled=false error=executable not found",
		},
		{
			name:   "created without exit recorded",
			status: "created",
			want:   "",
		},
		{
			name:     "healthy running",
			status:   "running",
			exitCode: 0,
			want:     "",
		},
		{
			name:     "running with leftover engine error",
			status:   "running",
			exitCode: 0,
			errText:  "previous start failed",
			want:     "exitCode=0 oomKilled=false error=previous start failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatContainerExitMessage(tt.status, tt.exitCode, tt.oomKilled, tt.errText)
			if got != tt.want {
				t.Fatalf("formatContainerExitMessage() = %q, want %q", got, tt.want)
			}
			if strings.Contains(got, "CRI reason=") {
				t.Fatalf("docker inspect text must not use CRI prefix, got %q", got)
			}
		})
	}
}
