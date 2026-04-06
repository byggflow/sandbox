package daemon

import (
	"container/heap"
	"net"
	"net/http"
	"sync"
	"time"
)

// rateLimiter tracks request counts per key (IP or identity).
// It uses a simple counter with a decay window — after the window elapses
// without new requests, the counter resets.
type rateLimiter struct {
	mu         sync.Mutex
	entries    map[string]*rateLimitEntry
	heap       rateLimitHeap
	max        int           // Max failures before blocking.
	window     time.Duration // Window after which counters reset.
	maxEntries int           // Max tracked entries before eviction.
}

type rateLimitEntry struct {
	key    string
	count  int
	lastAt time.Time
	index  int // Position in the heap.
}

func newRateLimiter(max int, window time.Duration, maxEntries int) *rateLimiter {
	rl := &rateLimiter{
		entries:    make(map[string]*rateLimitEntry),
		max:        max,
		window:     window,
		maxEntries: maxEntries,
	}
	heap.Init(&rl.heap)
	go rl.cleanup()
	return rl
}

// record increments the failure counter for an IP.
func (rl *rateLimiter) record(ip string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Evict oldest entry if map is at capacity.
	if len(rl.entries) >= rl.maxEntries && rl.heap.Len() > 0 {
		oldest := heap.Pop(&rl.heap).(*rateLimitEntry)
		delete(rl.entries, oldest.key)
	}

	e, ok := rl.entries[ip]
	if !ok || time.Since(e.lastAt) > rl.window {
		if ok {
			// Entry expired — remove from heap and re-add fresh.
			heap.Remove(&rl.heap, e.index)
		}
		entry := &rateLimitEntry{key: ip, count: 1, lastAt: time.Now()}
		rl.entries[ip] = entry
		heap.Push(&rl.heap, entry)
		return
	}
	e.count++
	e.lastAt = time.Now()
	heap.Fix(&rl.heap, e.index)
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
		heap.Remove(&rl.heap, e.index)
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
		for rl.heap.Len() > 0 {
			oldest := rl.heap[0]
			if now.Sub(oldest.lastAt) <= rl.window {
				break
			}
			heap.Pop(&rl.heap)
			delete(rl.entries, oldest.key)
		}
		rl.mu.Unlock()
	}
}

// rateLimitHeap is a min-heap of rateLimitEntry ordered by lastAt.
type rateLimitHeap []*rateLimitEntry

func (h rateLimitHeap) Len() int           { return len(h) }
func (h rateLimitHeap) Less(i, j int) bool { return h[i].lastAt.Before(h[j].lastAt) }
func (h rateLimitHeap) Swap(i, j int) {
	h[i], h[j] = h[j], h[i]
	h[i].index = i
	h[j].index = j
}

func (h *rateLimitHeap) Push(x interface{}) {
	entry := x.(*rateLimitEntry)
	entry.index = len(*h)
	*h = append(*h, entry)
}

func (h *rateLimitHeap) Pop() interface{} {
	old := *h
	n := len(old)
	entry := old[n-1]
	old[n-1] = nil
	entry.index = -1
	*h = old[:n-1]
	return entry
}

// remoteIP extracts the IP address from the request, stripping the port.
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
