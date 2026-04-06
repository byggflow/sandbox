package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/byggflow/sandbox/internal/config"
	"github.com/byggflow/sandbox/internal/proxy"
)

// FirecrackerRuntime implements Runtime using Firecracker microVMs.
type FirecrackerRuntime struct {
	cfg config.FirecrackerConfig
	log *slog.Logger

	nextCID   atomic.Uint32
	mu        sync.Mutex
	instances map[string]*vmInstance
	cidToID   map[uint32]string
}

type vmInstance struct {
	ID         string
	CID        uint32
	cmd        *exec.Cmd
	SocketPath string // API socket
	VsockPath  string // Vsock UDS
	RootFSPath string
	ConfigPath string
	AgentAddr  string
}

// firecrackerConfig is the JSON configuration passed to the Firecracker binary.
type firecrackerConfig struct {
	BootSource    fcBootSource    `json:"boot-source"`
	Drives        []fcDrive       `json:"drives"`
	MachineConfig fcMachineConfig `json:"machine-config"`
	Vsock         *fcVsock        `json:"vsock,omitempty"`
}

type fcBootSource struct {
	KernelImagePath string `json:"kernel_image_path"`
	BootArgs        string `json:"boot_args"`
}

type fcDrive struct {
	DriveID      string `json:"drive_id"`
	PathOnHost   string `json:"path_on_host"`
	IsRootDevice bool   `json:"is_root_device"`
	IsReadOnly   bool   `json:"is_read_only"`
}

type fcMachineConfig struct {
	VCPUCount  int `json:"vcpu_count"`
	MemSizeMiB int `json:"mem_size_mib"`
}

type fcVsock struct {
	GuestCID uint32 `json:"guest_cid"`
	UDSPath  string `json:"uds_path"`
}

// NewFirecrackerRuntime creates a new Firecracker runtime.
func NewFirecrackerRuntime(cfg config.FirecrackerConfig, log *slog.Logger) *FirecrackerRuntime {
	rt := &FirecrackerRuntime{
		cfg:       cfg,
		log:       log,
		instances: make(map[string]*vmInstance),
		cidToID:   make(map[uint32]string),
	}
	cidBase := cfg.VsockCIDBase
	if cidBase < 3 {
		cidBase = 100 // CIDs 0-2 are reserved.
	}
	rt.nextCID.Store(cidBase)
	return rt
}

func (r *FirecrackerRuntime) Name() string { return "firecracker" }

// Init validates that the Firecracker binary and kernel exist.
func (r *FirecrackerRuntime) Init(_ context.Context) error {
	if _, err := os.Stat(r.cfg.BinaryPath); err != nil {
		return fmt.Errorf("firecracker binary not found at %s: %w", r.cfg.BinaryPath, err)
	}
	if _, err := os.Stat(r.cfg.KernelPath); err != nil {
		return fmt.Errorf("kernel not found at %s: %w", r.cfg.KernelPath, err)
	}
	if r.cfg.RootFSDir != "" {
		if _, err := os.Stat(r.cfg.RootFSDir); err != nil {
			return fmt.Errorf("rootfs dir not found at %s: %w", r.cfg.RootFSDir, err)
		}
	}
	r.log.Info("firecracker runtime initialized",
		"binary", r.cfg.BinaryPath,
		"kernel", r.cfg.KernelPath,
	)
	return nil
}

