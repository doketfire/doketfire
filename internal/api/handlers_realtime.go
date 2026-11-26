package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/doketfire/doketfire/internal/collections"
	"github.com/doketfire/doketfire/internal/realtime"
	"github.com/doketfire/doketfire/internal/rules"
	"github.com/doketfire/doketfire/internal/storage"
)

// handleRealtimeStream implements GET /api/realtime using Server-Sent Events.
func (s *Server) handleRealtimeStream(w http.ResponseWriter, r *http.Request) {
	last := s.hub.Sequence()
	if raw := firstNonEmpty(r.Header.Get("Last-Event-ID"), r.URL.Query().Get("lastEventId")); raw != "" {
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_last_event_id", "Last-Event-ID must be an unsigned integer", nil)
			return
		}
		last = n
	}
	client, err := s.hub.Connect(last)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "realtime_unavailable", err.Error(), nil)
		return
	}
	defer s.hub.Unregister(client.ID)

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache, no-transform")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	rc := http.NewResponseController(w)
	if err := rc.Flush(); err != nil {
		return
	}
	if err := writeSSE(w, rc, "connect", 0, map[string]any{"clientId": client.ID}); err != nil {
		return
	}

	heartbeat := time.NewTicker(20 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, ok := <-client.Events():
			if !ok {
				return
			}
			if err := writeSSE(w, rc, ev.Collection, ev.Seq, ev); err != nil {
				return
			}
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
				return
			}
			if err := rc.Flush(); err != nil {
				return
			}
		}
	}
}

func writeSSE(w http.ResponseWriter, rc *http.ResponseController, event string, id uint64, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if id > 0 {
		if _, err := fmt.Fprintf(w, "id: %d\n", id); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload); err != nil {
		return err
	}
	return rc.Flush()
}

type realtimeSubscribeRequest struct {
	ClientID      string   `json:"clientId"`
	Subscriptions []string `json:"subscriptions"`
}

// handleRealtimeSubscribe implements POST /api/realtime.
func (s *Server) handleRealtimeSubscribe(w http.ResponseWriter, r *http.Request) {
	var req realtimeSubscribeRequest
	if err := s.decodeJSON(w, r, &req); err != nil {
		s.writeServiceError(w, r, err)
		return
	}
	if req.ClientID == "" {
		writeError(w, http.StatusBadRequest, "invalid_client", "clientId is required", nil)
		return
	}
	ident := realtime.Identity{}
	if id := identityFrom(r); id != nil {
		ident.Data = id.AuthMap()
		ident.Admin = id.IsAdmin()
	}
	err := s.hub.Subscribe(req.ClientID, ident, req.Subscriptions)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, realtime.ErrClientNotFound):
		writeError(w, http.StatusNotFound, "client_not_found", "realtime client is not connected", nil)
	case errors.Is(err, realtime.ErrInvalidSubscription):
		writeError(w, http.StatusUnprocessableEntity, "invalid_subscription", err.Error(), nil)
	default:
		s.writeServiceError(w, r, err)
	}
}

// authorizeRealtimeEvent evaluates list/view rules before delivering an event.
// It runs under the hub lock, so it must stay fast and never block indefinitely.
func (s *Server) authorizeRealtimeEvent(ident realtime.Identity, operation, collection string, doc collections.Document) bool {
	if ident.Admin {
		return true
	}
	var def *collections.CollectionDef
	if err := s.store.View(func(tx storage.ReadTx) error {
		var err error
		def, err = tx.Collection(collection)
		return err
	}); err != nil {
		return false
	}
	var resource map[string]any
	if doc != nil {
		resource = map[string]any(doc.SanitizeView())
	}
	allowed, err := s.rules.Allowed(def, operation, false, rules.Context{
		Operation: operation,
		Identity:  ident.Data,
		Resource:  resource,
	})
	if err != nil {
		s.logger.Warn("realtime rule evaluation failed",
			"collection", collection, "operation", operation, "error", err)
		return false
	}
	return allowed
}
