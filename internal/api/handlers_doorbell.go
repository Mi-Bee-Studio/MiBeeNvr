package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"

	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/doorbell"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// DoorbellService is the API-facing seam over the doorbell server (#971).
type DoorbellService interface {
	Calls() []*doorbell.Call
	Answer(callID string) error
	Reject(callID string, decline bool) error
	Hangup(callID string) error
	TalkBridge(callID string) (*doorbell.TalkBridge, error)
	// RegisteredStations lists the live mode-A registrations (settings
	// panel status strip).
	RegisteredStations() []doorbell.StationStatus
	// SnapshotPath resolves a call's snapshot (storage-root-relative) to an
	// absolute path for serving; empty when the call has no snapshot.
	SnapshotPath(call *doorbell.Call) (string, bool)
}

var doorbellTalkUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin:     checkWSOrigin,
}

// registerDoorbellRoutes mounts /doorbell/* (inside the authed group).
func (h *Handler) registerDoorbellRoutes(r chi.Router) {
	r.Get("/api/doorbell/calls", h.handleDoorbellCalls)
	r.Get("/api/doorbell/status", h.handleDoorbellStatus)
	r.Post("/api/doorbell/calls/{callID}/answer", h.handleDoorbellAnswer)
	r.Post("/api/doorbell/calls/{callID}/reject", h.handleDoorbellReject)
	r.Post("/api/doorbell/calls/{callID}/bye", h.handleDoorbellBye)
	r.Get("/api/doorbell/calls/{callID}/talk", h.handleDoorbellTalkWS)
	r.Get("/api/doorbell/calls/{callID}/snapshot", h.handleDoorbellSnapshot)
}

// handleDoorbellStatus reports the panel-facing runtime state: whether the
// config enables the ingress, whether the server is actually running (the
// two diverge between a settings save and the restart it needs), and the
// live mode-A registrations. Always 200 — unlike the call endpoints this is
// meaningful when the service is absent.
func (h *Handler) handleDoorbellStatus(w http.ResponseWriter, r *http.Request) {
	registered := []doorbell.StationStatus{}
	if h.doorbell != nil {
		registered = h.doorbell.RegisteredStations()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":    h.config != nil && h.config.Doorbell.Enabled,
		"running":    h.doorbell != nil,
		"registered": registered,
	})
}

// SetDoorbellService wires the doorbell server into the API (builders_http).
func (h *Handler) SetDoorbellService(s DoorbellService) {
	h.doorbell = s
}

