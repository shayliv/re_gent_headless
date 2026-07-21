// Package config manages per-user re_gent configuration stored at
// ~/.regent/config.toml with mode 0600. Only the owner can read it, keeping
// auth tokens off world-readable paths.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// ErrNotSignedIn is returned by CheckAuth when no valid token is stored.
var ErrNotSignedIn = errors.New("not signed in\n\nRun: rgt login <server-url>")

// ErrTokenInvalid is returned when a token is present but fails format validation.
var ErrTokenInvalid = errors.New("token is invalid")

// minTokenLen is the minimum length for a syntactically valid token.
const minTokenLen = 16

// UserConfig is the top-level user configuration.
type UserConfig struct {
	Auth Auth `toml:"auth"`
}

// Auth holds authentication credentials for a re_gent server.
type Auth struct {
	ServerURL string `toml:"server_url"`
	Token     string `toml:"token"`
}

// DefaultPath returns the default per-user config path: ~/.regent/config.toml.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}
	return filepath.Join(home, ".regent", "config.toml"), nil
}

// Load reads the default per-user config. A missing file returns an empty
// config (not an error); callers must use CheckAuth to detect unauthenticated state.
func Load() (*UserConfig, error) {
	path, err := DefaultPath()
	if err != nil {
		return &UserConfig{}, nil
	}
	return LoadFrom(path)
}

// LoadFrom reads a UserConfig from an explicit path. Missing file → empty config.
func LoadFrom(path string) (*UserConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &UserConfig{}, nil
		}
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg UserConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}
	return &cfg, nil
}

// Save writes cfg to the default per-user config path with mode 0600.
func Save(cfg *UserConfig) error {
	path, err := DefaultPath()
	if err != nil {
		return err
	}
	return SaveTo(path, cfg)
}

// SaveTo writes cfg to an explicit path atomically with mode 0600.
func SaveTo(path string, cfg *UserConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("chmod config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename config: %w", err)
	}
	return nil
}

// CheckAuth returns nil when cfg contains a usable token, otherwise
// ErrNotSignedIn or ErrTokenInvalid. Always fails closed.
func CheckAuth(cfg *UserConfig) error {
	if cfg == nil || cfg.Auth.Token == "" {
		return ErrNotSignedIn
	}
	if len(cfg.Auth.Token) < minTokenLen {
		return ErrTokenInvalid
	}
	return nil
}

// Redact returns a display-safe version of tok: first 4 chars + "****".
func Redact(tok string) string {
	if len(tok) < 4 {
		return "****"
	}
	return tok[:4] + "****"
}
