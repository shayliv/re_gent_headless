package cli

import (
	"errors"
	"testing"

	"github.com/regent-vcs/regent/internal/config"
)

// TestPushRejectsUnauthenticated verifies that runPush fails closed when no
// token is configured.  This is the primary acceptance gate for RE-9.
func TestPushRejectsUnauthenticated(t *testing.T) {
	err := runPush(&config.UserConfig{})
	if err == nil {
		t.Fatal("expected error for unauthenticated push, got nil")
	}
	if !errors.Is(err, config.ErrNotSignedIn) {
		t.Errorf("expected ErrNotSignedIn, got %v", err)
	}
}

// TestPushRejectsNilConfig ensures a nil config also fails closed.
func TestPushRejectsNilConfig(t *testing.T) {
	err := runPush(nil)
	if err == nil {
		t.Fatal("expected error for nil config, got nil")
	}
	if !errors.Is(err, config.ErrNotSignedIn) {
		t.Errorf("expected ErrNotSignedIn, got %v", err)
	}
}

// TestPushRejectsInvalidToken ensures a token that is present but too short
// also fails closed (ErrTokenInvalid, which is distinct from ErrNotSignedIn).
func TestPushRejectsInvalidToken(t *testing.T) {
	cfg := &config.UserConfig{Auth: config.Auth{Token: "short"}}
	err := runPush(cfg)
	if err == nil {
		t.Fatal("expected error for invalid token, got nil")
	}
	if !errors.Is(err, config.ErrTokenInvalid) {
		t.Errorf("expected ErrTokenInvalid, got %v", err)
	}
}

// TestPushAllowsValidToken verifies that a properly formed token passes the
// auth gate (even though the push itself is a no-op stub).
func TestPushAllowsValidToken(t *testing.T) {
	cfg := &config.UserConfig{Auth: config.Auth{
		ServerURL: "https://regent.example.com",
		Token:     "a-valid-token-of-sufficient-length",
	}}
	if err := runPush(cfg); err != nil {
		t.Errorf("unexpected error with valid token: %v", err)
	}
}

// TestPushAuthDegradationIsNonPanicking verifies that even pathological input
// does not cause a panic — auth failures must never crash an agent turn.
func TestPushAuthDegradationIsNonPanicking(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("runPush panicked: %v", r)
		}
	}()
	// Nil config simulates an auth subsystem failure without a config file.
	_ = runPush(nil)
}
