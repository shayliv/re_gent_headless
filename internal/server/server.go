// Package server provides the regent demo HTTP object/ref store.
// It exposes a content-addressed push/pull API and a minimal web UI
// (multi-repo, under /repos/{repo}/...), and additionally a single-repo
// http.Handler implementing the rgt remote protocol (/objects, /refs)
// used by `rgt push` and its tests.
// Each repository is stored on disk using internal/store verbatim.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/regent-vcs/regent/internal/store"
)

const (
	maxObjectSize = 50 << 20 // 50 MB
	maxRefBody    = 4 << 10  // 4 KB for ref update JSON
)

var (
	// repoNameRE restricts repo names to safe filesystem tokens (no slashes, no traversal).
	repoNameRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,62}[a-zA-Z0-9]$|^[a-zA-Z0-9]$`)

	// hashRE is a valid BLAKE3 hex hash (64 lowercase hex chars).
	hashRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

	// refSegRE validates a single segment of a ref path.
	refSegRE = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9:._-]*$`)
)

// putRefRequest is the body of PUT /repos/{repo}/refs/{ref...}.
type putRefRequest struct {
	Expected string `json:"expected"` // current hash, or "" for a new ref
	New      string `json:"new"`      // desired new hash
}

// sessionInfo is used by the repo web-view template.
type sessionInfo struct {
	Name string
	Head string
	Time string
}

// Server is the regent demo HTTP server.
// Each named repository is backed by its own store.Store in dataDir/repos/{name}/.
type Server struct {
	dataDir string
	logger  *slog.Logger
	mux     *http.ServeMux

	mu     sync.RWMutex
	stores map[string]*store.Store
}

// New creates a Server rooted at dataDir.
// dataDir must not be inside the user's source repository.
func New(dataDir string, logger *slog.Logger) (*Server, error) {
	if err := os.MkdirAll(filepath.Join(dataDir, "repos"), 0o755); err != nil {
		return nil, fmt.Errorf("create server data dir: %w", err)
	}

	if logger == nil {
		logger = slog.Default()
	}

	s := &Server{
		dataDir: dataDir,
		logger:  logger,
		stores:  make(map[string]*store.Store),
	}

	mux := http.NewServeMux()
	// API endpoints — more specific patterns are registered first.
	mux.HandleFunc("POST /repos/{repo}/objects", s.postObject)
	mux.HandleFunc("GET /repos/{repo}/objects/{hash}", s.getObject)
	mux.HandleFunc("GET /repos/{repo}/refs/{ref...}", s.getRef)
	mux.HandleFunc("PUT /repos/{repo}/refs/{ref...}", s.putRef)
	// Web views
	mux.HandleFunc("GET /repos/{repo}/", s.repoView)
	mux.HandleFunc("GET /repos/{repo}", s.repoView)
	mux.HandleFunc("GET /", s.rootView)

	s.mux = mux
	return s, nil
}

// ServeHTTP implements http.Handler.
// Requests with '.' or '..' path segments are rejected before the mux can clean them.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if hasTraversalSegment(r.URL.Path) {
		writeError(w, http.StatusBadRequest, "path contains traversal sequences ('.' or '..')")
		return
	}
	s.mux.ServeHTTP(w, r)
}

// hasTraversalSegment reports whether any segment of a URL path is "." or "..".
func hasTraversalSegment(path string) bool {
	for _, seg := range strings.Split(path, "/") {
		if seg == "." || seg == ".." {
			return true
		}
	}
	return false
}

// ---- repo store management ----

