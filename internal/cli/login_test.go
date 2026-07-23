package cli

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/regent-vcs/regent/internal/config"
)

func TestRunLoginRejectsEmptyServerURL(t *testing.T) {
	err := runLogin("", "sometoken12345678", func(*config.UserConfig) error { return nil })
	if err == nil {
		t.Fatal("expected error for empty server URL")
	}
}

func TestRunLoginRejectsEmptyToken(t *testing.T) {
	err := runLogin("https://regent.example.com", "", func(*config.UserConfig) error {
		t.Fatal("saveFn should not be called with empty token")
		return nil
	})
	if err == nil {
		t.Fatal("expected error for empty token")
	}
}

func TestRunLoginRejectsShortToken(t *testing.T) {
	err := runLogin("https://regent.example.com", "short", func(*config.UserConfig) error {
		t.Fatal("saveFn should not be called with short token")
		return nil
	})
	if err == nil {
		t.Fatal("expected error for token shorter than minimum length")
	}
}

func TestRunLoginPersistsCredentials(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.toml")

	saved := false
	err := runLogin("https://regent.example.com", "a-valid-token-of-sufficient-length", func(cfg *config.UserConfig) error {
		saved = true
		return config.SaveTo(cfgPath, cfg)
	})
	if err != nil {
		t.Fatalf("runLogin: %v", err)
	}
	if !saved {
		t.Fatal("saveFn was not called")
	}

	got, err := config.LoadFrom(cfgPath)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if got.Auth.ServerURL != "https://regent.example.com" {
		t.Errorf("ServerURL: got %q, want %q", got.Auth.ServerURL, "https://regent.example.com")
	}
	if got.Auth.Token != "a-valid-token-of-sufficient-length" {
		t.Errorf("Token not persisted correctly")
	}
}

func TestRunLoginSaveError(t *testing.T) {
	saveErr := errors.New("disk full")
	err := runLogin("https://regent.example.com", "a-valid-token-of-sufficient-length", func(*config.UserConfig) error {
		return saveErr
	})
	if err == nil {
		t.Fatal("expected error when save fails")
	}
}

func TestRunLoginTrimsTrailingSlash(t *testing.T) {
	var capturedURL string
	_ = runLogin("https://regent.example.com/", "a-valid-token-of-sufficient-length", func(cfg *config.UserConfig) error {
		capturedURL = cfg.Auth.ServerURL
		return nil
	})
	if capturedURL != "https://regent.example.com" {
		t.Errorf("expected trailing slash trimmed, got %q", capturedURL)
	}
}
