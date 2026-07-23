package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/regent-vcs/regent/internal/store"
)

// newTestServer creates a Server backed by t.TempDir() and returns the httptest.Server.
func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	srv, err := New(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return srv, ts
}

// postBlob pushes raw bytes to the server and returns the reported hash.
func postBlob(t *testing.T, ts *httptest.Server, repo string, data []byte) string {
	t.Helper()
	resp, err := http.Post(ts.URL+"/repos/"+repo+"/objects", "application/octet-stream", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("POST object: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST object: status %d, body: %s", resp.StatusCode, body)
	}
	var result map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode POST response: %v", err)
	}
	h := result["hash"]
	if h == "" {
		t.Fatal("POST object: empty hash in response")
	}
	return h
}

// putRefHTTP performs a ref CAS update and returns the HTTP response.
func putRefHTTP(t *testing.T, ts *httptest.Server, repo, ref, expected, newHash string) *http.Response {
	t.Helper()
	body, _ := json.Marshal(putRefRequest{Expected: expected, New: newHash})
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/repos/"+repo+"/refs/"+ref, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT ref: %v", err)
	}
	return resp
}

func TestPostObject(t *testing.T) {
	_, ts := newTestServer(t)

	h := postBlob(t, ts, "myrepo", []byte("hello, regent"))
	if len(h) != 64 {
		t.Errorf("expected 64-char hash, got %d chars: %s", len(h), h)
	}
}

func TestGetObject_RoundTrip(t *testing.T) {
	_, ts := newTestServer(t)

	content := []byte("round-trip content")
	h := postBlob(t, ts, "myrepo", content)

	resp, err := http.Get(ts.URL + "/repos/myrepo/objects/" + h)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET object: status %d", resp.StatusCode)
	}
	got, _ := io.ReadAll(resp.Body)
	if !bytes.Equal(got, content) {
		t.Errorf("content mismatch: got %q, want %q", got, content)
	}
}

func TestGetObject_NotFound(t *testing.T) {
	_, ts := newTestServer(t)

	hash := strings.Repeat("a", 64)
	resp, err := http.Get(ts.URL + "/repos/myrepo/objects/" + hash)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestGetObject_InvalidHash(t *testing.T) {
	_, ts := newTestServer(t)

	resp, err := http.Get(ts.URL + "/repos/myrepo/objects/not-a-hash")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestGetObject_PathTraversalInRepo(t *testing.T) {
	_, ts := newTestServer(t)

	hash := strings.Repeat("a", 64)
	resp, err := http.Get(ts.URL + "/repos/../etc/objects/" + hash)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	// ServeHTTP rejects paths with '.' or '..' segments before the mux sees them.
	if resp.StatusCode == http.StatusOK {
		t.Errorf("expected non-200 for path traversal, got 200")
	}
}

func TestPostObject_InvalidRepo(t *testing.T) {
	_, ts := newTestServer(t)

	tests := []string{
		strings.Repeat("a", 65),
		"repo with spaces",
	}
	for _, name := range tests {
		resp, err := http.Post(ts.URL+"/repos/"+name+"/objects", "application/octet-stream", bytes.NewReader([]byte("data")))
		if err != nil {
			t.Logf("POST %q: connection error (expected for traversal): %v", name, err)
			continue
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusCreated {
			t.Errorf("POST /repos/%q/objects: expected non-201, got 201", name)
		}
	}
}

func TestPutRef_NewRef(t *testing.T) {
	_, ts := newTestServer(t)

	// Push the blob so the ref can point to it.
	h := postBlob(t, ts, "r", []byte("blob data"))

	resp := putRefHTTP(t, ts, "r", "sessions/session1", "", h)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("PUT ref: status %d, body: %s", resp.StatusCode, body)
	}

	// Read it back.
	getResp, err := http.Get(ts.URL + "/repos/r/refs/sessions/session1")
	if err != nil {
		t.Fatal(err)
	}
	defer getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET ref: status %d", getResp.StatusCode)
	}
	var result map[string]string
	_ = json.NewDecoder(getResp.Body).Decode(&result)
	if result["hash"] != h {
		t.Errorf("ref hash mismatch: got %q, want %q", result["hash"], h)
	}
}

func TestPutRef_CAS(t *testing.T) {
	_, ts := newTestServer(t)

	h1 := postBlob(t, ts, "r", []byte("v1"))
	h2 := postBlob(t, ts, "r", []byte("v2"))

	// Create the ref.
	resp := putRefHTTP(t, ts, "r", "sessions/sess", "", h1)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create ref: status %d", resp.StatusCode)
	}

	// Advance with correct expected hash.
	resp2 := putRefHTTP(t, ts, "r", "sessions/sess", h1, h2)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp2.Body)
		t.Fatalf("advance ref: status %d, body: %s", resp2.StatusCode, body)
	}
}

