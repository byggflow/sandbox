package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/byggflow/sandbox/internal/identity"
)

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

	// Capture the template via the pluggable backend (Docker commit, Firecracker snapshot, etc.).
	tplBackend := d.TemplateBackendFor(sbx)
	imageTag := "byggflow-sandbox:" + tplID
	ref, imageSize, err := tplBackend.Capture(r.Context(), sbx.ContainerID, imageTag)
	if err != nil {
		d.Log.Error("template capture failed", "sandbox", req.SandboxID, "error", err)
		writeError(w, http.StatusInternalServerError, "template capture failed")
		return
	}

	// Check max template size.
	if d.Config.Limits.MaxTemplateSize != "" {
		maxSize, parseErr := parseByteSize(d.Config.Limits.MaxTemplateSize)
		if parseErr == nil && imageSize > maxSize {
			if err := tplBackend.Remove(r.Context(), ref); err != nil {
				d.Log.Error("failed to remove oversized template image", "ref", ref, "error", err)
			}
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
		Image:     ref,
		Backend:   sbx.RuntimeName,
		Identity:  id.Value,
		Size:      imageSize,
		CreatedAt: time.Now(),
	}

	if err := d.Templates.Add(tpl); err != nil {
		d.Log.Error("template registration failed", "id", tplID, "error", err)
		writeError(w, http.StatusInternalServerError, "template registration failed")
		return
	}

	d.Log.Info("template created", "id", tplID, "sandbox", req.SandboxID, "image", ref)
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

	// Remove underlying image/snapshot via the appropriate backend.
	tplBackend := d.TemplateBackend
	if tb, ok := d.TemplateBackends[tpl.Backend]; ok {
		tplBackend = tb
	}
	if err := tplBackend.Remove(r.Context(), tpl.Image); err != nil {
		d.Log.Error("failed to remove template image on delete", "id", tplID, "image", tpl.Image, "error", err)
	}

	// Remove from registry.
	d.Templates.Remove(tplID)

	d.Log.Info("template deleted", "id", tplID)
	w.WriteHeader(http.StatusNoContent)
}
