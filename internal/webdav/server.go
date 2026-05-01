package webdav

import (
	"net/http"

	"github.com/Mi-Bee-Studio/MiBeeNvr/internal/storage"
	"golang.org/x/net/webdav"
)

// Server provides a read-only WebDAV server for browsing camera recordings.
type Server struct {
	store      *storage.Manager
	pathPrefix string
	authMW     func(http.Handler) http.Handler
}

// NewServer creates a new read-only WebDAV server.
// store provides the root directory for served files.
// pathPrefix is the URL prefix (e.g. "/dav").
// authMW is an optional authentication middleware; pass nil to skip auth.
func NewServer(store *storage.Manager, pathPrefix string, authMW func(http.Handler) http.Handler) *Server {
	if pathPrefix == "" {
		pathPrefix = "/dav"
	}
	return &Server{
		store:      store,
		pathPrefix: pathPrefix,
		authMW:     authMW,
	}
}

// Handler returns an http.Handler that serves read-only WebDAV requests.
// Only PROPFIND, GET, HEAD, and OPTIONS are allowed; all other methods return 403.
func (s *Server) Handler() http.Handler {
	davHandler := &webdav.Handler{
		Prefix:     s.pathPrefix,
		FileSystem: webdav.Dir(s.store.RootDir()),
		LockSystem: webdav.NewMemLS(),
	}

	var handler http.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			davHandler.ServeHTTP(w, r)
		case "PROPFIND":
			davHandler.ServeHTTP(w, r)
		default:
			http.Error(w, "Forbidden: read-only WebDAV server", http.StatusForbidden)
		}
	})

	if s.authMW != nil {
		handler = s.authMW(handler)
	}

	return handler
}
