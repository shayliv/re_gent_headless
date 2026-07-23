package main

import (
	"os"
	"testing"
)

// TestMain neutralises ambient server-mode configuration for the hook tests.
//
// The hook entry points call capture.Open, which consults the real environment
// and ~/.regent/config.toml. Without this guard a machine configured for server
// mode routes these tests' capture into a shared machine-local cache instead of
// the per-test .regent/ directory they assert against. See the equivalent guard
// in internal/capture for the full rationale.
func TestMain(m *testing.M) {
	os.Setenv("REGENT_SERVER_URL", "")
	os.Exit(m.Run())
}
