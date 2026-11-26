// Package server is the HTTP surface: the JSON API, the collaboration socket and
// the embedded web app.
package server

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/renatobardi/oute-draw/internal/config"
	"github.com/renatobardi/oute-draw/internal/hub"
	"github.com/renatobardi/oute-draw/internal/store"
)

const (
	maxJSONBody = 40 << 20
	maxFileBody = 20 << 20
)

type Server struct {
	cfg   config.Config
	store *store.Store
	hub   *hub.Hub
	web   fs.FS
	log   *slog.Logger
	// http is the client for outbound calls (the AI provider).
	http *http.Client

	usersMu sync.Mutex
	users   map[string]cachedUser
}

type cachedUser struct {
	user    store.User
	expires time.Time
}

func New(cfg config.Config, st *store.Store, web fs.FS, log *slog.Logger) *Server {
	return &Server{
		cfg: cfg, store: st, hub: hub.New(), web: web, log: log,
		http:  &http.Client{Timeout: 90 * time.Second},
		users: map[string]cachedUser{},
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := s.store.Ping(r.Context()); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, "ok")
	})

	// Workspace API: every route needs a signed-in user.
	u := func(pattern string, role string, h func(http.ResponseWriter, *http.Request, store.User)) {
		mux.HandleFunc(pattern, s.withUser(role, h))
	}
	u("GET /api/me", "viewer", s.handleMe)
	u("GET /api/users", "viewer", s.handleListUsers)
	u("PATCH /api/users/{id}", "admin", s.handleSetUserRole)
	u("GET /api/library", "viewer", s.handleGetLibrary)
	u("PUT /api/library", "viewer", s.handlePutLibrary)

	u("GET /api/collections", "viewer", s.handleListCollections)
	u("POST /api/collections", "editor", s.handleCreateCollection)
	u("PATCH /api/collections/{id}", "editor", s.handleRenameCollection)
	u("DELETE /api/collections/{id}", "editor", s.handleDeleteCollection)

	u("GET /api/scenes", "viewer", s.handleListScenes)
	u("POST /api/scenes", "editor", s.handleCreateScene)
	u("PATCH /api/scenes/{id}", "editor", s.handlePatchScene)
	u("DELETE /api/scenes/{id}", "editor", s.handleDeleteScene)
	u("POST /api/scenes/{id}/restore", "editor", s.handleRestoreScene)
	u("POST /api/scenes/{id}/duplicate", "editor", s.handleDuplicateScene)
	u("GET /api/scenes/{id}/thumbnail", "viewer", s.handleThumbnail)

	u("GET /api/scenes/{id}/versions", "viewer", s.handleListVersions)
	u("POST /api/scenes/{id}/versions", "editor", s.handleCreateVersion)
	u("GET /api/scenes/{id}/versions/{vid}", "viewer", s.handleGetVersion)
	u("DELETE /api/scenes/{id}/versions/{vid}", "editor", s.handleDeleteVersion)
	u("POST /api/scenes/{id}/versions/{vid}/restore", "editor", s.handleRestoreVersion)

	u("GET /api/scenes/{id}/shares", "viewer", s.handleListShares)
	u("POST /api/scenes/{id}/shares", "editor", s.handleCreateShare)
	u("DELETE /api/scenes/{id}/shares/{sid}", "editor", s.handleDeleteShare)

	u("POST /api/ai/text-to-diagram", "editor", s.handleTextToDiagram)

	// The editing surface exists twice: for members by scene id, and for anyone
	// holding a share link by its token. Same handlers, different door.
	s.sceneRoutes(mux, "/api/scenes/{id}", s.memberAccess)
	s.sceneRoutes(mux, "/api/public/s/{token}", s.linkAccess)
	mux.HandleFunc("GET /api/public/s/{token}/info", s.handleShareInfo)
	mux.HandleFunc("POST /api/public/s/{token}/unlock", s.handleShareUnlock)

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not found")
	})
	mux.HandleFunc("/", s.handleStatic)

	return s.securityHeaders(s.sameOrigin(mux))
}

type access func(r *http.Request) (sceneID string, a actor, status int)

func (s *Server) sceneRoutes(mux *http.ServeMux, prefix string, acc access) {
	h := func(method, suffix string, needEdit bool, fn func(http.ResponseWriter, *http.Request, string, actor)) {
		mux.HandleFunc(method+" "+prefix+suffix, func(w http.ResponseWriter, r *http.Request) {
			sceneID, a, status := acc(r)
			if status != 0 {
				writeError(w, status, http.StatusText(status))
				return
			}
			if needEdit && !a.CanEdit {
				writeError(w, http.StatusForbidden, "read-only access")
				return
			}
			fn(w, r, sceneID, a)
		})
	}
	h("GET", "", false, s.handleGetScene)
	h("PUT", "/content", true, s.handlePutContent)
	h("GET", "/files/{fileId}", false, s.handleGetFile)
	h("PUT", "/files/{fileId}", true, s.handlePutFile)
	h("GET", "/comments", false, s.handleListComments)
	h("POST", "/comments", false, s.handleCreateComment)
	h("PATCH", "/comments/{cid}", false, s.handlePatchComment)
	h("DELETE", "/comments/{cid}", false, s.handleDeleteComment)
	h("GET", "/ws", false, s.handleWS)
}

// securityHeaders sets the headers that do not depend on the route. Share pages may
// be embedded elsewhere; the workspace may not be framed at all.
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		if !strings.HasPrefix(r.URL.Path, "/s/") && !strings.HasPrefix(r.URL.Path, "/assets/") {
			h.Set("Content-Security-Policy", "frame-ancestors 'self'")
		}
		next.ServeHTTP(w, r)
	})
}

// sameOrigin rejects state-changing requests that a browser sent from another site.
// The session cookie belongs to the proxy in front, so this is the app's own CSRF gate.
func (s *Server) sameOrigin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && r.Method != http.MethodOptions {
			if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
				writeError(w, http.StatusForbidden, "cross-site request")
				return
			}
			if origin := r.Header.Get("Origin"); origin != "" {
				if u, err := url.Parse(origin); err != nil || u.Host != r.Host {
					writeError(w, http.StatusForbidden, "cross-origin request")
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

// ---------------------------------------------------------------- helpers

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// fail maps a store error to a response and logs what the client must not see.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "body too large")
		} else {
			writeError(w, http.StatusBadRequest, "invalid JSON")
		}
		return false
	}
	return true
}

func cleanTitle(s, def string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200])
	}
	return s
}
