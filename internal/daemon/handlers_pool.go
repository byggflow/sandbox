package daemon

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
)

func (d *Daemon) handleListProfiles(w http.ResponseWriter, r *http.Request) {
	type profileEntry struct {
		Name    string  `json:"name"`
		Image   string  `json:"image"`
		Memory  string  `json:"memory"`
		CPU     float64 `json:"cpu"`
		Storage string  `json:"storage"`
	}
	profiles := []profileEntry{}
	for name, base := range d.Config.Pool.Base {
		profiles = append(profiles, profileEntry{
			Name:    name,
			Image:   base.Image,
			Memory:  base.Memory,
			CPU:     base.CPU,
			Storage: base.StorageOrDefault(),
		})
	}
	slices.SortFunc(profiles, func(a, b profileEntry) int {
		return strings.Compare(a.Name, b.Name)
	})
	writeJSON(w, http.StatusOK, profiles)
}

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

	if body.Count < 0 {
		writeError(w, http.StatusBadRequest, "count must be non-negative")
		return
	}

	if err := d.Pool.Resize(r.Context(), profile, body.Count); err != nil {
		d.Log.Error("pool resize failed", "profile", profile, "error", err)
		writeError(w, http.StatusInternalServerError, "pool resize failed")
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
		d.Log.Error("pool flush failed", "profile", profile, "error", err)
		writeError(w, http.StatusInternalServerError, "pool flush failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