func (h *Handler) handleDoorbellCalls(w http.ResponseWriter, r *http.Request) {
	if h.doorbell == nil {
		WriteError(w, http.StatusNotImplemented, "doorbell is not enabled")
		return
	}
	calls := h.doorbell.Calls()
	// Newest first — the popup cares about the freshest ring. Snapshot()
	// takes the per-call lock so the sweeper can end calls concurrently.
	out := make([]map[string]any, 0, len(calls))
	for i := len(calls) - 1; i >= 0; i-- {
		c := calls[i].Snapshot()
		out = append(out, map[string]any{
			"id":            c.ID,
			"user":          c.User,
			"name":          c.Name,
			"camera_id":     c.CameraID,
			"snapshot_path": c.SnapshotPath,
			"state":         c.State,
			"started_at":    c.StartedAt,
			"answered_at":   c.AnsweredAt,
			"ended_at":      c.EndedAt,
			"end_reason":    c.EndReason,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"calls": out})
}

func (h *Handler) doorbellCallAction(w http.ResponseWriter, r *http.Request, action func(string) error) {
	if h.doorbell == nil {
		WriteError(w, http.StatusNotImplemented, "doorbell is not enabled")
		return
	}
	callID := chi.URLParam(r, "callID")
	if err := action(callID); err != nil {
		switch {
		case errors.Is(err, doorbell.ErrUnknownCall):
			WriteError(w, http.StatusNotFound, "no such call")
		case errors.Is(err, doorbell.ErrCallState):
			WriteError(w, http.StatusConflict, "call is not in the required state")
		default:
			WriteError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (h *Handler) handleDoorbellAnswer(w http.ResponseWriter, r *http.Request) {
	h.doorbellCallAction(w, r, h.doorbell.Answer)
}

func (h *Handler) handleDoorbellReject(w http.ResponseWriter, r *http.Request) {
	h.doorbellCallAction(w, r, func(id string) error { return h.doorbell.Reject(id, true) })
}

func (h *Handler) handleDoorbellBye(w http.ResponseWriter, r *http.Request) {
	h.doorbellCallAction(w, r, h.doorbell.Hangup)
}

// handleDoorbellSnapshot serves the call's ring snapshot JPEG.
func (h *Handler) handleDoorbellSnapshot(w http.ResponseWriter, r *http.Request) {
	if h.doorbell == nil {
		WriteError(w, http.StatusNotImplemented, "doorbell is not enabled")
		return
	}
	callID := chi.URLParam(r, "callID")
	var call doorbell.Call
	found := false
	for _, c := range h.doorbell.Calls() {
		if c.ID == callID {
			call = c.Snapshot()
			found = true
			break
		}
	}
	if !found {
		WriteError(w, http.StatusNotFound, "no such call")
		return
	}
	path, ok := h.doorbell.SnapshotPath(&call)
	if !ok {
		WriteError(w, http.StatusNotFound, "no snapshot for this call")
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, path)
}

// handleDoorbellTalkWS is the two-way G.711 bridge: binary frames carry
// raw A-law/μ-law payloads both directions — the same contract as the
// GB28181 talk WebSocket, so the frontend reuses its mic capture path.
func (h *Handler) handleDoorbellTalkWS(w http.ResponseWriter, r *http.Request) {
	if h.doorbell == nil {
		WriteError(w, http.StatusNotImplemented, "doorbell is not enabled")
		return
	}
	callID := chi.URLParam(r, "callID")
	bridge, err := h.doorbell.TalkBridge(callID)
	if err != nil {
		switch {
		case errors.Is(err, doorbell.ErrUnknownCall):
			WriteError(w, http.StatusNotFound, "no such call")
		case errors.Is(err, doorbell.ErrCallState):
			WriteError(w, http.StatusConflict, "call is not answered")
		default:
			WriteError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	defer bridge.Close()

	conn, err := doorbellTalkUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	// doorbell → browser
	go func() {
		for payload := range bridge.Out {
			if err := conn.WriteMessage(websocket.BinaryMessage, payload); err != nil {
				return
			}
		}
	}()

	// browser → doorbell (close/teardown mirrors the gb28181 talk loop)
	for {
		mt, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		if mt == websocket.TextMessage {
			var cmd struct {
				Close bool `json:"close"`
			}
			if json.Unmarshal(data, &cmd) == nil && cmd.Close {
				return
			}
			continue
		}
		if mt == websocket.BinaryMessage {
			bridge.In <- data
		}
	}
}

// doorbellServerAdapter binds the doorbell server to the API seam and
// resolves snapshot paths through the storage manager (containment via
// ValidatePath, same as the recording download endpoint).
type doorbellServerAdapter struct {
	srv   DoorbellCore
	store *storage.Manager
}

// DoorbellCore is the subset of *doorbell.Server the adapter needs.
type DoorbellCore = interface {
	Calls() []*doorbell.Call
	Answer(callID string) error
	Reject(callID string, decline bool) error
	Hangup(callID string) error
	TalkBridge(callID string) (*doorbell.TalkBridge, error)
	RegisteredStations() []doorbell.StationStatus
}

// NewDoorbellAdapter builds the API seam for a doorbell server.
func NewDoorbellAdapter(srv DoorbellCore, store *storage.Manager) DoorbellService {
	return &doorbellServerAdapter{srv: srv, store: store}
}

func (a *doorbellServerAdapter) Calls() []*doorbell.Call { return a.srv.Calls() }
func (a *doorbellServerAdapter) Answer(id string) error  { return a.srv.Answer(id) }
func (a *doorbellServerAdapter) Reject(id string, decline bool) error {
	return a.srv.Reject(id, decline)
}
func (a *doorbellServerAdapter) Hangup(id string) error { return a.srv.Hangup(id) }
func (a *doorbellServerAdapter) TalkBridge(id string) (*doorbell.TalkBridge, error) {
	return a.srv.TalkBridge(id)
}

func (a *doorbellServerAdapter) RegisteredStations() []doorbell.StationStatus {
	return a.srv.RegisteredStations()
}

func (a *doorbellServerAdapter) SnapshotPath(call *doorbell.Call) (string, bool) {
	if call == nil || call.SnapshotPath == "" || a.store == nil {
		return "", false
	}
	path := filepath.ToSlash(call.SnapshotPath)
	abs, err := storage.ValidatePath(a.store.RootDir(), path)
	if err != nil {
		return "", false
	}
	return abs, true
}
