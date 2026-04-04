package env

import (
	"encoding/json"
	"fmt"
	"sync"
)

// Store is a thread-safe in-memory key-value store for environment variables.
type Store struct {
	mu   sync.RWMutex
	vars map[string]string
}

// NewStore returns a new empty environment store.
func NewStore() *Store {
	return &Store{vars: make(map[string]string)}
}

// GetParams is the params for env.get.
type GetParams struct {
	Key string `json:"key"`
}

// SetParams is the params for env.set.
type SetParams struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// DeleteParams is the params for env.delete.
type DeleteParams struct {
	Key string `json:"key"`
}

// Get returns the value for a key, or nil if not set.
func (s *Store) Get(raw json.RawMessage) (interface{}, error) {
	var p GetParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.Key == "" {
		return nil, fmt.Errorf("key is required")
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	val, ok := s.vars[p.Key]
	if !ok {
		return map[string]interface{}{"value": nil}, nil
	}
	return map[string]interface{}{"value": val}, nil
}

// Set stores a key-value pair.
func (s *Store) Set(raw json.RawMessage) (interface{}, error) {
	var p SetParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.Key == "" {
		return nil, fmt.Errorf("key is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.vars[p.Key] = p.Value
	return map[string]interface{}{}, nil
}

// Delete removes a key.
func (s *Store) Delete(raw json.RawMessage) (interface{}, error) {
	var p DeleteParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}
	if p.Key == "" {
		return nil, fmt.Errorf("key is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.vars, p.Key)
	return map[string]interface{}{}, nil
}

// List returns all stored variables.
func (s *Store) List(_ json.RawMessage) (interface{}, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	cp := make(map[string]string, len(s.vars))
	for k, v := range s.vars {
		cp[k] = v
	}
	return map[string]interface{}{"vars": cp}, nil
}
