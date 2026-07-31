package api

import (
	"net/http"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/update"
)

// updateChecker is the optional in-app version checker (sensing layer only).
// Injected from main via SetUpdateChecker, mirroring the apiMetrics/SetAPIMetrics
// package-var pattern: main.appVersion lives in package main and cannot be read
// here directly. Nil = version check disabled (handlers degrade gracefully).
var updateChecker *update.Checker

// SetUpdateChecker injects the version checker into the API package. Called once
// from main after RunFree builds the app. Passing nil disables the endpoints'
// live data (they still report the running version + deployment).
func SetUpdateChecker(c *update.Checker) {
	updateChecker = c
}

// handleVersion returns the running version and deployment mode (lightweight,
// no network). Used by the frontend on first paint.
//
// GET /api/version
func (h *Handler) handleVersion(w http.ResponseWriter, r *http.Request) {
	if updateChecker == nil {
		// No checker configured: report what we can without a network call.
		writeJSON(w, http.StatusOK, map[string]any{
			"current":    "",
			"deployment": update.Deployment(),
		})
		return
	}
	writeJSON(w, http.StatusOK, updateChecker.Status())
}

// handleUpdateCheck returns the cached version-check status. Use POST to force
// a refresh (the UI "check now" button) before returning.
//
// GET  /api/update/check  → cached status
// POST /api/update/check  → force refresh, then status
func (h *Handler) handleUpdateCheck(w http.ResponseWriter, r *http.Request) {
	if updateChecker == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"current":          "",
			"deployment":       update.Deployment(),
			"update_available": false,
		})
		return
	}
	if r.Method == http.MethodPost {
		// Force a refresh; ignore the error (cache still served).
		_, _ = updateChecker.Refresh(r.Context())
	}
	writeJSON(w, http.StatusOK, updateChecker.Status())
}