// repoStore returns the store for repo, creating it on first use.
func (s *Server) repoStore(name string) (*store.Store, error) {
	s.mu.RLock()
	st, ok := s.stores[name]
	s.mu.RUnlock()
	if ok {
		return st, nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Double-check after acquiring write lock.
	if st, ok = s.stores[name]; ok {
		return st, nil
	}

	repoDir := filepath.Join(s.dataDir, "repos", name)
	if err := os.MkdirAll(repoDir, 0o755); err != nil {
		return nil, fmt.Errorf("create repo dir: %w", err)
	}
	st, err := store.Open(repoDir)
	if err != nil {
		return nil, fmt.Errorf("open repo store: %w", err)
	}
	s.stores[name] = st
	return st, nil
}

// listRepos returns the names of all on-disk repos, filtered to valid names only.
func (s *Server) listRepos() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(s.dataDir, "repos"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var repos []string
	for _, e := range entries {
		if e.IsDir() && repoNameRE.MatchString(e.Name()) {
			repos = append(repos, e.Name())
		}
	}
	return repos, nil
}

// ---- validation ----

func validateRepo(name string) error {
	if name == "" {
		return fmt.Errorf("repo name is required")
	}
	if len(name) > 64 {
		return fmt.Errorf("repo name too long (max 64 characters)")
	}
	if !repoNameRE.MatchString(name) {
		return fmt.Errorf("invalid repo name %q: use letters, digits, dots, hyphens, underscores only", name)
	}
	return nil
}

func validateHash(h string) error {
	if !hashRE.MatchString(h) {
		return fmt.Errorf("invalid hash: must be exactly 64 lowercase hex characters")
	}
	return nil
}

// validateRef rejects ref names that contain path-traversal sequences or invalid characters.
func validateRef(name string) error {
	if name == "" {
		return fmt.Errorf("ref name is required")
	}
	if len(name) > 256 {
		return fmt.Errorf("ref name too long (max 256 characters)")
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("invalid ref segment %q: empty, '.', or '..' not allowed", seg)
		}
		if !refSegRE.MatchString(seg) {
			return fmt.Errorf("invalid ref segment %q: use letters, digits, ':', '.', '_', '-' only", seg)
		}
	}
	return nil
}

// ---- reachability ----

// verifyReachable checks that h exists in st and, if it parses as a Step,
// that the referenced Tree also exists.  This prevents advancing a ref to a
// step whose tree was never pushed.
func verifyReachable(st *store.Store, h store.Hash) error {
	data, err := st.ReadBlob(h)
	if err != nil {
		return fmt.Errorf("object %s not found", h)
	}

	var step store.Step
	if jsonErr := json.Unmarshal(data, &step); jsonErr != nil {
		// Not JSON — a raw blob ref is acceptable.
		return nil
	}

	if step.Tree == "" {
		// A Step without a tree is unusual but harmless.
		return nil
	}

	if !st.ObjectExists(step.Tree) {
		return fmt.Errorf("step %s references tree %s which has not been pushed", h, step.Tree)
	}

	return nil
}

// ---- response helpers ----

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// ---- HTTP handlers ----

// postObject handles POST /repos/{repo}/objects.
// The request body is the raw object bytes; the server returns the BLAKE3 hash.
func (s *Server) postObject(w http.ResponseWriter, r *http.Request) {
	repo := r.PathValue("repo")
	if err := validateRepo(repo); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	st, err := s.repoStore(repo)
	if err != nil {
		s.logger.Error("open repo", "repo", repo, "err", err)
		writeError(w, http.StatusInternalServerError, "could not open repository")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxObjectSize)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("object too large (max %d MB)", maxObjectSize>>20))
		return
	}

	h, err := st.WriteBlob(data)
	if err != nil {
		s.logger.Error("write blob", "repo", repo, "err", err)
		writeError(w, http.StatusInternalServerError, "write failed")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"hash": string(h)})
}

// getObject handles GET /repos/{repo}/objects/{hash}.
func (s *Server) getObject(w http.ResponseWriter, r *http.Request) {
	repo := r.PathValue("repo")
	hash := r.PathValue("hash")

	if err := validateRepo(repo); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateHash(hash); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	st, err := s.repoStore(repo)
	if err != nil {
		s.logger.Error("open repo", "repo", repo, "err", err)
		writeError(w, http.StatusInternalServerError, "could not open repository")
		return
	}

	data, err := st.ReadBlob(store.Hash(hash))
	if err != nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	_, _ = w.Write(data)
}

// getRef handles GET /repos/{repo}/refs/{ref...}.
func (s *Server) getRef(w http.ResponseWriter, r *http.Request) {
	repo := r.PathValue("repo")
	ref := r.PathValue("ref")

	if err := validateRepo(repo); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateRef(ref); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	st, err := s.repoStore(repo)
	if err != nil {
		s.logger.Error("open repo", "repo", repo, "err", err)
		writeError(w, http.StatusInternalServerError, "could not open repository")
		return
	}

	h, err := st.ReadRef(ref)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		writeError(w, http.StatusInternalServerError, "read ref failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"hash": string(h)})
}

// putRef handles PUT /repos/{repo}/refs/{ref...}.
// Body: {"expected":"<current-hash-or-empty>","new":"<new-hash>"}.
// Enforces CAS and verifies reachability before advancing the ref.
func (s *Server) putRef(w http.ResponseWriter, r *http.Request) {
	repo := r.PathValue("repo")
	ref := r.PathValue("ref")

	if err := validateRepo(repo); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := validateRef(ref); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxRefBody)
	var req putRefRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if err := validateHash(req.New); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("field 'new': %s", err))
		return
	}
	if req.Expected != "" {
		if err := validateHash(req.Expected); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("field 'expected': %s", err))
			return
		}
	}

	st, err := s.repoStore(repo)
	if err != nil {
		s.logger.Error("open repo", "repo", repo, "err", err)
		writeError(w, http.StatusInternalServerError, "could not open repository")
		return
	}

	// Verify all referenced objects exist before touching the ref.
	if err := verifyReachable(st, store.Hash(req.New)); err != nil {
		writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	if err := st.UpdateRef(ref, store.Hash(req.Expected), store.Hash(req.New)); err != nil {
		if errors.Is(err, store.ErrRefConflict) {
			writeError(w, http.StatusConflict, "ref was modified concurrently; read the current value and retry")
			return
		}
		s.logger.Error("update ref", "repo", repo, "ref", ref, "err", err)
		writeError(w, http.StatusInternalServerError, "ref update failed")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"hash": req.New})
}

