package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFrom_MissingFile(t *testing.T) {
	cfg, err := LoadFrom(filepath.Join(t.TempDir(), "config.toml"))
	if err != nil {
		t.Fatalf("want nil error for missing file, got %v", err)
	}
	if cfg.Auth.Token != "" {
		t.Fatalf("want empty token, got %q", cfg.Auth.Token)
	}
}

func TestSaveTo_LoadFrom_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	want := UserConfig{Auth: AuthConfig{ServerURL: "https://example.com", Token: "secret-token"}}
	if err := SaveTo(path, want); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	got, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if got.Auth.Token != want.Auth.Token {
		t.Errorf("token: got %q, want %q", got.Auth.Token, want.Auth.Token)
	}
	if got.Auth.ServerURL != want.Auth.ServerURL {
		t.Errorf("server_url: got %q, want %q", got.Auth.ServerURL, want.Auth.ServerURL)
	}
}

func TestSaveTo_CreatesParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "dir", "config.toml")
	if err := SaveTo(path, UserConfig{Auth: AuthConfig{Token: "tok"}}); err != nil {
		t.Fatalf("SaveTo should create parent dirs: %v", err)
	}
	got, err := LoadFrom(path)
	if err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}
	if got.Auth.Token != "tok" {
		t.Errorf("got %q, want %q", got.Auth.Token, "tok")
	}
}

func TestSaveTo_FilePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := SaveTo(path, UserConfig{Auth: AuthConfig{Token: "secret"}}); err != nil {
		t.Fatalf("SaveTo: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Errorf("file permissions: got %04o, want %04o", mode, 0o600)
	}
}

func TestLoadFrom_InvalidTOML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("[broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFrom(path)
	if err == nil {
		t.Fatal("want error for invalid TOML, got nil")
	}
}
