package daemon

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/byggflow/sandbox/internal/identity"
)

func (d *Daemon) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// In multi-tenant mode, force identity filter to the authenticated identity.
	// In single-user mode, allow optional filtering.
	identityFilter := r.URL.Query().Get("identity")
	if d.Verifier() != nil {
		identityFilter = r.Header.Get(identity.Header)
	}

	// Subscribe BEFORE replaying history so we don't miss events in the gap.
	subID, ch := d.Events.Subscribe(64)
	if ch == nil {
		writeError(w, http.StatusServiceUnavailable, "too many event subscribers")
		return
	}
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

	// In multi-tenant mode, force identity filter to the authenticated identity.
	identityFilter := r.URL.Query().Get("identity")
	if d.Verifier() != nil {
		identityFilter = r.Header.Get(identity.Header)
	}

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