// ---- web views ----

var rootTmpl = template.Must(template.New("root").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>re_gent demo server</title>
<style>
body{font-family:monospace;max-width:800px;margin:2rem auto;padding:0 1rem}
h1{border-bottom:1px solid #ccc;padding-bottom:.5rem}
ul{list-style:none;padding:0}
li{padding:.25rem 0}
a{color:#0066cc}
</style>
</head>
<body>
<h1>re_gent demo server</h1>
{{- if .Repos}}
<h2>Repositories</h2>
<ul>
{{- range .Repos}}
<li><a href="/repos/{{.}}/">{{.}}</a></li>
{{- end}}
</ul>
{{- else}}
<p>No repositories yet.</p>
<p>Push objects with <code>POST /repos/{name}/objects</code> to create one.</p>
{{- end}}
</body>
</html>
`))

var repoTmpl = template.Must(template.New("repo").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>{{.Repo}} — re_gent</title>
<style>
body{font-family:monospace;max-width:900px;margin:2rem auto;padding:0 1rem}
h1{border-bottom:1px solid #ccc;padding-bottom:.5rem}
table{border-collapse:collapse;width:100%}
th,td{text-align:left;padding:.4rem .6rem;border-bottom:1px solid #eee}
th{background:#f5f5f5}
code{background:#f5f5f5;padding:.1rem .3rem;border-radius:3px;font-size:.9em}
a{color:#0066cc}
.empty{color:#888}
</style>
</head>
<body>
<h1>{{.Repo}}</h1>
<p><a href="/">&#8592; all repos</a></p>
{{- if .Sessions}}
<h2>Sessions</h2>
<table>
<tr><th>Ref</th><th>Head step</th><th>Last seen</th></tr>
{{- range .Sessions}}
<tr>
  <td>{{.Name}}</td>
  <td><code>{{.Head}}</code></td>
  <td>{{.Time}}</td>
</tr>
{{- end}}
</table>
{{- else}}
<p class="empty">No sessions yet.</p>
{{- end}}
</body>
</html>
`))

func (s *Server) rootView(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	repos, err := s.listRepos()
	if err != nil {
		s.logger.Error("list repos", "err", err)
		repos = nil
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = rootTmpl.Execute(w, struct{ Repos []string }{Repos: repos})
}

func (s *Server) repoView(w http.ResponseWriter, r *http.Request) {
	repo := r.PathValue("repo")
	if err := validateRepo(repo); err != nil {
		http.Error(w, "invalid repo name", http.StatusBadRequest)
		return
	}

	st, err := s.repoStore(repo)
	if err != nil {
		s.logger.Error("open repo", "repo", repo, "err", err)
		http.Error(w, "could not open repository", http.StatusInternalServerError)
		return
	}

	refs, err := st.ListRefs("sessions")
	if err != nil {
		s.logger.Error("list refs", "repo", repo, "err", err)
		refs = map[string]store.Hash{}
	}

	sessions := make([]sessionInfo, 0, len(refs))
	for name, h := range refs {
		headShort := string(h)
		if len(headShort) > 12 {
			headShort = headShort[:12]
		}
		info := sessionInfo{Name: name, Head: headShort}
		if step, stepErr := st.ReadStep(h); stepErr == nil && step.TimestampNanos > 0 {
			info.Time = time.Unix(0, step.TimestampNanos).UTC().Format("2006-01-02 15:04:05Z")
		}
		sessions = append(sessions, info)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = repoTmpl.Execute(w, struct {
		Repo     string
		Sessions []sessionInfo
	}{Repo: repo, Sessions: sessions})
}

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
