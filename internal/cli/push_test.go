package cli

import (
	"errors"
	"testing"

	"github.com/regent-vcs/regent/internal/config"
)

func TestRunPush_RejectsUnauthenticated(t *testing.T) {
	err := runPush(&config.UserConfig{})
	if !errors.Is(err, config.ErrNotSignedIn) {
		t.Errorf("expected ErrNotSignedIn, got %v", err)
	}
}

func TestRunPush_RejectsShortToken(t *testing.T) {
	err := runPush(&config.UserConfig{Auth: config.Auth{Token: "short"}})
	if !errors.Is(err, config.ErrTokenInvalid) {
		t.Errorf("expected ErrTokenInvalid, got %v", err)
	}
}

func TestRunPush_AcceptsValidToken(t *testing.T) {
	err := runPush(&config.UserConfig{Auth: config.Auth{
		ServerURL: "https://example.com",
		Token:     "tok-abcdefgh12345678",
	}})
	if err != nil {
		t.Errorf("expected no error for valid token, got %v", err)
	}
}
