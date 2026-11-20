package systemapi

// Exported surface for the api root's tests. Each handler wrapper constructs a
// per-call server — the route-time server in Register keeps the DB-txn rate
// state, so only stateless paths go through here.

import (
	"net/http"
	"time"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/config"
)

func FormatUptime(d time.Duration) string { return formatUptime(d) }

func FormatOffset(seconds int) string { return formatOffset(seconds) }

func BuildAPIKeyInfo(keys []config.APIKeyConfig, lastUsed map[string]time.Time) []map[string]any {
	return buildAPIKeyInfo(keys, lastUsed)
}

func HumanBytes(b int64) string { return humanBytes(b) }

func ParseProcStat(data []byte) (total, idle, iowait uint64, err error) {
	return parseProcStat(data)
}

func ParseLoadAvg(data []byte) (one, five, fifteen float64, err error) {
	return parseLoadAvg(data)
}

func ReadCPURaw() (total, idle, iowait uint64, err error) {
	return readCPURaw()
}

func ReadMemoryInfo() (total, available uint64, err error) {
	return readMemoryInfo()
}

func ReadNetworkInfo() (bytesSent, bytesRecv uint64, err error) {
	return readNetworkInfo()
}

func ReadProcessRSS() uint64 { return readProcessRSS() }

func HandleSystemStats(deps Deps, w http.ResponseWriter, r *http.Request) {
	(&server{deps: deps}).handleSystemStats(w, r)
}

func HandleStorageCandidates(deps Deps, w http.ResponseWriter, r *http.Request) {
	(&server{deps: deps}).handleStorageCandidates(w, r)
}

func HandleUpdateSettings(deps Deps, w http.ResponseWriter, r *http.Request) {
	(&server{deps: deps}).handleUpdateSettings(w, r)
}

func HandleAddStorageCandidate(deps Deps, w http.ResponseWriter, r *http.Request) {
	(&server{deps: deps}).handleAddStorageCandidate(w, r)
}

func HandleGetCameraStorageRoot(deps Deps, w http.ResponseWriter, r *http.Request) {
	(&server{deps: deps}).handleGetCameraStorageRoot(w, r)
}

func HandleRemoveStorageCandidate(deps Deps, w http.ResponseWriter, r *http.Request) {
	(&server{deps: deps}).handleRemoveStorageCandidate(w, r)
}

func HandleSetCameraStorageRoot(deps Deps, w http.ResponseWriter, r *http.Request) {
	(&server{deps: deps}).handleSetCameraStorageRoot(w, r)
}

func HandleGenerateAPIKey(deps Deps, w http.ResponseWriter, r *http.Request) {
	(&server{deps: deps}).handleGenerateAPIKey(w, r)
}

func HandleRevokeAPIKey(deps Deps, w http.ResponseWriter, r *http.Request) {
	(&server{deps: deps}).handleRevokeAPIKey(w, r)
}

func SyncAPIKeyStore(deps Deps) { (&server{deps: deps}).syncAPIKeyStore() }

func APIKeyLastUsed(deps Deps) map[string]time.Time {
	return (&server{deps: deps}).apiKeyLastUsed()
}
