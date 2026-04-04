package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/byggflow/sandbox/internal/identity"
)

// Template represents a saved sandbox template (committed Docker image).
type Template struct {
	ID         string    `json:"id"`
	Label      string    `json:"label"`
	Image      string    `json:"image"`
	Identity   string    `json:"identity,omitempty"`
	Size       int64     `json:"size"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used,omitempty"`
}

// TemplateRegistry manages template metadata in a thread-safe manner.
type TemplateRegistry struct {
	mu        sync.RWMutex
	templates map[string]*Template
}

// NewTemplateRegistry creates a new template registry.
func NewTemplateRegistry() *TemplateRegistry {
	return &TemplateRegistry{
		templates: make(map[string]*Template),
	}
}

// Add registers a template. Returns an error if the ID already exists.
func (r *TemplateRegistry) Add(tpl *Template) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.templates[tpl.ID]; exists {
		return fmt.Errorf("template %s already exists", tpl.ID)
	}
	r.templates[tpl.ID] = tpl
	return nil
}

// Get retrieves a template by ID.
func (r *TemplateRegistry) Get(id string) (*Template, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tpl, ok := r.templates[id]
	return tpl, ok
}

// Remove removes a template from the registry. Returns the template if found.
func (r *TemplateRegistry) Remove(id string) (*Template, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	tpl, ok := r.templates[id]
	if ok {
		delete(r.templates, id)
	}
	return tpl, ok
}

// List returns all templates matching the given identity.
// If the identity is empty (single-user mode), all templates are returned.
func (r *TemplateRegistry) List(id identity.Identity) []*Template {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*Template
	for _, tpl := range r.templates {
		if id.Empty() || tpl.Identity == id.Value {
			result = append(result, tpl)
		}
	}
	return result
}

// CountByIdentity returns the number of templates belonging to the given identity.
func (r *TemplateRegistry) CountByIdentity(ident string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	count := 0
	for _, tpl := range r.templates {
		if tpl.Identity == ident {
			count++
		}
	}
	return count
}

// Count returns the total number of registered templates.
func (r *TemplateRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.templates)
}

// All returns a snapshot of all templates in the registry.
func (r *TemplateRegistry) All() []*Template {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]*Template, 0, len(r.templates))
	for _, tpl := range r.templates {
		result = append(result, tpl)
	}
	return result
}

// MarkUsed updates the LastUsedAt timestamp for a template.
func (r *TemplateRegistry) MarkUsed(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if tpl, ok := r.templates[id]; ok {
		tpl.LastUsedAt = time.Now()
	}
}

// GenerateTemplateID creates a new template ID in the format tpl-{8 hex chars}.
func GenerateTemplateID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate template id: %w", err)
	}
	return "tpl-" + hex.EncodeToString(b), nil
}
