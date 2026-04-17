package config

import (
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	defaultHTTPAddr           = ":8080"
	defaultDBPath             = "./data/app.db"
	defaultLogLevel           = "info"
	defaultLogFormat          = "pretty"
	defaultBasePath           = ""
	defaultPublicSubPath      = "/sub/"
	defaultProfileTitle       = "3xui-user-sync"
	defaultSessionTTL         = 24 * time.Hour
	defaultSessionIdleTimeout = 8 * time.Hour
	defaultRememberTTL        = 30 * 24 * time.Hour
	defaultRequestTimeout     = 15 * time.Second
)

type Config struct {
	HTTPAddr           string
	DBPath             string
	LogLevel           string
	LogFormat          string
	BasePath           string
	PublicSubPath      string
	ProfileTitle       string
	SecureCookie       bool
	SessionTTL         time.Duration
	SessionIdleTimeout time.Duration
	RememberTTL        time.Duration
	RequestTimeout     time.Duration
	BootstrapAdminUser string
	BootstrapAdminPass string
}

func Load() (Config, error) {
	cfg := Config{}

	flag.StringVar(&cfg.HTTPAddr, "http", envString("HTTP_ADDR", defaultHTTPAddr), "HTTP listen address")
	flag.StringVar(&cfg.DBPath, "db", envString("DB_PATH", defaultDBPath), "SQLite database path")
	flag.StringVar(&cfg.LogLevel, "log-level", envString("LOG_LEVEL", defaultLogLevel), "zerolog level")
	flag.StringVar(&cfg.LogFormat, "log-format", envString("LOG_FORMAT", defaultLogFormat), "log format: json|pretty")
	flag.StringVar(&cfg.BasePath, "base-path", envString("BASE_PATH", defaultBasePath), "path prefix for UI and API")
	flag.StringVar(&cfg.PublicSubPath, "subscription-path", envString("PUBLIC_SUBSCRIPTION_PATH", defaultPublicSubPath), "public subscription path under base path")
	flag.StringVar(&cfg.ProfileTitle, "profile-title", envString("PROFILE_TITLE", defaultProfileTitle), "profile title for aggregated subscriptions")
	flag.BoolVar(&cfg.SecureCookie, "secure-cookie", envBool("SECURE_COOKIE", false), "set Secure on session cookies")

	sessionTTL, err := envDuration("SESSION_TTL", defaultSessionTTL)
	if err != nil {
		return cfg, fmt.Errorf("SESSION_TTL: %w", err)
	}
	idleTTL, err := envDuration("SESSION_IDLE_TIMEOUT", defaultSessionIdleTimeout)
	if err != nil {
		return cfg, fmt.Errorf("SESSION_IDLE_TIMEOUT: %w", err)
	}
	rememberTTL, err := envDuration("REMEMBER_ME_TTL", defaultRememberTTL)
	if err != nil {
		return cfg, fmt.Errorf("REMEMBER_ME_TTL: %w", err)
	}
	requestTimeout, err := envDuration("REQUEST_TIMEOUT", defaultRequestTimeout)
	if err != nil {
		return cfg, fmt.Errorf("REQUEST_TIMEOUT: %w", err)
	}

	cfg.SessionTTL = sessionTTL
	cfg.SessionIdleTimeout = idleTTL
	cfg.RememberTTL = rememberTTL
	cfg.RequestTimeout = requestTimeout
	cfg.BootstrapAdminUser = os.Getenv("ADMIN_USERNAME")
	cfg.BootstrapAdminPass = os.Getenv("ADMIN_PASSWORD")

	flag.Parse()

	cfg.BasePath = normalizePrefix(cfg.BasePath)
	cfg.PublicSubPath = normalizePath(cfg.PublicSubPath, "/sub/")
	cfg.ProfileTitle = strings.TrimSpace(cfg.ProfileTitle)
	if cfg.ProfileTitle == "" {
		cfg.ProfileTitle = defaultProfileTitle
	}
	if cfg.SessionIdleTimeout > cfg.SessionTTL {
		return cfg, fmt.Errorf("SESSION_IDLE_TIMEOUT must be <= SESSION_TTL")
	}
	if cfg.HTTPAddr == "" {
		return cfg, fmt.Errorf("http listen address is required")
	}
	if cfg.DBPath == "" {
		return cfg, fmt.Errorf("db path is required")
	}

	return cfg, nil
}

func envString(name, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	return time.ParseDuration(value)
}

func envBool(name string, fallback bool) bool {
	value := strings.TrimSpace(strings.ToLower(os.Getenv(name)))
	if value == "" {
		return fallback
	}
	return value == "1" || value == "true" || value == "yes" || value == "on"
}

func normalizePrefix(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || v == "/" {
		return ""
	}
	if !strings.HasPrefix(v, "/") {
		v = "/" + v
	}
	return strings.TrimRight(v, "/")
}

func normalizePath(v, fallback string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return fallback
	}
	if !strings.HasPrefix(v, "/") {
		v = "/" + v
	}
	if !strings.HasSuffix(v, "/") {
		v += "/"
	}
	return v
}
