package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/byggflow/sandbox/internal/identity"
	"github.com/byggflow/sandbox/internal/proxy"
)

// SandboxState represents the lifecycle state of a sandbox.
type SandboxState string

const (
	StateCreating     SandboxState = "creating"
	StateRunning      SandboxState = "running"
	StateDisconnected SandboxState = "disconnected"
	StateStopping     SandboxState = "stopping"
	StateStopped      SandboxState = "stopped"
)

// Sandbox represents a running sandbox container.
type Sandbox struct {
	ID          string            `json:"id"`
	ContainerID string            `json:"container_id"`
	Image       string            `json:"image"`
	State       SandboxState      `json:"state"`
	Identity    identity.Identity `json:"-"`
	IdentityStr string            `json:"identity,omitempty"`
	AgentAddr   string            `json:"-"`
	AuthToken   string            `json:"-"` // Token for agent authentication.
	Created     time.Time         `json:"created"`
	TTL         int               `json:"ttl"`
	Memory      int64             `json:"-"`
	CPU         float64           `json:"-"`
	Profile     string            `json:"profile,omitempty"`
	Template    string            `json:"template,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`

	// Session tracking.
	Session *proxy.Session `json:"-"`

	// TTL reconnection fields.
	DisconnectedAt time.Time `json:"-"`
	reaperCancel   chan struct{}

	// Notification buffer for disconnected state.
	Buffer *NotificationBuffer `json:"-"`

	// Agent connection kept alive during disconnect for buffering.
	Agent *proxy.AgentConn `json:"-"`

	// Active port tunnels keyed by container port.
	Tunnels map[int]*Tunnel `json:"-"`

	mu sync.Mutex
}

// SandboxInfo is the JSON-serializable sandbox information returned by the API.
type SandboxInfo struct {
	ID       string            `json:"id"`
	Image    string            `json:"image"`
	State    SandboxState      `json:"state"`
	Created  time.Time         `json:"created"`
	TTL      int               `json:"ttl"`
	Profile  string            `json:"profile,omitempty"`
	Template string            `json:"template,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
}

// Info returns the public API representation of the sandbox.
func (s *Sandbox) Info() SandboxInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return SandboxInfo{
		ID:       s.ID,
		Image:    s.Image,
		State:    s.State,
		Created:  s.Created,
		TTL:      s.TTL,
		Profile:  s.Profile,
		Template: s.Template,
		Labels:   s.Labels,
	}
}

// SetState atomically sets the sandbox state.
func (s *Sandbox) SetState(state SandboxState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.State = state
}

// GetState atomically gets the sandbox state.
func (s *Sandbox) GetState() SandboxState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.State
}

// SetSession atomically sets the active session, returning the old one.
func (s *Sandbox) SetSession(session *proxy.Session) *proxy.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.Session
	s.Session = session
	return old
}

// GetSession atomically gets the active session.
func (s *Sandbox) GetSession() *proxy.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Session
}

// StartReaper starts a goroutine that will call destroyFn after TTL expires.
// Returns immediately. Can be cancelled by calling CancelReaper.
func (s *Sandbox) StartReaper(ttl time.Duration, destroyFn func()) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Cancel any existing reaper.
	if s.reaperCancel != nil {
		close(s.reaperCancel)
	}

	cancel := make(chan struct{})
	s.reaperCancel = cancel

	go func() {
		timer := time.NewTimer(ttl)
		defer timer.Stop()
		select {
		case <-timer.C:
			destroyFn()
		case <-cancel:
			// Reaper cancelled (reconnect happened).
		}
	}()
}

// CancelReaper cancels the TTL reaper if one is running.
func (s *Sandbox) CancelReaper() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.reaperCancel != nil {
		close(s.reaperCancel)
		s.reaperCancel = nil
	}
}

// Registry manages active sandboxes.
type Registry struct {
	mu        sync.RWMutex
	sandboxes map[string]*Sandbox
}

// NewRegistry creates a new sandbox registry.
func NewRegistry() *Registry {
	return &Registry{
		sandboxes: make(map[string]*Sandbox),
	}
}

// Add registers a sandbox. Returns an error if the ID already exists.
func (r *Registry) Add(sbx *Sandbox) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.sandboxes[sbx.ID]; exists {
		return fmt.Errorf("sandbox %s already exists", sbx.ID)
	}
	r.sandboxes[sbx.ID] = sbx
	return nil
}

// Get retrieves a sandbox by ID.
func (r *Registry) Get(id string) (*Sandbox, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sbx, ok := r.sandboxes[id]
	return sbx, ok
}

// Remove removes a sandbox from the registry.
func (r *Registry) Remove(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.sandboxes, id)
}

// List returns all sandboxes matching the given identity.
// If the identity is empty (single-user mode), all sandboxes are returned.
func (r *Registry) List(id identity.Identity) []*Sandbox {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []*Sandbox
	for _, sbx := range r.sandboxes {
		if id.Empty() || sbx.Identity.Matches(id) {
			result = append(result, sbx)
		}
	}
	return result
}

// CountByIdentity returns the number of sandboxes belonging to the given identity.
func (r *Registry) CountByIdentity(identity string) int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	count := 0
	for _, sbx := range r.sandboxes {
		if sbx.IdentityStr == identity {
			count++
		}
	}
	return count
}

// Count returns the total number of registered sandboxes.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.sandboxes)
}

// All returns every sandbox in the registry.
func (r *Registry) All() []*Sandbox {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*Sandbox, 0, len(r.sandboxes))
	for _, sbx := range r.sandboxes {
		result = append(result, sbx)
	}
	return result
}

// ContainerIP extracts the container IP from AgentAddr (strips the :port suffix).
func (s *Sandbox) ContainerIP() string {
	host, _, err := net.SplitHostPort(s.AgentAddr)
	if err != nil {
		return s.AgentAddr
	}
	return host
}

// GenerateID creates a new sandbox ID. If nodeID is non-empty, the format is
// sbx-{nodeID}-{8 hex}, otherwise sbx-{8 hex}. Embedding the node ID in the
// sandbox ID lets a routing proxy parse the target node from the ID alone,
// without a lookup table.
func GenerateID(nodeID string) (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate sandbox id: %w", err)
	}
	if nodeID != "" {
		return "sbx-" + nodeID + "-" + hex.EncodeToString(b), nil
	}
	return "sbx-" + hex.EncodeToString(b), nil
}

// GenerateAuthToken creates a random 32-byte hex token for agent authentication.
func GenerateAuthToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate auth token: %w", err)
	}
	return hex.EncodeToString(b), nil
}
