package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/migration"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
)

// Storage migration — per-camera, hot, background (#395 rework).
//
// Switching a camera's storage (or the default) is HOT: NEW segments go to
// the new root immediately. Historical recordings move afterwards through
// the background idle-time migrator (rate-limited copy, per-row rewrite,
// verified source delete), so the user never waits and never restarts. The
// only restart in the storage story is mounting a NEWLY authorized directory
// (docker bind-mount — platform-inherent).

// StorageMigrator is the migrator surface the API needs (implemented by
// *migration.Migrator; nil-tolerant for tests).
type StorageMigrator interface {
	Enqueue(cameraID, toRoot string, deleteSource bool) *migration.Job
	Status() (string, []*migration.Job)
}

// SetStorageMigrator wires the background migrator service.
func (h *Handler) SetStorageMigrator(m StorageMigrator) {
	h.migrationMgr = m
}

// validCameraRoot checks a requested camera root: it must be the default or
// one of the platform-granted candidates, and pass a file-writability probe
// (camera roots never host the database — no SQLite requirement).
func (h *Handler) validCameraRoot(root string) (string, bool) {
	root = strings.TrimRight(strings.TrimSpace(root), "/")
	if root == "" {
		return "", true
	}
	if !strings.HasPrefix(root, "/") {
		return "", false
	}
	if root != strings.TrimRight(h.config.Storage.RootDir, "/") {
		found := false
		for _, c := range h.config.Storage.Candidates {
			if root == strings.TrimRight(c, "/") {
				found = true
				break
			}
		}
		if !found {
			return "", false
		}
	}
	if err := storage.ProbeDir(root); err != nil {
		return "", false
	}
	return root, true
}

