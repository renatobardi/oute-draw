package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/renatobardi/oute-draw/internal/store"
)

// actor is whoever is acting on one scene: a workspace member or a share-link guest.
type actor struct {
	UserID     *string
	Name       string
	CanEdit    bool
	CanComment bool
	// Admin may delete other people's comments.
	Admin bool
	Guest bool
}

var roleRank = map[string]int{"viewer": 1, "editor": 2, "admin": 3}

// currentUser resolves the signed-in user. In proxy mode the identity is the email
// header oauth2-proxy sets; the deployment must make sure nothing else can reach the
// app and that the edge strips that header from client requests.
func (s *Server) currentUser(r *http.Request) (store.User, bool) {
	email, name := s.cfg.DevEmail, ""
	if s.cfg.AuthMode == "proxy" {
		email = strings.ToLower(strings.TrimSpace(r.Header.Get(s.cfg.EmailHeader)))
		name = strings.TrimSpace(r.Header.Get(s.cfg.NameHeader))
	}
	if email == "" || !strings.Contains(email, "@") {
		return store.User{}, false
	}
	if name == "" {
		name = email[:strings.Index(email, "@")]
	}
	s.usersMu.Lock()
	c, ok := s.users[email]
	s.usersMu.Unlock()
	if ok && time.Now().Before(c.expires) {
		return c.user, true
	}
	role, force := s.cfg.DefaultRole, false
	if s.cfg.AdminEmails[email] || s.cfg.AuthMode == "dev" {
		role, force = "admin", true
	}
	u, err := s.store.EnsureUser(r.Context(), email, name, role, force)
	if err != nil {
		s.log.Error("ensure user", "err", err)
		return store.User{}, false
	}
	s.usersMu.Lock()
	s.users[email] = cachedUser{user: u, expires: time.Now().Add(30 * time.Second)}
	s.usersMu.Unlock()
	return u, true
}

func (s *Server) forgetUsers() {
	s.usersMu.Lock()
	clear(s.users)
	s.usersMu.Unlock()
}

func (s *Server) withUser(minRole string, h func(http.ResponseWriter, *http.Request, store.User)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, ok := s.currentUser(r)
		if !ok {
			writeError(w, http.StatusUnauthorized, "sign in required")
			return
		}
		if roleRank[u.Role] < roleRank[minRole] {
			writeError(w, http.StatusForbidden, "your role does not allow this")
			return
		}
		h(w, r, u)
	}
}

func (s *Server) memberAccess(r *http.Request) (string, actor, int) {
	u, ok := s.currentUser(r)
	if !ok {
		return "", actor{}, http.StatusUnauthorized
	}
	return r.PathValue("id"), actor{
		UserID: &u.ID, Name: u.Name,
		CanEdit: roleRank[u.Role] >= roleRank["editor"], CanComment: true,
		Admin: u.Role == "admin",
	}, 0
}

// linkAccess admits the holder of a live share token. A password-protected link
// also needs the proof handed out by handleShareUnlock.
func (s *Server) linkAccess(r *http.Request) (string, actor, int) {
	link, err := s.store.ShareByToken(r.Context(), r.PathValue("token"))
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Error("share lookup", "err", err)
			return "", actor{}, http.StatusInternalServerError
		}
		return "", actor{}, http.StatusNotFound
	}
	if link.HasPassword {
		proof := r.Header.Get("X-Share-Access")
		if proof == "" {
			proof = r.URL.Query().Get("access")
		}
		if !hmac.Equal([]byte(proof), []byte(s.shareProof(link))) {
			return "", actor{}, http.StatusUnauthorized
		}
	}
	name := r.Header.Get("X-Guest-Name")
	if name == "" {
		name = r.URL.Query().Get("name")
	}
	if dec, err := url.QueryUnescape(name); err == nil {
		name = dec
	}
	edit := link.Mode == "edit"
	return link.SceneID, actor{Name: cleanTitle(name, "Convidado"), CanEdit: edit, CanComment: edit, Guest: true}, 0
}

// shareProof is bound to the link and to its current password hash, so changing or
// removing the link invalidates every proof handed out before.
func (s *Server) shareProof(l store.ShareLink) string {
	m := hmac.New(sha256.New, s.cfg.Secret)
	m.Write([]byte(l.ID + "\x00" + l.PasswordHash))
	return hex.EncodeToString(m.Sum(nil))
}

func (s *Server) handleShareInfo(w http.ResponseWriter, r *http.Request) {
	link, err := s.store.ShareByToken(r.Context(), r.PathValue("token"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mode": link.Mode, "hasPassword": link.HasPassword})
}

func (s *Server) handleShareUnlock(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Password string `json:"password"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	link, err := s.store.ShareByToken(r.Context(), r.PathValue("token"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !link.HasPassword || bcrypt.CompareHashAndPassword([]byte(link.PasswordHash), []byte(body.Password)) != nil {
		// bcrypt already costs time; a fixed delay on top blunts guessing further.
		time.Sleep(400 * time.Millisecond)
		writeError(w, http.StatusUnauthorized, "wrong password")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"access": s.shareProof(link)})
}

// ---------------------------------------------------------------- members

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request, u store.User) {
	writeJSON(w, http.StatusOK, map[string]any{
		"user": u,
		"features": map[string]bool{
			"ai": s.cfg.AnthropicAPIKey != "",
		},
	})
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request, _ store.User) {
	users, err := s.store.ListUsers(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, users)
}

func (s *Server) handleSetUserRole(w http.ResponseWriter, r *http.Request, me store.User) {
	var body struct {
		Role string `json:"role"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if roleRank[body.Role] == 0 {
		writeError(w, http.StatusBadRequest, "unknown role")
		return
	}
	if r.PathValue("id") == me.ID {
		writeError(w, http.StatusBadRequest, "you cannot change your own role")
		return
	}
	if err := s.store.SetUserRole(r.Context(), r.PathValue("id"), body.Role); err != nil {
		s.fail(w, r, err)
		return
	}
	s.forgetUsers()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetLibrary(w http.ResponseWriter, r *http.Request, u store.User) {
	lib, err := s.store.Library(r.Context(), u.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, lib)
}

func (s *Server) handlePutLibrary(w http.ResponseWriter, r *http.Request, u store.User) {
	var items []any
	if !readJSON(w, r, &items) {
		return
	}
	raw, err := jsonMarshal(items)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid library")
		return
	}
	if err := s.store.SetLibrary(r.Context(), u.ID, raw); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
