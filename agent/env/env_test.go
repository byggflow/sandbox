package env

import (
	"encoding/json"
	"testing"
)

func TestSetAndGet(t *testing.T) {
	s := NewStore()

	_, err := s.Set(json.RawMessage(`{"key":"FOO","value":"bar"}`))
	if err != nil {
		t.Fatalf("Set: %v", err)
	}

	result, err := s.Get(json.RawMessage(`{"key":"FOO"}`))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	m := result.(map[string]interface{})
	if m["value"] != "bar" {
		t.Errorf("value = %v, want bar", m["value"])
	}
}

func TestGetMissing(t *testing.T) {
	s := NewStore()

	result, err := s.Get(json.RawMessage(`{"key":"NOPE"}`))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	m := result.(map[string]interface{})
	if m["value"] != nil {
		t.Errorf("value = %v, want nil", m["value"])
	}
}

func TestDelete(t *testing.T) {
	s := NewStore()

	s.Set(json.RawMessage(`{"key":"X","value":"1"}`))
	s.Delete(json.RawMessage(`{"key":"X"}`))

	result, _ := s.Get(json.RawMessage(`{"key":"X"}`))
	m := result.(map[string]interface{})
	if m["value"] != nil {
		t.Errorf("value = %v, want nil after delete", m["value"])
	}
}

func TestList(t *testing.T) {
	s := NewStore()

	s.Set(json.RawMessage(`{"key":"A","value":"1"}`))
	s.Set(json.RawMessage(`{"key":"B","value":"2"}`))

	result, err := s.List(nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	m := result.(map[string]interface{})
	vars := m["vars"].(map[string]string)

	if vars["A"] != "1" || vars["B"] != "2" {
		t.Errorf("vars = %v, want A=1 B=2", vars)
	}
}

func TestSetEmptyKey(t *testing.T) {
	s := NewStore()

	_, err := s.Set(json.RawMessage(`{"key":"","value":"x"}`))
	if err == nil {
		t.Fatal("expected error for empty key")
	}
}

func TestGetEmptyKey(t *testing.T) {
	s := NewStore()

	_, err := s.Get(json.RawMessage(`{"key":""}`))
	if err == nil {
		t.Fatal("expected error for empty key")
	}
}