func TestPutRef_CASConflict(t *testing.T) {
	_, ts := newTestServer(t)

	h1 := postBlob(t, ts, "r", []byte("v1"))
	h2 := postBlob(t, ts, "r", []byte("v2"))

	// Create the ref.
	resp := putRefHTTP(t, ts, "r", "sessions/sess", "", h1)
	resp.Body.Close()

	// Try to advance with wrong expected hash.
	resp2 := putRefHTTP(t, ts, "r", "sessions/sess", h2 /* wrong */, h2)
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusConflict {
		t.Errorf("expected 409, got %d", resp2.StatusCode)
	}
}

func TestPutRef_UnreachableObject(t *testing.T) {
	_, ts := newTestServer(t)

	// Don't push the blob — just try to set a ref to a non-existent hash.
	fakeHash := strings.Repeat("b", 64)
	resp := putRefHTTP(t, ts, "r", "sessions/s", "", fakeHash)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("expected 422, got %d", resp.StatusCode)
	}
}

func TestPutRef_StepWithMissingTree(t *testing.T) {
	_, ts := newTestServer(t)
	const repo = "myrepo"

	// Build a Step that references a tree we will NOT push.
	treeHash := store.Hash(strings.Repeat("c", 64))
	step := store.Step{
		Tree:           treeHash,
		SessionID:      "test-session",
		TimestampNanos: time.Now().UnixNano(),
	}
	stepData, _ := json.Marshal(step)

	// Push only the step blob, NOT the tree.
	stepHash := postBlob(t, ts, repo, stepData)

	resp := putRefHTTP(t, ts, repo, "sessions/test-session", "", stepHash)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnprocessableEntity {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("expected 422 (missing tree), got %d: %s", resp.StatusCode, body)
	}
}

func TestPutRef_StepWithReachableTree(t *testing.T) {
	_, ts := newTestServer(t)
	const repo = "myrepo"

	// Push a tree blob first.
	tree := store.Tree{Entries: []store.TreeEntry{{Path: "hello.txt", Blob: store.Hash(strings.Repeat("d", 64))}}}
	treeData, _ := json.Marshal(tree)
	treeHash := postBlob(t, ts, repo, treeData)

	// Push a step that references the tree.
	step := store.Step{
		Tree:           store.Hash(treeHash),
		SessionID:      "reachable-session",
		TimestampNanos: time.Now().UnixNano(),
	}
	stepData, _ := json.Marshal(step)
	stepHash := postBlob(t, ts, repo, stepData)

	// Ref advance should succeed because tree IS in the store.
	resp := putRefHTTP(t, ts, repo, "sessions/reachable-session", "", stepHash)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Errorf("expected 200, got %d: %s", resp.StatusCode, body)
	}
}

func TestPutRef_InvalidRefName(t *testing.T) {
	_, ts := newTestServer(t)
	h := postBlob(t, ts, "r", []byte("x"))

	badRefs := []string{
		"../evil",
		"./current",
		"sessions/../etc",
		strings.Repeat("a", 257),
	}
	for _, ref := range badRefs {
		resp := putRefHTTP(t, ts, "r", ref, "", h)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Errorf("PUT /repos/r/refs/%q: expected non-200, got 200", ref)
		}
	}
}

