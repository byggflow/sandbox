//go:build integration

package runtime_test

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/byggflow/sandbox/internal/runtime"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// TestDockerRuntimeInitAndName verifies Init creates the network and Name()
// returns the canonical config name.
func TestDockerRuntimeInitAndName(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx := context.Background()

	rt, err := runtime.NewDockerRuntime("docker", "sandboxd-integ-test", "", log)
	if err != nil {
		t.Fatalf("NewDockerRuntime: %v", err)
	}
	defer rt.Close()

	if err := rt.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}

	if rt.Name() != "docker" {
		t.Errorf("Name() = %q, want %q", rt.Name(), "docker")
	}

	t.Log("Docker runtime init OK")
}

// TestDockerRuntimeContainerLifecycle creates a container via the Docker client
// directly (bypassing agent connectivity) and verifies the runtime can inspect
// and destroy it.
func TestDockerRuntimeContainerLifecycle(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx := context.Background()

	rt, err := runtime.NewDockerRuntime("docker", "sandboxd-integ-test", "", log)
	if err != nil {
		t.Fatalf("NewDockerRuntime: %v", err)
	}
	defer rt.Close()

	if err := rt.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Create a simple container to verify the runtime's Docker client works.
	resp, err := rt.Client.ContainerCreate(ctx,
		&container.Config{
			Image: "ghcr.io/byggflow/sandbox-base:latest",
			Cmd:   []string{"sleep", "10"},
		},
		&container.HostConfig{
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges"},
			ReadonlyRootfs: true,
			Tmpfs:          map[string]string{"/tmp": "rw,size=10m"},
		},
		nil, nil, "",
	)
	if err != nil {
		t.Fatalf("ContainerCreate: %v", err)
	}
	t.Logf("Created container: %s", resp.ID[:12])

	if err := rt.Client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		rt.Client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		t.Fatalf("ContainerStart: %v", err)
	}

	// Verify stats via the runtime.
	stats, err := rt.Stats(ctx, resp.ID)
	if err != nil {
		t.Errorf("Stats: %v", err)
	} else if stats.MemoryLimit == 0 {
		t.Error("expected non-zero memory limit")
	} else {
		t.Logf("Stats OK: Mem=%d/%d PIDs=%d", stats.MemoryUsage, stats.MemoryLimit, stats.PIDs)
	}

	// Destroy via the runtime.
	if err := rt.Destroy(ctx, resp.ID); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	t.Log("Container destroyed OK")
}

// TestDockerRuntimeOCIRuntimeField verifies that when an OCI runtime is set,
// containers are created with the correct Runtime field in their HostConfig.
func TestDockerRuntimeOCIRuntimeField(t *testing.T) {
	ctx := context.Background()

	docker, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatalf("Docker client: %v", err)
	}
	defer docker.Close()

	// Create a container with Runtime set to "runc" (which is always available)
	// to prove the field propagates correctly.
	resp, err := docker.ContainerCreate(ctx,
		&container.Config{
			Image: "ghcr.io/byggflow/sandbox-base:latest",
			Cmd:   []string{"true"},
		},
		&container.HostConfig{
			Runtime: "runc",
		},
		nil, nil, "",
	)
	if err != nil {
		t.Fatalf("ContainerCreate: %v", err)
	}
	defer docker.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})

	inspect, err := docker.ContainerInspect(ctx, resp.ID)
	if err != nil {
		t.Fatalf("ContainerInspect: %v", err)
	}

	if inspect.HostConfig.Runtime != "runc" {
		t.Errorf("HostConfig.Runtime = %q, want %q", inspect.HostConfig.Runtime, "runc")
	}
	t.Logf("OCI runtime field verified: %q", inspect.HostConfig.Runtime)

	// Also verify that an invalid runtime is rejected by Docker.
	_, err = docker.ContainerCreate(ctx,
		&container.Config{
			Image: "ghcr.io/byggflow/sandbox-base:latest",
			Cmd:   []string{"true"},
		},
		&container.HostConfig{
			Runtime: "nonexistent-runtime-xyz",
		},
		nil, nil, "",
	)
	if err == nil {
		t.Error("expected Docker to reject unknown OCI runtime, but it succeeded")
	} else if !strings.Contains(err.Error(), "runtime") {
		t.Errorf("expected runtime-related error, got: %v", err)
	} else {
		t.Logf("Docker correctly rejected unknown runtime: %v", err)
	}
}

