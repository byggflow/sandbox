package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Config is the top-level sandboxd configuration.
type Config struct {
	Server      ServerConfig      `toml:"server"`
	MultiTenant MultiTenantConfig `toml:"multi_tenant"`
	Limits      LimitsConfig      `toml:"limits"`
	Network     NetworkConfig     `toml:"network"`
	Pool        PoolConfig        `toml:"pool"`
}

// MultiTenantConfig enables cryptographic identity verification.
// When enabled, all requests (except health/metrics) must carry a valid
// Ed25519 signature from the proxy over the identity and limit headers.
//
// PublicKeys supports multiple keys for zero-downtime key rotation.
// During rotation: add the new key, update the proxy, then remove the old key.
//
// Environment variable override: SANDBOXD_PUBLIC_KEYS (comma-separated)
// takes precedence over the config file values.
type MultiTenantConfig struct {
	Enabled    bool     `toml:"enabled"`
	PublicKeys []string `toml:"public_keys"` // Base64-encoded Ed25519 public keys. Multiple for rotation.
}

// ServerConfig holds listener and data directory settings.
type ServerConfig struct {
	Socket  string `toml:"socket"`
	TCP     string `toml:"tcp"`
	TLSCert string `toml:"tls_cert"` // Path to TLS certificate file for TCP listener.
	TLSKey  string `toml:"tls_key"`  // Path to TLS private key file for TCP listener.
	DataDir string `toml:"data_dir"`
	NodeID  string `toml:"node_id"` // Short identifier for this node, embedded in sandbox IDs (e.g. "eu1", "us2a").
}

// LimitsConfig holds resource limit settings.
type LimitsConfig struct {
	MaxSandboxes            int     `toml:"max_sandboxes"`
	MaxMemory               string  `toml:"max_memory"`
	MaxCPU                  float64 `toml:"max_cpu"`
	MaxTTL                  int     `toml:"max_ttl"`
	MaxTemplates            int     `toml:"max_templates"`
	MaxTemplateSize         string  `toml:"max_template_size"`
	TemplateExpiryDays      int     `toml:"template_expiry_days"`
}

// NetworkConfig holds Docker network settings.
type NetworkConfig struct {
	BridgeName string `toml:"bridge_name"`
}

// PoolConfig holds warm pool settings.
type PoolConfig struct {
	TotalWarm        int                      `toml:"total_warm"`
	MinPerImage      int                      `toml:"min_per_image"`
	MinBase          int                      `toml:"min_base"`
	MaxWarm          int                      `toml:"max_warm"`
	RebalanceWindow  string                   `toml:"rebalance_window"`
	HealthInterval   string                   `toml:"health_interval"`
	LivenessTimeout  string                   `toml:"liveness_timeout"`
	Base             map[string]BaseImageConfig `toml:"base"`
}

// BaseImageConfig defines a pool base image profile.
type BaseImageConfig struct {
	Image  string  `toml:"image"`
	Memory string  `toml:"memory"`
	CPU    float64 `toml:"cpu"`
}

// MaxMemoryBytes parses MaxMemory as bytes.
func (l *LimitsConfig) MaxMemoryBytes() (int64, error) {
	return parseByteSize(l.MaxMemory)
}

// RebalanceWindowDuration parses RebalanceWindow as a duration.
func (p *PoolConfig) RebalanceWindowDuration() (time.Duration, error) {
	return parseDuration(p.RebalanceWindow)
}

// HealthIntervalDuration parses HealthInterval as a duration.
func (p *PoolConfig) HealthIntervalDuration() (time.Duration, error) {
	return parseDuration(p.HealthInterval)
}

// LivenessTimeoutDuration parses LivenessTimeout as a duration.
func (p *PoolConfig) LivenessTimeoutDuration() (time.Duration, error) {
	return parseDuration(p.LivenessTimeout)
}

// MemoryBytes parses Memory as bytes for a base image config.
func (b *BaseImageConfig) MemoryBytes() (int64, error) {
	return parseByteSize(b.Memory)
}

// Defaults returns a Config with sensible default values.
func Defaults() Config {
	return Config{
		Server: ServerConfig{
			Socket:  "/var/run/sandboxd/sandboxd.sock",
			TCP:     "",
			DataDir: "/var/lib/sandboxd",
		},
		Limits: LimitsConfig{
			MaxSandboxes:       100,
			MaxMemory:          "4g",
			MaxCPU:                  4.0,
			MaxTTL:                  86400,
			MaxTemplates:            50,
			MaxTemplateSize:         "2g",
			TemplateExpiryDays:      60,
		},
		Network: NetworkConfig{
			BridgeName: "sandboxd-net",
		},
		Pool: PoolConfig{
			TotalWarm:       30,
			MinPerImage:     0,
			MinBase:         2,
			MaxWarm:         60,
			RebalanceWindow: "1h",
			HealthInterval:  "10s",
			LivenessTimeout: "5ms",
			Base: map[string]BaseImageConfig{
				"default": {
					Image:  "byggflow/sandbox-base:latest",
					Memory: "512m",
					CPU:    1.0,
				},
			},
		},
	}
}

// Load reads and parses the TOML config file at path.
// Missing fields retain their default values.
func Load(path string) (Config, error) {
	cfg := Defaults()

	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	if err := toml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}

	if err := cfg.validate(); err != nil {
		return Config{}, fmt.Errorf("validate config: %w", err)
	}

	return cfg, nil
}

func (c *Config) validate() error {
	if c.Server.Socket == "" {
		return fmt.Errorf("server.socket is required")
	}
	if c.MultiTenant.Enabled && len(c.MultiTenant.PublicKeys) == 0 {
		return fmt.Errorf("multi_tenant.public_keys requires at least one key when enabled")
	}
	if (c.Server.TLSCert == "") != (c.Server.TLSKey == "") {
		return fmt.Errorf("server.tls_cert and server.tls_key must both be set or both be empty")
	}
	if c.Limits.MaxSandboxes <= 0 {
		return fmt.Errorf("limits.max_sandboxes must be positive")
	}
	if c.Pool.TotalWarm < 0 {
		return fmt.Errorf("pool.total_warm must be non-negative")
	}
	if c.Pool.MaxWarm < c.Pool.TotalWarm {
		return fmt.Errorf("pool.max_warm must be >= pool.total_warm")
	}
	return nil
}

// parseByteSize parses a human-readable byte size like "512m", "4g", "1024k".
func parseByteSize(s string) (int64, error) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, fmt.Errorf("empty size string")
	}

	multiplier := int64(1)
	suffix := s[len(s)-1]
	switch suffix {
	case 'k':
		multiplier = 1024
		s = s[:len(s)-1]
	case 'm':
		multiplier = 1024 * 1024
		s = s[:len(s)-1]
	case 'g':
		multiplier = 1024 * 1024 * 1024
		s = s[:len(s)-1]
	case 't':
		multiplier = 1024 * 1024 * 1024 * 1024
		s = s[:len(s)-1]
	default:
		if suffix < '0' || suffix > '9' {
			return 0, fmt.Errorf("unknown size suffix: %c", suffix)
		}
	}

	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse size number: %w", err)
	}

	return n * multiplier, nil
}

// parseDuration parses a duration string like "10s", "1h", "5ms".
func parseDuration(s string) (time.Duration, error) {
	return time.ParseDuration(s)
}
