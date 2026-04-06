//go:build integration

package runtime_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/byggflow/sandbox/internal/config"
	"github.com/byggflow/sandbox/internal/proxy"
	"github.com/byggflow/sandbox/internal/runtime"
	"log/slog"
)

func TestFirecrackerIntegration(t *testing.T) {
	// Skip if not on Linux with KVM.
	if _, err := os.Stat("/dev/kvm"); err != nil {
		t.Skip("no /dev/kvm, skipping Firecracker test")
	}
	if _, err := os.Stat("/usr/local/bin/firecracker"); err != nil {
		t.Skip("firecracker binary not found")
	}
	if _, err := os.Stat("/var/lib/sandboxd/kernels/vmlinux"); err != nil {
		t.Skip("vmlinux kernel not found")
	}
	if _, err := os.Stat("/var/lib/sandboxd/rootfs/sandbox-base.rootfs"); err != nil {
		t.Skip("sandbox-base rootfs not found")
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	cfg := config.FirecrackerConfig{
		BinaryPath:   "/usr/local/bin/firecracker",
		KernelPath:   "/var/lib/sandboxd/kernels/vmlinux",
		RootFSDir:    "/var/lib/sandboxd/rootfs",
		VsockCIDBase: 200,
	}

	rt := runtime.NewFirecrackerRuntime(cfg, log)
	ctx := context.Background()

	// Init
	if err := rt.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Create VM
	inst, err := rt.Create(ctx, runtime.CreateOpts{
		Image:   "sandbox-base.rootfs",
		Memory:  256 * 1024 * 1024,
		CPU:     1,
		Storage: "500m",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Logf("Created VM: ID=%s AgentAddr=%s", inst.ID, inst.AgentAddr)

	// DialAgent + Ping
	conn, err := rt.DialAgent(ctx, inst.ID, 5*time.Second)
	if err != nil {
		rt.Destroy(ctx, inst.ID)
		t.Fatalf("DialAgent: %v", err)
	}
	agent := proxy.Wrap(conn)
	if err := agent.Ping(2 * time.Second); err != nil {
		agent.Close()
		rt.Destroy(ctx, inst.ID)
		t.Fatalf("Ping: %v", err)
	}
	agent.Close()
	t.Log("Ping OK via vsock")

	// Destroy
	if err := rt.Destroy(ctx, inst.ID); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	t.Log("VM destroyed successfully")
}
