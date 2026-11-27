package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/renatobardi/oute-draw/internal/scene"
)

// autoVersionEvery is the minimum gap between two automatic history snapshots of
// one scene; autoVersionKeep caps how many automatic ones are kept.
const (
	autoVersionEvery = 10 * time.Minute
	autoVersionKeep  = 100
)

type SceneMeta struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	CollectionID *string    `json:"collectionId"`
	OwnerName    string     `json:"ownerName"`
	Version      int64      `json:"version"`
	HasThumbnail bool       `json:"hasThumbnail"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
	DeletedAt    *time.Time `json:"deletedAt"`
}

type Scene struct {
	SceneMeta
	Elements   []json.RawMessage `json:"elements"`
	AppState   json.RawMessage   `json:"appState"`
	SlideOrder json.RawMessage   `json:"slideOrder"`
	FileIDs    []string          `json:"fileIds"`
}

const sceneMetaCols = `s.id, s.title, s.collection_id, coalesce(nullif(u.name, ''), u.email, ''), s.version,
	s.thumbnail IS NOT NULL, s.created_at, s.updated_at, s.deleted_at`

func scanMeta(r pgx.Row, m *SceneMeta, extra ...any) error {
	dest := append([]any{&m.ID, &m.Title, &m.CollectionID, &m.OwnerName, &m.Version,
		&m.HasThumbnail, &m.CreatedAt, &m.UpdatedAt, &m.DeletedAt}, extra...)
	return r.Scan(dest...)
}

type SceneFilter struct {
	// Collection: "" = every collection, "none" = scenes outside any, else an id.
	Collection string
	Query      string
	Trash      bool
}

func (s *Store) ListScenes(ctx context.Context, f SceneFilter) ([]SceneMeta, error) {
	q := `SELECT ` + sceneMetaCols + ` FROM scenes s LEFT JOIN users u ON u.id = s.owner_id
		WHERE (s.deleted_at IS NOT NULL) = $1
		  AND ($2 = '' OR ($2 = 'none' AND s.collection_id IS NULL) OR s.collection_id::text = $2)
		  AND ($3 = '' OR s.title ILIKE '%' || $3 || '%' OR s.text_content ILIKE '%' || $3 || '%')
		ORDER BY s.updated_at DESC LIMIT 500`
	rows, err := s.pool.Query(ctx, q, f.Trash, f.Collection, likeEscape(f.Query))
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (SceneMeta, error) {
		var m SceneMeta
		return m, scanMeta(r, &m)
	})
}

func likeEscape(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		if r == '%' || r == '_' || r == '\\' {
			out = append(out, '\\')
		}
		out = append(out, r)
	}
	return string(out)
}

type NewScene struct {
	Title        string
	CollectionID *string
	OwnerID      string
	Elements     []json.RawMessage
	AppState     json.RawMessage
}

func (s *Store) CreateScene(ctx context.Context, n NewScene) (string, error) {
	id := NewID()
	if n.Elements == nil {
		n.Elements = []json.RawMessage{}
	}
	if len(n.AppState) == 0 {
		n.AppState = json.RawMessage(`{}`)
	}
	els, err := json.Marshal(n.Elements)
	if err != nil {
		return "", err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO scenes (id, title, collection_id, owner_id, elements, app_state, text_content)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		id, n.Title, n.CollectionID, n.OwnerID, els, n.AppState, scene.Text(n.Elements))
	return id, notFound(err)
}

func (s *Store) GetScene(ctx context.Context, id string) (Scene, error) {
	var sc Scene
	var els json.RawMessage
	err := scanMeta(s.pool.QueryRow(ctx, `SELECT `+sceneMetaCols+`, s.elements, s.app_state, s.slide_order
		FROM scenes s LEFT JOIN users u ON u.id = s.owner_id WHERE s.id = $1`, id),
		&sc.SceneMeta, &els, &sc.AppState, &sc.SlideOrder)
	if err != nil {
		return sc, notFound(err)
	}
	if err := json.Unmarshal(els, &sc.Elements); err != nil {
		return sc, err
	}
	rows, err := s.pool.Query(ctx, `SELECT file_id FROM scene_files WHERE scene_id = $1`, id)
	if err != nil {
		return sc, err
	}
	sc.FileIDs, err = pgx.CollectRows(rows, pgx.RowTo[string])
	if sc.FileIDs == nil {
		sc.FileIDs = []string{}
	}
	return sc, err
}

type SceneMetaPatch struct {
	Title *string
	// SetCollection distinguishes "leave as is" from "move out of any collection".
	SetCollection bool
	CollectionID  *string
	SlideOrder    json.RawMessage
}

func (s *Store) UpdateSceneMeta(ctx context.Context, id string, p SceneMetaPatch) error {
	tag, err := s.pool.Exec(ctx, `UPDATE scenes SET
			title = coalesce($2, title),
			collection_id = CASE WHEN $3 THEN $4::uuid ELSE collection_id END,
			slide_order = coalesce($5, slide_order),
			updated_at = CASE WHEN $2::text IS NOT NULL THEN now() ELSE updated_at END
		WHERE id = $1`, id, p.Title, p.SetCollection, p.CollectionID, nullJSON(p.SlideOrder))
	if err != nil {
		return notFound(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func nullJSON(j json.RawMessage) any {
	if len(j) == 0 {
		return nil
	}
	return []byte(j)
}

type ContentSave struct {
	Elements []json.RawMessage
	// AppState and Thumbnail are optional; nil keeps what is stored.
	AppState   json.RawMessage
	Thumbnail  []byte
	AuthorName string
}

type SaveResult struct {
	Version int64 `json:"version"`
	// Changed is false when every incoming element was already known or stale.
	Changed bool `json:"changed"`
}

// SaveContent merges the incoming elements into the stored scene under a row lock,
// so concurrent saves from several editors never overwrite each other.
func (s *Store) SaveContent(ctx context.Context, id string, c ContentSave) (SaveResult, error) {
	var res SaveResult
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var rawEls, appState json.RawMessage
	var deleted *time.Time
	err = tx.QueryRow(ctx, `SELECT elements, app_state, version, deleted_at FROM scenes WHERE id = $1 FOR UPDATE`, id).
		Scan(&rawEls, &appState, &res.Version, &deleted)
	if err != nil {
		return res, notFound(err)
	}
	if deleted != nil {
		return res, ErrNotFound
	}
	var stored []json.RawMessage
	if err := json.Unmarshal(rawEls, &stored); err != nil {
		return res, err
	}
	merged, changed := scene.Merge(stored, c.Elements)
	res.Changed = changed
	if changed {
		if err := autoSnapshot(ctx, tx, id, stored, rawEls, appState, c.AuthorName); err != nil {
			return res, err
		}
		res.Version++
	}
	mergedJSON, err := json.Marshal(merged)
	if err != nil {
		return res, err
	}
	_, err = tx.Exec(ctx, `UPDATE scenes SET elements = $2, text_content = $3, version = $4,
			app_state = coalesce($5, app_state), thumbnail = coalesce($6, thumbnail),
			updated_at = CASE WHEN $7 THEN now() ELSE updated_at END
		WHERE id = $1`, id, mergedJSON, scene.Text(merged), res.Version, nullJSON(c.AppState), c.Thumbnail, changed)
	if err != nil {
		return res, err
	}
	return res, tx.Commit(ctx)
}

// autoSnapshot records the pre-change state when the last automatic snapshot is old
// enough, so an editing session can always be undone as a whole.
func autoSnapshot(ctx context.Context, tx pgx.Tx, id string, stored []json.RawMessage, rawEls, appState json.RawMessage, author string) error {
	if scene.Live(stored) == 0 {
		return nil
	}
	var recent bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM scene_versions
		WHERE scene_id = $1 AND auto AND created_at > $2)`, id, time.Now().Add(-autoVersionEvery)).Scan(&recent); err != nil {
		return err
	}
	if recent {
		return nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO scene_versions (id, scene_id, auto, author_name, elements, app_state)
		VALUES ($1, $2, true, $3, $4, $5)`, NewID(), id, author, rawEls, appState); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM scene_versions WHERE scene_id = $1 AND auto AND id NOT IN (
		SELECT id FROM scene_versions WHERE scene_id = $1 AND auto ORDER BY created_at DESC LIMIT $2)`, id, autoVersionKeep)
	return err
}

