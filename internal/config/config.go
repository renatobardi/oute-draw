// Package config reads the process environment once, at startup.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
)

type Config struct {
	Listen      string
	DatabaseURL string
	DBSchema    string

	// AuthMode "proxy" trusts the identity header set by oauth2-proxy; "dev" signs
	// everyone in as DevEmail and must never face a network.
	AuthMode    string
	EmailHeader string
	NameHeader  string
	DevEmail    string
	AdminEmails map[string]bool
	DefaultRole string

	// Secret signs the tokens that prove a share-link password was given.
	Secret []byte

	AnthropicAPIKey string
	AIModel         string

	// ICEServers is handed to browsers for voice calls, as WebRTC RTCIceServer JSON.
	ICEServers json.RawMessage
}

func env(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func Load() (Config, error) {
	c := Config{
		Listen:          env("DRAW_LISTEN", ":8080"),
		DatabaseURL:     env("DRAW_DATABASE_URL", ""),
		DBSchema:        env("DRAW_DB_SCHEMA", ""),
		AuthMode:        env("DRAW_AUTH_MODE", "proxy"),
		EmailHeader:     env("DRAW_AUTH_EMAIL_HEADER", "X-Forwarded-Email"),
		NameHeader:      env("DRAW_AUTH_NAME_HEADER", "X-Forwarded-Preferred-Username"),
		DevEmail:        strings.ToLower(env("DRAW_DEV_EMAIL", "dev@localhost")),
		DefaultRole:     env("DRAW_DEFAULT_ROLE", "editor"),
		Secret:          []byte(env("DRAW_SECRET", "")),
		AnthropicAPIKey: env("ANTHROPIC_API_KEY", ""),
		AIModel:         env("DRAW_AI_MODEL", "claude-opus-5-5"),
		AdminEmails:     map[string]bool{},
		ICEServers:      json.RawMessage(env("DRAW_ICE_SERVERS", `[{"urls":"stun:stun.l.google.com:19302"}]`)),
	}
	for _, e := range strings.Split(env("DRAW_ADMIN_EMAILS", ""), ",") {
		if e = strings.ToLower(strings.TrimSpace(e)); e != "" {
			c.AdminEmails[e] = true
		}
	}
	var errs []error
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("DRAW_DATABASE_URL is required"))
	}
	switch c.AuthMode {
	case "proxy":
		if len(c.Secret) < 32 {
			errs = append(errs, errors.New("DRAW_SECRET must have at least 32 characters"))
		}
	case "dev":
		if len(c.Secret) == 0 {
			c.Secret = []byte("dev-only-secret-never-use-in-production")
		}
	default:
		errs = append(errs, fmt.Errorf("DRAW_AUTH_MODE must be proxy or dev, got %q", c.AuthMode))
	}
	var ice []map[string]any
	if err := json.Unmarshal(c.ICEServers, &ice); err != nil {
		errs = append(errs, fmt.Errorf("DRAW_ICE_SERVERS must be a JSON array of ICE servers: %w", err))
	}
	switch c.DefaultRole {
	case "editor", "viewer":
	default:
		errs = append(errs, fmt.Errorf("DRAW_DEFAULT_ROLE must be editor or viewer, got %q", c.DefaultRole))
	}
	return c, errors.Join(errs...)
}
