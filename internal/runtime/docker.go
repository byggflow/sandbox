package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net"
	"time"

	"github.com/byggflow/sandbox/internal/proxy"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

// DockerRuntime implements Runtime using Docker containers.
type DockerRuntime struct {
	Client      *client.Client
	NetworkID   string
	NetworkName string
	Log         *slog.Logger
}

// NewDockerRuntime creates a DockerRuntime with a new Docker client.
func NewDockerRuntime(networkName string, log *slog.Logger) (*DockerRuntime, error) {
	docker, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("create docker client: %w", err)
	}
	return &DockerRuntime{
		Client:      docker,
		NetworkName: networkName,
		Log:         log,
	}, nil
}

func (r *DockerRuntime) Name() string { return "docker" }

// Init ensures the Docker bridge network exists.
func (r *DockerRuntime) Init(ctx context.Context) error {
	nets, err := r.Client.NetworkList(ctx, network.ListOptions{
		Filters: filters.NewArgs(filters.Arg("name", r.NetworkName)),
	})
	if err != nil {
		return fmt.Errorf("list networks: %w", err)
	}

	for _, n := range nets {
		if n.Name == r.NetworkName {
			r.NetworkID = n.ID
			r.Log.Info("using existing network", "name", r.NetworkName, "id", n.ID[:12])
			return nil
		}
	}

	netResp, err := r.Client.NetworkCreate(ctx, r.NetworkName, network.CreateOptions{
		Driver: "bridge",
		Options: map[string]string{
			"com.docker.network.bridge.enable_icc": "false",
		},
	})
	if err != nil {
		return fmt.Errorf("create network %s: %w", r.NetworkName, err)
	}

	r.NetworkID = netResp.ID
	r.Log.Info("created network", "name", r.NetworkName, "id", netResp.ID[:12])
	return nil
}

// Create creates and starts a Docker container, waits for the agent to be
// reachable, and returns the instance.
func (r *DockerRuntime) Create(ctx context.Context, opts CreateOpts) (*Instance, error) {
	nanoCPUs := int64(opts.CPU * 1e9)

	var envVars []string
	if opts.AuthToken != "" {
		envVars = append(envVars, "SANDBOX_AUTH_TOKEN="+opts.AuthToken)
	}

	labels := map[string]string{"sandboxd": "true"}
	for k, v := range opts.Labels {
		labels[k] = v
	}

	storage := opts.Storage
	if storage == "" {
		storage = "500m"
	}

	resp, err := r.Client.ContainerCreate(ctx,
		&container.Config{
			Image:  opts.Image,
			Env:    envVars,
			Labels: labels,
		},
		&container.HostConfig{
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges"},
			ReadonlyRootfs: true,
			Resources: container.Resources{
				Memory:    opts.Memory,
				NanoCPUs:  nanoCPUs,
				PidsLimit: ptrInt64(256),
			},
			Tmpfs: map[string]string{
				"/tmp":  "rw,noexec,nosuid,size=100m",
				"/root": "rw,nosuid,size=" + storage,
			},
		},
		&network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				r.NetworkName: {
					NetworkID: r.NetworkID,
				},
			},
		},
		nil,
		"",
	)
	if err != nil {
		return nil, fmt.Errorf("docker create: %w", err)
	}

	if err := r.Client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		_ = r.Client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return nil, fmt.Errorf("docker start: %w", err)
	}

	inspect, err := r.Client.ContainerInspect(ctx, resp.ID)
	if err != nil {
		_ = r.Client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return nil, fmt.Errorf("docker inspect: %w", err)
	}

	ip := ""
	if nw, ok := inspect.NetworkSettings.Networks[r.NetworkName]; ok {
		ip = nw.IPAddress
	}
	if ip == "" {
		_ = r.Client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return nil, fmt.Errorf("no IP on network %s", r.NetworkName)
	}

	agentAddr := ip + ":9111"

	// Wait for agent to be reachable.
	var lastErr error
	for attempt := 0; attempt < 30; attempt++ {
		agent, dialErr := proxy.Dial(agentAddr, 2*time.Second)
		if dialErr == nil {
			if opts.AuthToken != "" {
				if authErr := agent.Authenticate(opts.AuthToken, 2*time.Second); authErr != nil {
					agent.Close()
					lastErr = authErr
					time.Sleep(100 * time.Millisecond)
					continue
				}
			}
			if pingErr := agent.Ping(2 * time.Second); pingErr == nil {
				agent.Close()
				return &Instance{ID: resp.ID, AgentAddr: agentAddr}, nil
			}
			agent.Close()
		}
		lastErr = dialErr
		time.Sleep(100 * time.Millisecond)
	}

	_ = r.Client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
	return nil, fmt.Errorf("agent not reachable at %s: %w", agentAddr, lastErr)
}

