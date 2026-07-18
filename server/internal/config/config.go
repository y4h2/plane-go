package config

import (
	"os"
	"time"
)

// Config is the runtime configuration, all overridable via env.
type Config struct {
	Addr              string
	DatabaseURL       string
	SessionCookieName string
	WebURL            string
	SessionTTL        time.Duration
}

func Load() Config {
	return Config{
		Addr:              env("PLANE_GO_ADDR", ":4001"),
		DatabaseURL:       env("DATABASE_URL", "postgres://plane:plane@localhost:4010/plane_go?sslmode=disable"),
		SessionCookieName: env("SESSION_COOKIE_NAME", "session-id"),
		WebURL:            env("WEB_URL", "http://localhost:3000"),
		SessionTTL:        7 * 24 * time.Hour,
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
