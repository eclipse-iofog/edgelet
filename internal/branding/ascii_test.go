package branding

import "testing"

func TestReleaseLabel(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "v1.1.4", want: "v1.1.4"},
		{in: "  v1.1.4  ", want: "v1.1.4"},
		{in: "1.1.4", want: "v1.1.4"},
		{in: "dev", want: "vdev"},
		{in: "", want: "vdev"},
	}
	for _, tt := range tests {
		if got := ReleaseLabel(tt.in); got != tt.want {
			t.Errorf("ReleaseLabel(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