func (s *Store) SetSceneDeleted(ctx context.Context, id string, deleted bool) error {
	tag, err := s.pool.Exec(ctx, `UPDATE scenes SET deleted_at = CASE WHEN $2 THEN now() END WHERE id = $1`, id, deleted)
	if err != nil {
		return notFound(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// PurgeScene deletes for good; only scenes already in the trash can be purged.
func (s *Store) PurgeScene(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM scenes WHERE id = $1 AND deleted_at IS NOT NULL`, id)
	if err != nil {
		return notFound(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DuplicateScene(ctx context.Context, id, title, ownerID string) (string, error) {
	newID := NewID()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	tag, err := tx.Exec(ctx, `INSERT INTO scenes (id, title, collection_id, owner_id, elements, app_state, slide_order, text_content, thumbnail)
		SELECT $2::uuid, $3::text, collection_id, $4::uuid, elements, app_state, slide_order, text_content, thumbnail FROM scenes WHERE id = $1`,
		id, newID, title, ownerID)
	if err != nil {
		return "", notFound(err)
	}
	if tag.RowsAffected() == 0 {
		return "", ErrNotFound
	}
	if _, err := tx.Exec(ctx, `INSERT INTO scene_files (scene_id, file_id, mime_type, data)
		SELECT $2::uuid, file_id, mime_type, data FROM scene_files WHERE scene_id = $1`, id, newID); err != nil {
		return "", err
	}
	return newID, tx.Commit(ctx)
}

func (s *Store) Thumbnail(ctx context.Context, id string) ([]byte, error) {
	var b []byte
	err := s.pool.QueryRow(ctx, `SELECT thumbnail FROM scenes WHERE id = $1`, id).Scan(&b)
	if err == nil && b == nil {
		return nil, ErrNotFound
	}
	return b, notFound(err)
}

// ---------------------------------------------------------------- files

func (s *Store) PutFile(ctx context.Context, sceneID, fileID, mime string, data []byte) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO scene_files (scene_id, file_id, mime_type, data) VALUES ($1, $2, $3, $4)
		ON CONFLICT (scene_id, file_id) DO NOTHING`, sceneID, fileID, mime, data)
	return notFound(err)
}

func (s *Store) GetFile(ctx context.Context, sceneID, fileID string) (mime string, data []byte, err error) {
	err = s.pool.QueryRow(ctx, `SELECT mime_type, data FROM scene_files WHERE scene_id = $1 AND file_id = $2`,
		sceneID, fileID).Scan(&mime, &data)
	return mime, data, notFound(err)
}

// ---------------------------------------------------------------- versions

type Version struct {
	ID         string    `json:"id"`
	Label      string    `json:"label"`
	Auto       bool      `json:"auto"`
	AuthorName string    `json:"authorName"`
	CreatedAt  time.Time `json:"createdAt"`
}

type VersionContent struct {
	Version
	Elements []json.RawMessage `json:"elements"`
	AppState json.RawMessage   `json:"appState"`
}

func (s *Store) ListVersions(ctx context.Context, sceneID string) ([]Version, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, label, auto, author_name, created_at FROM scene_versions
		WHERE scene_id = $1 ORDER BY created_at DESC`, sceneID)
	if err != nil {
		return nil, notFound(err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Version, error) {
		var v Version
		return v, r.Scan(&v.ID, &v.Label, &v.Auto, &v.AuthorName, &v.CreatedAt)
	})
}

// CreateVersion snapshots the scene as it is stored now, under a label.
func (s *Store) CreateVersion(ctx context.Context, sceneID, label, author string) (Version, error) {
	v := Version{ID: NewID(), Label: label, AuthorName: author}
	err := s.pool.QueryRow(ctx, `INSERT INTO scene_versions (id, scene_id, label, auto, author_name, elements, app_state)
		SELECT $2::uuid, id, $3::text, false, $4::text, elements, app_state FROM scenes WHERE id = $1 RETURNING created_at`,
		sceneID, v.ID, label, author).Scan(&v.CreatedAt)
	return v, notFound(err)
}

func (s *Store) GetVersion(ctx context.Context, sceneID, versionID string) (VersionContent, error) {
	var v VersionContent
	var els json.RawMessage
	err := s.pool.QueryRow(ctx, `SELECT id, label, auto, author_name, created_at, elements, app_state
		FROM scene_versions WHERE scene_id = $1 AND id = $2`, sceneID, versionID).
		Scan(&v.ID, &v.Label, &v.Auto, &v.AuthorName, &v.CreatedAt, &els, &v.AppState)
	if err != nil {
		return v, notFound(err)
	}
	return v, json.Unmarshal(els, &v.Elements)
}

func (s *Store) DeleteVersion(ctx context.Context, sceneID, versionID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM scene_versions WHERE scene_id = $1 AND id = $2`, sceneID, versionID)
	return notFound(err)
}

// RestoreVersion makes the scene converge on a snapshot. The state being replaced is
// itself snapshotted first, so a restore can be undone.
func (s *Store) RestoreVersion(ctx context.Context, sceneID, versionID, author string) (SaveResult, error) {
	var res SaveResult
	v, err := s.GetVersion(ctx, sceneID, versionID)
	if err != nil {
		return res, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return res, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	var rawEls, appState json.RawMessage
	if err := tx.QueryRow(ctx, `SELECT elements, app_state, version FROM scenes WHERE id = $1 FOR UPDATE`, sceneID).
		Scan(&rawEls, &appState, &res.Version); err != nil {
		return res, notFound(err)
	}
	var current []json.RawMessage
	if err := json.Unmarshal(rawEls, &current); err != nil {
		return res, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO scene_versions (id, scene_id, label, auto, author_name, elements, app_state)
		VALUES ($1, $2, 'Antes de restaurar', false, $3, $4, $5)`, NewID(), sceneID, author, rawEls, appState); err != nil {
		return res, err
	}
	restored, _ := scene.Merge(nil, scene.RestoreTo(current, v.Elements))
	restoredJSON, err := json.Marshal(restored)
	if err != nil {
		return res, err
	}
	res.Version++
	res.Changed = true
	if _, err := tx.Exec(ctx, `UPDATE scenes SET elements = $2, text_content = $3, version = $4, updated_at = now() WHERE id = $1`,
		sceneID, restoredJSON, scene.Text(restored), res.Version); err != nil {
		return res, err
	}
	return res, tx.Commit(ctx)
}

// ---------------------------------------------------------------- share links

type ShareLink struct {
	ID          string     `json:"id"`
	SceneID     string     `json:"sceneId"`
	Token       string     `json:"token"`
	Mode        string     `json:"mode"`
	HasPassword bool       `json:"hasPassword"`
	ExpiresAt   *time.Time `json:"expiresAt"`
	CreatedAt   time.Time  `json:"createdAt"`

	PasswordHash string `json:"-"`
}

func scanShare(r pgx.Row, l *ShareLink) error {
	err := r.Scan(&l.ID, &l.SceneID, &l.Token, &l.Mode, &l.PasswordHash, &l.ExpiresAt, &l.CreatedAt)
	l.HasPassword = l.PasswordHash != ""
	return err
}

const shareCols = `id, scene_id, token, mode, password_hash, expires_at, created_at`

func (s *Store) ListShares(ctx context.Context, sceneID string) ([]ShareLink, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+shareCols+` FROM share_links WHERE scene_id = $1 ORDER BY created_at`, sceneID)
	if err != nil {
		return nil, notFound(err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ShareLink, error) {
		var l ShareLink
		return l, scanShare(r, &l)
	})
}

func (s *Store) CreateShare(ctx context.Context, sceneID, mode, passwordHash string, expiresAt *time.Time, userID string) (ShareLink, error) {
	var l ShareLink
	err := scanShare(s.pool.QueryRow(ctx, `INSERT INTO share_links (id, scene_id, token, mode, password_hash, expires_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING `+shareCols,
		NewID(), sceneID, NewToken(), mode, passwordHash, expiresAt, userID), &l)
	return l, notFound(err)
}

func (s *Store) DeleteShare(ctx context.Context, sceneID, shareID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM share_links WHERE scene_id = $1 AND id = $2`, sceneID, shareID)
	return notFound(err)
}

// ShareByToken resolves a live link: unknown, expired, or pointing at a trashed
// scene are all the same miss.
func (s *Store) ShareByToken(ctx context.Context, token string) (ShareLink, error) {
	var l ShareLink
	err := scanShare(s.pool.QueryRow(ctx, `SELECT l.id, l.scene_id, l.token, l.mode, l.password_hash, l.expires_at, l.created_at
		FROM share_links l JOIN scenes s ON s.id = l.scene_id
		WHERE l.token = $1 AND s.deleted_at IS NULL AND (l.expires_at IS NULL OR l.expires_at > now())`, token), &l)
	return l, notFound(err)
}

// ---------------------------------------------------------------- comments

type Comment struct {
	ID         string     `json:"id"`
	ThreadID   *string    `json:"threadId"`
	AuthorID   *string    `json:"authorId"`
	AuthorName string     `json:"authorName"`
	X          float64    `json:"x"`
	Y          float64    `json:"y"`
	Body       string     `json:"body"`
	ResolvedAt *time.Time `json:"resolvedAt"`
	CreatedAt  time.Time  `json:"createdAt"`
}

const commentCols = `id, thread_id, author_id, author_name, x, y, body, resolved_at, created_at`

func scanComment(r pgx.Row, c *Comment) error {
	return r.Scan(&c.ID, &c.ThreadID, &c.AuthorID, &c.AuthorName, &c.X, &c.Y, &c.Body, &c.ResolvedAt, &c.CreatedAt)
}

func (s *Store) ListComments(ctx context.Context, sceneID string) ([]Comment, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+commentCols+` FROM comments WHERE scene_id = $1 ORDER BY created_at`, sceneID)
	if err != nil {
		return nil, notFound(err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Comment, error) {
		var c Comment
		return c, scanComment(r, &c)
	})
}

type NewComment struct {
	SceneID    string
	ThreadID   *string
	AuthorID   *string
	AuthorName string
	X, Y       float64
	Body       string
}

func (s *Store) CreateComment(ctx context.Context, n NewComment) (Comment, error) {
	var c Comment
	// A reply must point at a thread root of the same scene.
	err := scanComment(s.pool.QueryRow(ctx, `INSERT INTO comments (id, scene_id, thread_id, author_id, author_name, x, y, body)
		SELECT $1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::text, $6::float8, $7::float8, $8::text
		WHERE $3::uuid IS NULL OR EXISTS (SELECT 1 FROM comments WHERE id = $3::uuid AND scene_id = $2::uuid AND thread_id IS NULL)
		RETURNING `+commentCols,
		NewID(), n.SceneID, n.ThreadID, n.AuthorID, n.AuthorName, n.X, n.Y, n.Body), &c)
	return c, notFound(err)
}

func (s *Store) GetComment(ctx context.Context, sceneID, id string) (Comment, error) {
	var c Comment
	err := scanComment(s.pool.QueryRow(ctx, `SELECT `+commentCols+` FROM comments WHERE scene_id = $1 AND id = $2`, sceneID, id), &c)
	return c, notFound(err)
}

type CommentPatch struct {
	Body     *string
	Resolved *bool
	X, Y     *float64
}

func (s *Store) UpdateComment(ctx context.Context, sceneID, id string, p CommentPatch) error {
	tag, err := s.pool.Exec(ctx, `UPDATE comments SET
			body = coalesce($3, body), x = coalesce($5, x), y = coalesce($6, y),
			resolved_at = CASE WHEN $4::boolean IS NULL THEN resolved_at WHEN $4 THEN now() END
		WHERE scene_id = $1 AND id = $2`, sceneID, id, p.Body, p.Resolved, p.X, p.Y)
	if err != nil {
		return notFound(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) DeleteComment(ctx context.Context, sceneID, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM comments WHERE scene_id = $1 AND id = $2`, sceneID, id)
	return notFound(err)
}
