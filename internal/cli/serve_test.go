package cli

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveDataDir(t *testing.T) {
	dir := t.TempDir()
	got, err := resolveDataDir(dir)
	if err != nil {
		t.Fatalf("resolveDataDir(%q): %v", dir, err)
	}
	if !filepath.IsAbs(got) {
		t.Fatalf("resolveDataDir returned a relative path %q", got)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory in this environment: %v", err)
	}
	got, err = resolveDataDir("")
	if err != nil {
		t.Fatalf("resolveDataDir(\"\"): %v", err)
	}
	if want := filepath.Join(home, ".regent-server"); got != want {
		t.Fatalf("default data dir = %q, want %q", got, want)
	}
}

func TestRunServeServesReposAndShutsDown(t *testing.T) {
	// Take a port, release it, and hand the address to the server. Anything
	// else would mean exporting the listener purely for tests.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	addr := probe.Addr().String()
	if err := probe.Close(); err != nil {
		t.Fatalf("probe close: %v", err)
	}

	dataDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runServe(ctx, serveParams{Addr: addr, DataDir: dataDir}) }()

	// Wait for the listener to come up.
	var resp *http.Response
	for i := 0; i < 50; i++ {
		resp, err = http.Get("http://" + addr + "/repos")
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		cancel()
		t.Fatalf("server never became reachable: %v", err)
	}
	defer resp.Body.Close()

	var lr struct {
		Repos []string `json:"repos"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		cancel()
		t.Fatalf("decode /repos: %v", err)
	}
	if len(lr.Repos) != 0 {
		cancel()
		t.Fatalf("fresh server lists repos %v, want none", lr.Repos)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runServe returned %v, want nil after shutdown", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runServe did not shut down")
	}

	if _, err := os.Stat(filepath.Join(dataDir, "repos")); err != nil {
		t.Fatalf("server did not create its data dir: %v", err)
	}
}

func TestRunServeReportsBindFailure(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer occupied.Close()

	err = runServe(context.Background(), serveParams{
		Addr:    occupied.Addr().String(),
		DataDir: t.TempDir(),
	})
	if err == nil {
		t.Fatal("runServe on an occupied port should fail")
	}
	if !strings.Contains(err.Error(), "listen") {
		t.Fatalf("error %v does not mention the listen failure", err)
	}
}

func TestServeCmdFlagDefaults(t *testing.T) {
	cmd := ServeCmd()
	if got := cmd.Flags().Lookup("addr").DefValue; got != DefaultServeAddr {
		t.Fatalf("--addr default = %q, want %q", got, DefaultServeAddr)
	}
	for _, name := range []string{"data", "max-object-size"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("serve is missing the --%s flag", name)
		}
	}
	cmd.SetArgs([]string{"unexpected"})
	cmd.SetOut(nopWriter{})
	cmd.SetErr(nopWriter{})
	if err := cmd.Execute(); err == nil {
		t.Fatal("rgt serve should reject positional arguments")
	}
}