// handleSetCameraStorageRoot switches ONE camera's storage (PUT
// /api/cameras/{id}/storage-root, body {root, migrate=true, delete_source}):
// sets the per-camera override (hot — applies at the next segment) and
// enqueues the background migration of that camera's history.
func (h *Handler) handleSetCameraStorageRoot(w http.ResponseWriter, r *http.Request) {
	if h.config == nil || h.store == nil {
		WriteError(w, http.StatusInternalServerError, "storage not available")
		return
	}
	cameraID := cameraIDFromRequest(r)
	if cameraID == "" {
		WriteError(w, http.StatusBadRequest, "camera id required")
		return
	}
	var body struct {
		Root         string `json:"root"`
		Migrate      *bool  `json:"migrate"`
		DeleteSource *bool  `json:"delete_source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	root, ok := h.validCameraRoot(body.Root)
	if !ok {
		WriteError(w, http.StatusBadRequest,
			"root must be the default storage or one of the available candidates, and writable")
		return
	}

	// Synchronous capacity precheck (migrator re-checks periodically too):
	// reject a doomed switch BEFORE applying the override, so the camera is
	// never left recording onto a target that cannot hold its history.
	migrate := body.Migrate == nil || *body.Migrate
	target := root
	if target == "" {
		target = h.store.RootDir()
	}
	if migrate && target != "" && h.db != nil {
		if needed, err := h.db.SumMigratableBytes(r.Context(), cameraID, target); err == nil && needed > 0 {
			if _, free, err := h.store.GetRootUsage(target); err == nil && int64(float64(free)*0.8) < needed {
				WriteError(w, http.StatusBadRequest, fmt.Sprintf(
					"insufficient space on %s: need %s, only %s free (20%% safety margin) — pick a larger storage or skip history migration",
					target, humanBytes(needed), humanBytes(free)))
				return
			}
		}
	}

	h.store.SetCameraRoot(cameraID, root)
	if h.config.Storage.CameraRoots == nil && root != "" {
		h.config.Storage.CameraRoots = make(map[string]string)
	}
	if root == "" {
		delete(h.config.Storage.CameraRoots, cameraID)
	} else if h.config.Storage.CameraRoots != nil {
		h.config.Storage.CameraRoots[cameraID] = root
	}
	if err := config.Save(h.configPath, h.config); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to save config")
		return
	}

	// Enqueue the history migration whenever a migrate-able target exists:
	// an explicit override OR the default (switching home migrates too).
	resp := map[string]any{"status": "updated", "camera_id": cameraID, "storage_root": root}
	if h.migrationMgr != nil {
		if migrate && target != "" {
			deleteSource := body.DeleteSource != nil && *body.DeleteSource
			job := h.migrationMgr.Enqueue(cameraID, h.store.RootFor(cameraID), deleteSource)
			resp["migration"] = job
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleGetCameraStorageRoot reports one camera's effective storage root and
// its active migration job (GET /api/cameras/{id}/storage-root).
func (h *Handler) handleGetCameraStorageRoot(w http.ResponseWriter, r *http.Request) {
	if h.config == nil || h.store == nil {
		WriteError(w, http.StatusInternalServerError, "storage not available")
		return
	}
	cameraID := cameraIDFromRequest(r)
	resp := map[string]any{
		"camera_id":      cameraID,
		"override_root":  h.store.CameraRoot(cameraID),
		"effective_root": h.store.RootFor(cameraID),
		"default_root":   h.store.RootDir(),
	}
	if h.migrationMgr != nil {
		_, jobs := h.migrationMgr.Status()
		for _, j := range jobs {
			if j.CameraID == cameraID && (j.State == "queued" || j.State == "running" || j.State == "paused") {
				resp["migration"] = j
				break
			}
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleStartStorageMigrate is the batch entry (POST /api/storage/migrate,
// body {target, delete_source}): switches the DEFAULT storage to target
// (hot), clears per-camera overrides (everything follows the new default),
// and enqueues a background migration per camera with history elsewhere.
func (h *Handler) handleStartStorageMigrate(w http.ResponseWriter, r *http.Request) {
	if h.config == nil || h.store == nil || h.db == nil {
		WriteError(w, http.StatusInternalServerError, "storage not available")
		return
	}
	var body struct {
		Target       string `json:"target"`
		DeleteSource *bool  `json:"delete_source"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	target := strings.TrimRight(strings.TrimSpace(body.Target), "/")
	if target == "" || !strings.HasPrefix(target, "/") {
		WriteError(w, http.StatusBadRequest, "target must be an absolute path")
		return
	}
	from := h.config.Storage.RootDir
	if target == from {
		WriteError(w, http.StatusBadRequest, "target is already the current recording root")
		return
	}
	if strings.HasPrefix(target+"/", from+"/") || strings.HasPrefix(from+"/", target+"/") {
		WriteError(w, http.StatusBadRequest, "target must not contain (or be inside) the current recording root")
		return
	}
	if err := storage.ProbeRoot(target); err != nil {
		WriteError(w, http.StatusBadRequest, "target not usable: "+err.Error())
		return
	}

	// Hot switch of the default; per-camera overrides are cleared so every
	// camera follows the new default.
	if err := h.store.SetRootDir(target); err != nil {
		WriteError(w, http.StatusBadRequest, "cannot switch recording root: "+err.Error())
		return
	}
	h.config.Storage.RootDir = target
	h.config.Storage.CameraRoots = nil
	if err := config.Save(h.configPath, h.config); err != nil {
		if rerr := h.store.SetRootDir(from); rerr != nil {
			slog.Warn("storage root rollback failed after config-save error", "error", rerr)
		}
		h.config.Storage.RootDir = from
		WriteError(w, http.StatusInternalServerError, "failed to save config")
		return
	}

	// Enqueue one background job per camera that has recordings outside the
	// new default (DB rows decide — the authoritative backlog).
	deleteSource := body.DeleteSource != nil && *body.DeleteSource
	var enqueued int
	if h.migrationMgr != nil {
		if cams, err := h.db.ListCameraIDs(r.Context()); err == nil {
			for _, camID := range cams {
				recs, err := h.db.ListMigratableRecordings(r.Context(), camID, target)
				if err != nil || len(recs) == 0 {
					continue
				}
				h.migrationMgr.Enqueue(camID, target, deleteSource)
				enqueued++
			}
		}
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"status": "updated", "target": target, "jobs_enqueued": enqueued,
	})
}

// handleStorageMigrateStatus reports the background migrator queue.
func (h *Handler) handleStorageMigrateStatus(w http.ResponseWriter, r *http.Request) {
	if h.migrationMgr == nil {
		writeJSON(w, http.StatusOK, map[string]any{"state": "idle", "jobs": []any{}})
		return
	}
	state, jobs := h.migrationMgr.Status()
	writeJSON(w, http.StatusOK, map[string]any{"state": state, "jobs": jobs})
}

// cameraIDFromRequest pulls the camera id from the chi URL param or, for
// non-router callers, the X-Camera-Id header.
func cameraIDFromRequest(r *http.Request) string {
	if id := r.PathValue("id"); id != "" {
		return id
	}
	return r.Header.Get("X-Camera-Id")
}

func humanBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	default:
		return fmt.Sprintf("%d B", b)
	}
}
