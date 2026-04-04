package daemon

import (
	"net"
	"net/http"
	"sync"
	"time"
)

const maxRateLimitEntries = 10_000

// rateLimiter tracks request counts per key (IP or identity).
// It uses a simple counter with a decay window — after the window elapses
// without new requests, the counter resets.
type rateLimiter struct {
	mu      sync.Mutex
	entries map[string]*rateLimitEntry
	max     int           // Max failures before blocking.
	window  time.Duration // Window after which counters reset.
}

type rateLimitEntry struct {
	count  int
	lastAt time.Time
}

func newRateLimiter(max int, window time.Duration) *rateLimiter {
	rl := &rateLimiter{
		entries: make(map[string]*rateLimitEntry),
		max:     max,
		window:  window,
	}
	go rl.cleanup()
	return rl
}

// record increments the failure counter for an IP.
func (rl *rateLimiter) record(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Evict oldest entry if map is at capacity.
	if len(rl.entries) >= maxRateLimitEntries {
		var oldestIP string
		var oldestTime time.Time
		for ip, e := range rl.entries {
			if oldestIP == "" || e.lastAt.Before(oldestTime) {
				oldestIP = ip
				oldestTime = e.lastAt
			}
		}
		if oldestIP != "" {
			delete(rl.entries, oldestIP)
		}
	}

	e, ok := rl.entries[ip]
	if !ok || time.Since(e.lastAt) > rl.window {
		rl.entries[ip] = &rateLimitEntry{count: 1, lastAt: time.Now()}
		return
	}
	e.count++
	e.lastAt = time.Now()
}

// isBlocked returns true if the IP has exceeded the failure limit within the window.
func (rl *rateLimiter) isBlocked(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	e, ok := rl.entries[ip]
	if !ok {
		return false
	}
	if time.Since(e.lastAt) > rl.window {
		delete(rl.entries, ip)
		return false
	}
	return e.count >= rl.max
}

// cleanup periodically removes stale entries.
func (rl *rateLimiter) cleanup() {
	ticker := time.NewTicker(rl.window)
	defer ticker.Stop()
	for range ticker.C {
		rl.mu.Lock()
		now := time.Now()
		for ip, e := range rl.entries {
			if now.Sub(e.lastAt) > rl.window {
				delete(rl.entries, ip)
			}
		}
		rl.mu.Unlock()
	}
}

// remoteIP extracts the IP address from the request, stripping the port.
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
