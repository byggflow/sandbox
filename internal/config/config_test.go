package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaults(t *testing.T) {
	cfg := Defaults()

	if cfg.Server.Socket != "/var/run/sandboxd/sandboxd.sock" {
		t.Errorf("unexpected default socket: %s", cfg.Server.Socket)
	}
	if cfg.Limits.MaxSandboxes != 100 {
		t.Errorf("unexpected default max_sandboxes: %d", cfg.Limits.MaxSandboxes)
	}
	if cfg.Pool.TotalWarm != 30 {
		t.Errorf("unexpected default total_warm: %d", cfg.Pool.TotalWarm)
	}
	if cfg.Network.BridgeName != "sandboxd-net" {
		t.Errorf("unexpected default bridge_name: %s", cfg.Network.BridgeName)
	}
	if _, ok := cfg.Pool.Base["default"]; !ok {
		t.Error("expected default base image config")
	}
}

func TestLoad(t *testing.T) {
	content := `
[server]
socket = "/tmp/test.sock"
tcp = "0.0.0.0:7522"
data_dir = "/tmp/sandboxd"
[limits]
max_sandboxes = 50
max_memory = "2g"
max_cpu = 2.0
max_ttl = 3600

[network]
bridge_name = "test-net"

[pool]
total_warm = 10
min_per_image = 1
min_base = 1
max_warm = 20
rebalance_window = "30m"
health_interval = "5s"
liveness_timeout = "10ms"

[pool.base.default]
image = "test/sandbox:latest"
memory = "256m"
cpu = 0.5

[pool.base.python]
image = "test/python:latest"
memory = "1g"
cpu = 2.0
`

	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}

	if cfg.Server.Socket != "/tmp/test.sock" {
		t.Errorf("unexpected socket: %s", cfg.Server.Socket)
	}
	if cfg.Server.TCP != "0.0.0.0:7522" {
		t.Errorf("unexpected tcp: %s", cfg.Server.TCP)
	}
	if cfg.Limits.MaxSandboxes != 50 {
		t.Errorf("unexpected max_sandboxes: %d", cfg.Limits.MaxSandboxes)
	}
	if cfg.Pool.TotalWarm != 10 {
		t.Errorf("unexpected total_warm: %d", cfg.Pool.TotalWarm)
	}

	py, ok := cfg.Pool.Base["python"]
	if !ok {
		t.Fatal("expected python base config")
	}
	if py.Image != "test/python:latest" {
		t.Errorf("unexpected python image: %s", py.Image)
	}
	mem, err := py.MemoryBytes()
	if err != nil {
		t.Fatal(err)
	}
	if mem != 1024*1024*1024 {
		t.Errorf("unexpected python memory bytes: %d", mem)
	}
}

func TestLoadMissing(t *testing.T) {
	_, err := Load("/nonexistent/path.toml")
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestLoadInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.toml")
	if err := os.WriteFile(path, []byte("not valid toml {{{"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Error("expected error for invalid TOML")
	}
}

func TestLoadValidationError(t *testing.T) {
	content := `
[server]
socket = "/tmp/test.sock"

[limits]
max_sandboxes = -1
`
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.toml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Error("expected validation error")
	}
}

func TestParseByteSize(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"512m", 512 * 1024 * 1024},
		{"4g", 4 * 1024 * 1024 * 1024},
		{"1024k", 1024 * 1024},
		{"1024", 1024},
		{"2t", 2 * 1024 * 1024 * 1024 * 1024},
	}
	for _, tt := range tests {
		got, err := ParseByteSize(tt.input)
		if err != nil {
			t.Errorf("ParseByteSize(%q): %v", tt.input, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseByteSize(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestRuntimeValidation(t *testing.T) {
	base := `
[server]
socket = "/tmp/test.sock"

[pool]
total_warm = 0
max_warm = 0

`
	tests := []struct {
		name    string
		extra   string
		wantErr bool
	}{
		{
			name:    "default runtime (empty)",
			extra:   "[pool.base.default]\nimage = \"test:latest\"\nmemory = \"512m\"\ncpu = 1.0\n",
			wantErr: false,
		},
		{
			name:    "explicit docker",
			extra:   "[pool.base.default]\nimage = \"test:latest\"\nmemory = \"512m\"\ncpu = 1.0\nruntime = \"docker\"\n",
			wantErr: false,
		},
		{
			name:    "docker+gvisor",
			extra:   "[pool.base.secure]\nimage = \"test:latest\"\nmemory = \"512m\"\ncpu = 1.0\nruntime = \"docker+gvisor\"\n",
			wantErr: false,
		},
		{
			name:  "firecracker with required config",
			extra: "[pool.base.fc]\nimage = \"fc.rootfs\"\nmemory = \"256m\"\ncpu = 1.0\nruntime = \"firecracker\"\n\n[firecracker]\nbinary_path = \"/usr/local/bin/firecracker\"\nkernel_path = \"/var/lib/firecracker/vmlinux\"\n",
			wantErr: false,
		},
		{
			name:    "firecracker missing binary_path",
			extra:   "[pool.base.fc]\nimage = \"fc.rootfs\"\nmemory = \"256m\"\ncpu = 1.0\nruntime = \"firecracker\"\n",
			wantErr: true,
		},
		{
			name:    "invalid runtime",
			extra:   "[pool.base.default]\nimage = \"test:latest\"\nmemory = \"512m\"\ncpu = 1.0\nruntime = \"docker+kata\"\n",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "config.toml")
			if err := os.WriteFile(path, []byte(base+tt.extra), 0644); err != nil {
				t.Fatal(err)
			}
			_, err := Load(path)
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestRuntimeOrDefault(t *testing.T) {
	tests := []struct {
		runtime string
		want    string
	}{
		{"", "docker"},
		{"docker", "docker"},
		{"docker+gvisor", "docker+gvisor"},
		{"firecracker", "firecracker"},
	}
	for _, tt := range tests {
		b := BaseImageConfig{Runtime: tt.runtime}
		got := b.RuntimeOrDefault()
		if got != tt.want {
			t.Errorf("RuntimeOrDefault(%q) = %q, want %q", tt.runtime, got, tt.want)
		}
	}
}

func TestParseByteSizeErrors(t *testing.T) {
	for _, input := range []string{"", "abc", "x"} {
		_, err := ParseByteSize(input)
		if err == nil {
			t.Errorf("expected error for ParseByteSize(%q)", input)
		}
	}
}
