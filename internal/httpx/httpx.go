// Package httpx holds HTTP response helpers shared by the API layer and
// satellite HTTP handlers (internal/upload), so the latter do not need to
// depend on the top-level api package.
package httpx

import (
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"strconv"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/model"
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

// WriteAPIError writes a typed error (model.CodedError or plain error) using
// the shared {"error", "code"} response shape — model.ErrorCode supplies the
// code (#1009: one error track). Domain packages call this directly; the api
// root keeps a writeAPIError wrapper for its remaining call sites.
func WriteAPIError(w http.ResponseWriter, status int, err error) {
	WriteJSON(w, status, map[string]string{"error": err.Error(), "code": model.ErrorCode(err)})
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

// ParsePagination parses the limit/offset query params with a caller-specified
// default limit and a hard upper bound — the single source of truth for the
// list handlers across domains (#222). offset defaults to 0, never negative.
func ParsePagination(r *http.Request, defaultLimit, maxLimit int) (limit, offset int) {
	limit = defaultLimit
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if maxLimit > 0 && limit > maxLimit {
		limit = maxLimit
	}
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	return limit, offset
}

// ValidateURL checks that a URL has a valid scheme and non-empty host — a
// basic sanity check; specific protocol validation is handled by callers.
func ValidateURL(rawURL string) bool {
	if rawURL == "" {
		return false
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	return parsed.Scheme != "" && parsed.Host != ""
}

// ValidateIP checks that a string is a valid IPv4/IPv6 address, supporting
// ip:port format.
func ValidateIP(ip string) bool {
	if host, _, err := net.SplitHostPort(ip); err == nil {
		return net.ParseIP(host) != nil
	}
	return net.ParseIP(ip) != nil
}

// WriteSSEEvent emits one server-sent event frame. Byte concatenation
// replaces fmt.Fprintf's format parsing + reflection on the per-event path
// (#875 L2); the frame layout is identical.
func WriteSSEEvent(w http.ResponseWriter, topic string, data []byte) {
	b := make([]byte, 0, len("event: \ndata: \n\n")+len(topic)+len(data))
	b = append(b, "event: "...)
	b = append(b, topic...)
	b = append(b, "\ndata: "...)
	b = append(b, data...)
	b = append(b, "\n\n"...)
	_, _ = w.Write(b)
}
