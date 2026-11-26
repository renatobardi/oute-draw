-- Everything lives in the tenant schema selected by search_path (oute-infra-ops ADR-0003):
-- no CREATE SCHEMA, no extension, no object outside the app's own schema.

CREATE TABLE users (
    id           uuid PRIMARY KEY,
    email        text NOT NULL UNIQUE,
    name         text NOT NULL DEFAULT '',
    role         text NOT NULL CHECK (role IN ('admin', 'editor', 'viewer')),
    library      jsonb NOT NULL DEFAULT '[]'::jsonb,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE collections (
    id         uuid PRIMARY KEY,
    name       text NOT NULL,
    owner_id   uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE scenes (
    id            uuid PRIMARY KEY,
    title         text NOT NULL,
    collection_id uuid REFERENCES collections (id) ON DELETE SET NULL,
    owner_id      uuid REFERENCES users (id) ON DELETE SET NULL,
    elements      jsonb NOT NULL DEFAULT '[]'::jsonb,
    app_state     jsonb NOT NULL DEFAULT '{}'::jsonb,
    slide_order   jsonb NOT NULL DEFAULT '[]'::jsonb,
    text_content  text NOT NULL DEFAULT '',
    thumbnail     bytea,
    version       bigint NOT NULL DEFAULT 0,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    deleted_at    timestamptz
);
CREATE INDEX scenes_collection_idx ON scenes (collection_id);
CREATE INDEX scenes_updated_idx ON scenes (updated_at DESC);

CREATE TABLE scene_files (
    scene_id   uuid NOT NULL REFERENCES scenes (id) ON DELETE CASCADE,
    file_id    text NOT NULL,
    mime_type  text NOT NULL,
    data       bytea NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (scene_id, file_id)
);

CREATE TABLE scene_versions (
    id          uuid PRIMARY KEY,
    scene_id    uuid NOT NULL REFERENCES scenes (id) ON DELETE CASCADE,
    label       text NOT NULL DEFAULT '',
    auto        boolean NOT NULL DEFAULT true,
    author_name text NOT NULL DEFAULT '',
    elements    jsonb NOT NULL,
    app_state   jsonb NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX scene_versions_scene_idx ON scene_versions (scene_id, created_at DESC);

CREATE TABLE share_links (
    id            uuid PRIMARY KEY,
    scene_id      uuid NOT NULL REFERENCES scenes (id) ON DELETE CASCADE,
    token         text NOT NULL UNIQUE,
    mode          text NOT NULL CHECK (mode IN ('view', 'edit')),
    password_hash text NOT NULL DEFAULT '',
    expires_at    timestamptz,
    created_by    uuid REFERENCES users (id) ON DELETE SET NULL,
    created_at    timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX share_links_scene_idx ON share_links (scene_id);

CREATE TABLE comments (
    id          uuid PRIMARY KEY,
    scene_id    uuid NOT NULL REFERENCES scenes (id) ON DELETE CASCADE,
    -- NULL for the first comment of a thread; replies point at it.
    thread_id   uuid REFERENCES comments (id) ON DELETE CASCADE,
    author_id   uuid REFERENCES users (id) ON DELETE SET NULL,
    author_name text NOT NULL,
    x           double precision NOT NULL DEFAULT 0,
    y           double precision NOT NULL DEFAULT 0,
    body        text NOT NULL,
    resolved_at timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX comments_scene_idx ON comments (scene_id, created_at);