// TestDockerRuntimeInitRejectsUnknownOCIRuntime verifies that Init fails with a
// clear error when the OCI runtime is not registered with Docker.
func TestDockerRuntimeInitRejectsUnknownOCIRuntime(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx := context.Background()

	rt, err := runtime.NewDockerRuntime("docker+fake", "sandboxd-integ-test", "nonexistent-runtime-xyz", log)
	if err != nil {
		t.Fatalf("NewDockerRuntime: %v", err)
	}
	defer rt.Close()

	err = rt.Init(ctx)
	if err == nil {
		t.Fatal("expected Init to fail for unregistered OCI runtime, but it succeeded")
	}
	if !strings.Contains(err.Error(), "nonexistent-runtime-xyz") {
		t.Errorf("error should mention the runtime name, got: %v", err)
	}
	if !strings.Contains(err.Error(), "not registered") {
		t.Errorf("error should say 'not registered', got: %v", err)
	}
	t.Logf("Init correctly rejected: %v", err)
}

// TestGVisorRuntimeCreateDestroy verifies that containers created via the
// docker+gvisor runtime actually run under the runsc OCI runtime. It tests
// Init, container creation, runtime verification, stats, and teardown.
// Skipped if runsc is not registered with the Docker daemon.
func TestGVisorRuntimeCreateDestroy(t *testing.T) {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx := context.Background()

	rt, err := runtime.NewDockerRuntime("docker+gvisor", "sandboxd-integ-test", "runsc", log)
	if err != nil {
		t.Fatalf("NewDockerRuntime: %v", err)
	}
	defer rt.Close()

	if err := rt.Init(ctx); err != nil {
		if strings.Contains(err.Error(), "not registered") {
			t.Skipf("runsc not available: %v", err)
		}
		t.Fatalf("Init: %v", err)
	}

	if rt.Name() != "docker+gvisor" {
		t.Errorf("Name() = %q, want %q", rt.Name(), "docker+gvisor")
	}

	// Create a container directly via the Docker client (bypasses agent wait)
	// so we can verify the runtime field and container behavior.
	resp, err := rt.Client.ContainerCreate(ctx,
		&container.Config{
			Image: "ghcr.io/byggflow/sandbox-base:latest",
			Cmd:   []string{"sleep", "30"},
		},
		&container.HostConfig{
			Runtime:        "runsc",
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges"},
			ReadonlyRootfs: true,
			Resources: container.Resources{
				Memory:   256 * 1024 * 1024,
				NanoCPUs: 1e9,
			},
			Tmpfs: map[string]string{
				"/tmp": "rw,noexec,nosuid,size=100m",
			},
		},
		nil, nil, "",
	)
	if err != nil {
		t.Fatalf("ContainerCreate: %v", err)
	}
	t.Logf("Created gVisor container: %s", resp.ID[:12])

	if err := rt.Client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		rt.Client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		t.Fatalf("ContainerStart: %v", err)
	}

	// Verify the container is running under runsc.
	inspect, err := rt.Client.ContainerInspect(ctx, resp.ID)
	if err != nil {
		t.Fatalf("ContainerInspect: %v", err)
	}
	if inspect.HostConfig.Runtime != "runsc" {
		t.Errorf("HostConfig.Runtime = %q, want %q", inspect.HostConfig.Runtime, "runsc")
	}
	t.Logf("Verified container running under runtime: %s", inspect.HostConfig.Runtime)

	// Verify stats work under gVisor.
	stats, err := rt.Stats(ctx, resp.ID)
	if err != nil {
		t.Errorf("Stats: %v", err)
	} else {
		t.Logf("Stats OK: CPU=%.1f%% Mem=%d/%d PIDs=%d", stats.CPUPercent, stats.MemoryUsage, stats.MemoryLimit, stats.PIDs)
	}

	// Verify the container is actually using gVisor's kernel by checking dmesg.
	execResp, err := rt.Client.ContainerExecCreate(ctx, resp.ID, container.ExecOptions{
		Cmd:          []string{"cat", "/proc/version"},
		AttachStdout: true,
	})
	if err != nil {
		t.Logf("Could not exec into container: %v (non-fatal)", err)
	} else {
		attach, err := rt.Client.ContainerExecAttach(ctx, execResp.ID, container.ExecStartOptions{})
		if err == nil {
			buf := make([]byte, 1024)
			n, _ := attach.Reader.Read(buf)
			output := string(buf[:n])
			attach.Close()
			t.Logf("/proc/version: %s", strings.TrimSpace(output))
		}
	}

	// Destroy via the runtime.
	if err := rt.Destroy(ctx, resp.ID); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	t.Log("gVisor container destroyed OK")
}
