package daemon

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/byggflow/sandbox/internal/identity"
	"github.com/byggflow/sandbox/internal/proxy"
	"github.com/byggflow/sandbox/protocol"
	"github.com/coder/websocket"
)

func (d *Daemon) handleCreateSandbox(w http.ResponseWriter, r *http.Request) {
	id := identity.Extract(r)
	limits := identity.ExtractLimits(r)

	// Per-identity rate limit on sandbox creation.
	rateKey := id.Value
	if rateKey == "" {
		rateKey = remoteIP(r)
	}
	if d.CreateLimit.isBlocked(rateKey) {
		w.Header().Set("Retry-After", "60")
		writeError(w, http.StatusTooManyRequests, "sandbox creation rate limit exceeded")
		return
	}
	d.CreateLimit.record(rateKey)

	var req CreateRequest
	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}

	if err := validateLabels(req.Labels); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	sbx, err := d.CreateSandbox(r.Context(), req, id, limits)
	if err != nil {
		if err == ErrAtCapacity {
			w.Header().Set("Retry-After", "2")
			writeError(w, http.StatusServiceUnavailable, "at capacity")
			return
		}
		if err == ErrIdentityQuotaExceeded {
			w.Header().Set("Retry-After", "2")
			writeError(w, http.StatusTooManyRequests, "identity sandbox quota exceeded")
			return
		}
		d.Log.Error("sandbox creation failed", "error", err)
		writeError(w, http.StatusInternalServerError, "sandbox creation failed")
		return
	}

	writeJSON(w, http.StatusCreated, sbx.Info())
}

func (d *Daemon) handleListSandboxes(w http.ResponseWriter, r *http.Request) {
	id := identity.Extract(r)

	// Parse label filters from query parameters.
	// Format: ?label=key:value — multiple label params act as AND filter.
	labelFilters := map[string]string{}
	for _, lbl := range r.URL.Query()["label"] {
		parts := strings.SplitN(lbl, ":", 2)
		if len(parts) == 2 {
			labelFilters[parts[0]] = parts[1]
		}
	}

	sandboxes := d.Registry.List(id)
	infos := make([]SandboxInfo, 0, len(sandboxes))
	for _, sbx := range sandboxes {
		if len(labelFilters) > 0 {
			if !sbx.MatchLabels(labelFilters) {
				continue
			}
		}
		infos = append(infos, sbx.Info())
	}

	writeJSON(w, http.StatusOK, infos)
}

