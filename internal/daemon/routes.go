package daemon

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"

	"github.com/byggflow/sandbox/internal/identity"
	"github.com/byggflow/sandbox/internal/proxy"
	"github.com/byggflow/sandbox/protocol"
	"nhooyr.io/websocket"
)

// registerRoutes sets up all HTTP handlers on the mux.
func registerRoutes(mux *http.ServeMux, d *Daemon) {
	// Tenant-authenticated routes: require valid signature in multi-tenant mode.
	tenant := d.tenantAuth

	mux.HandleFunc("POST /sandboxes", tenant(d.handleCreateSandbox))
	mux.HandleFunc("GET /sandboxes", tenant(d.handleListSandboxes))
	mux.HandleFunc("DELETE /sandboxes/{id}", tenant(d.handleDestroySandbox))
	mux.HandleFunc("GET /sandboxes/{id}/stats", tenant(d.handleSandboxStats))
	mux.HandleFunc("GET /sandboxes/{id}/ws", tenant(d.handleSandboxWS))

	mux.HandleFunc("POST /templates", tenant(d.handleCreateTemplate))
	mux.HandleFunc("GET /templates", tenant(d.handleListTemplates))
	mux.HandleFunc("GET /templates/{id}", tenant(d.handleGetTemplate))
	mux.HandleFunc("DELETE /templates/{id}", tenant(d.handleDeleteTemplate))

	mux.HandleFunc("GET /pools", tenant(d.handlePoolStatus))
	mux.HandleFunc("PUT /pools/{profile}", tenant(d.handlePoolResize))
	mux.HandleFunc("POST /pools/{profile}/flush", tenant(d.handlePoolFlush))

	mux.HandleFunc("GET /events", tenant(d.handleEvents))
	mux.HandleFunc("GET /events/history", tenant(d.handleEventsHistory))

	// Operational endpoints — no auth required.
	mux.HandleFunc("GET /health", d.handleHealth)
	mux.HandleFunc("GET /metrics", d.handleMetrics)
}

// tenantAuth is middleware that verifies Ed25519 signatures on identity headers
// when multi-tenant mode is enabled. In single-user mode it's a no-op passthrough.
func (d *Daemon) tenantAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.Verifier != nil {
			if err := d.Verifier.Verify(r); err != nil {
				writeError(w, http.StatusUnauthorized, fmt.Sprintf("signature verification failed: %v", err))
				return
			}
			// In multi-tenant mode, identity is required.
			if r.Header.Get(identity.Header) == "" {
				writeError(w, http.StatusUnauthorized, "X-Sandbox-Identity header required")
				return
			}
		}
		next(w, r)
	}
}

