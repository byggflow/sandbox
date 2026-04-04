package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/byggflow/sandbox/internal/identity"
)

func TestGenerateTemplateID(t *testing.T) {
	id, err := GenerateTemplateID()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.HasPrefix(id, "tpl-") {
		t.Errorf("expected prefix tpl-, got %s", id)
	}
	if len(id) != 12 { // "tpl-" + 8 hex chars
		t.Errorf("expected length 12, got %d (%s)", len(id), id)
	}

	// Uniqueness check.
	id2, err := GenerateTemplateID()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if id == id2 {
		t.Error("expected unique IDs")
	}
}

func TestTemplateRegistryAdd(t *testing.T) {
	reg := NewTemplateRegistry()

	tpl := &Template{
		ID:        "tpl-aabbccdd",
		Label:     "test",
		Image:     "byggflow-sandbox:tpl-aabbccdd",
		Identity:  "user1",
		Size:      1024,
		CreatedAt: time.Now(),
	}

	if err := reg.Add(tpl); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Duplicate add should fail.
	if err := reg.Add(tpl); err == nil {
		t.Error("expected error for duplicate template")
	}
}

func TestTemplateRegistryGetAndRemove(t *testing.T) {
	reg := NewTemplateRegistry()

	tpl := &Template{
		ID:    "tpl-11223344",
		Label: "test",
		Image: "img:latest",
	}

	reg.Add(tpl)

	got, ok := reg.Get("tpl-11223344")
	if !ok {
		t.Fatal("expected to find template")
	}
	if got.Label != "test" {
		t.Errorf("expected label test, got %s", got.Label)
	}

	// Non-existent.
	_, ok = reg.Get("tpl-notexist")
	if ok {
		t.Error("expected not found")
	}

	// Remove.
	removed, ok := reg.Remove("tpl-11223344")
	if !ok {
		t.Fatal("expected to remove template")
	}
	if removed.ID != "tpl-11223344" {
		t.Error("removed wrong template")
	}

	_, ok = reg.Get("tpl-11223344")
	if ok {
		t.Error("expected template to be gone after remove")
	}
}

func TestTemplateRegistryList(t *testing.T) {
	reg := NewTemplateRegistry()

	reg.Add(&Template{ID: "tpl-a", Identity: "user1"})
	reg.Add(&Template{ID: "tpl-b", Identity: "user1"})
	reg.Add(&Template{ID: "tpl-c", Identity: "user2"})

	// List for user1.
	list := reg.List(identity.Identity{Value: "user1"})
	if len(list) != 2 {
		t.Errorf("expected 2 templates for user1, got %d", len(list))
	}

	// List for user2.
	list = reg.List(identity.Identity{Value: "user2"})
	if len(list) != 1 {
		t.Errorf("expected 1 template for user2, got %d", len(list))
	}

	// List with empty identity (all).
	list = reg.List(identity.Identity{})
	if len(list) != 3 {
		t.Errorf("expected 3 templates for empty identity, got %d", len(list))
	}
}

func TestTemplateRegistryMarkUsed(t *testing.T) {
	reg := NewTemplateRegistry()

	tpl := &Template{
		ID:        "tpl-used",
		CreatedAt: time.Now(),
	}
	reg.Add(tpl)

	reg.MarkUsed("tpl-used")

	got, _ := reg.Get("tpl-used")
	if got.LastUsedAt.IsZero() {
		t.Error("expected LastUsedAt to be set")
	}
}

func TestTemplateRegistryCount(t *testing.T) {
	reg := NewTemplateRegistry()

	if reg.Count() != 0 {
		t.Error("expected 0")
	}

	reg.Add(&Template{ID: "tpl-1"})
	reg.Add(&Template{ID: "tpl-2"})

	if reg.Count() != 2 {
		t.Errorf("expected 2, got %d", reg.Count())
	}
}

func TestTemplateRegistryCountByIdentity(t *testing.T) {
	reg := NewTemplateRegistry()

	reg.Add(&Template{ID: "tpl-1", Identity: "alice"})
	reg.Add(&Template{ID: "tpl-2", Identity: "alice"})
	reg.Add(&Template{ID: "tpl-3", Identity: "bob"})
	reg.Add(&Template{ID: "tpl-4", Identity: ""})

	if got := reg.CountByIdentity("alice"); got != 2 {
		t.Errorf("alice: expected 2, got %d", got)
	}
	if got := reg.CountByIdentity("bob"); got != 1 {
		t.Errorf("bob: expected 1, got %d", got)
	}
	if got := reg.CountByIdentity("nobody"); got != 0 {
		t.Errorf("nobody: expected 0, got %d", got)
	}

	// Remove one and recheck.
	reg.Remove("tpl-1")
	if got := reg.CountByIdentity("alice"); got != 1 {
		t.Errorf("after remove: alice expected 1, got %d", got)
	}
}
