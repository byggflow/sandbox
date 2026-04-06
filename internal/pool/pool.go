package pool

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/byggflow/sandbox/internal/config"
	"github.com/byggflow/sandbox/internal/proxy"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

// WarmContainer is a pre-started container ready for assignment.
type WarmContainer struct {
	ContainerID string
	Image       string
	Profile     string
	IP          string
	Memory      int64
	CPU         float64
	AuthToken   string
	Agent       *proxy.AgentConn
	Created     time.Time
}

// Manager maintains a warm pool of pre-started containers.
type Manager struct {
	docker      *client.Client
	cfg         config.PoolConfig
	networkID   string
	networkName string
	log         *slog.Logger

	mu    sync.Mutex
	warm  map[string][]*WarmContainer // image -> warm containers
	freq  map[string][]time.Time       // image -> creation timestamps (sliding window)
	close chan struct{}
	wg    sync.WaitGroup
}

// Status describes the state of a pool profile.
type Status struct {
	Profile string `json:"profile"`
	Image   string `json:"image"`
	Ready   int    `json:"ready"`
	Memory  string `json:"memory"`
	CPU     float64 `json:"cpu"`
}

// NewManager creates a new pool manager.
func NewManager(docker *client.Client, cfg config.PoolConfig, networkID, networkName string, log *slog.Logger) *Manager {
	return &Manager{
		docker:      docker,
		cfg:         cfg,
		networkID:   networkID,
		networkName: networkName,
		log:         log,
		warm:        make(map[string][]*WarmContainer),
		freq:        make(map[string][]time.Time),
		close:       make(chan struct{}),
	}
}

// Start initializes the warm pool and starts background maintenance.
func (m *Manager) Start(ctx context.Context) error {
	// Create initial warm containers for each base image.
	for profile, base := range m.cfg.Base {
		mem, err := base.MemoryBytes()
		if err != nil {
			return fmt.Errorf("parse memory for profile %s: %w", profile, err)
		}

		count := m.cfg.MinBase
		m.log.Info("pre-warming pool", "profile", profile, "image", base.Image, "count", count)

		for i := 0; i < count; i++ {
			wc, err := m.createWarm(ctx, base.Image, profile, mem, base.CPU)
			if err != nil {
				m.log.Error("failed to create warm container", "profile", profile, "error", err)
				continue
			}
			m.mu.Lock()
			m.warm[base.Image] = append(m.warm[base.Image], wc)
			m.mu.Unlock()
		}
	}

	// Start health check loop.
	m.wg.Add(1)
	go m.healthLoop()

	// Start rebalance loop.
	m.wg.Add(1)
	go m.rebalanceLoop()

	return nil
}

// Stop shuts down the pool manager and destroys all warm containers.
func (m *Manager) Stop(ctx context.Context) {
	close(m.close)
	m.wg.Wait()

	m.mu.Lock()
	defer m.mu.Unlock()

	for image, containers := range m.warm {
		for _, wc := range containers {
			if wc.Agent != nil {
				wc.Agent.Close()
			}
			timeout := 5
			_ = m.docker.ContainerStop(ctx, wc.ContainerID, container.StopOptions{Timeout: &timeout})
			_ = m.docker.ContainerRemove(ctx, wc.ContainerID, container.RemoveOptions{Force: true})
		}
		delete(m.warm, image)
	}
}

// Claim attempts to grab a warm container for the given image.
// It performs a liveness ping before returning. If the container
// is dead, it's discarded and the next one is tried.
func (m *Manager) Claim(image string) (*WarmContainer, bool) {
	livenessTimeout, err := m.cfg.LivenessTimeoutDuration()
	if err != nil {
		livenessTimeout = 5 * time.Millisecond
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	containers := m.warm[image]
	for len(containers) > 0 {
		// Pop the first container.
		wc := containers[0]
		containers = containers[1:]
		m.warm[image] = containers

		// Liveness ping.
		if wc.Agent != nil {
			if err := wc.Agent.Ping(livenessTimeout); err != nil {
				m.log.Warn("warm container failed liveness", "container", wc.ContainerID[:12], "error", err)
				wc.Agent.Close()
				go func(id string) {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					_ = m.docker.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
				}(wc.ContainerID)
				continue
			}
		}

		return wc, true
	}

	return nil, false
}

// RecordCreation records that a sandbox was created for the given image.
func (m *Manager) RecordCreation(image string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.freq[image] = append(m.freq[image], time.Now())
}

// Statuses returns the current pool status for all profiles.
func (m *Manager) Statuses() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()

	var result []Status
	for profile, base := range m.cfg.Base {
		count := len(m.warm[base.Image])
		result = append(result, Status{
			Profile: profile,
			Image:   base.Image,
			Ready:   count,
			Memory:  base.Memory,
			CPU:     base.CPU,
		})
	}
	return result
}