func (d *Daemon) handleCreateSandbox(w http.ResponseWriter, r *http.Request) {
	id := identity.Extract(r)
	limits := identity.ExtractLimits(r)

	var req CreateRequest
	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
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
		writeError(w, http.StatusInternalServerError, err.Error())
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
			sbx.mu.Lock()
			labels := sbx.Labels
			sbx.mu.Unlock()
			match := true
			for k, v := range labelFilters {
				if labels[k] != v {
					match = false
					break
				}
			}
			if !match {
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
		writeError(w, http.StatusInternalServerError, err.Error())
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

	// One-shot container stats from Docker.
	statsResp, err := d.Docker.ContainerStatsOneShot(r.Context(), sbx.ContainerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("docker stats: %v", err))
		return
	}
	defer statsResp.Body.Close()

	var dockerStats container.StatsResponse
	if err := json.NewDecoder(statsResp.Body).Decode(&dockerStats); err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("decode stats: %v", err))
		return
	}

	// Calculate CPU percentage.
	cpuPercent := 0.0
	cpuDelta := float64(dockerStats.CPUStats.CPUUsage.TotalUsage) - float64(dockerStats.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(dockerStats.CPUStats.SystemUsage) - float64(dockerStats.PreCPUStats.SystemUsage)
	if systemDelta > 0 && cpuDelta >= 0 {
		onlineCPUs := float64(dockerStats.CPUStats.OnlineCPUs)
		if onlineCPUs == 0 {
			onlineCPUs = 1
		}
		cpuPercent = (cpuDelta / systemDelta) * onlineCPUs * 100.0
		cpuPercent = math.Round(cpuPercent*100) / 100
	}

	// Memory.
	memUsage := dockerStats.MemoryStats.Usage
	memLimit := dockerStats.MemoryStats.Limit
	memPercent := 0.0
	if memLimit > 0 {
		memPercent = float64(memUsage) / float64(memLimit) * 100.0
		memPercent = math.Round(memPercent*100) / 100
	}

	// Network: sum all interfaces.
	var netRx, netTx uint64
	for _, ns := range dockerStats.Networks {
		netRx += ns.RxBytes
		netTx += ns.TxBytes
	}

	// Uptime from sandbox creation time.
	uptimeSeconds := time.Since(sbx.Created).Seconds()
	uptimeSeconds = math.Round(uptimeSeconds*100) / 100

	stats := SandboxStats{
		CPUPercent:       cpuPercent,
		MemoryUsageBytes: memUsage,
		MemoryLimitBytes: memLimit,
		MemoryPercent:    memPercent,
		NetworkRxBytes:   netRx,
		NetworkTxBytes:   netTx,
		PIDs:             dockerStats.PidsStats.Current,
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
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // Origin check disabled for Unix socket use.
	})
	if err != nil {
		d.Log.Error("websocket upgrade failed", "error", err)
		return
	}

	// Session replacement: if there's an existing session, replace it.
	oldSession := sbx.GetSession()
	if oldSession != nil {
		d.Log.Info("replacing existing session", "sandbox", sbxID)
		// Send session.replaced notification to old connection.
		_ = oldSession.SendNotification(protocol.OpSessionReplaced, map[string]interface{}{
			"sandbox": sbxID,
			"reason":  "new session connected",
		})
		oldSession.Close()
	}

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

	// Start proxy session.
	session := proxy.NewSession(ws, agent, d.Log.With("sandbox", sbxID))
	sbx.SetSession(session)

	// If reconnecting, replay buffered notifications.
	if isReconnect && sbx.Buffer != nil {
		stateNotifs, streamNotifs, truncated := sbx.Buffer.Drain()
		buffered := len(stateNotifs) + len(streamNotifs)
		reconnectedAt := time.Now()

		// Send session.resumed notification.
		_ = session.SendNotification(protocol.OpSessionResumed, map[string]interface{}{
			"sandbox":         sbxID,
			"buffered":        buffered,
			"truncated":       truncated,
			"disconnected_at": disconnectedAt.Format(time.RFC3339),
			"reconnected_at":  reconnectedAt.Format(time.RFC3339),
		})

		// Replay state notifications.
		for _, n := range stateNotifs {
			_ = session.SendRawJSON(n.Payload)
		}
		// Replay stream notifications.
		for _, n := range streamNotifs {
			_ = session.SendRawJSON(n.Payload)
		}
	}

	d.Log.Info("websocket session started", "sandbox", sbxID)
	if err := session.Run(r.Context()); err != nil {
		// Only log if it's not a normal close.
		if !strings.Contains(err.Error(), "closed") &&
			!strings.Contains(err.Error(), "EOF") {
			d.Log.Error("session error", "sandbox", sbxID, "error", err)
		}
	}
	d.Log.Info("websocket session ended", "sandbox", sbxID)

	// Handle disconnect (TTL reaper or destroy).
	d.HandleDisconnect(sbx)
}

// Template routes

