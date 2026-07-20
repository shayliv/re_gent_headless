package capture

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/regent-vcs/regent/internal/remote"
	"github.com/regent-vcs/regent/internal/store"
)

// ─── shared sink contract ───────────────────────────────────────────────────

// sinkContractTest runs the same behavioural expectations against any CaptureSink.
func sinkContractTest(t *testing.T, newSink func() CaptureSink) {
	t.Helper()

	t.Run("EnqueueBlob_NonBlocking", func(t *testing.T) {
		s := newSink()
		defer func() { _ = s.Close() }()
		start := time.Now()
		for i := 0; i < 500; i++ {
			s.EnqueueBlob("aaaa", []byte("payload"))
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("EnqueueBlob blocked for %v", elapsed)
		}
	})

	t.Run("EnqueueRef_NonBlocking", func(t *testing.T) {
		s := newSink()
		defer func() { _ = s.Close() }()
		start := time.Now()
		for i := 0; i < 500; i++ {
			s.EnqueueRef("sessions/test", "", "bbbb")
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("EnqueueRef blocked for %v", elapsed)
		}
	})

	t.Run("Flush_ReturnsNil", func(t *testing.T) {
		s := newSink()
		defer func() { _ = s.Close() }()
		s.EnqueueBlob("cccc", []byte("x"))
		if err := s.Flush(); err != nil {
			t.Errorf("Flush() = %v, want nil", err)
		}
	})

	t.Run("Close_IsIdempotent", func(t *testing.T) {
		s := newSink()
		if err := s.Close(); err != nil {
			t.Fatalf("first Close() = %v", err)
		}
		if err := s.Close(); err != nil {
			t.Errorf("second Close() = %v", err)
		}
	})

	t.Run("EnqueueAfterClose_DoesNotPanic", func(t *testing.T) {
		s := newSink()
		_ = s.Close()
		// Must not panic or deadlock.
		s.EnqueueBlob("dddd", []byte("late"))
		s.EnqueueRef("sessions/late", "", "eeee")
	})
}

// ─── NoopSink ───────────────────────────────────────────────────────────────

func TestNoopSink(t *testing.T) {
	sinkContractTest(t, func() CaptureSink { return &NoopSink{} })
}

// ─── AsyncRemoteSink ────────────────────────────────────────────────────────

// testRemoteServer is a minimal in-memory HTTP server speaking the rgt remote
// protocol (HEAD/PUT /objects/{hash} and POST /refs/{name}).
type testRemoteServer struct {
	mu      sync.Mutex
	objects map[store.Hash][]byte
	refs    map[string]store.Hash
}

func newTestRemoteServer() *testRemoteServer {
	return &testRemoteServer{
		objects: make(map[store.Hash][]byte),
		refs:    make(map[string]store.Hash),
	}
}

func (s *testRemoteServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	const objPrefix = "/objects/"
	const refPrefix = "/refs/"
	switch {
	case len(r.URL.Path) > len(objPrefix) && r.URL.Path[:len(objPrefix)] == objPrefix:
		s.handleObject(w, r, store.Hash(r.URL.Path[len(objPrefix):]))
	case len(r.URL.Path) > len(refPrefix) && r.URL.Path[:len(refPrefix)] == refPrefix:
		s.handleRef(w, r, r.URL.Path[len(refPrefix):])
	default:
		http.NotFound(w, r)
	}
}

