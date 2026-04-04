package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/byggflow/sandbox/internal/config"
	"github.com/byggflow/sandbox/internal/identity"
	"github.com/byggflow/sandbox/internal/pool"
	"github.com/byggflow/sandbox/internal/proxy"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

// Daemon is the main sandboxd service.
type Daemon struct {
	Config    config.Config
	Docker    *client.Client
	Pool      *pool.Manager
	Registry  *Registry
	Templates *TemplateRegistry
	Server    *Server
	Metrics   *Metrics
	Events    *EventBus
	Log       *slog.Logger

	networkID string
	ctx       context.Context
	cancel    context.CancelFunc
}

// New creates a new Daemon instance.
func New(cfg config.Config, log *slog.Logger) (*Daemon, error) {
	docker, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("create docker client: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	d := &Daemon{
		Config:    cfg,
		Docker:    docker,
		Registry:  NewRegistry(),
		Templates: NewTemplateRegistry(),
		Metrics:   NewMetrics(),
		Events:  NewEventBus(0),
		Log:     log,
		ctx:     ctx,
		cancel:  cancel,
	}

	return d, nil
}

// Start initializes the daemon: ensures the Docker network exists,
// starts the warm pool, and begins listening for requests.
func (d *Daemon) Start(ctx context.Context) error {
	// Ensure the Docker network exists.
	networkID, err := d.ensureNetwork(ctx)
	if err != nil {
		return fmt.Errorf("ensure network: %w", err)
	}
	d.networkID = networkID

	// Create and start the pool manager.
	d.Pool = pool.NewManager(d.Docker, d.Config.Pool, d.networkID, d.Config.Network.BridgeName, d.Log)
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

	// Close Docker client.
	if d.Docker != nil {
		d.Docker.Close()
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
	image := req.Image
	profile := req.Profile
	memory := int64(0)
	cpu := 0.0
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
	}

	if image == "" {
		// Default to the "default" profile.
		base, ok := d.Config.Pool.Base["default"]
		if !ok {
			return nil, fmt.Errorf("no image specified and no default profile")
		}
		image = base.Image
		profile = "default"
		mem, err := base.MemoryBytes()
		if err != nil {
			return nil, fmt.Errorf("parse default memory: %w", err)
		}
		memory = mem
		cpu = base.CPU
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

	// Validate TTL: clamp to per-request limit, then to global limit.
	ttl := req.TTL
	if limits.MaxTTL > 0 && ttl > limits.MaxTTL {
		ttl = limits.MaxTTL
	}
	if ttl > d.Config.Limits.MaxTTL {
		ttl = d.Config.Limits.MaxTTL
	}

	// Generate ID.
	sbxID, err := GenerateID(d.Config.Server.NodeID)
	if err != nil {
		return nil, err
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
				Created:     time.Now(),
				TTL:         ttl,
				Memory:      memory,
				CPU:         cpu,
				Profile:     profile,
				Template:    templateID,
				Labels:      req.Labels,
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

	// Cold start: create a new container.
	d.Log.Info("cold starting sandbox", "id", sbxID, "image", image)
	containerID, agentAddr, err := d.createContainer(ctx, image, memory, cpu)
	if err != nil {
		return nil, fmt.Errorf("create container: %w", err)
	}

	sbx := &Sandbox{
		ID:          sbxID,
		ContainerID: containerID,
		Image:       image,
		State:       StateRunning,
		Identity:    id,
		IdentityStr: id.Value,
		AgentAddr:   agentAddr,
		Created:     time.Now(),
		TTL:         ttl,
		Memory:      memory,
		CPU:         cpu,
		Profile:     profile,
		Template:    templateID,
		Labels:      req.Labels,
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
	d.Log.Info("sandbox created (cold)", "id", sbxID, "container", containerID[:12])
	return sbx, nil
}

// DestroySandbox removes a sandbox and its container.
func (d *Daemon) DestroySandbox(ctx context.Context, sbx *Sandbox) error {
	sbx.CancelReaper()
	d.Metrics.IncDestroy()
	d.publishSandboxEvent("sandbox.destroyed", sbx, nil)
	return d.destroySandbox(ctx, sbx)
}

func (d *Daemon) destroySandbox(ctx context.Context, sbx *Sandbox) error {
	sbx.mu.Lock()
	sbx.State = StateStopping
	// Close persistent agent if any.
	if sbx.Agent != nil {
		sbx.Agent.Close()
		sbx.Agent = nil
	}
	sbx.mu.Unlock()

	d.Registry.Remove(sbx.ID)

	timeout := 5
	_ = d.Docker.ContainerStop(ctx, sbx.ContainerID, container.StopOptions{Timeout: &timeout})
	if err := d.Docker.ContainerRemove(ctx, sbx.ContainerID, container.RemoveOptions{Force: true}); err != nil {
		return fmt.Errorf("remove container %s: %w", sbx.ContainerID[:12], err)
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
	if err := agent.Ping(2 * time.Second); err != nil {
		agent.Close()
		return nil, fmt.Errorf("agent ping: %w", err)
	}
	return agent, nil
}

// createContainer creates and starts a new Docker container, waits for the
// agent to be reachable, and returns the container ID and agent address.
func (d *Daemon) createContainer(ctx context.Context, image string, memory int64, cpu float64) (string, string, error) {
	nanoCPUs := int64(cpu * 1e9)

	resp, err := d.Docker.ContainerCreate(ctx,
		&container.Config{
			Image: image,
			Labels: map[string]string{
				"sandboxd": "true",
			},
		},
		&container.HostConfig{
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges"},
			ReadonlyRootfs: false, // Writable for TTL/template sandboxes.
			Resources: container.Resources{
				Memory:    memory,
				NanoCPUs:  nanoCPUs,
				PidsLimit: ptrInt64(256),
			},
			Tmpfs: map[string]string{
				"/tmp": "rw,noexec,nosuid,size=100m",
			},
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				d.Config.Network.BridgeName: {
					NetworkID: d.networkID,
				},
			},
		},
		nil,
		"",
	)
	if err != nil {
		return "", "", fmt.Errorf("docker create: %w", err)
	}

	if err := d.Docker.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		_ = d.Docker.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return "", "", fmt.Errorf("docker start: %w", err)
	}

	// Get container IP.
	inspect, err := d.Docker.ContainerInspect(ctx, resp.ID)
	if err != nil {
		_ = d.Docker.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return "", "", fmt.Errorf("docker inspect: %w", err)
	}

	ip := ""
	if nw, ok := inspect.NetworkSettings.Networks[d.Config.Network.BridgeName]; ok {
		ip = nw.IPAddress
	}
	if ip == "" {
		_ = d.Docker.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return "", "", fmt.Errorf("no IP on network %s", d.Config.Network.BridgeName)
	}

	agentAddr := ip + ":9111"

	// Wait for agent to be reachable.
	var lastErr error
	for attempt := 0; attempt < 30; attempt++ {
		agent, err := proxy.Dial(agentAddr, 2*time.Second)
		if err == nil {
			if pingErr := agent.Ping(2 * time.Second); pingErr == nil {
				agent.Close()
				return resp.ID, agentAddr, nil
			}
			agent.Close()
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}

	_ = d.Docker.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
	return "", "", fmt.Errorf("agent not reachable at %s: %w", agentAddr, lastErr)
}

// ensureNetwork creates the sandboxd Docker bridge network if it doesn't exist.
func (d *Daemon) ensureNetwork(ctx context.Context) (string, error) {
	name := d.Config.Network.BridgeName

	// Check if it already exists.
	nets, err := d.Docker.NetworkList(ctx, network.ListOptions{
		Filters: filters.NewArgs(filters.Arg("name", name)),
	})
	if err != nil {
		return "", fmt.Errorf("list networks: %w", err)
	}

	for _, n := range nets {
		if n.Name == name {
			d.Log.Info("using existing network", "name", name, "id", n.ID[:12])
			return n.ID, nil
		}
	}

	// Create it.
	enableICC := "false"
	if d.Config.Network.ICC {
		enableICC = "true"
	}

	netResp, err := d.Docker.NetworkCreate(ctx, name, network.CreateOptions{
		Driver: "bridge",
		Options: map[string]string{
			"com.docker.network.bridge.enable_icc": enableICC,
		},
	})
	if err != nil {
		return "", fmt.Errorf("create network %s: %w", name, err)
	}

	d.Log.Info("created network", "name", name, "id", netResp.ID[:12])
	return netResp.ID, nil
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

			// Remove the Docker image.
			if tpl.Image != "" {
				_, err := d.Docker.ImageRemove(ctx, tpl.Image, image.RemoveOptions{Force: false, PruneChildren: true})
				if err != nil {
					d.Log.Error("failed to remove expired template image", "id", tpl.ID, "image", tpl.Image, "error", err)
				}
			}
		}
	}
}

func ptrInt64(v int64) *int64 {
	return &v
}

// parseByteSize wraps config.parseByteSize for use in the daemon package.
func parseByteSize(s string) (int64, error) {
	cfg := &config.LimitsConfig{MaxMemory: s}
	return cfg.MaxMemoryBytes()
}

// CreateRequest is the request body for POST /sandboxes.
type CreateRequest struct {
	Image    string            `json:"image"`
	Profile  string            `json:"profile"`
	Template string            `json:"template"`
	Memory   string            `json:"memory"`
	CPU      float64           `json:"cpu"`
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
