package config

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestSaveToAndLoadFrom_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	cfg := &UserConfig{Auth: Auth{ServerURL: "https://example.com", Token: "tok-abcdefgh12345678"}}

	if err := SaveTo(path, cfg); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}

	got, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if got.Auth.ServerURL != cfg.Auth.ServerURL {
		t.Errorf("ServerURL: got %q, want %q", got.Auth.ServerURL, cfg.Auth.ServerURL)
	}
	if got.Auth.Token != cfg.Auth.Token {
		t.Errorf("Token: got %q, want %q", got.Auth.Token, cfg.Auth.Token)
	}
}

func TestLoadFrom_MissingFile(t *testing.T) {
	cfg, err := LoadFrom(filepath.Join(t.TempDir(), "nonexistent.toml"))
	if err != nil {
		t.Fatalf("expected no error for missing file, got %v", err)
	}
	if cfg.Auth.Token != "" {
		t.Error("expected empty token for missing file")
	}
}

func TestCheckAuth_NotSignedIn(t *testing.T) {
	if err := CheckAuth(&UserConfig{}); !errors.Is(err, ErrNotSignedIn) {
		t.Errorf("expected ErrNotSignedIn, got %v", err)
	}
	if err := CheckAuth(nil); !errors.Is(err, ErrNotSignedIn) {
		t.Errorf("expected ErrNotSignedIn for nil, got %v", err)
	}
}

func TestCheckAuth_TokenTooShort(t *testing.T) {
	cfg := &UserConfig{Auth: Auth{Token: "short"}}
	if err := CheckAuth(cfg); !errors.Is(err, ErrTokenInvalid) {
		t.Errorf("expected ErrTokenInvalid, got %v", err)
	}
}

func TestCheckAuth_Valid(t *testing.T) {
	cfg := &UserConfig{Auth: Auth{Token: "tok-abcdefgh12345678"}}
	if err := CheckAuth(cfg); err != nil {
		t.Errorf("expected no error for valid token, got %v", err)
	}
}

func TestRedact(t *testing.T) {
	cases := []struct {
		tok  string
		want string
	}{
		{"tok-abcdefgh12345678", "tok-****"},
		{"ab", "****"},
		{"", "****"},
		{"abcd", "abcd****"},
	}
	for _, c := range cases {
		got := Redact(c.tok)
		if got != c.want {
			t.Errorf("Redact(%q) = %q, want %q", c.tok, got, c.want)
		}
	}
}
