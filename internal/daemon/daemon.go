package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/byggflow/sandbox/internal/config"
	"github.com/byggflow/sandbox/internal/identity"
	"github.com/byggflow/sandbox/internal/pool"
	"github.com/byggflow/sandbox/internal/proxy"
	"github.com/byggflow/sandbox/internal/runtime"
)

// Daemon is the main sandboxd service.
type Daemon struct {
	Config    config.Config
	Runtimes  map[string]runtime.Runtime // keyed by runtime name ("docker", "docker+gvisor", "firecracker")
	Pool      *pool.Manager
	Registry  *Registry
	Templates        *TemplateRegistry
	TemplateBackend  TemplateBackend            // Default template backend (Docker).
	TemplateBackends map[string]TemplateBackend // Per-runtime template backends.
	Server    *Server
	Metrics   *Metrics
	Events    *EventBus
	Tunnels   *TunnelManager
	verifier  atomic.Pointer[identity.Verifier] // Non-nil when multi-tenant mode is enabled.
	AuthLimit   *rateLimiter                    // Rate limiter for failed auth attempts.
	CreateLimit *rateLimiter                    // Rate limiter for sandbox creation per identity.
	Log       *slog.Logger

	ctx       context.Context
	cancel    context.CancelFunc
}

// New creates a new Daemon instance.
func New(cfg config.Config, log *slog.Logger) (*Daemon, error) {
	runtimes := make(map[string]runtime.Runtime)

	// Always create the Docker runtime (runc).
	dockerRT, err := runtime.NewDockerRuntime("docker", cfg.Network.BridgeName, "", log)
	if err != nil {
		return nil, fmt.Errorf("create docker runtime: %w", err)
	}
	runtimes["docker"] = dockerRT

	// Create the Docker+gVisor runtime if any profile uses it.
	for _, base := range cfg.Pool.Base {
		if base.RuntimeOrDefault() == "docker+gvisor" {
			gvRT, err := runtime.NewDockerRuntime("docker+gvisor", cfg.Network.BridgeName, "runsc", log)
			if err != nil {
				return nil, fmt.Errorf("create docker+gvisor runtime: %w", err)
			}
			runtimes["docker+gvisor"] = gvRT
			break
		}
	}

	// Create the Firecracker runtime if any profile uses it.
	for _, base := range cfg.Pool.Base {
		if base.RuntimeOrDefault() == "firecracker" {
			fcRT := runtime.NewFirecrackerRuntime(cfg.Firecracker, log)
			runtimes["firecracker"] = fcRT
			break
		}
	}

	ctx, cancel := context.WithCancel(context.Background())

	templateBackends := map[string]TemplateBackend{
		"docker": &DockerTemplateBackend{Docker: dockerRT.Client},
	}
	if gvRT, ok := runtimes["docker+gvisor"]; ok {
		templateBackends["docker+gvisor"] = &DockerTemplateBackend{Docker: gvRT.(*runtime.DockerRuntime).Client}
	}
	if fcRT, ok := runtimes["firecracker"].(*runtime.FirecrackerRuntime); ok {
		templateBackends["firecracker"] = runtime.NewFirecrackerTemplateBackend(fcRT, cfg.Server.DataDir)
	}

	d := &Daemon{
		Config:           cfg,
		Runtimes:         runtimes,
		Registry:         NewRegistry(),
		Templates:        NewTemplateRegistry(),
		TemplateBackend:  &DockerTemplateBackend{Docker: dockerRT.Client},
		TemplateBackends: templateBackends,
		Metrics:          NewMetrics(),
		Events:          NewEventBus(0),
		Tunnels:         NewTunnelManager(cfg.Limits.TunnelBindAddress, cfg.Limits.TunnelPortMin, cfg.Limits.TunnelPortMax, cfg.Limits.MaxConnectionsPerTunnel, log),
		AuthLimit:       newRateLimiter(10, 1*time.Minute, cfg.Limits.RateLimitEntries),
		CreateLimit:     newRateLimiter(cfg.Limits.CreateRateLimit, 1*time.Minute, cfg.Limits.RateLimitEntries),
		Log:             log,
		ctx:             ctx,
		cancel:          cancel,
	}

	if cfg.MultiTenant.Enabled {
		keys := cfg.MultiTenant.PublicKeys
		v, err := identity.NewVerifier(keys...)
		if err != nil {
			return nil, fmt.Errorf("multi-tenant verifier: %w", err)
		}
		d.verifier.Store(v)
		log.Info("multi-tenant mode enabled with Ed25519 signature verification", "keys", v.KeyCount())
	}

	return d, nil
}