// Resize changes the number of warm containers for a profile.
func (m *Manager) Resize(ctx context.Context, profile string, count int) error {
	base, ok := m.cfg.Base[profile]
	if !ok {
		return fmt.Errorf("unknown profile: %s", profile)
	}

	mem, err := base.MemoryBytes()
	if err != nil {
		return fmt.Errorf("parse memory: %w", err)
	}

	// Scale up: create containers until we reach the desired count.
	// Re-check count under lock after each creation to avoid overallocation.
	for {
		m.mu.Lock()
		current := len(m.warm[base.Image])
		if current >= count {
			m.mu.Unlock()
			break
		}
		m.mu.Unlock()

		wc, err := m.createWarm(ctx, base.Image, profile, mem, base.CPU)
		if err != nil {
			return fmt.Errorf("create warm container: %w", err)
		}
		m.mu.Lock()
		m.warm[base.Image] = append(m.warm[base.Image], wc)
		m.mu.Unlock()
	}

	// Scale down: remove excess containers.
	m.mu.Lock()
	current := len(m.warm[base.Image])
	if count < current {
		containers := m.warm[base.Image]
		excess := containers[count:]
		m.warm[base.Image] = containers[:count]
		m.mu.Unlock()

		for _, wc := range excess {
			if wc.Agent != nil {
				wc.Agent.Close()
			}
			_ = m.docker.ContainerRemove(ctx, wc.ContainerID, container.RemoveOptions{Force: true})
		}
	} else {
		m.mu.Unlock()
	}

	return nil
}

// Flush destroys and recreates all warm containers for a profile.
func (m *Manager) Flush(ctx context.Context, profile string) error {
	base, ok := m.cfg.Base[profile]
	if !ok {
		return fmt.Errorf("unknown profile: %s", profile)
	}

	mem, err := base.MemoryBytes()
	if err != nil {
		return fmt.Errorf("parse memory: %w", err)
	}

	// Destroy existing warm containers for this profile's image.
	m.mu.Lock()
	old := m.warm[base.Image]
	m.warm[base.Image] = nil
	m.mu.Unlock()

	for _, wc := range old {
		if wc.Agent != nil {
			wc.Agent.Close()
		}
		_ = m.docker.ContainerRemove(ctx, wc.ContainerID, container.RemoveOptions{Force: true})
	}

	// Recreate.
	count := m.cfg.MinBase
	for i := 0; i < count; i++ {
		wc, err := m.createWarm(ctx, base.Image, profile, mem, base.CPU)
		if err != nil {
			m.log.Error("failed to recreate warm container", "profile", profile, "error", err)
			continue
		}
		m.mu.Lock()
		m.warm[base.Image] = append(m.warm[base.Image], wc)
		m.mu.Unlock()
	}

	return nil
}

// createWarm creates and starts a single warm container.
func (m *Manager) createWarm(ctx context.Context, image, profile string, memory int64, cpu float64) (*WarmContainer, error) {
	nanoCPUs := int64(cpu * 1e9)

	// Generate auth token for this warm container.
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("generate auth token: %w", err)
	}
	authToken := hex.EncodeToString(tokenBytes)

	resp, err := m.docker.ContainerCreate(ctx,
		&container.Config{
			Image: image,
			Env:   []string{"SANDBOX_AUTH_TOKEN=" + authToken},
			Labels: map[string]string{
				"sandboxd":         "true",
				"sandboxd.pool":    "warm",
				"sandboxd.profile": profile,
			},
		},
		&container.HostConfig{
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges"},
			ReadonlyRootfs: true,
			Resources: container.Resources{
				Memory:    memory,
				NanoCPUs:  nanoCPUs,
				PidsLimit: ptrInt64(256),
			},
			Tmpfs: map[string]string{
				"/tmp":          "rw,noexec,nosuid,size=100m",
				"/root": "rw,nosuid,size=500m",
			},
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				m.networkName: {
					NetworkID: m.networkID,
				},
			},
		},
		nil,
		"",
	)
	if err != nil {
		return nil, fmt.Errorf("create container: %w", err)
	}

	if err := m.docker.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		_ = m.docker.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return nil, fmt.Errorf("start container: %w", err)
	}

	// Get container IP.
	inspect, err := m.docker.ContainerInspect(ctx, resp.ID)
	if err != nil {
		_ = m.docker.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return nil, fmt.Errorf("inspect container: %w", err)
	}

	ip := ""
	if nw, ok := inspect.NetworkSettings.Networks[m.networkName]; ok {
		ip = nw.IPAddress
	}
	if ip == "" {
		_ = m.docker.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return nil, fmt.Errorf("container has no IP on network %s", m.networkName)
	}

	agentAddr := ip + ":9111"

	// Wait for agent to be reachable with retries.
	var agent *proxy.AgentConn
	for attempt := 0; attempt < 20; attempt++ {
		agent, err = proxy.Dial(agentAddr, 2*time.Second)
		if err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		_ = m.docker.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return nil, fmt.Errorf("connect to agent at %s: %w", agentAddr, err)
	}

	// Verify liveness.
	if err := agent.Ping(2 * time.Second); err != nil {
		agent.Close()
		_ = m.docker.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return nil, fmt.Errorf("agent ping failed: %w", err)
	}

	// Authenticate with the agent.
	if err := agent.Authenticate(authToken, 2*time.Second); err != nil {
		agent.Close()
		_ = m.docker.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return nil, fmt.Errorf("agent auth: %w", err)
	}

	return &WarmContainer{
		ContainerID: resp.ID,
		Image:       image,
		Profile:     profile,
		IP:          ip,
		Memory:      memory,
		CPU:         cpu,
		AuthToken:   authToken,
		Agent:       agent,
		Created:     time.Now(),
	}, nil
}

