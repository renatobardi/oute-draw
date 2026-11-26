// Package store is the only code that talks to Postgres.
package store

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

// ErrNotFound is returned when the row asked for does not exist.
var ErrNotFound = errors.New("not found")

type Store struct {
	pool *pgxpool.Pool
}

// Open connects to Postgres. When schema is set it becomes the search_path of every
// connection, so the app works whether or not the role already defaults to its schema.
func Open(ctx context.Context, url, schema string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	if schema != "" {
		cfg.ConnConfig.RuntimeParams["search_path"] = pgx.Identifier{schema}.Sanitize()
	}
	cfg.MaxConns = 10
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Migrate applies the embedded migrations that have not run yet, each in its own
// transaction, under an advisory lock so two replicas cannot race.
func (s *Store) Migrate(ctx context.Context) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock(hashtext('oute-draw-migrate'))`); err != nil {
		return err
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtext('oute-draw-migrate'))`) //nolint:errcheck
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		return err
	}
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)
	for _, name := range names {
		var done bool
		if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE name = $1)`, name).Scan(&done); err != nil {
			return err
		}
		if done {
			continue
		}
		sql, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (name) VALUES ($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

// NewID returns a random UUIDv4.
func NewID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// NewToken returns an unguessable URL-safe token.
func NewToken() string {
	var b [24]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	// A malformed uuid in a URL, or a reference to a row that is not there (a file
	// for an unknown scene, a scene in an unknown collection), is a miss, not a
	// server error.
	var pg *pgconn.PgError
	if errors.As(err, &pg) && (pg.Code == "22P02" || pg.Code == "23503") {
		return ErrNotFound
	}
	return err
}

// ---------------------------------------------------------------- users

type User struct {
	ID         string    `json:"id"`
	Email      string    `json:"email"`
	Name       string    `json:"name"`
	Role       string    `json:"role"`
	CreatedAt  time.Time `json:"createdAt"`
	LastSeenAt time.Time `json:"lastSeenAt"`
}

// EnsureUser returns the user for email, creating it with role on first sight.
// forceRole, when set, is applied on every call (configured admins).
func (s *Store) EnsureUser(ctx context.Context, email, name, role string, forceRole bool) (User, error) {
	var u User
	q := `INSERT INTO users (id, email, name, role) VALUES ($1, $2, $3, $4)
		ON CONFLICT (email) DO UPDATE SET last_seen_at = now(),
			name = CASE WHEN EXCLUDED.name <> '' THEN EXCLUDED.name ELSE users.name END,
			role = CASE WHEN $5 THEN EXCLUDED.role ELSE users.role END
		RETURNING id, email, name, role, created_at, last_seen_at`
	err := s.pool.QueryRow(ctx, q, NewID(), email, name, role, forceRole).
		Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.CreatedAt, &u.LastSeenAt)
	return u, err
}

func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, email, name, role, created_at, last_seen_at FROM users ORDER BY email`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (User, error) {
		var u User
		return u, r.Scan(&u.ID, &u.Email, &u.Name, &u.Role, &u.CreatedAt, &u.LastSeenAt)
	})
}

func (s *Store) SetUserRole(ctx context.Context, id, role string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE users SET role = $2 WHERE id = $1`, id, role)
	if err != nil {
		return notFound(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) Library(ctx context.Context, userID string) (json.RawMessage, error) {
	var lib json.RawMessage
	err := s.pool.QueryRow(ctx, `SELECT library FROM users WHERE id = $1`, userID).Scan(&lib)
	return lib, notFound(err)
}

func (s *Store) SetLibrary(ctx context.Context, userID string, lib json.RawMessage) error {
	_, err := s.pool.Exec(ctx, `UPDATE users SET library = $2 WHERE id = $1`, userID, lib)
	return err
}

// ---------------------------------------------------------------- collections

type Collection struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	SceneCount int       `json:"sceneCount"`
	CreatedAt  time.Time `json:"createdAt"`
}

func (s *Store) ListCollections(ctx context.Context) ([]Collection, error) {
	rows, err := s.pool.Query(ctx, `SELECT c.id, c.name, c.created_at,
			(SELECT count(*) FROM scenes s WHERE s.collection_id = c.id AND s.deleted_at IS NULL)
		FROM collections c ORDER BY lower(c.name)`)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Collection, error) {
		var c Collection
		return c, r.Scan(&c.ID, &c.Name, &c.CreatedAt, &c.SceneCount)
	})
}

func (s *Store) CreateCollection(ctx context.Context, name, ownerID string) (Collection, error) {
	c := Collection{ID: NewID(), Name: name}
	err := s.pool.QueryRow(ctx, `INSERT INTO collections (id, name, owner_id) VALUES ($1, $2, $3) RETURNING created_at`,
		c.ID, name, ownerID).Scan(&c.CreatedAt)
	return c, err
}

func (s *Store) RenameCollection(ctx context.Context, id, name string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE collections SET name = $2 WHERE id = $1`, id, name)
	if err != nil {
		return notFound(err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteCollection removes the collection; its scenes fall back to "no collection".
func (s *Store) DeleteCollection(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM collections WHERE id = $1`, id)
	return notFound(err)
}
