package config

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLoadFromMissing(t *testing.T) {
	cfg, err := LoadFrom(filepath.Join(t.TempDir(), "nonexistent.toml"))
	if err != nil {
		t.Fatalf("LoadFrom missing file: got error %v, want nil", err)
	}
	if cfg.Auth.Token != "" || cfg.Auth.ServerURL != "" {
		t.Fatalf("expected empty config, got %+v", cfg)
	}
}

func TestSaveToRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	want := &UserConfig{Auth: Auth{
		ServerURL: "https://regent.example.com",
		Token:     "test-token-at-least-16-chars",
	}}
	if err := SaveTo(path, want); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}

	got, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if got.Auth.ServerURL != want.Auth.ServerURL {
		t.Errorf("ServerURL: got %q, want %q", got.Auth.ServerURL, want.Auth.ServerURL)
	}
	if got.Auth.Token != want.Auth.Token {
		t.Errorf("Token: got %q, want %q", got.Auth.Token, want.Auth.Token)
	}
}

func TestSaveToPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permission bits not enforced on Windows")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")

	cfg := &UserConfig{Auth: Auth{Token: "a-token-that-is-long-enough"}}
	if err := SaveTo(path, cfg); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("file permissions: got %o, want 0600", perm)
	}
}

func TestSaveToCreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a", "b", "config.toml")

	if err := SaveTo(path, &UserConfig{}); err != nil {
		t.Fatalf("SaveTo with nested dirs: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not created: %v", err)
	}
}

func TestCheckAuthEmpty(t *testing.T) {
	if err := CheckAuth(&UserConfig{}); !errors.Is(err, ErrNotSignedIn) {
		t.Errorf("empty config: got %v, want ErrNotSignedIn", err)
	}
}

func TestCheckAuthNil(t *testing.T) {
	if err := CheckAuth(nil); !errors.Is(err, ErrNotSignedIn) {
		t.Errorf("nil config: got %v, want ErrNotSignedIn", err)
	}
}

func TestCheckAuthTooShort(t *testing.T) {
	cfg := &UserConfig{Auth: Auth{Token: "short"}}
	if err := CheckAuth(cfg); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("short token: got %v, want ErrTokenInvalid", err)
	}
}

func TestCheckAuthValid(t *testing.T) {
	cfg := &UserConfig{Auth: Auth{Token: "a-valid-token-at-least-16"}}
	if err := CheckAuth(cfg); err != nil {
		t.Errorf("valid token: got %v, want nil", err)
	}
}

func TestRedact(t *testing.T) {
	cases := []struct {
		tok  string
		want string
	}{
		{"", "<no token>"},
		{"abc", "****"},
		{"abcd", "****"},
		{"abcde", "abcd****"},
		{"a-valid-token-at-least-16", "a-va****"},
	}
	for _, c := range cases {
		if got := Redact(c.tok); got != c.want {
			t.Errorf("Redact(%q) = %q, want %q", c.tok, got, c.want)
		}
	}
}
