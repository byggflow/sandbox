package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMetricsIncCounters(t *testing.T) {
	m := NewMetrics()

	m.IncCreateWarm()
	m.IncCreateWarm()
	m.IncCreateCold()
	m.IncDestroy()

	if got := m.sandboxCreatesWarm.Load(); got != 2 {
		t.Errorf("warm creates: got %d, want 2", got)
	}
	if got := m.sandboxCreatesCold.Load(); got != 1 {
		t.Errorf("cold creates: got %d, want 1", got)
	}
	if got := m.sandboxDestroys.Load(); got != 1 {
		t.Errorf("destroys: got %d, want 1", got)
	}
}

func TestMetricsIncRequest(t *testing.T) {
	m := NewMetrics()

	m.IncRequest("GET", "/sandboxes", 200)
	m.IncRequest("GET", "/sandboxes", 200)
	m.IncRequest("POST", "/sandboxes", 201)
	m.IncRequest("GET", "/sandboxes", 500)

	m.requestCountsMu.RLock()
	defer m.requestCountsMu.RUnlock()

	key200 := "GET|/sandboxes|200"
	if counter, ok := m.requestCounts[key200]; !ok || counter.Load() != 2 {
		t.Errorf("GET /sandboxes 200: got %v", m.requestCounts[key200])
	}

	key201 := "POST|/sandboxes|201"
	if counter, ok := m.requestCounts[key201]; !ok || counter.Load() != 1 {
		t.Errorf("POST /sandboxes 201: got %v", m.requestCounts[key201])
	}

	key500 := "GET|/sandboxes|500"
	if counter, ok := m.requestCounts[key500]; !ok || counter.Load() != 1 {
		t.Errorf("GET /sandboxes 500: got %v", m.requestCounts[key500])
	}
}

func TestMetricsObserveRequestDuration(t *testing.T) {
	m := NewMetrics()

	// Observe a 2ms request — should land in the 0.005 bucket and above.
	m.ObserveRequestDuration("GET", "/health", 2*time.Millisecond)

	m.histMu.Lock()
	defer m.histMu.Unlock()

	key := "GET|/health"
	counts, ok := m.histCounts[key]
	if !ok {
		t.Fatal("expected histogram entry for GET|/health")
	}

	// 0.002s should NOT be in the 0.001 bucket (0.002 > 0.001).
	if counts[0].Load() != 0 {
		t.Errorf("bucket 0.001: got %d, want 0", counts[0].Load())
	}

	// 0.002s should be in the 0.005 bucket (0.002 <= 0.005).
	if counts[1].Load() != 1 {
		t.Errorf("bucket 0.005: got %d, want 1", counts[1].Load())
	}

	// +Inf bucket should always have the observation.
	infIdx := len(m.histBuckets)
	if counts[infIdx].Load() != 1 {
		t.Errorf("+Inf bucket: got %d, want 1", counts[infIdx].Load())
	}

	if total := m.histTotals[key].Load(); total != 1 {
		t.Errorf("total: got %d, want 1", total)
	}

	sum := m.histSums[key].Load()
	if sum < 0.001 || sum > 0.003 {
		t.Errorf("sum: got %f, want ~0.002", sum)
	}
}

func TestMetricsMiddleware(t *testing.T) {
	m := NewMetrics()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := metricsMiddleware(m, inner)

	req := httptest.NewRequest("GET", "/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	m.requestCountsMu.RLock()
	counter, ok := m.requestCounts["GET|/health|200"]
	m.requestCountsMu.RUnlock()

	if !ok || counter.Load() != 1 {
		t.Errorf("expected 1 request recorded for GET /health 200")
	}
}

func TestMetricsMiddlewareSkipsMetricsEndpoint(t *testing.T) {
	m := NewMetrics()

	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	handler := metricsMiddleware(m, inner)

	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	m.requestCountsMu.RLock()
	_, ok := m.requestCounts["GET|/metrics|200"]
	m.requestCountsMu.RUnlock()

	if ok {
		t.Error("metrics endpoint should not be recorded")
	}
}

func TestNormalizePath(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/sandboxes", "/sandboxes"},
		{"/sandboxes/sbx-abc12345", "/sandboxes/:id"},
		{"/sandboxes/sbx-abc12345/ws", "/sandboxes/:id/ws"},
		{"/templates/tpl-xyz98765", "/templates/:id"},
		{"/health", "/health"},
		{"/pools/default", "/pools/default"},
	}

	for _, tt := range tests {
		got := normalizePath(tt.input)
		if got != tt.want {
			t.Errorf("normalizePath(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestHandleMetricsOutput(t *testing.T) {
	// Create a minimal daemon with just the fields needed for metrics.
	d := &Daemon{
		Registry: NewRegistry(),
		Metrics:  NewMetrics(),
	}

	// Add a sandbox to the registry.
	_ = d.Registry.Add(&Sandbox{ID: "sbx-test0001", Image: "test:latest"})

	// Record some metrics.
	d.Metrics.IncCreateWarm()
	d.Metrics.IncCreateCold()
	d.Metrics.IncCreateCold()
	d.Metrics.IncDestroy()
	d.Metrics.IncRequest("GET", "/health", 200)
	d.Metrics.ObserveRequestDuration("GET", "/health", 50*time.Millisecond)

	req := httptest.NewRequest("GET", "/metrics", nil)
	rec := httptest.NewRecorder()
	d.handleMetrics(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", rec.Code)
	}

	ct := rec.Header().Get("Content-Type")
	if !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content-type: got %q, want text/plain", ct)
	}

	body := rec.Body.String()

	// Check that expected metrics are present.
	expectations := []string{
		"sandboxd_sandboxes_active 1",
		`sandboxd_sandbox_creates_total{method="warm"} 1`,
		`sandboxd_sandbox_creates_total{method="cold"} 2`,
		"sandboxd_sandbox_destroys_total 1",
		`sandboxd_requests_total{method="GET",path="/health",status="200"} 1`,
		"sandboxd_request_duration_seconds_bucket",
		"sandboxd_request_duration_seconds_sum",
		"sandboxd_request_duration_seconds_count",
	}

	for _, exp := range expectations {
		if !strings.Contains(body, exp) {
			t.Errorf("missing expected metric: %q\n\nbody:\n%s", exp, body)
		}
	}
}

func TestResponseCapture(t *testing.T) {
	rec := httptest.NewRecorder()
	rc := &responseCapture{ResponseWriter: rec, status: http.StatusOK}

	// Write without explicit WriteHeader should default to 200.
	rc.Write([]byte("hello"))
	if rc.status != http.StatusOK {
		t.Errorf("status: got %d, want 200", rc.status)
	}

	// Explicit WriteHeader should be captured.
	rec2 := httptest.NewRecorder()
	rc2 := &responseCapture{ResponseWriter: rec2, status: http.StatusOK}
	rc2.WriteHeader(http.StatusNotFound)
	if rc2.status != http.StatusNotFound {
		t.Errorf("status: got %d, want 404", rc2.status)
	}

	// Second WriteHeader should be ignored.
	rc2.WriteHeader(http.StatusInternalServerError)
	if rc2.status != http.StatusNotFound {
		t.Errorf("status after second WriteHeader: got %d, want 404", rc2.status)
	}
}
