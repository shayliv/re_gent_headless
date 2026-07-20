package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/regent-vcs/regent/internal/store"
)

func makeStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Init(t.TempDir())
	if err != nil {
		t.Fatalf("store.Init: %v", err)
	}
	return s
}

func TestHandlerObjectHeadMissing(t *testing.T) {
	srv := httptest.NewServer(Handler(makeStore(t)))
	defer srv.Close()

	resp, err := http.Head(srv.URL + "/objects/aabbcc0000000000000000000000000000000000000000000000000000001234ab")
	if err != nil {
		t.Fatalf("HEAD: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestHandlerObjectPutAndHead(t *testing.T) {
	remStore := makeStore(t)
	remSrv := httptest.NewServer(Handler(remStore))
	defer remSrv.Close()

	// Write to a local store to get the canonical hash.
	local := makeStore(t)
	data := []byte("test object content")
	h, _ := local.WriteBlob(data)

	req, _ := http.NewRequest(http.MethodPut, remSrv.URL+"/objects/"+string(h), bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		t.Errorf("PUT: expected 201 or 200, got %d", resp.StatusCode)
	}

	// HEAD should now return 200.
	resp2, err := http.Head(remSrv.URL + "/objects/" + string(h))
	if err != nil {
		t.Fatalf("HEAD after PUT: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Errorf("HEAD after PUT: expected 200, got %d", resp2.StatusCode)
	}
}

func TestHandlerObjectPutIdempotent(t *testing.T) {
	local := makeStore(t)
	remStore := makeStore(t)
	remSrv := httptest.NewServer(Handler(remStore))
	defer remSrv.Close()

	data := []byte("idempotent content")
	blobHash, _ := local.WriteBlob(data)

	put := func() int {
		req, _ := http.NewRequest(http.MethodPut, remSrv.URL+"/objects/"+string(blobHash), bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/octet-stream")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("PUT: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if sc := put(); sc != http.StatusCreated && sc != http.StatusOK {
		t.Errorf("first PUT: unexpected status %d", sc)
	}
	if sc := put(); sc != http.StatusOK {
		t.Errorf("second PUT (idempotent): expected 200, got %d", sc)
	}
}

func TestHandlerRefGetSetCAS(t *testing.T) {
	s := makeStore(t)
	srv := httptest.NewServer(Handler(s))
	defer srv.Close()

	// GET non-existent ref → 404.
	resp, err := http.Get(srv.URL + "/refs/sessions/test-sess")
	if err != nil {
		t.Fatalf("GET ref: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}

	h, _ := s.WriteBlob([]byte("step-data"))

	postRef := func(url, old, newHash string) int {
		body, _ := json.Marshal(map[string]string{"old": old, "new": newHash})
		req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST ref: %v", err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	refURL := srv.URL + "/refs/sessions/test-sess"

	if sc := postRef(refURL, "", string(h)); sc != http.StatusOK {
		t.Errorf("create ref: expected 200, got %d", sc)
	}

	// GET the ref → should return the hash.
	resp2, err := http.Get(refURL)
	if err != nil {
		t.Fatalf("GET ref: %v", err)
	}
	defer resp2.Body.Close()
	body, _ := io.ReadAll(resp2.Body)
	var rr struct{ Hash string }
	_ = json.Unmarshal(body, &rr)
	if store.Hash(rr.Hash) != h {
		t.Errorf("GET ref: expected %s, got %s", h, rr.Hash)
	}

	// POST with wrong old value → 409.
	if sc := postRef(refURL, "wrongoldvalue", string(h)); sc != http.StatusConflict {
		t.Errorf("CAS conflict: expected 409, got %d", sc)
	}
}

func TestHandlerListRefs(t *testing.T) {
	s := makeStore(t)
	srv := httptest.NewServer(Handler(s))
	defer srv.Close()

	h, _ := s.WriteBlob([]byte("blob"))
	_ = s.UpdateRef("sessions/s1", "", h)

	resp, err := http.Get(srv.URL + "/refs?dir=sessions")
	if err != nil {
		t.Fatalf("GET /refs: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	var lr struct {
		Refs map[string]string `json:"refs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if lr.Refs["s1"] != string(h) {
		t.Errorf("expected ref s1=%s, got %s", h, lr.Refs["s1"])
	}
}
