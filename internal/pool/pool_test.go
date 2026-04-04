package pool

import (
	"testing"
	"time"

	"github.com/byggflow/sandbox/internal/config"
)

func TestRecordCreationAndFrequency(t *testing.T) {
	m := &Manager{
		cfg: config.PoolConfig{
			TotalWarm:       10,
			MinBase:         2,
			MaxWarm:         20,
			RebalanceWindow: "1h",
			HealthInterval:  "10s",
			LivenessTimeout: "5ms",
			Base: map[string]config.BaseImageConfig{
				"default": {Image: "test:latest", Memory: "512m", CPU: 1.0},
			},
		},
		warm:  make(map[string][]*WarmContainer),
		freq:  make(map[string][]time.Time),
		close: make(chan struct{}),
	}

	m.RecordCreation("test:latest")
	m.RecordCreation("test:latest")
	m.RecordCreation("other:latest")

	m.mu.Lock()
	defer m.mu.Unlock()

	if len(m.freq["test:latest"]) != 2 {
		t.Errorf("expected 2 creations for test:latest, got %d", len(m.freq["test:latest"]))
	}
	if len(m.freq["other:latest"]) != 1 {
		t.Errorf("expected 1 creation for other:latest, got %d", len(m.freq["other:latest"]))
	}
}

func TestClaimEmpty(t *testing.T) {
	m := &Manager{
		cfg: config.PoolConfig{
			LivenessTimeout: "5ms",
		},
		warm:  make(map[string][]*WarmContainer),
		freq:  make(map[string][]time.Time),
		close: make(chan struct{}),
	}

	_, ok := m.Claim("nonexistent:latest")
	if ok {
		t.Error("expected no warm container for nonexistent image")
	}
}

func TestStatusesEmpty(t *testing.T) {
	m := &Manager{
		cfg: config.PoolConfig{
			Base: map[string]config.BaseImageConfig{
				"default": {Image: "test:latest", Memory: "512m", CPU: 1.0},
			},
		},
		warm: make(map[string][]*WarmContainer),
		freq: make(map[string][]time.Time),
	}

	statuses := m.Statuses()
	if len(statuses) != 1 {
		t.Fatalf("expected 1 status, got %d", len(statuses))
	}
	if statuses[0].Ready != 0 {
		t.Errorf("expected 0 ready, got %d", statuses[0].Ready)
	}
	if statuses[0].Profile != "default" {
		t.Errorf("expected profile 'default', got %s", statuses[0].Profile)
	}
}

func TestRebalanceNoData(t *testing.T) {
	m := &Manager{
		cfg: config.PoolConfig{
			TotalWarm:       10,
			MinBase:         2,
			RebalanceWindow: "1h",
			Base: map[string]config.BaseImageConfig{
				"default": {Image: "test:latest", Memory: "512m", CPU: 1.0},
			},
		},
		warm:  make(map[string][]*WarmContainer),
		freq:  make(map[string][]time.Time),
		close: make(chan struct{}),
	}

	// Should not panic with no data.
	m.rebalance()
}
