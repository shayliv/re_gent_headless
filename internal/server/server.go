// Package server provides an HTTP handler that implements the rgt remote protocol.
// It is used in tests (via httptest) and can be embedded in a standalone server binary.
package server

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strings"

	"github.com/regent-vcs/regent/internal/store"
)

// Handler returns an http.Handler backed by s that implements the rgt remote
// protocol:
//
//	HEAD   /objects/{hash}         → 200 if exists, 404 otherwise
//	PUT    /objects/{hash}         → idempotent object upload
//	GET    /refs/{name...}         → {"hash":"..."} or 404
//	POST   /refs/{name...}         → CAS ref update
//	GET    /refs?dir={dir}         → {"refs":{"name":"hash",...}}
func Handler(s *store.Store) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/objects/", func(w http.ResponseWriter, r *http.Request) {
		handleObjects(w, r, s)
	})
	mux.HandleFunc("/refs", func(w http.ResponseWriter, r *http.Request) {
		handleRefsRoot(w, r, s)
	})
	mux.HandleFunc("/refs/", func(w http.ResponseWriter, r *http.Request) {
		handleNamedRef(w, r, s)
	})
	return mux
}

func handleObjects(w http.ResponseWriter, r *http.Request, s *store.Store) {
	hash := store.Hash(strings.TrimPrefix(r.URL.Path, "/objects/"))
	if hash == "" {
		http.Error(w, "missing hash", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodHead:
		if s.ObjectExists(hash) {
			w.WriteHeader(http.StatusOK)
		} else {
			http.NotFound(w, r)
		}

	case http.MethodPut:
		// Idempotent: if we already have it, skip the write.
		if s.ObjectExists(hash) {
			w.WriteHeader(http.StatusOK)
			return
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if _, err := s.WriteBlob(data); err != nil {
			http.Error(w, "write blob: "+err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func handleRefsRoot(w http.ResponseWriter, r *http.Request, s *store.Store) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	dir := r.URL.Query().Get("dir")
	refs, err := s.ListRefs(dir)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Convert to map[string]string for JSON
	out := make(map[string]string, len(refs))
	for k, v := range refs {
		out[k] = string(v)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Refs map[string]string `json:"refs"`
	}{Refs: out})
}

func handleNamedRef(w http.ResponseWriter, r *http.Request, s *store.Store) {
	name := strings.TrimPrefix(r.URL.Path, "/refs/")
	if name == "" {
		http.Error(w, "missing ref name", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		h, err := s.ReadRef(name)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			Hash string `json:"hash"`
		}{Hash: string(h)})

	case http.MethodPost:
		var req struct {
			Old string `json:"old"`
			New string `json:"new"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "decode body: "+err.Error(), http.StatusBadRequest)
			return
		}
		err := s.UpdateRef(name, store.Hash(req.Old), store.Hash(req.New))
		if err != nil {
			if errors.Is(err, store.ErrRefConflict) {
				current, _ := s.ReadRef(name)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				_ = json.NewEncoder(w).Encode(struct {
					Hash string `json:"hash"`
				}{Hash: string(current)})
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}
