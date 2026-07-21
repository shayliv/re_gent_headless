// Package remote implements the client half of re_gent's server mode: an HTTP
// client for the object/ref protocol, a durable outbox for offline resilience,
// and the push/hydrate walkers that move a session DAG between a local cache
// and the server.
//
// In server mode the server is the source of truth. The local directory is a
// disposable write-ahead cache; see docs/server-mode.md for the failure-mode
// contract this package implements.
package remote

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// DefaultTimeout bounds all network work performed inside a single hook
// invocation. Hooks run inside a live agent turn, so this is deliberately
// short: exceeding it spools and returns rather than stalling the agent.
const DefaultTimeout = 5 * time.Second

// maxTimeout caps operator-supplied timeouts. A hook that blocks longer than
// this is indistinguishable from a hung agent, so we refuse to honour it.
const maxTimeout = 60 * time.Second

// Config describes how (and whether) capture talks to a re_gent server.
type Config struct {
	// ServerURL is the base URL of the re_gent server, e.g. https://regent.example.com.
	ServerURL string
	// RepoID is the repository name registered with the server.
	RepoID string
	// Token is the bearer token used for authentication. It is never logged.
	Token string
	// Timeout bounds all network work for one hook invocation.
	Timeout time.Duration
	// CacheDir overrides the default machine-local cache location.
	CacheDir string
}

// Enabled reports whether server mode is configured. Both a server URL and a
// repo id are required: half a configuration is treated as no configuration so
// that a typo degrades to local mode rather than to a broken remote.
func (c Config) Enabled() bool {
	return c.ServerURL != "" && c.RepoID != ""
}

// Validate checks a server-mode configuration without contacting the server.
func (c Config) Validate() error {
	if c.ServerURL == "" {
		return fmt.Errorf("server url is required")
	}
	u, err := url.Parse(c.ServerURL)
	if err != nil {
		return fmt.Errorf("invalid server url: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("invalid server url %q: scheme must be http or https", c.ServerURL)
	}
	if u.Host == "" {
		return fmt.Errorf("invalid server url %q: missing host", c.ServerURL)
	}
	return ValidateRepoID(c.RepoID)
}

// ValidateRepoID mirrors the server's repo-name rules so that an unusable id is
// rejected on the client instead of producing a confusing 400 mid-turn.
func ValidateRepoID(repo string) error {
	if repo == "" {
		return fmt.Errorf("repo id is required")
	}
	if len(repo) > 64 {
		return fmt.Errorf("repo id too long (max 64 characters)")
	}
	for _, r := range repo {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.' || r == '_' || r == '-':
		default:
			return fmt.Errorf("invalid repo id %q: use letters, digits, '.', '_', '-' only", repo)
		}
	}
	switch repo[0] {
	case '.', '-', '_':
		return fmt.Errorf("invalid repo id %q: must start with a letter or digit", repo)
	}
	return nil
}

// fileConfig is the on-disk shape of ~/.regent/config.toml. Only the [server]
// table is read here; other tables (e.g. [auth]) are ignored so this file can
// be shared with other re_gent features.
type fileConfig struct {
	Server struct {
		URL     string `toml:"url"`
		RepoID  string `toml:"repo_id"`
		Token   string `toml:"token"`
		Timeout string `toml:"timeout"`
	} `toml:"server"`
}

// Env is a lookup function with the shape of os.LookupEnv. Tests inject a map
// so that configuration resolution never depends on the ambient environment.
type Env func(string) (string, bool)

// OSEnv reads the real process environment.
func OSEnv(key string) (string, bool) { return os.LookupEnv(key) }

// LoadConfig resolves server-mode configuration. Environment variables win over
// the config file so an operator can disable or redirect server mode for one
// process without editing shared state.
//
// A malformed config file is reported as an error but never panics; callers in
// the hook path treat any error as "server mode unavailable" and fall back.
func LoadConfig(env Env, configPath string) (Config, error) {
	if env == nil {
		env = OSEnv
	}

	var cfg Config
	fileErr := loadFileConfig(configPath, &cfg)

	if v, ok := env("REGENT_SERVER_URL"); ok {
		cfg.ServerURL = strings.TrimSpace(v)
	}
	if v, ok := env("REGENT_REPO_ID"); ok {
		cfg.RepoID = strings.TrimSpace(v)
	}
	if v, ok := env("REGENT_TOKEN"); ok {
		cfg.Token = strings.TrimSpace(v)
	}
	if v, ok := env("REGENT_CACHE_DIR"); ok {
		cfg.CacheDir = strings.TrimSpace(v)
	}
	if v, ok := env("REGENT_SERVER_TIMEOUT"); ok {
		d, err := time.ParseDuration(strings.TrimSpace(v))
		if err != nil {
			return Config{}, fmt.Errorf("invalid REGENT_SERVER_TIMEOUT %q: %w", v, err)
		}
		cfg.Timeout = d
	}

	cfg.ServerURL = strings.TrimRight(cfg.ServerURL, "/")
	cfg.Timeout = clampTimeout(cfg.Timeout)

	if fileErr != nil {
		return Config{}, fileErr
	}
	return cfg, nil
}

func loadFileConfig(path string, cfg *Config) error {
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		// A missing config file is the common case, not an error.
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", path, err)
	}

	var fc fileConfig
	if err := toml.Unmarshal(data, &fc); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}

	cfg.ServerURL = strings.TrimSpace(fc.Server.URL)
	cfg.RepoID = strings.TrimSpace(fc.Server.RepoID)
	cfg.Token = strings.TrimSpace(fc.Server.Token)
	if fc.Server.Timeout != "" {
		d, err := time.ParseDuration(fc.Server.Timeout)
		if err != nil {
			return fmt.Errorf("parse %s: invalid server.timeout %q: %w", path, fc.Server.Timeout, err)
		}
		cfg.Timeout = d
	}
	return nil
}

func clampTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return DefaultTimeout
	}
	if d > maxTimeout {
		return maxTimeout
	}
	return d
}

// DefaultConfigPath returns ~/.regent/config.toml, or "" when the home
// directory cannot be determined.
func DefaultConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".regent", "config.toml")
}

// CacheDirFor returns the machine-local cache directory backing server mode.
//
// The cache lives outside the working tree on purpose: in server mode the repo
// must not need a .regent/ directory at all. The cache is disposable — every
// object and ref in it is either already on the server or listed in the spool.
func CacheDirFor(cfg Config) (string, error) {
	if err := ValidateRepoID(cfg.RepoID); err != nil {
		return "", err
	}
	base := cfg.CacheDir
	if base == "" {
		userCache, err := os.UserCacheDir()
		if err != nil {
			return "", fmt.Errorf("locate user cache dir: %w", err)
		}
		base = filepath.Join(userCache, "regent")
	}
	return filepath.Join(base, "repos", cfg.RepoID), nil
}

// Redact renders a token safe to log: it never reveals more than a short prefix.
func Redact(token string) string {
	if token == "" {
		return ""
	}
	if len(token) < 8 {
		return "****"
	}
	return token[:4] + "****"
}