// Verifier returns the current multi-tenant verifier, or nil if disabled.
func (d *Daemon) Verifier() *identity.Verifier {
	return d.verifier.Load()
}

// Start initializes the daemon: initializes runtimes, starts the warm pool,
// and begins listening for requests.
func (d *Daemon) Start(ctx context.Context) error {
	// Initialize all runtimes (e.g. ensure Docker network, validate Firecracker paths).
	for name, rt := range d.Runtimes {
		if err := rt.Init(ctx); err != nil {
			return fmt.Errorf("init runtime %s: %w", name, err)
		}
	}

	// Create and start the pool manager.
	d.Pool = pool.NewManager(d.Runtimes, d.Config.Pool, d.Log)
	if err := d.Pool.Start(ctx); err != nil {
		d.Log.Error("pool start failed (continuing without warm pool)", "error", err)
	}

	// Create and start the HTTP server.
	d.Server = NewServer(d)
	if err := d.Server.Start(); err != nil {
		return fmt.Errorf("start server: %w", err)
	}

	// Start template expiry goroutine if configured.
	if d.Config.Limits.TemplateExpiryDays > 0 {
		go d.runTemplateExpiry(d.ctx)
	}

	d.Log.Info("sandboxd started",
		"socket", d.Config.Server.Socket,
		"tcp", d.Config.Server.TCP,
	)

	return nil
}

// Shutdown gracefully stops the daemon.
func (d *Daemon) Shutdown(ctx context.Context) error {
	d.Log.Info("shutting down sandboxd")
	d.cancel()

	// Stop accepting new connections.
	if d.Server != nil {
		if err := d.Server.Shutdown(ctx); err != nil {
			d.Log.Error("server shutdown error", "error", err)
		}
	}

	// Destroy all active sandboxes.
	for _, sbx := range d.Registry.All() {
		sbx.CancelReaper()
		if err := d.destroySandbox(ctx, sbx); err != nil {
			d.Log.Error("failed to destroy sandbox on shutdown", "id", sbx.ID, "error", err)
		}
	}

	// Stop the warm pool.
	if d.Pool != nil {
		d.Pool.Stop(ctx)
	}

	// Close all runtimes.
	for name, rt := range d.Runtimes {
		if err := rt.Close(); err != nil {
			d.Log.Error("failed to close runtime", "runtime", name, "error", err)
		}
	}

	d.Log.Info("sandboxd stopped")
	return nil
}

// Reload reloads the configuration file and applies changes.
func (d *Daemon) Reload(path string) error {
	cfg, err := config.Load(path)
	if err != nil {
		return fmt.Errorf("reload config: %w", err)
	}

	// Check for listen address changes (require restart).
	if cfg.Server.Socket != d.Config.Server.Socket || cfg.Server.TCP != d.Config.Server.TCP {
		d.Log.Warn("listen address changed; restart required for this to take effect")
	}

	// Update verifier if multi-tenant config changed.
	if cfg.MultiTenant.Enabled {
		keys := cfg.MultiTenant.PublicKeys
		v, err := identity.NewVerifier(keys...)
		if err != nil {
			return fmt.Errorf("reload multi-tenant verifier: %w", err)
		}
		d.verifier.Store(v)
		d.Log.Info("multi-tenant verifier reloaded", "keys", v.KeyCount())
	} else {
		d.verifier.Store(nil)
	}

	// Update config.
	d.Config = cfg

	d.Log.Info("configuration reloaded")
	return nil
}

