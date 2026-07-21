package capture

import (
	"os"
	"testing"
)

// TestMain neutralises ambient server-mode configuration for the whole package.
//
// Open now consults the real environment and ~/.regent/config.toml. Without this
// guard, a developer machine that happens to have a [server] section — or a CI
// job that exports REGENT_SERVER_URL — silently flips every local-mode test in
// this package into server mode: they then share one machine-local cache, so
// they cross-contaminate each other's refs and steps and fail for reasons that
// have nothing to do with the code under test.
//
// Setting the variable to an empty value (rather than unsetting it) is what
// makes this airtight: environment wins over the config file in LoadConfig, and
// Config.Enabled requires a non-empty URL, so this one variable disables server
// mode no matter what the file says. Tests that *want* server mode override it
// with t.Setenv, which is scoped and restored automatically.
func TestMain(m *testing.M) {
	os.Setenv("REGENT_SERVER_URL", "")
	os.Exit(m.Run())
}
