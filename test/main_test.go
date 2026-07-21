package test

import (
	"os"
	"testing"
)

// TestMain neutralises ambient server-mode configuration for the acceptance
// suite.
//
// These tests drive the real rgt binary against a .regent/ directory they
// create themselves. Capture and the read commands both prefer server mode when
// it is configured, so on a machine with REGENT_SERVER_URL set (or a [server]
// section in ~/.regent/config.toml) the binary would read and write a shared
// machine-local cache instead of the fixture — and the assertions would be
// measuring the wrong store.
//
// Child processes inherit this through os.Environ(), so clearing it here covers
// the exec'd binary as well as in-process code.
func TestMain(m *testing.M) {
	os.Setenv("REGENT_SERVER_URL", "")
	os.Exit(m.Run())
}
