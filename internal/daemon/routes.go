package daemon

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/byggflow/sandbox/internal/proxy"
	"github.com/byggflow/sandbox/protocol"
	"nhooyr.io/websocket"
)

// registerRoutes sets up all HTTP handlers on the mux.
func registerRoutes(mux *http.ServeMux, d *Daemon) {
	mux.HandleFunc("POST /sandboxes", d.handleCreateSandbox)
	mux.HandleFunc("GET /sandboxes", d.handleListSandboxes)
	mux.HandleFunc("DELETE /sandboxes/{id}", d.handleDestroySandbox)
	mux.HandleFunc("GET /sandboxes/{id}/ws", d.handleSandboxWS)

	mux.HandleFunc("POST /templates", d.handleCreateTemplate)
	mux.HandleFunc("GET /templates", d.handleListTemplates)
	mux.HandleFunc("GET /templates/{id}", d.handleGetTemplate)
	mux.HandleFunc("DELETE /templates/{id}", d.handleDeleteTemplate)

	mux.HandleFunc("GET /pools", d.handlePoolStatus)
	mux.HandleFunc("PUT /pools/{profile}", d.handlePoolResize)
	mux.HandleFunc("POST /pools/{profile}/flush", d.handlePoolFlush)

	mux.HandleFunc("GET /health", d.handleHealth)
}

func (d *Daemon) handleCreateSandbox(w http.ResponseWriter, r *http.Request) {
	// Extract identity.
	id := d.Identity.Extract(r)
	if d.Identity.Required() && id.Empty() {
		writeError(w, http.StatusUnauthorized, "identity required")
		return
	}

	var req CreateRequest
	if r.Body != nil {
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}

	sbx, err := d.CreateSandbox(r.Context(), req, id)
	if err != nil {
		if err == ErrAtCapacity {
			w.Header().Set("Retry-After", "2")
			writeError(w, http.StatusServiceUnavailable, "at capacity")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, sbx.Info())
}

func (d *Daemon) handleListSandboxes(w http.ResponseWriter, r *http.Request) {
	id := d.Identity.Extract(r)
	if d.Identity.Required() && id.Empty() {
		writeError(w, http.StatusUnauthorized, "identity required")
		return
	}

	sandboxes := d.Registry.List(id)
	infos := make([]SandboxInfo, 0, len(sandboxes))
	for _, sbx := range sandboxes {
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

	id := d.Identity.Extract(r)
	if d.Identity.Required() && id.Empty() {
		writeError(w, http.StatusUnauthorized, "identity required")
		return
	}

	sbx, ok := d.Registry.Get(sbxID)
	if !ok {
		writeError(w, http.StatusNotFound, "sandbox not found")
		return
	}

	// Check identity match.
	if d.Identity.Required() && !sbx.Identity.Matches(id) {
		writeError(w, http.StatusNotFound, "sandbox not found")
		return
	}

	if err := d.DestroySandbox(r.Context(), sbx); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (d *Daemon) handleSandboxWS(w http.ResponseWriter, r *http.Request) {
	sbxID := r.PathValue("id")
	if sbxID == "" {
		writeError(w, http.StatusBadRequest, "sandbox id required")
		return
	}

	id := d.Identity.Extract(r)
	if d.Identity.Required() && id.Empty() {
		writeError(w, http.StatusUnauthorized, "identity required")
		return
	}

	sbx, ok := d.Registry.Get(sbxID)
	if !ok {
		writeError(w, http.StatusNotFound, "sandbox not found")
		return
	}

	if d.Identity.Required() && !sbx.Identity.Matches(id) {
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
	id := d.Identity.Extract(r)
	if d.Identity.Required() && id.Empty() {
		writeError(w, http.StatusUnauthorized, "identity required")
		return
	}

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

	// Check template limit.
	if d.Templates.Count() >= d.Config.Limits.MaxTemplates {
		writeError(w, http.StatusServiceUnavailable, "template limit reached")
		return
	}

	// Find the sandbox.
	sbx, ok := d.Registry.Get(req.SandboxID)
	if !ok {
		writeError(w, http.StatusNotFound, "sandbox not found")
		return
	}

	if d.Identity.Required() && !sbx.Identity.Matches(id) {
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
	id := d.Identity.Extract(r)
	if d.Identity.Required() && id.Empty() {
		writeError(w, http.StatusUnauthorized, "identity required")
		return
	}

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

	id := d.Identity.Extract(r)
	if d.Identity.Required() && id.Empty() {
		writeError(w, http.StatusUnauthorized, "identity required")
		return
	}

	tpl, ok := d.Templates.Get(tplID)
	if !ok {
		writeError(w, http.StatusNotFound, "template not found")
		return
	}

	if d.Identity.Required() && tpl.Identity != id.Value {
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

	id := d.Identity.Extract(r)
	if d.Identity.Required() && id.Empty() {
		writeError(w, http.StatusUnauthorized, "identity required")
		return
	}

	tpl, ok := d.Templates.Get(tplID)
	if !ok {
		writeError(w, http.StatusNotFound, "template not found")
		return
	}

	if d.Identity.Required() && tpl.Identity != id.Value {
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

func (d *Daemon) handleHealth(w http.ResponseWriter, r *http.Request) {
	resp := map[string]interface{}{
		"status":    "ok",
		"sandboxes": d.Registry.Count(),
		"pool":      d.Pool.Statuses(),
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
