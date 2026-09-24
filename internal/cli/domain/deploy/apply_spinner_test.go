package deploy

import "testing"

func TestApplySpinnerMessage(t *testing.T) {
	tests := []struct {
		target Target
		want   string
	}{
		{TargetControlPlane, "Applying control plane manifest..."},
		{TargetRegistries, "Applying registry manifest..."},
		{TargetModels, "Applying model manifest..."},
		{TargetKnowledge, "Applying knowledge manifest..."},
		{TargetMicroservices, "Applying manifest..."},
	}
	for _, tt := range tests {
		if got := applySpinnerMessage(tt.target); got != tt.want {
			t.Fatalf("target %q: want %q got %q", tt.target, tt.want, got)
		}
	}
}
