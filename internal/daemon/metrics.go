package daemon

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Metrics holds all Prometheus metrics for the sandboxd daemon.
type Metrics struct {
	// Counters for sandbox lifecycle.
	sandboxCreatesWarm atomic.Int64
	sandboxCreatesCold atomic.Int64
	sandboxDestroys    atomic.Int64

	// Request counters by method+path+status.
	requestCounts   map[string]*atomic.Int64
	requestCountsMu sync.RWMutex

	// Request duration histogram.
	histMu      sync.Mutex
	histBuckets []float64
	histCounts  map[string][]atomic.Int64 // key -> per-bucket counts
	histSums    map[string]*atomicFloat64 // key -> sum of observed values
	histTotals  map[string]*atomic.Int64  // key -> total observation count
}

// atomicFloat64 provides a thread-safe float64 using atomic operations.
type atomicFloat64 struct {
	bits atomic.Uint64
}

func (a *atomicFloat64) Add(delta float64) {
	for {
		old := a.bits.Load()
		new := math.Float64frombits(old) + delta
		if a.bits.CompareAndSwap(old, math.Float64bits(new)) {
			return
		}
	}
}

func (a *atomicFloat64) Load() float64 {
	return math.Float64frombits(a.bits.Load())
}

// NewMetrics creates a new Metrics instance.
func NewMetrics() *Metrics {
	buckets := []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5}
	return &Metrics{
		histBuckets:   buckets,
		requestCounts: make(map[string]*atomic.Int64),
		histCounts:    make(map[string][]atomic.Int64),
		histSums:      make(map[string]*atomicFloat64),
		histTotals:    make(map[string]*atomic.Int64),
	}
}

// IncCreateWarm increments the warm sandbox creation counter.
func (m *Metrics) IncCreateWarm() {
	m.sandboxCreatesWarm.Add(1)
}

// IncCreateCold increments the cold sandbox creation counter.
func (m *Metrics) IncCreateCold() {
	m.sandboxCreatesCold.Add(1)
}

// IncDestroy increments the sandbox destroy counter.
func (m *Metrics) IncDestroy() {
	m.sandboxDestroys.Add(1)
}

// IncRequest increments the request counter for the given method, path, and status.
func (m *Metrics) IncRequest(method, path string, status int) {
	key := method + "|" + path + "|" + strconv.Itoa(status)

	m.requestCountsMu.RLock()
	counter, ok := m.requestCounts[key]
	m.requestCountsMu.RUnlock()

	if !ok {
		m.requestCountsMu.Lock()
		counter, ok = m.requestCounts[key]
		if !ok {
			counter = &atomic.Int64{}
			m.requestCounts[key] = counter
		}
		m.requestCountsMu.Unlock()
	}

	counter.Add(1)
}

// ObserveRequestDuration records a request duration observation for the histogram.
func (m *Metrics) ObserveRequestDuration(method, path string, duration time.Duration) {
	key := method + "|" + path
	seconds := duration.Seconds()

	m.histMu.Lock()
	counts, ok := m.histCounts[key]
	if !ok {
		counts = make([]atomic.Int64, len(m.histBuckets)+1) // +1 for +Inf
		m.histCounts[key] = counts
		m.histSums[key] = &atomicFloat64{}
		m.histTotals[key] = &atomic.Int64{}
	}
	m.histMu.Unlock()

	// Increment bucket counters (cumulative).
	for i, bound := range m.histBuckets {
		if seconds <= bound {
			counts[i].Add(1)
		}
	}
	// Always increment +Inf bucket.
	counts[len(m.histBuckets)].Add(1)

	m.histSums[key].Add(seconds)
	m.histTotals[key].Add(1)
}

// responseCapture wraps http.ResponseWriter to capture the status code.
type responseCapture struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (rc *responseCapture) WriteHeader(code int) {
	if !rc.wrote {
		rc.status = code
		rc.wrote = true
	}
	rc.ResponseWriter.WriteHeader(code)
}

func (rc *responseCapture) Write(b []byte) (int, error) {
	if !rc.wrote {
		rc.status = http.StatusOK
		rc.wrote = true
	}
	return rc.ResponseWriter.Write(b)
}

// metricsMiddleware wraps an http.Handler to record request metrics.
func metricsMiddleware(m *Metrics, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip recording metrics for the metrics endpoint itself.
		if r.URL.Path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		rc := &responseCapture{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rc, r)
		duration := time.Since(start)

		path := normalizePath(r.URL.Path)
		m.IncRequest(r.Method, path, rc.status)
		m.ObserveRequestDuration(r.Method, path, duration)
	})
}

// normalizePath replaces dynamic path segments with a placeholder.
func normalizePath(p string) string {
	parts := strings.Split(p, "/")
	for i, part := range parts {
		// Heuristic: sandbox and template IDs start with "sbx-" or "tpl-",
		// and pool profiles are single path segments after known prefixes.
		if strings.HasPrefix(part, "sbx-") || strings.HasPrefix(part, "tpl-") {
			parts[i] = ":id"
		}
	}
	return strings.Join(parts, "/")
}