// Create launches a Firecracker microVM with the given options.
func (r *FirecrackerRuntime) Create(ctx context.Context, opts CreateOpts) (*Instance, error) {
	cid := r.nextCID.Add(1)

	vmID := fmt.Sprintf("fc-%d", cid)

	// Resolve rootfs image path.
	rootfsPath := opts.Image
	if r.cfg.RootFSDir != "" && !filepath.IsAbs(rootfsPath) {
		rootfsPath = filepath.Join(r.cfg.RootFSDir, rootfsPath)
	}

	// Create a copy-on-write overlay of the rootfs for this VM.
	cowPath := rootfsPath + fmt.Sprintf(".cow.%d", cid)
	if err := copyFile(rootfsPath, cowPath); err != nil {
		return nil, fmt.Errorf("create rootfs overlay: %w", err)
	}

	// Set up vsock UDS path. Remove stale sockets from prior runs.
	socketDir := os.TempDir()
	apiSocket := filepath.Join(socketDir, fmt.Sprintf("fc-%d-api.sock", cid))
	vsockUDS := filepath.Join(socketDir, fmt.Sprintf("fc-%d-vsock.sock", cid))
	os.Remove(apiSocket)
	os.Remove(vsockUDS)

	// Memory in MiB.
	memMiB := int(opts.Memory / (1024 * 1024))
	if memMiB < 128 {
		memMiB = 128
	}

	vcpus := int(opts.CPU)
	if vcpus < 1 {
		vcpus = 1
	}

	// Build boot args.
	bootArgs := "console=ttyS0 reboot=k panic=1 pci=off init=/init"
	if opts.AuthToken != "" {
		bootArgs += " sandbox.auth_token=" + opts.AuthToken
	}

	// Write Firecracker config.
	fcCfg := firecrackerConfig{
		BootSource: fcBootSource{
			KernelImagePath: r.cfg.KernelPath,
			BootArgs:        bootArgs,
		},
		Drives: []fcDrive{
			{
				DriveID:      "rootfs",
				PathOnHost:   cowPath,
				IsRootDevice: true,
				IsReadOnly:   false,
			},
		},
		MachineConfig: fcMachineConfig{
			VCPUCount:  vcpus,
			MemSizeMiB: memMiB,
		},
		Vsock: &fcVsock{
			GuestCID: cid,
			UDSPath:  vsockUDS,
		},
	}

	cfgPath := filepath.Join(socketDir, fmt.Sprintf("fc-%d-config.json", cid))
	cfgData, err := json.Marshal(fcCfg)
	if err != nil {
		return nil, fmt.Errorf("marshal firecracker config: %w", err)
	}
	if err := os.WriteFile(cfgPath, cfgData, 0600); err != nil {
		return nil, fmt.Errorf("write firecracker config: %w", err)
	}

	// Start Firecracker process.
	cmd := exec.CommandContext(ctx, r.cfg.BinaryPath,
		"--api-sock", apiSocket,
		"--config-file", cfgPath,
	)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		os.Remove(cowPath)
		os.Remove(cfgPath)
		return nil, fmt.Errorf("start firecracker: %w", err)
	}

	// Reap the process in the background to avoid zombies and release
	// the goroutine that exec.CommandContext uses for cancellation.
	go cmd.Wait()

	agentAddr := fmt.Sprintf("vsock:%d:9111", cid)

	vm := &vmInstance{
		ID:         vmID,
		CID:        cid,
		cmd:        cmd,
		SocketPath: apiSocket,
		VsockPath:  vsockUDS,
		RootFSPath: cowPath,
		ConfigPath: cfgPath,
		AgentAddr:  agentAddr,
	}

	r.mu.Lock()
	r.instances[vmID] = vm
	r.cidToID[cid] = vmID
	r.mu.Unlock()

	// Wait for agent to become reachable via vsock.
	var lastErr error
	for attempt := 0; attempt < 30; attempt++ {
		conn, dialErr := r.dialVsock(cid, 9111, 2*time.Second)
		if dialErr == nil {
			agent := proxy.Wrap(conn)
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
				return &Instance{ID: vmID, AgentAddr: agentAddr}, nil
			}
			agent.Close()
		}
		lastErr = dialErr
		time.Sleep(100 * time.Millisecond)
	}

	// Cleanup on failure.
	_ = r.Destroy(ctx, vmID)
	return nil, fmt.Errorf("agent not reachable via vsock CID %d: %w", cid, lastErr)
}

// Destroy stops a Firecracker microVM and cleans up its resources.
func (r *FirecrackerRuntime) Destroy(_ context.Context, instanceID string) error {
	r.mu.Lock()
	vm, ok := r.instances[instanceID]
	if ok {
		delete(r.instances, instanceID)
		delete(r.cidToID, vm.CID)
	}
	r.mu.Unlock()

	if !ok {
		return fmt.Errorf("unknown instance: %s", instanceID)
	}

	// Kill the Firecracker process. The background cmd.Wait() goroutine
	// started in Create will reap the zombie once the signal is delivered.
	if vm.cmd.Process != nil {
		_ = vm.cmd.Process.Kill()
	}

	// Clean up files.
	os.Remove(vm.SocketPath)
	os.Remove(vm.VsockPath)
	os.Remove(vm.RootFSPath)
	os.Remove(vm.ConfigPath)

	r.log.Info("firecracker VM destroyed", "id", instanceID, "cid", vm.CID)
	return nil
}

// Stats returns resource usage for a Firecracker microVM.
// For now, this queries /proc/{pid}/stat for basic metrics.
func (r *FirecrackerRuntime) Stats(_ context.Context, instanceID string) (*Stats, error) {
	r.mu.Lock()
	vm, ok := r.instances[instanceID]
	r.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("unknown instance: %s", instanceID)
	}

	// Read basic stats from /proc.
	statPath := fmt.Sprintf("/proc/%d/stat", vm.cmd.Process.Pid)
	data, err := os.ReadFile(statPath)
	if err != nil {
		return nil, fmt.Errorf("read proc stat: %w", err)
	}

	// Parse basic fields from /proc/[pid]/stat.
	fields := splitProcStat(string(data))
	_ = fields // Basic implementation; full parsing would extract CPU/memory.

	return &Stats{}, nil
}

