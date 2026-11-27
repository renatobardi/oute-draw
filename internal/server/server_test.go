package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"

	"github.com/renatobardi/oute-draw/internal/config"
	"github.com/renatobardi/oute-draw/internal/store"
)

// The suite runs against a real Postgres, in an existing schema whose tables it drops:
//
//	DRAW_TEST_DATABASE_URL=postgres://… DRAW_TEST_DB_SCHEMA=draw_test go test ./...
type env struct {
	t   *testing.T
	url string
}

func setup(t *testing.T) *env {
	t.Helper()
	dbURL := os.Getenv("DRAW_TEST_DATABASE_URL")
	if dbURL == "" {
		t.Skip("DRAW_TEST_DATABASE_URL not set")
	}
	schema := os.Getenv("DRAW_TEST_DB_SCHEMA")
	if schema == "" {
		schema = "draw_test"
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	// Like the production tenant role, the test role owns objects in its schema but
	// cannot create schemas: start clean by dropping the tables, not the schema.
	rows, err := conn.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname = $1`, schema)
	if err != nil {
		t.Fatal(err)
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range tables {
		if _, err := conn.Exec(ctx, "DROP TABLE "+pgx.Identifier{schema, name}.Sanitize()+" CASCADE"); err != nil {
			t.Fatal(err)
		}
	}
	conn.Close(ctx)

	st, err := store.Open(ctx, dbURL, schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// Migrating twice must be a no-op.
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		AuthMode: "proxy", EmailHeader: "X-Forwarded-Email", NameHeader: "X-Forwarded-Preferred-Username",
		DefaultRole: "editor", AdminEmails: map[string]bool{"admin@oute.pro": true},
		Secret:     []byte("test-secret-test-secret-test-secret"),
		ICEServers: json.RawMessage(`[]`),
	}
	web := fstest.MapFS{
		"index.html":    {Data: []byte("<html>app</html>")},
		"assets/app.js": {Data: []byte("console.log(1)")},
	}
	srv := httptest.NewServer(New(cfg, st, web, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler())
	t.Cleanup(srv.Close)
	return &env{t: t, url: srv.URL}
}

type resp struct {
	status int
	body   []byte
}

func (r resp) json(t *testing.T, v any) {
	t.Helper()
	if err := json.Unmarshal(r.body, v); err != nil {
		t.Fatalf("decode %q: %v", r.body, err)
	}
}

// do sends a request as email ("" = anonymous) with optional extra headers.
func (e *env) do(method, path, email string, body any, headers ...string) resp {
	e.t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case []byte:
		rd = bytes.NewReader(b)
	default:
		j, _ := json.Marshal(b)
		rd = bytes.NewReader(j)
	}
	req, _ := http.NewRequest(method, e.url+path, rd)
	if email != "" {
		req.Header.Set("X-Forwarded-Email", email)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return resp{res.StatusCode, b}
}

func (e *env) want(r resp, status int) resp {
	e.t.Helper()
	if r.status != status {
		e.t.Fatalf("status = %d, want %d; body: %s", r.status, status, r.body)
	}
	return r
}

const admin, editor = "admin@oute.pro", "ed@oute.pro"

type sceneResp struct {
	Scene struct {
		ID       string            `json:"id"`
		Title    string            `json:"title"`
		Version  int64             `json:"version"`
		Elements []json.RawMessage `json:"elements"`
		FileIDs  []string          `json:"fileIds"`
	} `json:"scene"`
	Access struct {
		CanEdit    bool `json:"canEdit"`
		CanComment bool `json:"canComment"`
		Guest      bool `json:"guest"`
	} `json:"access"`
}

func raw(s ...string) []json.RawMessage {
	out := make([]json.RawMessage, len(s))
	for i := range s {
		out[i] = json.RawMessage(s[i])
	}
	return out
}

func (e *env) newScene(title string, elements ...string) string {
	e.t.Helper()
	var out struct{ ID string }
	e.want(e.do("POST", "/api/scenes", admin, map[string]any{"title": title, "elements": raw(elements...)}), 201).json(e.t, &out)
	return out.ID
}

func TestAuthAndRoles(t *testing.T) {
	e := setup(t)
	e.want(e.do("GET", "/api/me", "", nil), 401)
	e.want(e.do("GET", "/api/scenes", "", nil), 401)

	var me struct {
		User store.User `json:"user"`
	}
	e.want(e.do("GET", "/api/me", admin, nil), 200).json(t, &me)
	if me.User.Role != "admin" {
		t.Fatalf("configured admin got role %q", me.User.Role)
	}
	e.want(e.do("GET", "/api/me", editor, nil), 200).json(t, &me)
	if me.User.Role != "editor" {
		t.Fatalf("new user got role %q", me.User.Role)
	}
	edID := me.User.ID

	// Only an admin changes roles, and a viewer cannot write.
	e.want(e.do("PATCH", "/api/users/"+edID, editor, map[string]string{"role": "admin"}), 403)
	e.want(e.do("PATCH", "/api/users/"+edID, admin, map[string]string{"role": "viewer"}), 204)
	e.want(e.do("POST", "/api/scenes", editor, map[string]string{"title": "x"}), 403)
	id := e.newScene("Roles")
	e.want(e.do("PUT", "/api/scenes/"+id+"/content", editor, map[string]any{"elements": raw(`{"id":"a","version":1}`)}), 403)
	e.want(e.do("GET", "/api/scenes/"+id, editor, nil), 200)

	// A cross-site write is refused even with a valid identity.
	e.want(e.do("POST", "/api/scenes", admin, map[string]string{"title": "x"}, "Sec-Fetch-Site", "cross-site"), 403)
	e.want(e.do("POST", "/api/scenes", admin, map[string]string{"title": "x"}, "Origin", "https://evil.example"), 403)
}

func TestSceneLifecycleAndMerge(t *testing.T) {
	e := setup(t)
	var col store.Collection
	e.want(e.do("POST", "/api/collections", admin, map[string]string{"name": "Arquitetura"}), 201).json(t, &col)

	id := e.newScene("Diagrama", `{"id":"a","type":"text","text":"gateway","version":1,"versionNonce":5,"index":"a0"}`)
	e.want(e.do("PATCH", "/api/scenes/"+id, admin, map[string]any{"collectionId": col.ID, "title": "Diagrama v2"}), 204)

	// Two editors save concurrently-edited copies: both changes must survive, and a
	// stale copy of "a" must not undo the newer one.
	e.want(e.do("PUT", "/api/scenes/"+id+"/content", admin, map[string]any{"elements": raw(
		`{"id":"a","type":"text","text":"api gateway","version":2,"versionNonce":5,"index":"a0"}`)}), 200)
	var save store.SaveResult
	e.want(e.do("PUT", "/api/scenes/"+id+"/content", editor, map[string]any{"elements": raw(
		`{"id":"a","type":"text","text":"gateway","version":1,"versionNonce":5,"index":"a0"}`,
		`{"id":"b","type":"text","text":"database","version":1,"versionNonce":1,"index":"a1"}`)}), 200).json(t, &save)
	if !save.Changed || save.Version != 2 {
		t.Fatalf("save = %+v", save)
	}
	var sc sceneResp
	e.want(e.do("GET", "/api/scenes/"+id, admin, nil), 200).json(t, &sc)
	if len(sc.Scene.Elements) != 2 || !strings.Contains(string(sc.Scene.Elements[0]), "api gateway") {
		t.Fatalf("merge lost an edit: %s", sc.Scene.Elements)
	}
	if sc.Scene.Title != "Diagrama v2" || !sc.Access.CanEdit {
		t.Fatalf("scene = %+v", sc)
	}

	// Search reaches the text inside the drawing, per collection.
	var list []store.SceneMeta
	e.want(e.do("GET", "/api/scenes?q=DATABASE&collection="+col.ID, admin, nil), 200).json(t, &list)
	if len(list) != 1 {
		t.Fatalf("search found %d scenes", len(list))
	}
	e.want(e.do("GET", "/api/scenes?q=100%25", admin, nil), 200).json(t, &list)
	if len(list) != 0 {
		t.Fatal("LIKE wildcard was not escaped")
	}

	// Duplicate, trash, restore, purge.
	var dup struct{ ID string }
	e.want(e.do("POST", "/api/scenes/"+id+"/duplicate", admin, map[string]string{"title": "Cópia"}), 201).json(t, &dup)
	e.want(e.do("DELETE", "/api/scenes/"+dup.ID+"?permanent=1", admin, nil), 404) // not in the trash yet
	e.want(e.do("DELETE", "/api/scenes/"+dup.ID, admin, nil), 204)
	e.want(e.do("GET", "/api/scenes?trash=1", admin, nil), 200).json(t, &list)
	if len(list) != 1 || list[0].ID != dup.ID {
		t.Fatalf("trash = %+v", list)
	}
	e.want(e.do("PUT", "/api/scenes/"+dup.ID+"/content", admin, map[string]any{"elements": raw(`{"id":"z","version":1}`)}), 404)
	e.want(e.do("POST", "/api/scenes/"+dup.ID+"/restore", admin, nil), 204)
	e.want(e.do("DELETE", "/api/scenes/"+dup.ID, admin, nil), 204)
	e.want(e.do("DELETE", "/api/scenes/"+dup.ID+"?permanent=1", admin, nil), 204)
	e.want(e.do("GET", "/api/scenes/"+dup.ID, admin, nil), 404)
	e.want(e.do("GET", "/api/scenes/not-a-uuid", admin, nil), 404)

	// Deleting the collection keeps its scenes.
	e.want(e.do("DELETE", "/api/collections/"+col.ID, admin, nil), 204)
	e.want(e.do("GET", "/api/scenes?collection=none", admin, nil), 200).json(t, &list)
	if len(list) != 1 {
		t.Fatalf("scene did not survive its collection: %+v", list)
	}
}

func TestFilesAndThumbnail(t *testing.T) {
	e := setup(t)
	id := e.newScene("Imagens")
	png := []byte("\x89PNG\r\n\x1a\nfake")
	e.want(e.do("PUT", "/api/scenes/"+id+"/files/abc123", admin, png, "Content-Type", "image/png"), 204)
	e.want(e.do("PUT", "/api/scenes/"+id+"/files/evil", admin, []byte("<html>"), "Content-Type", "text/html"), 400)
	e.want(e.do("PUT", "/api/scenes/"+id+"/files/bad..id", admin, png, "Content-Type", "image/png"), 400)
	e.want(e.do("PUT", "/api/scenes/00000000-0000-4000-8000-000000000000/files/abc", admin, png, "Content-Type", "image/png"), 404)
	got := e.want(e.do("GET", "/api/scenes/"+id+"/files/abc123", admin, nil), 200)
	if !bytes.Equal(got.body, png) {
		t.Fatal("file did not round-trip")
	}
	var sc sceneResp
	e.want(e.do("GET", "/api/scenes/"+id, admin, nil), 200).json(t, &sc)
	if len(sc.Scene.FileIDs) != 1 || sc.Scene.FileIDs[0] != "abc123" {
		t.Fatalf("fileIds = %v", sc.Scene.FileIDs)
	}

	e.want(e.do("GET", "/api/scenes/"+id+"/thumbnail", admin, nil), 404)
	thumb := "data:image/png;base64,iVBORw0KGgo="
	e.want(e.do("PUT", "/api/scenes/"+id+"/content", admin, map[string]any{"elements": raw(), "thumbnail": thumb}), 200)
	e.want(e.do("GET", "/api/scenes/"+id+"/thumbnail", admin, nil), 200)
	e.want(e.do("PUT", "/api/scenes/"+id+"/content", admin, map[string]any{"elements": raw(), "thumbnail": "aGVsbG8="}), 400)
}

func TestVersionHistoryRestore(t *testing.T) {
	e := setup(t)
	id := e.newScene("Histórico",
		`{"id":"a","type":"text","text":"v1","version":1,"versionNonce":1,"index":"a0"}`)
	var v store.Version
	e.want(e.do("POST", "/api/scenes/"+id+"/versions", admin, map[string]string{"label": "Marco 1"}), 201).json(t, &v)

	// The save also leaves an automatic snapshot of what was there before it.
	e.want(e.do("PUT", "/api/scenes/"+id+"/content", admin, map[string]any{"elements": raw(
		`{"id":"a","type":"text","text":"v2","version":7,"versionNonce":1,"index":"a0"}`,
		`{"id":"b","type":"text","text":"new","version":3,"versionNonce":1,"index":"a1"}`)}), 200)
	var versions []store.Version
	e.want(e.do("GET", "/api/scenes/"+id+"/versions", admin, nil), 200).json(t, &versions)
	if len(versions) != 2 {
		t.Fatalf("versions = %+v", versions)
	}

	e.want(e.do("POST", "/api/scenes/"+id+"/versions/"+v.ID+"/restore", admin, nil), 200)
	var sc sceneResp
	e.want(e.do("GET", "/api/scenes/"+id, admin, nil), 200).json(t, &sc)
	type el struct {
		ID        string `json:"id"`
		Text      string `json:"text"`
		Version   int64  `json:"version"`
		IsDeleted bool   `json:"isDeleted"`
	}
	byID := map[string]el{}
	for _, r := range sc.Scene.Elements {
		var x el
		_ = json.Unmarshal(r, &x)
		byID[x.ID] = x
	}
	// Restored content must outrank what every open client still holds.
	if a := byID["a"]; a.Text != "v1" || a.Version != 8 {
		t.Fatalf("a = %+v", a)
	}
	if b := byID["b"]; !b.IsDeleted || b.Version != 4 {
		t.Fatalf("b = %+v", b)
	}
	// A client that missed the restore saves its stale copy: the restore holds.
	e.want(e.do("PUT", "/api/scenes/"+id+"/content", admin, map[string]any{"elements": raw(
		`{"id":"a","type":"text","text":"v2","version":7,"versionNonce":1,"index":"a0"}`)}), 200)
	e.want(e.do("GET", "/api/scenes/"+id, admin, nil), 200).json(t, &sc)
	if !strings.Contains(string(sc.Scene.Elements[0]), `"v1"`) {
		t.Fatalf("stale save undid the restore: %s", sc.Scene.Elements[0])
	}
	e.want(e.do("GET", "/api/scenes/"+id+"/versions", admin, nil), 200).json(t, &versions)
	if len(versions) != 3 {
		t.Fatalf("restore did not snapshot the replaced state: %d versions", len(versions))
	}
}

func TestShareLinks(t *testing.T) {
	e := setup(t)
	id := e.newScene("Público", `{"id":"a","version":1,"versionNonce":1}`)
	other := e.newScene("Privado")

	var view, edit, locked store.ShareLink
	e.want(e.do("POST", "/api/scenes/"+id+"/shares", admin, map[string]string{"mode": "view"}), 201).json(t, &view)
	e.want(e.do("POST", "/api/scenes/"+id+"/shares", admin, map[string]string{"mode": "edit"}), 201).json(t, &edit)
	e.want(e.do("POST", "/api/scenes/"+id+"/shares", admin, map[string]string{"mode": "view", "password": "s3nha"}), 201).json(t, &locked)
	e.want(e.do("POST", "/api/scenes/"+id+"/shares", admin, map[string]string{"mode": "admin"}), 400)
	if bytes.Contains(e.do("GET", "/api/scenes/"+id+"/shares", admin, nil).body, []byte("$2a$")) {
		t.Fatal("password hash leaked in the share list")
	}

	pub := func(token string) string { return "/api/public/s/" + token }

	// View link: anonymous read, no write, no comments.
	var sc sceneResp
	e.want(e.do("GET", pub(view.Token), "", nil), 200).json(t, &sc)
	if sc.Access.CanEdit || !sc.Access.Guest || sc.Scene.ID != id {
		t.Fatalf("view access = %+v", sc.Access)
	}
	change := map[string]any{"elements": raw(`{"id":"g","version":1,"versionNonce":1}`)}
	e.want(e.do("PUT", pub(view.Token)+"/content", "", change), 403)
	e.want(e.do("POST", pub(view.Token)+"/comments", "", map[string]any{"body": "oi"}), 403)

	// Edit link: anonymous write lands in the scene.
	e.want(e.do("PUT", pub(edit.Token)+"/content", "", change, "X-Guest-Name", "Ana"), 200)
	e.want(e.do("GET", "/api/scenes/"+id, admin, nil), 200).json(t, &sc)
	if len(sc.Scene.Elements) != 2 {
		t.Fatalf("guest edit missing: %s", sc.Scene.Elements)
	}

	// A token only opens its own scene, and nothing of the workspace API.
	e.want(e.do("GET", "/api/scenes/"+other, "", nil), 401)
	e.want(e.do("GET", "/api/public/s/nope", "", nil), 404)

	// Password link: locked until the proof is presented; wrong password gets none.
	e.want(e.do("GET", pub(locked.Token), "", nil), 401)
	e.want(e.do("POST", pub(locked.Token)+"/unlock", "", map[string]string{"password": "errada"}), 401)
	var proof struct{ Access string }
	e.want(e.do("POST", pub(locked.Token)+"/unlock", "", map[string]string{"password": "s3nha"}), 200).json(t, &proof)
	e.want(e.do("GET", pub(locked.Token), "", nil, "X-Share-Access", proof.Access), 200)
	e.want(e.do("GET", pub(view.Token), "", nil, "X-Share-Access", "bogus"), 200)

	// Revoked, expired and trashed all close the door.
	e.want(e.do("DELETE", "/api/scenes/"+id+"/shares/"+view.ID, admin, nil), 204)
	e.want(e.do("GET", pub(view.Token), "", nil), 404)
	var soon store.ShareLink
	e.want(e.do("POST", "/api/scenes/"+id+"/shares", admin, map[string]any{"mode": "view", "expiresAt": time.Now().Add(1500 * time.Millisecond)}), 201).json(t, &soon)
	e.want(e.do("GET", pub(soon.Token), "", nil), 200)
	time.Sleep(1600 * time.Millisecond)
	e.want(e.do("GET", pub(soon.Token), "", nil), 404)
	e.want(e.do("DELETE", "/api/scenes/"+id, admin, nil), 204)
	e.want(e.do("GET", pub(edit.Token), "", nil), 404)
}

func TestComments(t *testing.T) {
	e := setup(t)
	id := e.newScene("Revisão")
	base := "/api/scenes/" + id + "/comments"
	var root, reply store.Comment
	e.want(e.do("POST", base, admin, map[string]any{"body": "Ajustar aqui", "x": 10.5, "y": -3}), 201).json(t, &root)
	e.want(e.do("POST", base, editor, map[string]any{"body": "Feito", "threadId": root.ID}), 201).json(t, &reply)
	e.want(e.do("POST", base, editor, map[string]any{"body": "x", "threadId": reply.ID}), 404) // replies do not nest
	e.want(e.do("POST", base, editor, map[string]any{"body": "  "}), 400)

	// Anyone resolves; only the author (or an admin) edits and deletes.
	e.want(e.do("PATCH", base+"/"+root.ID, editor, map[string]any{"resolved": true}), 204)
	e.want(e.do("PATCH", base+"/"+root.ID, editor, map[string]any{"body": "hack"}), 403)
	e.want(e.do("DELETE", base+"/"+root.ID, editor, nil), 403)
	var list []store.Comment
	e.want(e.do("GET", base, admin, nil), 200).json(t, &list)
	if len(list) != 2 || list[0].ResolvedAt == nil || list[0].Body != "Ajustar aqui" {
		t.Fatalf("comments = %+v", list)
	}
	e.want(e.do("DELETE", base+"/"+root.ID, admin, nil), 204)
	e.want(e.do("GET", base, admin, nil), 200).json(t, &list)
	if len(list) != 0 {
		t.Fatal("deleting a thread must delete its replies")
	}
}

func TestLibrary(t *testing.T) {
	e := setup(t)
	e.want(e.do("PUT", "/api/library", admin, []map[string]any{{"id": "lib1", "status": "unpublished", "elements": []any{}}}), 204)
	got := e.want(e.do("GET", "/api/library", admin, nil), 200)
	if !bytes.Contains(got.body, []byte("lib1")) {
		t.Fatalf("library = %s", got.body)
	}
	if got := e.want(e.do("GET", "/api/library", editor, nil), 200); strings.TrimSpace(string(got.body)) != "[]" {
		t.Fatalf("libraries are per user, got %s", got.body)
	}
}

func TestCollaborationSocket(t *testing.T) {
	e := setup(t)
	id := e.newScene("Ao vivo")
	var view store.ShareLink
	e.want(e.do("POST", "/api/scenes/"+id+"/shares", admin, map[string]string{"mode": "view"}), 201).json(t, &view)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(e.url, "http")
	dial := func(path, email string) *websocket.Conn {
		h := http.Header{}
		if email != "" {
			h.Set("X-Forwarded-Email", email)
		}
		c, _, err := websocket.Dial(ctx, wsURL+path, &websocket.DialOptions{HTTPHeader: h})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { c.CloseNow() })
		return c
	}
	next := func(c *websocket.Conn) map[string]any {
		_, data, err := c.Read(ctx)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		_ = json.Unmarshal(data, &m)
		return m
	}

	a := dial("/api/scenes/"+id+"/ws", admin)
	if m := next(a); m["t"] != "hello" {
		t.Fatalf("first message = %v", m)
	}
	guest := dial("/api/public/s/"+view.Token+"/ws?name=Ana%20Lu", "")
	if m := next(guest); m["t"] != "hello" || len(m["peers"].([]any)) != 1 {
		t.Fatalf("guest hello = %v", m)
	}
	if m := next(a); m["t"] != "join" || m["peer"].(map[string]any)["name"] != "Ana Lu" {
		t.Fatalf("join = %v", m)
	}

	// An editor's update reaches the guest; a view-only guest cannot push one, but
	// its cursor still travels.
	_ = a.Write(ctx, websocket.MessageText, []byte(`{"t":"update","elements":[{"id":"a"}]}`))
	if m := next(guest); m["t"] != "relay" || m["m"].(map[string]any)["t"] != "update" {
		t.Fatalf("relay = %v", m)
	}
	_ = guest.Write(ctx, websocket.MessageText, []byte(`{"t":"update","elements":[{"id":"evil"}]}`))
	_ = guest.Write(ctx, websocket.MessageText, []byte(`{"t":"pointer","x":1,"y":2}`))
	if m := next(a); m["m"].(map[string]any)["t"] != "pointer" {
		t.Fatalf("a read-only guest pushed an update: %v", m)
	}

	// Comment changes are announced to the room.
	e.want(e.do("POST", "/api/scenes/"+id+"/comments", admin, map[string]any{"body": "olha"}), 201)
	if m := next(guest); m["t"] != "comments" {
		t.Fatalf("notify = %v", m)
	}
	_ = next(a)
	guest.Close(websocket.StatusNormalClosure, "")
	if m := next(a); m["t"] != "leave" {
		t.Fatalf("leave = %v", m)
	}

	// No identity, no socket.
	if _, _, err := websocket.Dial(ctx, wsURL+"/api/scenes/"+id+"/ws", nil); err == nil {
		t.Fatal("anonymous socket was accepted")
	}
}

func TestStaticAndHeaders(t *testing.T) {
	e := setup(t)
	for _, p := range []string{"/", "/scene/abc", "/s/token"} {
		if r := e.want(e.do("GET", p, "", nil), 200); !bytes.Contains(r.body, []byte("app")) {
			t.Fatalf("%s did not serve the app shell", p)
		}
	}
	e.want(e.do("GET", "/assets/app.js", "", nil), 200)
	e.want(e.do("GET", "/assets/missing.js", "", nil), 404)
	e.want(e.do("GET", "/api/nope", admin, nil), 404)
	e.want(e.do("GET", "/healthz", "", nil), 200)

	res, err := http.Get(e.url + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.Header.Get("Content-Security-Policy") != "frame-ancestors 'self'" {
		t.Fatal("workspace pages must not be frameable by other sites")
	}
	res, _ = http.Get(e.url + "/s/token")
	res.Body.Close()
	if res.Header.Get("Content-Security-Policy") != "" {
		t.Fatal("share pages must stay embeddable")
	}
}