func (d *Daemon) handleCreateTemplate(w http.ResponseWriter, r *http.Request) {
	id := identity.Extract(r)
	limits := identity.ExtractLimits(r)

	var req CreateTemplateRequest
	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}

	if req.SandboxID == "" {
		writeError(w, http.StatusBadRequest, "sandbox_id is required")
		return
	}

	// Check global template limit.
	if d.Templates.Count() >= d.Config.Limits.MaxTemplates {
		writeError(w, http.StatusServiceUnavailable, "template limit reached")
		return
	}

	// Check per-identity template limit (from proxy header).
	if limits.MaxTemplates > 0 && !id.Empty() {
		if d.Templates.CountByIdentity(id.Value) >= limits.MaxTemplates {
			w.Header().Set("Retry-After", "2")
			writeError(w, http.StatusTooManyRequests, "identity template quota exceeded")
			return
		}
	}

	// Find the sandbox.
	sbx, ok := d.Registry.Get(req.SandboxID)
	if !ok {
		writeError(w, http.StatusNotFound, "sandbox not found")
		return
	}

	if !id.Empty() && !sbx.Identity.Matches(id) {
		writeError(w, http.StatusNotFound, "sandbox not found")
		return
	}

	// Generate template ID.
	tplID, err := GenerateTemplateID()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to generate template ID")
		return
	}

	// Docker commit the container.
	imageTag := "byggflow-sandbox:" + tplID
	commitResp, err := d.Docker.ContainerCommit(r.Context(), sbx.ContainerID, containerCommitOptions(imageTag))
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Sprintf("docker commit failed: %v", err))
		return
	}

	// Get the image size.
	imageInspect, _, err := d.Docker.ImageInspectWithRaw(r.Context(), commitResp.ID)
	imageSize := int64(0)
	if err == nil {
		imageSize = imageInspect.Size
	}

	// Check max template size.
	if d.Config.Limits.MaxTemplateSize != "" {
		maxSize, parseErr := parseByteSize(d.Config.Limits.MaxTemplateSize)
		if parseErr == nil && imageSize > maxSize {
			// Remove the image since it's too large.
			_, _ = d.Docker.ImageRemove(r.Context(), commitResp.ID, imageRemoveOptions())
			writeError(w, http.StatusBadRequest, fmt.Sprintf("template size %d exceeds limit", imageSize))
			return
		}
	}

	label := req.Label
	if label == "" {
		label = tplID
	}

	tpl := &Template{
		ID:        tplID,
		Label:     label,
		Image:     imageTag,
		Identity:  id.Value,
		Size:      imageSize,
		CreatedAt: time.Now(),
	}

	if err := d.Templates.Add(tpl); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	d.Log.Info("template created", "id", tplID, "sandbox", req.SandboxID, "image", imageTag)
	writeJSON(w, http.StatusCreated, tpl)
}

func (d *Daemon) handleListTemplates(w http.ResponseWriter, r *http.Request) {
	id := identity.Extract(r)

	templates := d.Templates.List(id)
	if templates == nil {
		templates = []*Template{}
	}
	writeJSON(w, http.StatusOK, templates)
}

func (d *Daemon) handleGetTemplate(w http.ResponseWriter, r *http.Request) {
	tplID := r.PathValue("id")
	if tplID == "" {
		writeError(w, http.StatusBadRequest, "template id required")
		return
	}

	id := identity.Extract(r)

	tpl, ok := d.Templates.Get(tplID)
	if !ok {
		writeError(w, http.StatusNotFound, "template not found")
		return
	}

	if !id.Empty() && tpl.Identity != id.Value {
		writeError(w, http.StatusNotFound, "template not found")
		return
	}

	writeJSON(w, http.StatusOK, tpl)
}

func (d *Daemon) handleDeleteTemplate(w http.ResponseWriter, r *http.Request) {
	tplID := r.PathValue("id")
	if tplID == "" {
		writeError(w, http.StatusBadRequest, "template id required")
		return
	}

	id := identity.Extract(r)

	tpl, ok := d.Templates.Get(tplID)
	if !ok {
		writeError(w, http.StatusNotFound, "template not found")
		return
	}

	if !id.Empty() && tpl.Identity != id.Value {
		writeError(w, http.StatusNotFound, "template not found")
		return
	}

	// Remove Docker image.
	_, _ = d.Docker.ImageRemove(r.Context(), tpl.Image, imageRemoveOptions())

	// Remove from registry.
	d.Templates.Remove(tplID)

	d.Log.Info("template deleted", "id", tplID)
	w.WriteHeader(http.StatusNoContent)
}

// Pool routes

func (d *Daemon) handlePoolStatus(w http.ResponseWriter, r *http.Request) {
	statuses := d.Pool.Statuses()
	writeJSON(w, http.StatusOK, statuses)
}

