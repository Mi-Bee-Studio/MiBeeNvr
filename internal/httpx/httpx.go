// Package httpx holds HTTP response helpers shared by the API layer and
// satellite HTTP handlers (internal/upload), so the latter do not need to
// depend on the top-level api package.
package httpx

import (
	"encoding/json"
	"net/http"
)

// WriteJSON writes v as a JSON response with the given status code.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// WriteError writes the error response as {"error": msg, "code": code} with
// the given status. Every error response in the system carries this shape —
// hand-written messages get a status-derived code here, typed (CodedError)
// failures get their specific code via api.writeAPIError / model.ErrorCode
// (#1009: one error track, two entry points).
func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg, "code": codeForStatus(status)})
}

// codeForStatus maps an HTTP status to the coarse machine-readable code used
// when no typed error is available. Add specific statuses as callers need
// them; unknown 4xx fall back to CLIENT_ERROR, everything else to INTERNAL.
func codeForStatus(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "BAD_REQUEST"
	case http.StatusUnauthorized:
		return "UNAUTHORIZED"
	case http.StatusForbidden:
		return "FORBIDDEN"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusMethodNotAllowed:
		return "METHOD_NOT_ALLOWED"
	case http.StatusRequestTimeout:
		return "REQUEST_TIMEOUT"
	case http.StatusConflict:
		return "CONFLICT"
	case http.StatusGone:
		return "GONE"
	case http.StatusRequestEntityTooLarge:
		return "PAYLOAD_TOO_LARGE"
	case http.StatusUnsupportedMediaType:
		return "UNSUPPORTED_MEDIA_TYPE"
	case http.StatusUnprocessableEntity:
		return "UNPROCESSABLE_ENTITY"
	case http.StatusTooManyRequests:
		return "RATE_LIMITED"
	case http.StatusInternalServerError:
		return "INTERNAL"
	case http.StatusNotImplemented:
		return "NOT_IMPLEMENTED"
	case http.StatusBadGateway:
		return "BAD_GATEWAY"
	case http.StatusServiceUnavailable:
		return "SERVICE_UNAVAILABLE"
	case http.StatusGatewayTimeout:
		return "GATEWAY_TIMEOUT"
	}
	if status >= 400 && status < 500 {
		return "CLIENT_ERROR"
	}
	return "INTERNAL"
}