// CreateSandbox creates a new sandbox from the given parameters.
func (d *Daemon) CreateSandbox(ctx context.Context, req CreateRequest, id identity.Identity, limits identity.RequestLimits) (*Sandbox, error) {
	// Check global sandbox limit.
	if d.Registry.Count() >= d.Config.Limits.MaxSandboxes {
		return nil, ErrAtCapacity
	}

	// Check per-identity sandbox limit (from proxy header).
	if limits.MaxConcurrent > 0 && !id.Empty() {
		if d.Registry.CountByIdentity(id.Value) >= limits.MaxConcurrent {
			return nil, ErrIdentityQuotaExceeded
		}
	}

	// Determine image and resource config.
	var image string
	profile := req.Profile
	memory := int64(0)
	cpu := 0.0
	storage := "500m"
	templateID := ""

	// If a template is specified, resolve the image from the template.
	if req.Template != "" {
		tpl, ok := d.Templates.Get(req.Template)
		if !ok {
			return nil, fmt.Errorf("unknown template: %s", req.Template)
		}
		image = tpl.Image
		templateID = tpl.ID
		d.Templates.MarkUsed(tpl.ID)
	}

	if profile != "" {
		base, ok := d.Config.Pool.Base[profile]
		if !ok {
			return nil, fmt.Errorf("unknown profile: %s", profile)
		}
		if image == "" {
			image = base.Image
		}
		mem, err := base.MemoryBytes()
		if err != nil {
			return nil, fmt.Errorf("parse memory for profile %s: %w", profile, err)
		}
		memory = mem
		cpu = base.CPU
		storage = base.StorageOrDefault()
	}

	if image == "" {
		// Default to the "default" profile.
		base, ok := d.Config.Pool.Base["default"]
		if !ok {
			return nil, fmt.Errorf("no profile specified and no default profile configured")
		}
		image = base.Image
		profile = "default"
		mem, err := base.MemoryBytes()
		if err != nil {
			return nil, fmt.Errorf("parse default memory: %w", err)
		}
		memory = mem
		cpu = base.CPU
		storage = base.StorageOrDefault()
	}

	// Override with request-level limits (only if lower than config max).
	if req.Memory != "" {
		rm, err := parseByteSize(req.Memory)
		if err == nil && rm > 0 {
			maxMem, _ := d.Config.Limits.MaxMemoryBytes()
			if rm <= maxMem {
				memory = rm
			}
		}
	}
	if req.CPU > 0 && req.CPU <= d.Config.Limits.MaxCPU {
		cpu = req.CPU
	}
	if req.Storage != "" {
		if _, err := parseByteSize(req.Storage); err != nil {
			return nil, fmt.Errorf("invalid storage size: %w", err)
		}
		storage = req.Storage
	}

	// Check global memory limit.
	if d.Config.Limits.MaxMemory != "" {
		maxMem, err := config.ParseByteSize(d.Config.Limits.MaxMemory)
		if err == nil && maxMem > 0 && memory > 0 {
			if d.Registry.AllocMemory()+memory > maxMem {
				return nil, ErrAtCapacity
			}
		}
	}

	// Check global CPU limit.
	if d.Config.Limits.MaxCPU > 0 && cpu > 0 {
		if d.Registry.AllocCPU()+cpu > d.Config.Limits.MaxCPU {
			return nil, ErrAtCapacity
		}
	}

	// Validate TTL: clamp to per-request limit, then to global limit.
	ttl := req.TTL
	if limits.MaxTTL > 0 && ttl > limits.MaxTTL {
		ttl = limits.MaxTTL
	}
	if ttl > d.Config.Limits.MaxTTL {
		ttl = d.Config.Limits.MaxTTL
	}

	// Generate ID and auth token.
	sbxID, err := GenerateID(d.Config.Server.NodeID)
	if err != nil {
		return nil, err
	}
	authToken, err := GenerateAuthToken()
	if err != nil {
		return nil, err
	}

	// Resolve runtime for this profile.
	runtimeName := "docker"
	if profile != "" {
		if base, ok := d.Config.Pool.Base[profile]; ok {
			runtimeName = base.RuntimeOrDefault()
		}
	}

	rt, ok := d.Runtimes[runtimeName]
	if !ok {
		return nil, fmt.Errorf("runtime %q not available", runtimeName)
	}

	// Record creation frequency.
	d.Pool.RecordCreation(image)

	// Try to claim a warm container (only if not using a template image).
	if templateID == "" {
		warm, ok := d.Pool.Claim(image)
		if ok {
			sbx := &Sandbox{
				ID:          sbxID,
				ContainerID: warm.ContainerID,
				Image:       image,
				State:       StateRunning,
				Identity:    id,
				IdentityStr: id.Value,
				AgentAddr:   warm.IP + ":9111",
				AuthToken:   warm.AuthToken,
				Created:     time.Now(),
				TTL:         ttl,
				Memory:      memory,
				CPU:         cpu,
				Profile:     profile,
				Template:    templateID,
				Labels:      req.Labels,
				RuntimeName: runtimeName,
				Buffer:      NewNotificationBuffer(),
			}
			if err := d.Registry.Add(sbx); err != nil {
				return nil, err
			}
			d.Metrics.IncCreateWarm()
			d.publishSandboxEvent("sandbox.created", sbx, map[string]interface{}{
				"image":   image,
				"profile": profile,
				"labels":  req.Labels,
				"method":  "warm",
			})
			d.Log.Info("sandbox created (warm)", "id", sbxID, "container", warm.ContainerID[:12])
			return sbx, nil
		}
	}

	// Cold start: create a new instance via the runtime.
	d.Log.Info("cold starting sandbox", "id", sbxID, "image", image, "runtime", runtimeName)
	inst, err := rt.Create(ctx, runtime.CreateOpts{
		Image:     image,
		Memory:    memory,
		CPU:       cpu,
		Storage:   storage,
		AuthToken: authToken,
		Labels:    req.Labels,
		Profile:   profile,
	})
	if err != nil {
		return nil, fmt.Errorf("create instance: %w", err)
	}

	sbx := &Sandbox{
		ID:          sbxID,
		ContainerID: inst.ID,
		Image:       image,
		State:       StateRunning,
		Identity:    id,
		IdentityStr: id.Value,
		AgentAddr:   inst.AgentAddr,
		AuthToken:   authToken,
		Created:     time.Now(),
		TTL:         ttl,
		Memory:      memory,
		CPU:         cpu,
		Profile:     profile,
		Template:    templateID,
		Labels:      req.Labels,
		RuntimeName: runtimeName,
		Buffer:      NewNotificationBuffer(),
	}
	if err := d.Registry.Add(sbx); err != nil {
		return nil, err
	}

	d.Metrics.IncCreateCold()
	d.publishSandboxEvent("sandbox.created", sbx, map[string]interface{}{
		"image":   image,
		"profile": profile,
		"labels":  req.Labels,
		"method":  "cold",
	})
	d.Log.Info("sandbox created (cold)", "id", sbxID, "container", inst.ID[:12])
	return sbx, nil
}