// Destroy stops and removes a Docker container.
func (r *DockerRuntime) Destroy(ctx context.Context, instanceID string) error {
	timeout := 5
	_ = r.Client.ContainerStop(ctx, instanceID, container.StopOptions{Timeout: &timeout})
	if err := r.Client.ContainerRemove(ctx, instanceID, container.RemoveOptions{Force: true}); err != nil {
		return fmt.Errorf("remove container %s: %w", instanceID[:12], err)
	}
	return nil
}

// Stats returns resource usage metrics for a Docker container.
func (r *DockerRuntime) Stats(ctx context.Context, instanceID string) (*Stats, error) {
	statsResp, err := r.Client.ContainerStatsOneShot(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("docker stats: %w", err)
	}
	defer statsResp.Body.Close()

	var dockerStats container.StatsResponse
	if err := json.NewDecoder(statsResp.Body).Decode(&dockerStats); err != nil {
		return nil, fmt.Errorf("decode stats: %w", err)
	}

	cpuPercent := 0.0
	cpuDelta := float64(dockerStats.CPUStats.CPUUsage.TotalUsage) - float64(dockerStats.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(dockerStats.CPUStats.SystemUsage) - float64(dockerStats.PreCPUStats.SystemUsage)
	if systemDelta > 0 && cpuDelta >= 0 {
		onlineCPUs := float64(dockerStats.CPUStats.OnlineCPUs)
		if onlineCPUs == 0 {
			onlineCPUs = 1
		}
		cpuPercent = (cpuDelta / systemDelta) * onlineCPUs * 100.0
		cpuPercent = math.Round(cpuPercent*100) / 100
	}

	memUsage := dockerStats.MemoryStats.Usage
	memLimit := dockerStats.MemoryStats.Limit
	memPercent := 0.0
	if memLimit > 0 {
		memPercent = float64(memUsage) / float64(memLimit) * 100.0
		memPercent = math.Round(memPercent*100) / 100
	}

	var netRx, netTx uint64
	for _, ns := range dockerStats.Networks {
		netRx += ns.RxBytes
		netTx += ns.TxBytes
	}

	return &Stats{
		CPUPercent:    cpuPercent,
		MemoryUsage:   memUsage,
		MemoryLimit:   memLimit,
		MemoryPercent: memPercent,
		NetRxBytes:    netRx,
		NetTxBytes:    netTx,
		PIDs:          dockerStats.PidsStats.Current,
	}, nil
}

// DialAgent dials the agent via TCP.
func (r *DockerRuntime) DialAgent(_ context.Context, _ string, timeout time.Duration) (net.Conn, error) {
	// For Docker, we don't use instanceID — DialAgent is called after the
	// caller has the AgentAddr. This method exists for symmetry with
	// Firecracker where vsock requires the CID from instance metadata.
	// The daemon will use proxy.Dial(sbx.AgentAddr, timeout) directly for
	// Docker containers since it already has the address.
	return nil, fmt.Errorf("DockerRuntime.DialAgent: use proxy.Dial with AgentAddr directly")
}

// Close closes the Docker client.
func (r *DockerRuntime) Close() error {
	return r.Client.Close()
}

func ptrInt64(v int64) *int64 {
	return &v
}
