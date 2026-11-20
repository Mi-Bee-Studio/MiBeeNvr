package gbapi

import (
	"net/http"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/httpx"
	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/slogx"
)

// logger keeps the historical "api" component identity so journal lines are
// byte-identical across the extraction.
var logger = slogx.Component("api")

// WriteError delegates to the shared leaf helper.
func WriteError(w http.ResponseWriter, status int, msg string) {
	httpx.WriteError(w, status, msg)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	httpx.WriteJSON(w, status, v)
}