// DestroySandbox removes a sandbox and its container.
func (d *Daemon) DestroySandbox(ctx context.Context, sbx *Sandbox) error {
	sbx.CancelReaper()
	return d.destroySandbox(ctx, sbx)
}

func (d *Daemon) destroySandbox(ctx context.Context, sbx *Sandbox) error {
	// Atomically claim ownership of this destroy. Only one goroutine wins.
	if !d.Registry.Remove(sbx.ID) {
		return nil
	}

	d.Metrics.IncDestroy()
	d.publishSandboxEvent("sandbox.destroyed", sbx, nil)

	sbx.mu.Lock()
	sbx.State = StateStopping
	// Close persistent agent if any.
	if sbx.Agent != nil {
		sbx.Agent.Close()
		sbx.Agent = nil
	}
	// Close all port tunnels.
	for port, t := range sbx.Tunnels {
		d.Tunnels.Close(t)
		delete(sbx.Tunnels, port)
	}
	sbx.mu.Unlock()

	rt, ok := d.Runtimes[sbx.RuntimeName]
	if !ok {
		rt = d.Runtimes["docker"] // fallback for sandboxes created before runtime field existed
	}
	if err := rt.Destroy(ctx, sbx.ContainerID); err != nil {
		return fmt.Errorf("destroy instance %s: %w", sbx.ContainerID[:12], err)
	}

	sbx.mu.Lock()
	sbx.State = StateStopped
	sbx.mu.Unlock()

	d.Log.Info("sandbox destroyed", "id", sbx.ID)
	return nil
}

// HandleDisconnect handles a WebSocket session ending for a sandbox.
// If TTL > 0, the sandbox enters disconnected state with a reaper.
// If TTL == 0, the sandbox is destroyed.
func (d *Daemon) HandleDisconnect(sbx *Sandbox) {
	sbx.mu.Lock()
	sbx.Session = nil
	ttl := sbx.TTL

	if ttl <= 0 {
		sbx.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := d.destroySandbox(ctx, sbx); err != nil {
			d.Log.Error("failed to destroy sandbox on disconnect", "id", sbx.ID, "error", err)
		}
		return
	}

	// Close all port tunnels — they don't survive disconnect.
	for port, t := range sbx.Tunnels {
		d.Tunnels.Close(t)
		delete(sbx.Tunnels, port)
	}

	// Enter disconnected state.
	sbx.State = StateDisconnected
	sbx.DisconnectedAt = time.Now()
	sbx.mu.Unlock()

	d.publishSandboxEvent("sandbox.disconnected", sbx, map[string]interface{}{
		"ttl": ttl,
	})
	d.Log.Info("sandbox disconnected, starting TTL reaper", "id", sbx.ID, "ttl", ttl)

	// Start reaper.
	sbx.StartReaper(time.Duration(ttl)*time.Second, func() {
		d.Log.Info("TTL expired, destroying sandbox", "id", sbx.ID)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := d.destroySandbox(ctx, sbx); err != nil {
			d.Log.Error("failed to destroy sandbox on TTL expiry", "id", sbx.ID, "error", err)
		}
	})
}

