package middleware

import (
	"bufio"
	"net"
	"net/http"
)
// StatusRecorder wraps http.ResponseWriter to capture status code and response size.
type StatusRecorder struct {
	http.ResponseWriter
	Status int
	Bytes  int
}

func (r *StatusRecorder) WriteHeader(code int) {
	r.Status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *StatusRecorder) Write(b []byte) (int, error) {
	if r.Status == 0 {
		r.Status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.Bytes += n
	return n, err
}

// Hijack implements the http.Hijacker interface.
// Required for WebSocket upgrade — gorilla/websocket calls Hijack
// to take over the underlying TCP connection.
func (r *StatusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return r.ResponseWriter.(http.Hijacker).Hijack()
}