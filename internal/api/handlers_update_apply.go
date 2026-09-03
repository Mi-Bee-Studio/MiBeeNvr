package api

// Upgrade-execution API (#648): the human entry point for the #647 bare-metal
// pipeline. The app process itself cannot replace its binary (sandboxed to
// ReadWritePaths=/var/lib/mibee-nvr), so POST /apply performs the same
// handoff as the automatic hook: write a request file, start the polkit-
// authorized root helper. Cross-restart progress comes from the lifecycle
// files the helper persists in the data dir.

import (
	"net/http"
	"os"
	"os/exec"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/update"
)

// Injectable seams (tests override; mirrors the updateChecker pattern).
var (
	applyStartUnitFn  = update.StartHelperUnit
	applyUnitActiveFn = helperUnitActive
)

const helperUnit = "mibee-nvr-update.service"

// helperUnitActive reports whether the root helper unit is currently running
// (unprivileged systemctl is-active — no polkit involved).
func helperUnitActive() bool {
	return exec.Command("systemctl", "is-active", "--quiet", helperUnit).Run() == nil
}

// handleUpdateApply triggers a bare-metal upgrade via the root helper.
//
// POST /api/update/apply  (BasicAuth protected)
//
// Responses:
//   - 200 {id, state: requested|applying, from, to} — triggered (or already in flight)
//   - 409 — docker deployment (never executable) or no update available
//   - 500 — request write or helper start failed (hint included)
func (h *Handler) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if updateChecker == nil {
		WriteError(w, http.StatusServiceUnavailable, "update check is disabled")
		return
	}
	if update.Deployment() == "docker" {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":      "auto-upgrade is not available for Docker deployments — the container is immutable",
			"deployment": "docker",
			"guidance":   "use Watchtower or `docker compose pull && docker compose up -d` (see docs: deployment-autoupdate)",
		})
		return
	}
	st := updateChecker.Status()
	if !st.UpdateAvailable || st.Latest == "" {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "no update available",
			"state": "idle",
		})
		return
	}
	dataDir := h.dataDir()
	if dataDir == "" {
		WriteError(w, http.StatusServiceUnavailable, "storage root not configured")
		return
	}

	// Idempotency: while a triggered apply is still pending/running, return
	// its status instead of stacking another request.
	if _, err := os.Stat(update.RequestFilePath(dataDir)); err == nil && applyUnitActiveFn() {
		writeJSON(w, http.StatusOK, applyStatusPayload(h, dataDir))
		return
	}

	id := update.NewApplyID()
	if _, err := update.WriteRequest(dataDir, update.AutoRequest{
		TargetTag: st.Latest,
		ID:        id,
		Manual:    true,
	}); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to write update request: "+err.Error())
		return
	}
	if err := applyStartUnitFn(helperUnit); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{
			"error": "failed to start " + helperUnit + ": " + err.Error(),
			"hint":  "is the polkit rule installed (install.sh)? fallback: sudo mibee-nvr update",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":    id,
		"state": "requested",
		"from":  st.Current,
		"to":    st.Latest,
	})
}

// handleUpdateApplyStatus reports the cross-restart apply state synthesized
// from the lifecycle files and unit liveness:
//
//	applying           — helper unit is running right now
//	requested          — request file present, helper not (yet) running
//	success | failed_rolled_back | failed — terminal state from last-apply file
//	idle               — nothing ever happened
//
// GET /api/update/apply/status
func (h *Handler) handleUpdateApplyStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, applyStatusPayload(h, h.dataDir()))
}

// handleUpdateHistory returns recent upgrade history rows, newest first.
//
// GET /api/update/history
func (h *Handler) handleUpdateHistory(w http.ResponseWriter, _ *http.Request) {
	dataDir := h.dataDir()
	if dataDir == "" {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	rows, err := update.ReadHistory(dataDir, 10)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to read update history: "+err.Error())
		return
	}
	if rows == nil {
		rows = []update.HistoryEntry{}
	}
	writeJSON(w, http.StatusOK, rows)
}

func applyStatusPayload(h *Handler, dataDir string) map[string]any {
	resp := map[string]any{"state": "idle", "auto_apply": false}
	if h.config != nil {
		resp["auto_apply"] = h.config.Update.IsAutoApply()
	}
	if dataDir == "" {
		return resp
	}
	if applyUnitActiveFn() {
		resp["state"] = "applying"
	} else if _, err := os.Stat(update.RequestFilePath(dataDir)); err == nil {
		resp["state"] = "requested"
	}
	if last, ok := update.ReadLastApply(dataDir); ok {
		// A terminal state only wins once the helper has finished; while it
		// is applying, "applying" is the live truth and last-apply is the
		// stage marker.
		if resp["state"] != "applying" {
			resp["state"] = last.State
		}
		resp["id"] = last.ID
		resp["from"] = last.From
		resp["to"] = last.To
		resp["error"] = last.Error
		resp["time"] = last.UpdatedAt
	}
	return resp
}

// dataDir returns the configured storage root ("" when unavailable).
func (h *Handler) dataDir() string {
	if h.config == nil {
		return ""
	}
	return h.config.Storage.RootDir
}