// ConnectAgent dials the agent for a sandbox.
func (d *Daemon) ConnectAgent(sbx *Sandbox) (*proxy.AgentConn, error) {
	agent, err := proxy.Dial(sbx.AgentAddr, 5*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connect to agent: %w", err)
	}
	// Authenticate if the sandbox has an auth token.
	if sbx.AuthToken != "" {
		if err := agent.Authenticate(sbx.AuthToken, 5*time.Second); err != nil {
			agent.Close()
			return nil, fmt.Errorf("agent auth: %w", err)
		}
	}
	if err := agent.Ping(2 * time.Second); err != nil {
		agent.Close()
		return nil, fmt.Errorf("agent ping: %w", err)
	}
	return agent, nil
}

// RuntimeFor returns the runtime associated with the sandbox, falling back to Docker.
func (d *Daemon) RuntimeFor(sbx *Sandbox) runtime.Runtime {
	if rt, ok := d.Runtimes[sbx.RuntimeName]; ok {
		return rt
	}
	return d.Runtimes["docker"]
}

// TemplateBackendFor returns the template backend for the sandbox's runtime.
func (d *Daemon) TemplateBackendFor(sbx *Sandbox) TemplateBackend {
	if tb, ok := d.TemplateBackends[sbx.RuntimeName]; ok {
		return tb
	}
	return d.TemplateBackend
}

// runTemplateExpiry periodically checks for expired templates and removes them.
// A template is expired if its LastUsedAt is older than template_expiry_days.
// Templates that have never been used fall back to CreatedAt for the check.
func (d *Daemon) runTemplateExpiry(ctx context.Context) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	d.Log.Info("template expiry started", "expiry_days", d.Config.Limits.TemplateExpiryDays)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.expireTemplates(ctx)
		}
	}
}

func (d *Daemon) expireTemplates(ctx context.Context) {
	expiryDays := d.Config.Limits.TemplateExpiryDays
	if expiryDays <= 0 {
		return
	}

	cutoff := time.Now().Add(-time.Duration(expiryDays) * 24 * time.Hour)

	for _, tpl := range d.Templates.All() {
		// Use LastUsedAt if available, otherwise fall back to CreatedAt.
		ref := tpl.LastUsedAt
		if ref.IsZero() {
			ref = tpl.CreatedAt
		}

		if ref.Before(cutoff) {
			d.Log.Info("expiring template", "id", tpl.ID, "label", tpl.Label, "last_used", ref)

			if _, ok := d.Templates.Remove(tpl.ID); !ok {
				continue
			}

			// Remove the underlying image/snapshot via the appropriate backend.
			if tpl.Image != "" {
				tplBackend := d.TemplateBackend
				if tb, ok := d.TemplateBackends[tpl.Backend]; ok {
					tplBackend = tb
				}
				if err := tplBackend.Remove(ctx, tpl.Image); err != nil {
					d.Log.Error("failed to remove expired template image", "id", tpl.ID, "image", tpl.Image, "error", err)
				}
			}
		}
	}
}

// parseByteSize wraps config.parseByteSize for use in the daemon package.
func parseByteSize(s string) (int64, error) {
	cfg := &config.LimitsConfig{MaxMemory: s}
	return cfg.MaxMemoryBytes()
}

// CreateRequest is the request body for POST /sandboxes.
type CreateRequest struct {
	Profile  string            `json:"profile"`
	Template string            `json:"template"`
	Memory   string            `json:"memory"`
	CPU      float64           `json:"cpu"`
	Storage  string            `json:"storage"` // tmpfs size for /root (e.g. "500m", "1g"). Overrides profile default.
	TTL      int               `json:"ttl"`
	Labels   map[string]string `json:"labels"`
}

// CreateTemplateRequest is the request body for POST /templates.
type CreateTemplateRequest struct {
	SandboxID string `json:"sandbox_id"`
	Label     string `json:"label"`
}

// ErrAtCapacity indicates the sandbox limit has been reached.
var ErrAtCapacity = fmt.Errorf("at capacity")

// ErrIdentityQuotaExceeded indicates the per-identity sandbox limit has been reached.
var ErrIdentityQuotaExceeded = fmt.Errorf("identity quota exceeded")