func (d *Daemon) handleDestroySandbox(w http.ResponseWriter, r *http.Request) {
	sbxID := r.PathValue("id")
	if sbxID == "" {
		writeError(w, http.StatusBadRequest, "sandbox id required")
		return
	}

	id := identity.Extract(r)

	sbx, ok := d.Registry.Get(sbxID)
	if !ok {
		writeError(w, http.StatusNotFound, "sandbox not found")
		return
	}

	// Check identity match.
	if !id.Empty() && !sbx.Identity.Matches(id) {
		writeError(w, http.StatusNotFound, "sandbox not found")
		return
	}

	if err := d.DestroySandbox(r.Context(), sbx); err != nil {
		d.Log.Error("sandbox destroy failed", "error", err)
		writeError(w, http.StatusInternalServerError, "sandbox destroy failed")
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// SandboxStats is the JSON response for GET /sandboxes/{id}/stats.
type SandboxStats struct {
	CPUPercent       float64 `json:"cpu_percent"`
	MemoryUsageBytes uint64  `json:"memory_usage_bytes"`
	MemoryLimitBytes uint64  `json:"memory_limit_bytes"`
	MemoryPercent    float64 `json:"memory_percent"`
	NetworkRxBytes   uint64  `json:"network_rx_bytes"`
	NetworkTxBytes   uint64  `json:"network_tx_bytes"`
	PIDs             uint64  `json:"pids"`
	UptimeSeconds    float64 `json:"uptime_seconds"`
}

func (d *Daemon) handleSandboxStats(w http.ResponseWriter, r *http.Request) {
	sbxID := r.PathValue("id")
	if sbxID == "" {
		writeError(w, http.StatusBadRequest, "sandbox id required")
		return
	}

	id := identity.Extract(r)

	sbx, ok := d.Registry.Get(sbxID)
	if !ok {
		writeError(w, http.StatusNotFound, "sandbox not found")
		return
	}

	if !id.Empty() && !sbx.Identity.Matches(id) {
		writeError(w, http.StatusNotFound, "sandbox not found")
		return
	}

	// Get stats from the runtime.
	rt := d.RuntimeFor(sbx)
	rtStats, err := rt.Stats(r.Context(), sbx.ContainerID)
	if err != nil {
		d.Log.Error("runtime stats failed", "sandbox", sbxID, "error", err)
		writeError(w, http.StatusInternalServerError, "failed to retrieve sandbox stats")
		return
	}

	uptimeSeconds := time.Since(sbx.Created).Seconds()
	uptimeSeconds = math.Round(uptimeSeconds*100) / 100

	stats := SandboxStats{
		CPUPercent:       rtStats.CPUPercent,
		MemoryUsageBytes: rtStats.MemoryUsage,
		MemoryLimitBytes: rtStats.MemoryLimit,
		MemoryPercent:    rtStats.MemoryPercent,
		NetworkRxBytes:   rtStats.NetRxBytes,
		NetworkTxBytes:   rtStats.NetTxBytes,
		PIDs:             rtStats.PIDs,
		UptimeSeconds:    uptimeSeconds,
	}

	writeJSON(w, http.StatusOK, stats)
}

func (d *Daemon) handleSandboxWS(w http.ResponseWriter, r *http.Request) {
	sbxID := r.PathValue("id")
	if sbxID == "" {
		writeError(w, http.StatusBadRequest, "sandbox id required")
		return
	}

	id := identity.Extract(r)

	sbx, ok := d.Registry.Get(sbxID)
	if !ok {
		writeError(w, http.StatusNotFound, "sandbox not found")
		return
	}

	if !id.Empty() && !sbx.Identity.Matches(id) {
		writeError(w, http.StatusNotFound, "sandbox not found")
		return
	}

	// Upgrade to WebSocket.
	// Only skip origin check for Unix socket connections; TCP connections
	// require a valid Origin header to prevent cross-site WebSocket hijacking.
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: isUnixSocket(r),
	})
	if err != nil {
		d.Log.Error("websocket upgrade failed", "error", err)
		return
	}
	ws.SetReadLimit(2 * 1024 * 1024) // 2MB to accommodate 1MB encrypted chunks + overhead

	// Check if reconnecting from disconnected state.
	isReconnect := false
	var disconnectedAt time.Time
	state := sbx.GetState()
	if state == StateDisconnected {
		isReconnect = true
		sbx.mu.Lock()
		disconnectedAt = sbx.DisconnectedAt
		sbx.mu.Unlock()
		sbx.CancelReaper()
		sbx.SetState(StateRunning)
		d.publishSandboxEvent("sandbox.reconnected", sbx, map[string]interface{}{
			"disconnected_at": disconnectedAt.Format(time.RFC3339),
		})
		d.Log.Info("sandbox reconnected", "id", sbxID)
	}

	// Connect to the agent.
	agent, err := d.ConnectAgent(sbx)
	if err != nil {
		d.Log.Error("agent connect failed", "sandbox", sbxID, "error", err)
		ws.Close(websocket.StatusInternalError, "agent unreachable")
		return
	}

	// Start proxy session and atomically swap with any existing session.
	session := proxy.NewSession(ws, agent, d.Log.With("sandbox", sbxID))
	oldSession := sbx.SetSession(session)
	if oldSession != nil {
		d.Log.Info("replacing existing session", "sandbox", sbxID)
		_ = oldSession.SendNotification(protocol.OpSessionReplaced, map[string]interface{}{
			"sandbox": sbxID,
			"reason":  "new session connected",
		})
		oldSession.Close()
	}

	// If reconnecting, replay buffered notifications.
	if isReconnect && sbx.Buffer != nil {
		d.replayBufferedNotifications(r.Context(), session, sbx, sbxID, disconnectedAt)
	}

	d.Log.Info("websocket session started", "sandbox", sbxID)
	if err := session.Run(r.Context()); err != nil {
		if !isSessionCloseError(err) {
			d.Log.Error("session error", "sandbox", sbxID, "error", err)
		}
	}
	d.Log.Info("websocket session ended", "sandbox", sbxID)

	// Handle disconnect (TTL reaper or destroy).
	d.HandleDisconnect(sbx)
}

func (d *Daemon) replayBufferedNotifications(ctx context.Context, session *proxy.Session, sbx *Sandbox, sbxID string, disconnectedAt time.Time) {
	stateNotifs, streamNotifs, truncated := sbx.Buffer.Drain()
	buffered := len(stateNotifs) + len(streamNotifs)
	reconnectedAt := time.Now()

	// Send session.resumed notification.
	if err := session.SendNotification(protocol.OpSessionResumed, map[string]interface{}{
		"sandbox":         sbxID,
		"buffered":        buffered,
		"truncated":       truncated,
		"disconnected_at": disconnectedAt.Format(time.RFC3339),
		"reconnected_at":  reconnectedAt.Format(time.RFC3339),
	}); err != nil {
		d.Log.Error("failed to send session.resumed notification", "sandbox", sbxID, "error", err)
	}

	// Replay state notifications.
	for _, n := range stateNotifs {
		if err := session.SendRawJSON(n.Payload); err != nil {
			d.Log.Warn("failed to replay state notification", "sandbox", sbxID, "error", err)
			break
		}
	}
	// Replay stream notifications.
	for _, n := range streamNotifs {
		if err := session.SendRawJSON(n.Payload); err != nil {
			d.Log.Warn("failed to replay stream notification", "sandbox", sbxID, "error", err)
			break
		}
	}
}
