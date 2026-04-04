package daemon

import (
	"context"
	"fmt"
	"testing"
)

// fakeTemplateBackend implements TemplateBackend for testing.
type fakeTemplateBackend struct {
	captureRef  string
	captureSize int64
	captureErr  error
	removeErr   error
	removedRefs []string
}

func (f *fakeTemplateBackend) Capture(_ context.Context, containerID, tag string) (string, int64, error) {
	if f.captureErr != nil {
		return "", 0, f.captureErr
	}
	return f.captureRef, f.captureSize, nil
}

func (f *fakeTemplateBackend) Remove(_ context.Context, ref string) error {
	f.removedRefs = append(f.removedRefs, ref)
	return f.removeErr
}

func TestTemplateBackendInterface(t *testing.T) {
	// Verify fakeTemplateBackend satisfies the interface.
	var _ TemplateBackend = &fakeTemplateBackend{}

	// Verify DockerTemplateBackend satisfies the interface.
	var _ TemplateBackend = &DockerTemplateBackend{}
}

func TestFakeBackendCapture(t *testing.T) {
	backend := &fakeTemplateBackend{
		captureRef:  "byggflow-sandbox:tpl-abcd1234",
		captureSize: 50 * 1024 * 1024,
	}

	ref, size, err := backend.Capture(context.Background(), "container-123", "byggflow-sandbox:tpl-abcd1234")
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if ref != "byggflow-sandbox:tpl-abcd1234" {
		t.Errorf("ref = %q, want %q", ref, "byggflow-sandbox:tpl-abcd1234")
	}
	if size != 50*1024*1024 {
		t.Errorf("size = %d, want %d", size, 50*1024*1024)
	}
}

func TestFakeBackendCaptureError(t *testing.T) {
	backend := &fakeTemplateBackend{
		captureErr: fmt.Errorf("docker commit failed"),
	}

	_, _, err := backend.Capture(context.Background(), "container-123", "tag")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestFakeBackendRemove(t *testing.T) {
	backend := &fakeTemplateBackend{}

	err := backend.Remove(context.Background(), "byggflow-sandbox:tpl-abcd1234")
	if err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if len(backend.removedRefs) != 1 || backend.removedRefs[0] != "byggflow-sandbox:tpl-abcd1234" {
		t.Errorf("removedRefs = %v, want [byggflow-sandbox:tpl-abcd1234]", backend.removedRefs)
	}
}
