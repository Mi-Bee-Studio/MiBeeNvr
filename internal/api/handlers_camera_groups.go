package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"
)

// Camera group registry endpoints (v37). The registry lets an EMPTY group
// exist ahead of any member camera; per-camera group_name stays the membership
// source of truth, so rename/delete walk the member cameras in one call
// (drag-and-drop moves go through PUT /api/cameras/{id} instead).

// maxCameraGroupName caps group label length — they are user-facing labels
// rendered in section headers, not identifiers.
const maxCameraGroupName = 64

// maxCameraGroups caps the registry size (the order payload is the full list).
const maxCameraGroups = 200

func normalizeCameraGroupName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", fmt.Errorf("group name cannot be empty")
	}
	if utf8.RuneCountInString(name) > maxCameraGroupName {
		return "", fmt.Errorf("group name too long (max %d chars)", maxCameraGroupName)
	}
	return name, nil
}

func (h *Handler) handleListCameraGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := h.db.ListCameraGroups(r.Context())
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to list camera groups")
		return
	}
	if groups == nil {
		groups = []string{}
	}
	writeJSON(w, http.StatusOK, groups)
}

// handleSetCameraGroupsOrder stores the full named-group order (v38
// drag-to-reorder on the management page). Unknown names are registered
// (reordering a derived-only group persists it); names absent from the list
// keep their old positions.
func (h *Handler) handleSetCameraGroupsOrder(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Names []string `json:"names"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if len(body.Names) > maxCameraGroups {
		WriteError(w, http.StatusBadRequest, fmt.Sprintf("too many groups (max %d)", maxCameraGroups))
	}
	seen := make(map[string]bool, len(body.Names))
	for _, raw := range body.Names {
		name, err := normalizeCameraGroupName(raw)
		if err != nil {
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		if seen[name] {
			WriteError(w, http.StatusBadRequest, "duplicate group name: "+name)
			return
		}
		seen[name] = true
	}
	if err := h.db.SetCameraGroupsOrder(r.Context(), body.Names); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to set camera group order")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"count": len(body.Names)})
}

func (h *Handler) handleCreateCameraGroup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	name, err := normalizeCameraGroupName(body.Name)
	if err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := h.db.UpsertCameraGroup(r.Context(), name); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to create camera group")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"name": name})
}

func (h *Handler) handleRenameCameraGroup(w http.ResponseWriter, r *http.Request) {
	oldName := chi.URLParam(r, "name")
	var body struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	newName, err := normalizeCameraGroupName(body.Name)
	if err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	if oldName == "" || oldName == newName {
		WriteError(w, http.StatusBadRequest, "rename requires a distinct new group name")
		return
	}
	// Renaming onto an existing name MERGES the two groups (both member sets
	// land on the target); that is a valid operation, not a conflict.
	if err := h.db.RenameCameraGroup(r.Context(), oldName, newName); err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to rename camera group")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"name": newName})
}

func (h *Handler) handleDeleteCameraGroup(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if name == "" {
		WriteError(w, http.StatusBadRequest, "group name cannot be empty")
		return
	}
	ungrouped, err := h.db.DeleteCameraGroup(r.Context(), name)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, "failed to delete camera group")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ungrouped": ungrouped})
}