func TestGetRef_NotFound(t *testing.T) {
	_, ts := newTestServer(t)

	resp, err := http.Get(ts.URL + "/repos/r/refs/sessions/nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestObjectSizeLimit(t *testing.T) {
	_, ts := newTestServer(t)

	// Build a body slightly over maxObjectSize.
	big := make([]byte, maxObjectSize+1)
	resp, err := http.Post(ts.URL+"/repos/r/objects", "application/octet-stream", bytes.NewReader(big))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413, got %d", resp.StatusCode)
	}
}

func TestRootView(t *testing.T) {
	_, ts := newTestServer(t)

	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /: status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("re_gent demo server")) {
		t.Errorf("root page missing expected heading")
	}
}

func TestRepoView(t *testing.T) {
	_, ts := newTestServer(t)

	// Create a repo by pushing an object.
	postBlob(t, ts, "viewrepo", []byte("seed"))

	resp, err := http.Get(ts.URL + "/repos/viewrepo/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /repos/viewrepo/: status %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("viewrepo")) {
		t.Errorf("repo page missing repo name")
	}
}

func TestPostObjectIdempotent(t *testing.T) {
	_, ts := newTestServer(t)

	data := []byte("idempotent blob")
	h1 := postBlob(t, ts, "r", data)
	h2 := postBlob(t, ts, "r", data)

	if h1 != h2 {
		t.Errorf("identical content produced different hashes: %s vs %s", h1, h2)
	}
}

// TestConcurrentPushes verifies that concurrent object uploads from multiple
// goroutines do not corrupt the store (race-detected by -race flag).
func TestConcurrentPushes(t *testing.T) {
	_, ts := newTestServer(t)
	const (
		goroutines = 8
		objects    = 10
	)

	var wg sync.WaitGroup
	errCh := make(chan error, goroutines*objects)

	for g := 0; g < goroutines; g++ {
		g := g
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < objects; i++ {
				data := []byte(fmt.Sprintf("goroutine-%d-object-%d", g, i))
				resp, err := http.Post(
					ts.URL+"/repos/concurrent/objects",
					"application/octet-stream",
					bytes.NewReader(data),
				)
				if err != nil {
					errCh <- fmt.Errorf("g%d i%d POST: %w", g, i, err)
					continue
				}
				resp.Body.Close()
				if resp.StatusCode != http.StatusCreated {
					errCh <- fmt.Errorf("g%d i%d POST: status %d", g, i, resp.StatusCode)
				}
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Error(err)
	}
}

// TestConcurrentRefUpdates verifies CAS serializes concurrent ref writes correctly.
// Exactly one goroutine at a time wins; others get 409 Conflict.
func TestConcurrentRefUpdates(t *testing.T) {
	_, ts := newTestServer(t)
	const repo = "cas-test"
	const goroutines = 6

	// Push one blob per goroutine.
	hashes := make([]string, goroutines)
	for i := range hashes {
		hashes[i] = postBlob(t, ts, repo, []byte(fmt.Sprintf("version-%d", i)))
	}

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		current = "" // current expected hash
		wins    int
	)

	for i := 0; i < goroutines; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			mu.Lock()
			exp := current
			mu.Unlock()

			resp := putRefHTTP(t, ts, repo, "sessions/shared", exp, hashes[i])
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()

			if resp.StatusCode == http.StatusOK {
				mu.Lock()
				current = hashes[i]
				wins++
				mu.Unlock()
			} else if resp.StatusCode != http.StatusConflict {
				t.Errorf("goroutine %d: unexpected status %d: %s", i, resp.StatusCode, body)
			}
		}()
	}

	wg.Wait()

	if wins == 0 {
		t.Error("expected at least one successful ref update")
	}
}