func (d *Daemon) handlePoolResize(w http.ResponseWriter, r *http.Request) {
	profile := r.PathValue("profile")
	if profile == "" {
		writeError(w, http.StatusBadRequest, "profile required")
		return
	}

	var body struct {
		Count int `json:"count"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if err := d.Pool.Resize(r.Context(), profile, body.Count); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (d *Daemon) handlePoolFlush(w http.ResponseWriter, r *http.Request) {
	profile := r.PathValue("profile")
	if profile == "" {
		writeError(w, http.StatusBadRequest, "profile required")
		return
	}

	if err := d.Pool.Flush(r.Context(), profile); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (d *Daemon) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// Optional identity filter.
	identityFilter := r.URL.Query().Get("identity")

	// Subscribe BEFORE replaying history so we don't miss events in the gap.
	subID, ch := d.Events.Subscribe(64)
	defer d.Events.Unsubscribe(subID)

	// Set SSE headers.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Replay missed events if the client sent Last-Event-ID (standard SSE reconnect).
	if lastID := r.Header.Get("Last-Event-ID"); lastID != "" {
		var afterID uint64
		if _, err := fmt.Sscanf(lastID, "%d", &afterID); err == nil && afterID > 0 {
			missed, complete := d.Events.Since(afterID)
			if !complete {
				// Tell the client some events were lost.
				fmt.Fprintf(w, "event: events.gap\ndata: {\"after_id\":%d,\"message\":\"some events were lost\"}\n\n", afterID)
				flusher.Flush()
			}
			for _, event := range missed {
				if identityFilter != "" && event.Identity != identityFilter {
					continue
				}
				data, err := json.Marshal(event)
				if err != nil {
					continue
				}
				fmt.Fprintf(w, "id: %d\ndata: %s\n\n", event.ID, data)
			}
			flusher.Flush()
		}
	}

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-ch:
			if !ok {
				return
			}

			// Filter by identity if requested.
			if identityFilter != "" && event.Identity != identityFilter {
				continue
			}

			data, err := json.Marshal(event)
			if err != nil {
				d.Log.Error("failed to marshal event", "error", err)
				continue
			}

			fmt.Fprintf(w, "id: %d\ndata: %s\n\n", event.ID, data)
			flusher.Flush()
		}
	}
}

func (d *Daemon) handleEventsHistory(w http.ResponseWriter, r *http.Request) {
	afterStr := r.URL.Query().Get("after")
	var afterID uint64
	if afterStr != "" {
		if _, err := fmt.Sscanf(afterStr, "%d", &afterID); err != nil {
			writeError(w, http.StatusBadRequest, "invalid 'after' parameter — must be a numeric event ID")
			return
		}
	}

	limitStr := r.URL.Query().Get("limit")
	limit := 1000
	if limitStr != "" {
		if _, err := fmt.Sscanf(limitStr, "%d", &limit); err != nil || limit <= 0 {
			writeError(w, http.StatusBadRequest, "invalid 'limit' parameter")
			return
		}
		if limit > 10_000 {
			limit = 10_000
		}
	}

	identityFilter := r.URL.Query().Get("identity")

	events, complete := d.Events.Since(afterID)

	// Apply identity filter and limit.
	filtered := make([]Event, 0, len(events))
	for _, ev := range events {
		if identityFilter != "" && ev.Identity != identityFilter {
			continue
		}
		filtered = append(filtered, ev)
		if len(filtered) >= limit {
			break
		}
	}

	resp := map[string]interface{}{
		"events":   filtered,
		"complete": complete,
		"seq":      d.Events.Seq(),
	}
	writeJSON(w, http.StatusOK, resp)
}

func (d *Daemon) handleHealth(w http.ResponseWriter, r *http.Request) {
	resp := map[string]interface{}{
		"status":    "ok",
		"sandboxes": d.Registry.Count(),
	}
	if d.Pool != nil {
		resp["pool"] = d.Pool.Statuses()
	}
	if d.Config.Server.NodeID != "" {
		resp["node_id"] = d.Config.Server.NodeID
	}
	writeJSON(w, http.StatusOK, resp)
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Error("failed to write json response", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{
		"error": fmt.Sprintf("%s", message),
	})
}
