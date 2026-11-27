package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/renatobardi/oute-draw/internal/store"
)

func jsonMarshal(v any) (json.RawMessage, error) { return json.Marshal(v) }

// ---------------------------------------------------------------- collections

func (s *Server) handleListCollections(w http.ResponseWriter, r *http.Request, _ store.User) {
	cols, err := s.store.ListCollections(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, cols)
}

func (s *Server) handleCreateCollection(w http.ResponseWriter, r *http.Request, u store.User) {
	var body struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	c, err := s.store.CreateCollection(r.Context(), cleanTitle(body.Name, "Nova coleção"), u.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (s *Server) handleRenameCollection(w http.ResponseWriter, r *http.Request, _ store.User) {
	var body struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if err := s.store.RenameCollection(r.Context(), r.PathValue("id"), cleanTitle(body.Name, "Sem nome")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteCollection(w http.ResponseWriter, r *http.Request, _ store.User) {
	if err := s.store.DeleteCollection(r.Context(), r.PathValue("id")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- scenes (workspace)

func (s *Server) handleListScenes(w http.ResponseWriter, r *http.Request, _ store.User) {
	q := r.URL.Query()
	list, err := s.store.ListScenes(r.Context(), store.SceneFilter{
		Collection: q.Get("collection"),
		Query:      strings.TrimSpace(q.Get("q")),
		Trash:      q.Get("trash") == "1",
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateScene(w http.ResponseWriter, r *http.Request, u store.User) {
	var body struct {
		Title        string            `json:"title"`
		CollectionID *string           `json:"collectionId"`
		Elements     []json.RawMessage `json:"elements"`
		AppState     json.RawMessage   `json:"appState"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	id, err := s.store.CreateScene(r.Context(), store.NewScene{
		Title: cleanTitle(body.Title, "Sem título"), CollectionID: body.CollectionID, OwnerID: u.ID,
		Elements: body.Elements, AppState: body.AppState,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (s *Server) handlePatchScene(w http.ResponseWriter, r *http.Request, _ store.User) {
	var raw map[string]json.RawMessage
	if !readJSON(w, r, &raw) {
		return
	}
	var p store.SceneMetaPatch
	if v, ok := raw["title"]; ok {
		var t string
		if json.Unmarshal(v, &t) != nil {
			writeError(w, http.StatusBadRequest, "invalid title")
			return
		}
		t = cleanTitle(t, "Sem título")
		p.Title = &t
	}
	if v, ok := raw["collectionId"]; ok {
		p.SetCollection = true
		if json.Unmarshal(v, &p.CollectionID) != nil {
			writeError(w, http.StatusBadRequest, "invalid collectionId")
			return
		}
	}
	if v, ok := raw["slideOrder"]; ok {
		var order []string
		if json.Unmarshal(v, &order) != nil {
			writeError(w, http.StatusBadRequest, "invalid slideOrder")
			return
		}
		p.SlideOrder = v
	}
	id := r.PathValue("id")
	if err := s.store.UpdateSceneMeta(r.Context(), id, p); err != nil {
		s.fail(w, r, err)
		return
	}
	s.hub.Notify(id, "meta", nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteScene(w http.ResponseWriter, r *http.Request, _ store.User) {
	id := r.PathValue("id")
	var err error
	if r.URL.Query().Get("permanent") == "1" {
		err = s.store.PurgeScene(r.Context(), id)
	} else {
		err = s.store.SetSceneDeleted(r.Context(), id, true)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRestoreScene(w http.ResponseWriter, r *http.Request, _ store.User) {
	if err := s.store.SetSceneDeleted(r.Context(), r.PathValue("id"), false); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDuplicateScene(w http.ResponseWriter, r *http.Request, u store.User) {
	var body struct {
		Title string `json:"title"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	id, err := s.store.DuplicateScene(r.Context(), r.PathValue("id"), cleanTitle(body.Title, "Cópia"), u.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (s *Server) handleThumbnail(w http.ResponseWriter, r *http.Request, _ store.User) {
	png, err := s.store.Thumbnail(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	_, _ = w.Write(png)
}

// ---------------------------------------------------------------- scene content (members and links)

func (s *Server) handleGetScene(w http.ResponseWriter, r *http.Request, sceneID string, a actor) {
	sc, err := s.store.GetScene(r.Context(), sceneID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// A link never opens a trashed scene; a member may look at it from the trash.
	if sc.DeletedAt != nil && a.Guest {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"scene": sc,
		"access": map[string]any{
			"canEdit": a.CanEdit && sc.DeletedAt == nil, "canComment": a.CanComment,
			"guest": a.Guest, "name": a.Name,
		},
		"iceServers": s.cfg.ICEServers,
	})
}

func (s *Server) handlePutContent(w http.ResponseWriter, r *http.Request, sceneID string, a actor) {
	var body struct {
		Elements  []json.RawMessage `json:"elements"`
		AppState  json.RawMessage   `json:"appState"`
		Thumbnail string            `json:"thumbnail"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	save := store.ContentSave{Elements: body.Elements, AppState: body.AppState, AuthorName: a.Name}
	if body.Thumbnail != "" {
		png, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(body.Thumbnail, "data:image/png;base64,"))
		if err != nil || len(png) > 2<<20 || !strings.HasPrefix(string(png), "\x89PNG") {
			writeError(w, http.StatusBadRequest, "invalid thumbnail")
			return
		}
		save.Thumbnail = png
	}
	res, err := s.store.SaveContent(r.Context(), sceneID, save)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

var fileIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// Only images are stored, and they are served back with the type they came with:
// SVG is the one that could carry script, so it never renders as a document.
var allowedMime = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
	"image/svg+xml": true, "image/bmp": true, "image/avif": true, "image/x-icon": true, "image/jfif": true,
}

func (s *Server) handleGetFile(w http.ResponseWriter, r *http.Request, sceneID string, _ actor) {
	mime, data, err := s.store.GetFile(r.Context(), sceneID, r.PathValue("fileId"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", mime)
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
	h.Set("Content-Disposition", "attachment")
	h.Set("Cache-Control", "private, max-age=31536000, immutable")
	_, _ = w.Write(data)
}

func (s *Server) handlePutFile(w http.ResponseWriter, r *http.Request, sceneID string, _ actor) {
	fileID := r.PathValue("fileId")
	mime, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
	mime = strings.TrimSpace(mime)
	if !fileIDRe.MatchString(fileID) || !allowedMime[mime] {
		writeError(w, http.StatusBadRequest, "unsupported file")
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxFileBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			writeError(w, http.StatusRequestEntityTooLarge, "file too large")
		} else {
			writeError(w, http.StatusBadRequest, "upload interrupted")
		}
		return
	}
	if len(data) == 0 {
		writeError(w, http.StatusBadRequest, "empty file")
		return
	}
	if err := s.store.PutFile(r.Context(), sceneID, fileID, mime, data); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- versions

func (s *Server) handleListVersions(w http.ResponseWriter, r *http.Request, _ store.User) {
	list, err := s.store.ListVersions(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateVersion(w http.ResponseWriter, r *http.Request, u store.User) {
	var body struct {
		Label string `json:"label"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	v, err := s.store.CreateVersion(r.Context(), r.PathValue("id"), cleanTitle(body.Label, "Versão salva"), u.Name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) handleGetVersion(w http.ResponseWriter, r *http.Request, _ store.User) {
	v, err := s.store.GetVersion(r.Context(), r.PathValue("id"), r.PathValue("vid"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleDeleteVersion(w http.ResponseWriter, r *http.Request, _ store.User) {
	if err := s.store.DeleteVersion(r.Context(), r.PathValue("id"), r.PathValue("vid")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRestoreVersion(w http.ResponseWriter, r *http.Request, u store.User) {
	id := r.PathValue("id")
	res, err := s.store.RestoreVersion(r.Context(), id, r.PathValue("vid"), u.Name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Everyone with the scene open pulls the restored state and converges on it.
	s.hub.Notify(id, "reload", nil)
	writeJSON(w, http.StatusOK, res)
}

// ---------------------------------------------------------------- share links

func (s *Server) handleListShares(w http.ResponseWriter, r *http.Request, _ store.User) {
	list, err := s.store.ListShares(r.Context(), r.PathValue("id"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateShare(w http.ResponseWriter, r *http.Request, u store.User) {
	var body struct {
		Mode      string     `json:"mode"`
		Password  string     `json:"password"`
		ExpiresAt *time.Time `json:"expiresAt"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	if body.Mode != "view" && body.Mode != "edit" {
		writeError(w, http.StatusBadRequest, "mode must be view or edit")
		return
	}
	if body.ExpiresAt != nil && body.ExpiresAt.Before(time.Now()) {
		writeError(w, http.StatusBadRequest, "expiry is in the past")
		return
	}
	hash := ""
	if body.Password != "" {
		if len(body.Password) > 72 {
			writeError(w, http.StatusBadRequest, "password too long")
			return
		}
		b, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		hash = string(b)
	}
	link, err := s.store.CreateShare(r.Context(), r.PathValue("id"), body.Mode, hash, body.ExpiresAt, u.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, link)
}

func (s *Server) handleDeleteShare(w http.ResponseWriter, r *http.Request, _ store.User) {
	if err := s.store.DeleteShare(r.Context(), r.PathValue("id"), r.PathValue("sid")); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- comments

func (s *Server) handleListComments(w http.ResponseWriter, r *http.Request, sceneID string, a actor) {
	if !a.CanComment {
		writeJSON(w, http.StatusOK, []store.Comment{})
		return
	}
	list, err := s.store.ListComments(r.Context(), sceneID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateComment(w http.ResponseWriter, r *http.Request, sceneID string, a actor) {
	if !a.CanComment {
		writeError(w, http.StatusForbidden, "comments are not available on this link")
		return
	}
	var body struct {
		ThreadID *string `json:"threadId"`
		X        float64 `json:"x"`
		Y        float64 `json:"y"`
		Body     string  `json:"body"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	text := strings.TrimSpace(body.Body)
	if text == "" || len(text) > 10000 {
		writeError(w, http.StatusBadRequest, "comment must have between 1 and 10000 characters")
		return
	}
	c, err := s.store.CreateComment(r.Context(), store.NewComment{
		SceneID: sceneID, ThreadID: body.ThreadID, AuthorID: a.UserID, AuthorName: a.Name,
		X: body.X, Y: body.Y, Body: text,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.hub.Notify(sceneID, "comments", nil)
	writeJSON(w, http.StatusCreated, c)
}

// owns reports whether the actor may edit or delete the comment itself. Guests are
// anonymous, so nothing proves a guest wrote a given comment: only members own.
func owns(a actor, c store.Comment) bool {
	if a.Admin {
		return true
	}
	return a.UserID != nil && c.AuthorID != nil && *a.UserID == *c.AuthorID
}

func (s *Server) handlePatchComment(w http.ResponseWriter, r *http.Request, sceneID string, a actor) {
	if !a.CanComment {
		writeError(w, http.StatusForbidden, "comments are not available on this link")
		return
	}
	var p struct {
		Body     *string  `json:"body"`
		Resolved *bool    `json:"resolved"`
		X        *float64 `json:"x"`
		Y        *float64 `json:"y"`
	}
	if !readJSON(w, r, &p) {
		return
	}
	cid := r.PathValue("cid")
	if p.Body != nil {
		c, err := s.store.GetComment(r.Context(), sceneID, cid)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if !owns(a, c) {
			writeError(w, http.StatusForbidden, "only the author can edit a comment")
			return
		}
		t := strings.TrimSpace(*p.Body)
		if t == "" || len(t) > 10000 {
			writeError(w, http.StatusBadRequest, "comment must have between 1 and 10000 characters")
			return
		}
		p.Body = &t
	}
	if err := s.store.UpdateComment(r.Context(), sceneID, cid, store.CommentPatch{Body: p.Body, Resolved: p.Resolved, X: p.X, Y: p.Y}); err != nil {
		s.fail(w, r, err)
		return
	}
	s.hub.Notify(sceneID, "comments", nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeleteComment(w http.ResponseWriter, r *http.Request, sceneID string, a actor) {
	cid := r.PathValue("cid")
	c, err := s.store.GetComment(r.Context(), sceneID, cid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !owns(a, c) {
		writeError(w, http.StatusForbidden, "only the author can delete a comment")
		return
	}
	if err := s.store.DeleteComment(r.Context(), sceneID, cid); err != nil {
		s.fail(w, r, err)
		return
	}
	s.hub.Notify(sceneID, "comments", nil)
	w.WriteHeader(http.StatusNoContent)
}