// DialAgent connects to the agent inside a Firecracker microVM via vsock.
func (r *FirecrackerRuntime) DialAgent(_ context.Context, instanceID string, timeout time.Duration) (net.Conn, error) {
	r.mu.Lock()
	vm, ok := r.instances[instanceID]
	r.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("unknown instance: %s", instanceID)
	}

	return r.dialVsock(vm.CID, 9111, timeout)
}

// Close cleans up all running VMs.
func (r *FirecrackerRuntime) Close() error {
	r.mu.Lock()
	ids := make([]string, 0, len(r.instances))
	for id := range r.instances {
		ids = append(ids, id)
	}
	r.mu.Unlock()

	ctx := context.Background()
	for _, id := range ids {
		if err := r.Destroy(ctx, id); err != nil {
			r.log.Error("failed to destroy VM on close", "id", id, "error", err)
		}
	}
	return nil
}

// dialVsock connects to a guest vsock port via Firecracker's UDS proxy.
// Firecracker exposes a Unix domain socket; the host connects to it and sends
// "CONNECT <port>\n". Firecracker responds with "OK <local_port>\n" and then
// proxies the connection to the guest's vsock listener.
func (r *FirecrackerRuntime) dialVsock(cid uint32, port uint32, timeout time.Duration) (net.Conn, error) {
	// Look up the vsock UDS path via CID → ID → instance.
	r.mu.Lock()
	var udsPath string
	if vmID, ok := r.cidToID[cid]; ok {
		if vm, ok := r.instances[vmID]; ok {
			udsPath = vm.VsockPath
		}
	}
	r.mu.Unlock()

	if udsPath == "" {
		return nil, fmt.Errorf("no vsock UDS path for CID %d", cid)
	}

	conn, err := net.DialTimeout("unix", udsPath, timeout)
	if err != nil {
		return nil, fmt.Errorf("connect to vsock UDS: %w", err)
	}

	// Send CONNECT command.
	connectCmd := fmt.Sprintf("CONNECT %d\n", port)
	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("set deadline: %w", err)
	}
	if _, err := conn.Write([]byte(connectCmd)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send CONNECT: %w", err)
	}

	// Read response line (e.g. "OK 1073741824\n").
	buf := make([]byte, 64)
	n, err := conn.Read(buf)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read CONNECT response: %w", err)
	}
	resp := string(buf[:n])
	if len(resp) < 2 || resp[:2] != "OK" {
		conn.Close()
		return nil, fmt.Errorf("vsock CONNECT failed: %s", resp)
	}

	// Clear deadline — connection is now established.
	_ = conn.SetDeadline(time.Time{})

	return conn, nil
}

// copyFile creates a copy of src at dst. On Linux with reflink-capable
// filesystems, this will be a CoW copy.
func copyFile(src, dst string) error {
	// Try cp --reflink=auto first for CoW support.
	cmd := exec.Command("cp", "--reflink=auto", src, dst)
	if err := cmd.Run(); err != nil {
		// Fallback: standard copy.
		data, readErr := os.ReadFile(src)
		if readErr != nil {
			return fmt.Errorf("read %s: %w", src, readErr)
		}
		return os.WriteFile(dst, data, 0644)
	}
	return nil
}

// splitProcStat splits a /proc/[pid]/stat line into fields, handling the
// comm field which may contain spaces and parentheses.
func splitProcStat(s string) []string {
	// Find the last ')' to skip the comm field.
	idx := 0
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ')' {
			idx = i + 2 // skip ") "
			break
		}
	}
	// PID and comm are the first two fields.
	var fields []string
	fields = append(fields, s[:idx])
	rest := s[idx:]
	for _, f := range splitWhitespace(rest) {
		fields = append(fields, f)
	}
	return fields
}

func splitWhitespace(s string) []string {
	var result []string
	start := -1
	for i, c := range s {
		if c == ' ' || c == '\t' || c == '\n' {
			if start >= 0 {
				result = append(result, s[start:i])
				start = -1
			}
		} else {
			if start < 0 {
				start = i
			}
		}
	}
	if start >= 0 {
		result = append(result, s[start:])
	}
	return result
}

// VsockAddr formats a vsock address string.
func VsockAddr(cid, port uint32) string {
	return "vsock:" + strconv.FormatUint(uint64(cid), 10) + ":" + strconv.FormatUint(uint64(port), 10)
}
