package runtime

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// FirecrackerTemplateBackend implements TemplateBackend for Firecracker
// by snapshotting the VM's rootfs disk state.
type FirecrackerTemplateBackend struct {
	rt      *FirecrackerRuntime
	dataDir string // Directory to store snapshot files.
}

// NewFirecrackerTemplateBackend creates a template backend for Firecracker VMs.
func NewFirecrackerTemplateBackend(rt *FirecrackerRuntime, dataDir string) *FirecrackerTemplateBackend {
	return &FirecrackerTemplateBackend{rt: rt, dataDir: dataDir}
}

// Capture creates a template by copying the VM's current rootfs state.
// The tag is used as the snapshot filename.
func (b *FirecrackerTemplateBackend) Capture(_ context.Context, instanceID, tag string) (string, int64, error) {
	b.rt.mu.Lock()
	vm, ok := b.rt.instances[instanceID]
	b.rt.mu.Unlock()

	if !ok {
		return "", 0, fmt.Errorf("unknown instance: %s", instanceID)
	}

	// Create snapshot directory if needed.
	snapshotDir := filepath.Join(b.dataDir, "snapshots")
	if err := os.MkdirAll(snapshotDir, 0755); err != nil {
		return "", 0, fmt.Errorf("create snapshot dir: %w", err)
	}

	// Copy the rootfs (which includes all writes via the CoW overlay).
	snapshotPath := filepath.Join(snapshotDir, tag+".rootfs")
	if err := copyFile(vm.RootFSPath, snapshotPath); err != nil {
		return "", 0, fmt.Errorf("snapshot rootfs: %w", err)
	}

	info, err := os.Stat(snapshotPath)
	if err != nil {
		return "", 0, fmt.Errorf("stat snapshot: %w", err)
	}

	return snapshotPath, info.Size(), nil
}

// Remove deletes a snapshot file.
func (b *FirecrackerTemplateBackend) Remove(_ context.Context, ref string) error {
	return os.Remove(ref)
}