// healthLoop periodically pings warm containers and removes dead ones.
func (m *Manager) healthLoop() {
	defer m.wg.Done()

	interval, err := m.cfg.HealthIntervalDuration()
	if err != nil {
		interval = 10 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-m.close:
			return
		case <-ticker.C:
			m.healthCheck()
		}
	}
}

// healthCheckConcurrency is the max number of parallel pings during health checks.
const healthCheckConcurrency = 10

func (m *Manager) healthCheck() {
	// Snapshot all warm containers under lock.
	m.mu.Lock()
	type entry struct {
		image string
		wc    *WarmContainer
	}
	var all []entry
	for image, containers := range m.warm {
		for _, wc := range containers {
			all = append(all, entry{image: image, wc: wc})
		}
	}
	m.mu.Unlock()

	if len(all) == 0 {
		return
	}

	// Ping in parallel with bounded concurrency.
	type result struct {
		entry entry
		alive bool
	}
	results := make([]result, len(all))
	sem := make(chan struct{}, healthCheckConcurrency)
	var wg sync.WaitGroup

	for i, e := range all {
		wg.Add(1)
		go func(idx int, e entry) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if e.wc.Agent == nil {
				results[idx] = result{entry: e, alive: false}
				return
			}
			if err := e.wc.Agent.Ping(2 * time.Second); err != nil {
				m.log.Warn("health check failed", "container", e.wc.ContainerID[:12], "error", err)
				results[idx] = result{entry: e, alive: false}
				return
			}
			results[idx] = result{entry: e, alive: true}
		}(i, e)
	}
	wg.Wait()

	// Track which images were in the snapshot so we can preserve newly added ones.
	snapshotImages := make(map[string]bool)
	for _, e := range all {
		snapshotImages[e.image] = true
	}

	// Rebuild warm map under lock.
	m.mu.Lock()
	newWarm := make(map[string][]*WarmContainer)
	for _, r := range results {
		if r.alive {
			newWarm[r.entry.image] = append(newWarm[r.entry.image], r.entry.wc)
		} else {
			wc := r.entry.wc
			if wc.Agent != nil {
				wc.Agent.Close()
			}
			go func(id string) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				_ = m.docker.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
			}(wc.ContainerID)
		}
	}
	// Preserve images that were added after the snapshot was taken.
	for image, containers := range m.warm {
		if !snapshotImages[image] && len(containers) > 0 {
			newWarm[image] = containers
		}
	}
	m.warm = newWarm
	m.mu.Unlock()
}

// rebalanceLoop periodically adjusts warm container allocation based on creation frequency.
func (m *Manager) rebalanceLoop() {
	defer m.wg.Done()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-m.close:
			return
		case <-ticker.C:
			m.rebalance()
		}
	}
}

func (m *Manager) rebalance() {
	window, err := m.cfg.RebalanceWindowDuration()
	if err != nil {
		window = time.Hour
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	cutoff := time.Now().Add(-window)

	// Clean old entries and count frequencies.
	totalCreations := 0
	imageFreq := make(map[string]int)
	for image, timestamps := range m.freq {
		var recent []time.Time
		for _, t := range timestamps {
			if t.After(cutoff) {
				recent = append(recent, t)
			}
		}
		m.freq[image] = recent
		imageFreq[image] = len(recent)
		totalCreations += len(recent)
	}

	if totalCreations == 0 {
		return // No data to rebalance on.
	}

	// Calculate desired warm counts per image.
	budget := m.cfg.TotalWarm

	// Reserve min_base slots for each base image.
	reserved := 0
	for _, base := range m.cfg.Base {
		current := len(m.warm[base.Image])
		if current < m.cfg.MinBase {
			reserved += m.cfg.MinBase - current
		}
	}

	remaining := budget - reserved
	if remaining < 0 {
		remaining = 0
	}

	// Distribute remaining slots proportionally by frequency.
	// This is informational - we don't aggressively create/destroy,
	// just ensure minimums are met. Excess drains naturally.
	for _, base := range m.cfg.Base {
		current := len(m.warm[base.Image])
		desired := m.cfg.MinBase

		if totalCreations > 0 {
			freq := imageFreq[base.Image]
			proportional := (freq * remaining) / totalCreations
			if proportional > desired {
				desired = proportional
			}
		}

		if current < desired {
			m.log.Info("rebalance: pool needs more containers",
				"image", base.Image,
				"current", current,
				"desired", desired,
			)
		}
	}
}

func ptrInt64(v int64) *int64 {
	return &v
}
