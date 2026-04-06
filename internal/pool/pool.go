package pool

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/byggflow/sandbox/internal/config"
	"github.com/byggflow/sandbox/internal/proxy"
	"github.com/byggflow/sandbox/internal/runtime"
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
	RuntimeName string
}

// Manager maintains a warm pool of pre-started containers.
type Manager struct {
	runtimes map[string]runtime.Runtime
	cfg      config.PoolConfig
	log      *slog.Logger

	mu    sync.Mutex
	warm  map[string][]*WarmContainer // image -> warm containers
	freq  map[string][]time.Time      // image -> creation timestamps (sliding window)
	close chan struct{}
	wg    sync.WaitGroup
}

// Status describes the state of a pool profile.
type Status struct {
	Profile string  `json:"profile"`
	Image   string  `json:"image"`
	Ready   int     `json:"ready"`
	Memory  string  `json:"memory"`
	CPU     float64 `json:"cpu"`
}

// NewManager creates a new pool manager.
func NewManager(runtimes map[string]runtime.Runtime, cfg config.PoolConfig, log *slog.Logger) *Manager {
	return &Manager{
		runtimes: runtimes,
		cfg:      cfg,
		log:      log,
		warm:     make(map[string][]*WarmContainer),
		freq:     make(map[string][]time.Time),
		close:    make(chan struct{}),
	}
}

// Start initializes the warm pool and starts background maintenance.
func (m *Manager) Start(ctx context.Context) error {
	// Pre-warm containers asynchronously so the daemon can start serving immediately.
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		for profile, base := range m.cfg.Base {
			mem, err := base.MemoryBytes()
			if err != nil {
				m.log.Error("parse memory for pool profile", "profile", profile, "error", err)
				continue
			}

			count := m.cfg.MinBase
			m.log.Info("pre-warming pool", "profile", profile, "image", base.Image, "count", count)

			for i := 0; i < count; i++ {
				wc, err := m.createWarm(ctx, base.Image, profile, mem, base.CPU, base.StorageOrDefault(), base.RuntimeOrDefault())
				if err != nil {
					m.log.Error("failed to create warm container", "profile", profile, "error", err)
					continue
				}
				m.mu.Lock()
				m.warm[base.Image] = append(m.warm[base.Image], wc)
				m.mu.Unlock()
			}
		}
	}()

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
			if rt, ok := m.runtimes[wc.RuntimeName]; ok {
				_ = rt.Destroy(ctx, wc.ContainerID)
			}
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
				go func(id, rtName string) {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					if rt, ok := m.runtimes[rtName]; ok {
						_ = rt.Destroy(ctx, id)
					}
				}(wc.ContainerID, wc.RuntimeName)
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
	for {
		m.mu.Lock()
		current := len(m.warm[base.Image])
		if current >= count {
			m.mu.Unlock()
			break
		}
		m.mu.Unlock()

		wc, err := m.createWarm(ctx, base.Image, profile, mem, base.CPU, base.StorageOrDefault(), base.RuntimeOrDefault())
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
			if rt, ok := m.runtimes[wc.RuntimeName]; ok {
				_ = rt.Destroy(ctx, wc.ContainerID)
			}
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
		if rt, ok := m.runtimes[wc.RuntimeName]; ok {
			_ = rt.Destroy(ctx, wc.ContainerID)
		}
	}

	// Recreate.
	count := m.cfg.MinBase
	for i := 0; i < count; i++ {
		wc, err := m.createWarm(ctx, base.Image, profile, mem, base.CPU, base.StorageOrDefault(), base.RuntimeOrDefault())
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

// createWarm creates and starts a single warm container via the runtime.
func (m *Manager) createWarm(ctx context.Context, image, profile string, memory int64, cpu float64, storage, runtimeName string) (*WarmContainer, error) {
	rt, ok := m.runtimes[runtimeName]
	if !ok {
		return nil, fmt.Errorf("runtime %q not available", runtimeName)
	}

	// Generate auth token for this warm container.
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return nil, fmt.Errorf("generate auth token: %w", err)
	}
	authToken := hex.EncodeToString(tokenBytes)

	inst, err := rt.Create(ctx, runtime.CreateOpts{
		Image:     image,
		Memory:    memory,
		CPU:       cpu,
		Storage:   storage,
		AuthToken: authToken,
		Labels: map[string]string{
			"sandboxd.pool":    "warm",
			"sandboxd.profile": profile,
		},
		Profile: profile,
	})
	if err != nil {
		return nil, fmt.Errorf("create instance: %w", err)
	}

	// Connect to agent. The runtime's Create already verified reachability,
	// but we need to hold a persistent AgentConn for health checks.
	agentAddr := inst.AgentAddr
	var agent *proxy.AgentConn
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		agent, lastErr = proxy.Dial(agentAddr, 2*time.Second)
		if lastErr == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if lastErr != nil {
		_ = rt.Destroy(ctx, inst.ID)
		return nil, fmt.Errorf("connect to agent at %s: %w", agentAddr, lastErr)
	}

	// Authenticate with the agent.
	if err := agent.Authenticate(authToken, 2*time.Second); err != nil {
		agent.Close()
		_ = rt.Destroy(ctx, inst.ID)
		return nil, fmt.Errorf("agent auth: %w", err)
	}

	// Verify liveness.
	if err := agent.Ping(2 * time.Second); err != nil {
		agent.Close()
		_ = rt.Destroy(ctx, inst.ID)
		return nil, fmt.Errorf("agent ping failed: %w", err)
	}

	// Extract IP from AgentAddr (strip ":port").
	ip := agentAddr
	if host, _, err := splitHostPort(agentAddr); err == nil {
		ip = host
	}

	return &WarmContainer{
		ContainerID: inst.ID,
		Image:       image,
		Profile:     profile,
		IP:          ip,
		Memory:      memory,
		CPU:         cpu,
		AuthToken:   authToken,
		Agent:       agent,
		Created:     time.Now(),
		RuntimeName: runtimeName,
	}, nil
}

// splitHostPort extracts the host from a host:port address.
func splitHostPort(addr string) (string, string, error) {
	return net.SplitHostPort(addr)
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
			go func(id, rtName string) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if rt, ok := m.runtimes[rtName]; ok {
					_ = rt.Destroy(ctx, id)
				}
			}(wc.ContainerID, wc.RuntimeName)
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
		if len(recent) == 0 {
			delete(m.freq, image)
		} else {
			m.freq[image] = recent
		}
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
