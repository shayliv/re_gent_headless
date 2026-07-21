package capture

import (
	"testing"
)

func TestResolveAuthor_EnvOverride(t *testing.T) {
	t.Setenv("REGENT_AUTHOR_NAME", "Alice")
	t.Setenv("REGENT_AUTHOR_EMAIL", "alice@example.com")

	a := ResolveAuthor()
	if a.Name != "Alice" {
		t.Errorf("Name: got %q, want Alice", a.Name)
	}
	if a.Email != "alice@example.com" {
		t.Errorf("Email: got %q, want alice@example.com", a.Email)
	}
}

func TestResolveAuthor_GitEnvFallback(t *testing.T) {
	t.Setenv("REGENT_AUTHOR_NAME", "")
	t.Setenv("GIT_AUTHOR_NAME", "Bob")
	t.Setenv("GIT_AUTHOR_EMAIL", "bob@example.com")

	a := ResolveAuthor()
	if a.Name != "Bob" {
		t.Errorf("Name: got %q, want Bob", a.Name)
	}
	if a.Email != "bob@example.com" {
		t.Errorf("Email: got %q, want bob@example.com", a.Email)
	}
}

func TestResolveAuthor_NoEnv_ReturnsAuthor(t *testing.T) {
	t.Setenv("REGENT_AUTHOR_NAME", "")
	t.Setenv("GIT_AUTHOR_NAME", "")

	// git config may or may not be set in CI; we just want no panic.
	a := ResolveAuthor()
	_ = a
}