func (s *testRemoteServer) handleObject(w http.ResponseWriter, r *http.Request, hash store.Hash) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch r.Method {
	case http.MethodHead:
		if _, ok := s.objects[hash]; ok {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	case http.MethodPut:
		data, _ := io.ReadAll(r.Body)
		s.objects[hash] = data
		w.WriteHeader(http.StatusCreated)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (s *testRemoteServer) handleRef(w http.ResponseWriter, r *http.Request, name string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Old string `json:"old"`
		New string `json:"new"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	current := s.refs[name]
	if string(current) != req.Old {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"hash": string(current)})
		return
	}
	s.refs[name] = store.Hash(req.New)
	w.WriteHeader(http.StatusOK)
}

func (s *testRemoteServer) hasObject(h store.Hash) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.objects[h]
	return ok
}

func (s *testRemoteServer) getRef(name string) store.Hash {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.refs[name]
}

func newAsyncSinkWithServer(t *testing.T) (*testRemoteServer, *AsyncRemoteSink) {
	t.Helper()
	srv := newTestRemoteServer()
	hs := httptest.NewServer(srv)
	rem := remote.NewHTTP(hs.URL, nil)
	sink := NewAsyncRemoteSink(rem, "")
	t.Cleanup(func() {
		_ = sink.Close()
		hs.Close()
	})
	return srv, sink
}

func TestAsyncRemoteSink_Contract(t *testing.T) {
	sinkContractTest(t, func() CaptureSink {
		srv := newTestRemoteServer()
		hs := httptest.NewServer(srv)
		rem := remote.NewHTTP(hs.URL, nil)
		sink := NewAsyncRemoteSink(rem, "")
		t.Cleanup(func() {
			_ = sink.Close()
			hs.Close()
		})
		return sink
	})
}

func TestAsyncRemoteSink_ReplicatesBlobToServer(t *testing.T) {
	srv, sink := newAsyncSinkWithServer(t)

	data := []byte("hello regent")
	hash := store.Hash("aabbccdd")
	sink.EnqueueBlob(hash, data)
	if err := sink.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if !srv.hasObject(hash) {
		t.Error("blob not found on remote after Flush")
	}
}

func TestAsyncRemoteSink_ReplicatesRefToServer(t *testing.T) {
	srv, sink := newAsyncSinkWithServer(t)

	sink.EnqueueRef("sessions/testsession", "", "step1")
	if err := sink.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	if got := srv.getRef("sessions/testsession"); got != "step1" {
		t.Errorf("ref = %q, want %q", got, "step1")
	}
}

func TestAsyncRemoteSink_SkipsDuplicateBlob(t *testing.T) {
	srv, sink := newAsyncSinkWithServer(t)

	data := []byte("dedupe me")
	hash := store.Hash("deadbeef")
	sink.EnqueueBlob(hash, data)
	if err := sink.Flush(); err != nil {
		t.Fatalf("first flush: %v", err)
	}

	// Overwrite on the server with a sentinel to detect a spurious second PUT.
	srv.mu.Lock()
	srv.objects[hash] = []byte("server-modified")
	srv.mu.Unlock()

	sink.EnqueueBlob(hash, data)
	if err := sink.Flush(); err != nil {
		t.Fatalf("second flush: %v", err)
	}

	srv.mu.Lock()
	got := srv.objects[hash]
	srv.mu.Unlock()
	if string(got) != "server-modified" {
		t.Error("second EnqueueBlob overwrote an already-present object")
	}
}

// TestAsyncRemoteSink_ServerUnreachable verifies the agent turn is never blocked
// when the remote server is unavailable. This is the key acceptance criterion for RE-10.
func TestAsyncRemoteSink_ServerUnreachable(t *testing.T) {
	// Start a server then close it immediately to produce refused-connection errors.
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	hs.Close()

	rem := remote.NewHTTP(hs.URL, nil)
	sink := NewAsyncRemoteSink(rem, "")
	defer func() { _ = sink.Close() }()

	start := time.Now()
	for i := 0; i < 10; i++ {
		sink.EnqueueBlob("deadbeef", []byte("x"))
		sink.EnqueueRef("sessions/s", "", "h1")
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("Enqueue calls blocked for %v with unreachable server", elapsed)
	}

	// Flush may take time due to retries, but must not hang forever.
	done := make(chan struct{})
	go func() { _ = sink.Flush(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Flush hung with unreachable server")
	}
}

// ─── Recorder integration ───────────────────────────────────────────────────

func TestRecorder_SinkReceivesStepBlobAndRef(t *testing.T) {
	root := t.TempDir()
	if _, err := store.Init(root); err != nil {
		t.Fatalf("init: %v", err)
	}

	srv, sink := newAsyncSinkWithServer(t)

	rec, ok, err := Open(root)
	if err != nil || !ok {
		t.Fatalf("Open: %v %v", err, ok)
	}
	rec.Sink = sink
	defer func() { _ = rec.Close() }()

	meta := SessionMetadata{SessionID: "sess1", Origin: OriginCodexCLI}

	if err := rec.UpsertSession(meta); err != nil {
		t.Fatalf("UpsertSession: %v", err)
	}
	if err := rec.RecordUserPrompt(UserPrompt{SessionMetadata: meta, TurnID: "t1", Prompt: "hi"}); err != nil {
		t.Fatalf("RecordUserPrompt: %v", err)
	}
	toolInput := json.RawMessage(`{"command":"echo hi"}`)
	if err := rec.RecordToolUse(ToolUse{
		SessionMetadata: meta,
		TurnID:          "t1",
		ToolName:        "Bash",
		ToolUseID:       "u1",
		ToolInput:       toolInput,
		ToolResponse:    json.RawMessage(`"hi\n"`),
	}); err != nil {
		t.Fatalf("RecordToolUse: %v", err)
	}
	if err := rec.RecordAssistantAndFinalize(AssistantResponse{
		SessionMetadata:      meta,
		TurnID:               "t1",
		LastAssistantMessage: "done",
	}); err != nil {
		t.Fatalf("RecordAssistantAndFinalize: %v", err)
	}

	if err := sink.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	// Tool-input blob must have been replicated.
	inputHash, _ := rec.Store.WriteBlob(toolInput)
	if !srv.hasObject(inputHash) {
		t.Error("tool-input blob not replicated to remote")
	}

	// Session ref must have been replicated.
	sessionID := canonicalSessionID(OriginCodexCLI, "sess1")
	if srv.getRef("sessions/"+sessionID) == "" {
		t.Error("session ref not replicated to remote")
	}
}

// TestRecorder_ServerUnreachable_TurnStillSucceeds is the primary acceptance test:
// an unreachable remote must not prevent the agent turn from completing.
func TestRecorder_ServerUnreachable_TurnStillSucceeds(t *testing.T) {
	root := t.TempDir()
	if _, err := store.Init(root); err != nil {
		t.Fatalf("init: %v", err)
	}

	// Server that refuses connections immediately.
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	hs.Close()

	rec, ok, err := Open(root)
	if err != nil || !ok {
		t.Fatalf("Open: %v %v", err, ok)
	}
	rem := remote.NewHTTP(hs.URL, nil)
	rec.Sink = NewAsyncRemoteSink(rem, "")
	defer func() { _ = rec.Close() }()

	meta := SessionMetadata{SessionID: "sess-unreachable", Origin: OriginCodexCLI}
	_ = rec.UpsertSession(meta)
	_ = rec.RecordUserPrompt(UserPrompt{SessionMetadata: meta, TurnID: "t1", Prompt: "hi"})
	_ = rec.RecordToolUse(ToolUse{
		SessionMetadata: meta,
		TurnID:          "t1",
		ToolName:        "Bash",
		ToolUseID:       "u1",
		ToolInput:       json.RawMessage(`{"command":"echo hi"}`),
		ToolResponse:    json.RawMessage(`"ok"`),
	})

	start := time.Now()
	err = rec.RecordAssistantAndFinalize(AssistantResponse{
		SessionMetadata:      meta,
		TurnID:               "t1",
		LastAssistantMessage: "done",
	})
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("RecordAssistantAndFinalize blocked for %v with unreachable server", elapsed)
	}
	if err != nil {
		t.Errorf("RecordAssistantAndFinalize failed: %v (must succeed even with unreachable server)", err)
	}

	// Local step must still have been written.
	sessionID := canonicalSessionID(OriginCodexCLI, "sess-unreachable")
	steps, err := rec.Index.ListSteps(sessionID, 5)
	if err != nil {
		t.Fatalf("ListSteps: %v", err)
	}
	if len(steps) != 1 {
		t.Errorf("expected 1 local step, got %d", len(steps))
	}
}
