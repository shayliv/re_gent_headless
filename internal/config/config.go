// Package config manages the global per-user re_gent configuration stored in
// ~/.regent/config.toml. This file holds authentication tokens; it is written
// with mode 0600 so other users cannot read it.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

// UserConfig is the structure of ~/.regent/config.toml.
type UserConfig struct {
	Auth AuthConfig `toml:"auth"`
}

// AuthConfig stores the authentication token and the server it belongs to.
type AuthConfig struct {
	ServerURL string `toml:"server_url"`
	Token     string `toml:"token"`
}

// DefaultPath returns the path to the global config file (~/.regent/config.toml).
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".regent", "config.toml"), nil
}

// LoadFrom reads the user config from path. A missing file is treated as an
// empty config (no error).
func LoadFrom(path string) (UserConfig, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return UserConfig{}, nil
	}
	if err != nil {
		return UserConfig{}, fmt.Errorf("read config %s: %w", path, err)
	}
	var cfg UserConfig
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return UserConfig{}, fmt.Errorf("parse config %s: %w", path, err)
	}
	return cfg, nil
}

// SaveTo writes cfg to path with mode 0600. Parent directories are created if
// needed.
func SaveTo(path string, cfg UserConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	// 0o600: token must not be world-readable.
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("write config %s: %w", path, err)
	}
	return nil
}

// Load reads the user config from the default path.
func Load() (UserConfig, error) {
	p, err := DefaultPath()
	if err != nil {
		return UserConfig{}, err
	}
	return LoadFrom(p)
}

// Save writes cfg to the default path.
func Save(cfg UserConfig) error {
	p, err := DefaultPath()
	if err != nil {
		return err
	}
	return SaveTo(p, cfg)
}
