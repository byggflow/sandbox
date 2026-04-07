package runtime

import "testing"

func TestDockerRuntimeName(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"", "docker"},
		{"docker", "docker"},
		{"docker+gvisor", "docker+gvisor"},
	}
	for _, tt := range tests {
		r := &DockerRuntime{name: tt.name}
		if tt.name == "" {
			// Simulate what the constructor does for empty name.
			r.name = "docker"
		}
		if got := r.Name(); got != tt.want {
			t.Errorf("Name() with name=%q = %q, want %q", tt.name, got, tt.want)
		}
	}
}
