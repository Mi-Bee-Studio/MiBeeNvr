package api

// Transitional system-domain shims: the api root's tests call these Handler
// methods / package helpers directly; they delegate into systemapi. Delete as
// the tests migrate.

import (
	"net/http"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/api/systemapi"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

func (h *Handler) SetTranscodeManager(mgr TranscodeManagerAPI) { h.transcodeMgr = mgr }

func (h *Handler) SetStorageMigrator(m StorageMigrator) { h.migrationMgr = m }

func (h *Handler) handleSystemStats(w http.ResponseWriter, r *http.Request) {
	systemapi.HandleSystemStats(systemDeps(h), w, r)
}

func (h *Handler) handleStorageCandidates(w http.ResponseWriter, r *http.Request) {
	systemapi.HandleStorageCandidates(systemDeps(h), w, r)
}

func (h *Handler) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	systemapi.HandleUpdateSettings(systemDeps(h), w, r)
}

func (h *Handler) handleAddStorageCandidate(w http.ResponseWriter, r *http.Request) {
	systemapi.HandleAddStorageCandidate(systemDeps(h), w, r)
}

func (h *Handler) handleGetCameraStorageRoot(w http.ResponseWriter, r *http.Request) {
	systemapi.HandleGetCameraStorageRoot(systemDeps(h), w, r)
}

func (h *Handler) handleRemoveStorageCandidate(w http.ResponseWriter, r *http.Request) {
	systemapi.HandleRemoveStorageCandidate(systemDeps(h), w, r)
}

func (h *Handler) handleSetCameraStorageRoot(w http.ResponseWriter, r *http.Request) {
	systemapi.HandleSetCameraStorageRoot(systemDeps(h), w, r)
}

func (h *Handler) handleGenerateAPIKey(w http.ResponseWriter, r *http.Request) {
	systemapi.HandleGenerateAPIKey(systemDeps(h), w, r)
}

func (h *Handler) handleRevokeAPIKey(w http.ResponseWriter, r *http.Request) {
	systemapi.HandleRevokeAPIKey(systemDeps(h), w, r)
}

func (h *Handler) syncAPIKeyStore() { systemapi.SyncAPIKeyStore(systemDeps(h)) }

func (h *Handler) apiKeyLastUsed() map[string]time.Time {
	return systemapi.APIKeyLastUsed(systemDeps(h))
}

func formatUptime(d time.Duration) string { return systemapi.FormatUptime(d) }

func formatOffset(seconds int) string { return systemapi.FormatOffset(seconds) }

func buildAPIKeyInfo(keys []config.APIKeyConfig, lastUsed map[string]time.Time) []map[string]any {
	return systemapi.BuildAPIKeyInfo(keys, lastUsed)
}

func humanBytes(b int64) string { return systemapi.HumanBytes(b) }

func parseProcStat(data []byte) (total, idle, iowait uint64, err error) {
	return systemapi.ParseProcStat(data)
}

func parseLoadAvg(data []byte) (one, five, fifteen float64, err error) {
	return systemapi.ParseLoadAvg(data)
}

func readCPURaw() (total, idle, iowait uint64, err error) {
	return systemapi.ReadCPURaw()
}

func readMemoryInfo() (total, available uint64, err error) {
	return systemapi.ReadMemoryInfo()
}

func readNetworkInfo() (bytesSent, bytesRecv uint64, err error) {
	return systemapi.ReadNetworkInfo()
}

func readProcessRSS() uint64 { return systemapi.ReadProcessRSS() }