// handleMetrics writes all metrics in Prometheus text exposition format.
func (d *Daemon) handleMetrics(w http.ResponseWriter, r *http.Request) {
	var b strings.Builder

	// sandboxd_sandboxes_active gauge.
	b.WriteString("# HELP sandboxd_sandboxes_active Current number of active sandboxes.\n")
	b.WriteString("# TYPE sandboxd_sandboxes_active gauge\n")
	fmt.Fprintf(&b, "sandboxd_sandboxes_active %d\n", d.Registry.Count())

	// sandboxd_pool_ready gauge per profile.
	b.WriteString("# HELP sandboxd_pool_ready Number of warm containers ready per profile.\n")
	b.WriteString("# TYPE sandboxd_pool_ready gauge\n")
	if d.Pool != nil {
		for _, s := range d.Pool.Statuses() {
			fmt.Fprintf(&b, "sandboxd_pool_ready{profile=%q} %d\n", s.Profile, s.Ready)
		}
	}

	// sandboxd_sandbox_creates_total counter.
	b.WriteString("# HELP sandboxd_sandbox_creates_total Total number of sandbox creations.\n")
	b.WriteString("# TYPE sandboxd_sandbox_creates_total counter\n")
	fmt.Fprintf(&b, "sandboxd_sandbox_creates_total{method=\"warm\"} %d\n", d.Metrics.sandboxCreatesWarm.Load())
	fmt.Fprintf(&b, "sandboxd_sandbox_creates_total{method=\"cold\"} %d\n", d.Metrics.sandboxCreatesCold.Load())

	// sandboxd_sandbox_destroys_total counter.
	b.WriteString("# HELP sandboxd_sandbox_destroys_total Total number of sandbox destructions.\n")
	b.WriteString("# TYPE sandboxd_sandbox_destroys_total counter\n")
	fmt.Fprintf(&b, "sandboxd_sandbox_destroys_total %d\n", d.Metrics.sandboxDestroys.Load())

	// sandboxd_requests_total counter.
	b.WriteString("# HELP sandboxd_requests_total Total number of HTTP requests.\n")
	b.WriteString("# TYPE sandboxd_requests_total counter\n")

	d.Metrics.requestCountsMu.RLock()
	// Sort keys for deterministic output.
	reqKeys := make([]string, 0, len(d.Metrics.requestCounts))
	for k := range d.Metrics.requestCounts {
		reqKeys = append(reqKeys, k)
	}
	sort.Strings(reqKeys)
	for _, key := range reqKeys {
		counter := d.Metrics.requestCounts[key]
		parts := strings.SplitN(key, "|", 3)
		if len(parts) != 3 {
			continue
		}
		fmt.Fprintf(&b, "sandboxd_requests_total{method=%q,path=%q,status=%q} %d\n",
			parts[0], parts[1], parts[2], counter.Load())
	}
	d.Metrics.requestCountsMu.RUnlock()

	// sandboxd_request_duration_seconds histogram.
	b.WriteString("# HELP sandboxd_request_duration_seconds HTTP request duration in seconds.\n")
	b.WriteString("# TYPE sandboxd_request_duration_seconds histogram\n")

	d.Metrics.histMu.Lock()
	histKeys := make([]string, 0, len(d.Metrics.histCounts))
	for k := range d.Metrics.histCounts {
		histKeys = append(histKeys, k)
	}
	sort.Strings(histKeys)

	for _, key := range histKeys {
		counts := d.Metrics.histCounts[key]
		sum := d.Metrics.histSums[key].Load()
		total := d.Metrics.histTotals[key].Load()

		parts := strings.SplitN(key, "|", 2)
		if len(parts) != 2 {
			continue
		}
		method, path := parts[0], parts[1]
		labels := fmt.Sprintf("method=%q,path=%q", method, path)

		// Cumulative bucket counts.
		var cumulative int64
		for i, bound := range d.Metrics.histBuckets {
			cumulative += counts[i].Load()
			fmt.Fprintf(&b, "sandboxd_request_duration_seconds_bucket{%s,le=\"%s\"} %d\n",
				labels, formatFloat(bound), cumulative)
		}
		cumulative += counts[len(d.Metrics.histBuckets)].Load()
		fmt.Fprintf(&b, "sandboxd_request_duration_seconds_bucket{%s,le=\"+Inf\"} %d\n", labels, cumulative)
		fmt.Fprintf(&b, "sandboxd_request_duration_seconds_sum{%s} %s\n", labels, formatFloat(sum))
		fmt.Fprintf(&b, "sandboxd_request_duration_seconds_count{%s} %d\n", labels, total)
	}
	d.Metrics.histMu.Unlock()

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(b.String()))
}

// formatFloat formats a float64 without trailing zeros.
func formatFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}
