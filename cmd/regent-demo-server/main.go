// Command regent-demo-server is a minimal, self-hosted demo of the re_gent
// server: it stores captured objects and session refs pushed from local
// .regent stores and renders how different agents' changes are recorded.
//
// It reuses the exact same content-addressed engine (internal/store) as the CLI,
// so a blob uploaded here hashes identically and the step DAG stays valid.
//
// This is a demo/proof-of-concept for the client-server milestone, not the final
// production server.
package main

import (
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/regent-vcs/regent/internal/store"
)

type server struct {
	dataDir string

	mu     sync.Mutex
	stores map[string]*store.Store // repo name -> per-repo store
}

func main() {
	addr := flag.String("addr", ":8099", "listen address")
	data := flag.String("data", "serverdata", "directory holding per-repo stores")
	flag.Parse()

	abs, err := filepath.Abs(*data)
	if err != nil {
		log.Fatalf("resolve data dir: %v", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}

	s := &server{dataDir: abs, stores: map[string]*store.Store{}}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	mux.HandleFunc("PUT /repos/{repo}/objects/{hash}", s.handlePutObject)
	mux.HandleFunc("GET /repos/{repo}/objects/{hash}", s.handleHasObject)
	mux.HandleFunc("PUT /repos/{repo}/refs/{name...}", s.handlePutRef)
	mux.HandleFunc("GET /", s.handleIndex)

	log.Printf("regent-demo-server listening on %s (data: %s)", *addr, abs)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		log.Fatal(err)
	}
}

// repoStore returns (creating if needed) the per-repo store under dataDir.
func (s *server) repoStore(repo string) (*store.Store, error) {
	if !safeName(repo) {
		return nil, fmt.Errorf("invalid repo name %q", repo)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if st, ok := s.stores[repo]; ok {
		return st, nil
	}
	base := filepath.Join(s.dataDir, repo)
	regentDir := filepath.Join(base, ".regent")

	var st *store.Store
	var err error
	if _, statErr := os.Stat(regentDir); statErr == nil {
		st, err = store.Open(regentDir)
	} else {
		st, err = store.Init(base)
	}
	if err != nil {
		return nil, err
	}
	s.stores[repo] = st
	return st, nil
}

func (s *server) handlePutObject(w http.ResponseWriter, r *http.Request) {
	repo := r.PathValue("repo")
	hash := r.PathValue("hash")
	st, err := s.repoStore(repo)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	got, err := st.WriteBlob(body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Content addressing must hold: the hash we stored under equals the client's.
	if string(got) != hash {
		http.Error(w, fmt.Sprintf("hash mismatch: client=%s server=%s", hash, got), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusCreated)
}

func (s *server) handleHasObject(w http.ResponseWriter, r *http.Request) {
	st, err := s.repoStore(r.PathValue("repo"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if st.ObjectExists(store.Hash(r.PathValue("hash"))) {
		w.WriteHeader(http.StatusOK)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func (s *server) handlePutRef(w http.ResponseWriter, r *http.Request) {
	repo := r.PathValue("repo")
	name := r.PathValue("name") // e.g. sessions/claude_code--s1
	st, err := s.repoStore(repo)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	newHash := store.Hash(strings.TrimSpace(string(body)))
	if newHash == "" {
		http.Error(w, "empty ref value", http.StatusBadRequest)
		return
	}
	current, _ := st.ReadRef(name) // "" if absent
	if err := st.UpdateRefWithRetry(name, current, newHash, 8); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	w.WriteHeader(http.StatusOK)
}

// --- view ---

type stepView struct {
	Short   string
	Origin  string
	TurnID  string
	Tools   string
	Changed []string
}

type sessionView struct {
	Name   string
	Origin string
	Steps  []stepView
}

type repoView struct {
	Name     string
	Sessions []sessionView
}

func (s *server) handleIndex(w http.ResponseWriter, _ *http.Request) {
	repos, err := s.collectRepos()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := indexTmpl.Execute(w, repos); err != nil {
		log.Printf("render index: %v", err)
	}
}

func (s *server) collectRepos() ([]repoView, error) {
	entries, err := os.ReadDir(s.dataDir)
	if err != nil {
		return nil, err
	}
	var repos []repoView
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rv, err := s.repoView(e.Name())
		if err != nil {
			log.Printf("repo %s: %v", e.Name(), err)
			continue
		}
		repos = append(repos, rv)
	}
	sort.Slice(repos, func(i, j int) bool { return repos[i].Name < repos[j].Name })
	return repos, nil
}

func (s *server) repoView(repo string) (repoView, error) {
	st, err := s.repoStore(repo)
	if err != nil {
		return repoView{}, err
	}
	refs, err := st.ListRefs("sessions")
	if err != nil {
		return repoView{}, err
	}
	rv := repoView{Name: repo}
	for name, tip := range refs {
		sv := sessionView{Name: name}
		// Walk the step chain from the tip toward the root.
		for h := tip; h != ""; {
			step, err := st.ReadStep(h)
			if err != nil {
				break
			}
			if sv.Origin == "" {
				sv.Origin = step.Origin
			}
			sv.Steps = append(sv.Steps, stepView{
				Short:   short(string(h)),
				Origin:  step.Origin,
				TurnID:  step.TurnID,
				Tools:   toolNames(step),
				Changed: changedFiles(st, step),
			})
			h = step.Parent
		}
		rv.Sessions = append(rv.Sessions, sv)
	}
	sort.Slice(rv.Sessions, func(i, j int) bool { return rv.Sessions[i].Name < rv.Sessions[j].Name })
	return rv, nil
}

func toolNames(step *store.Step) string {
	var names []string
	for _, c := range step.Causes {
		names = append(names, c.ToolName)
	}
	if len(names) == 0 && step.Cause.ToolName != "" {
		names = append(names, step.Cause.ToolName)
	}
	return strings.Join(names, ", ")
}

// changedFiles returns the paths whose blob differs from the parent step's tree.
func changedFiles(st *store.Store, step *store.Step) []string {
	cur, err := st.ReadTree(step.Tree)
	if err != nil {
		return nil
	}
	parentBlobs := map[string]store.Hash{}
	if step.Parent != "" {
		if ps, err := st.ReadStep(step.Parent); err == nil {
			if pt, err := st.ReadTree(ps.Tree); err == nil {
				for _, e := range pt.Entries {
					parentBlobs[e.Path] = e.Blob
				}
			}
		}
	}
	var changed []string
	for _, e := range cur.Entries {
		if parentBlobs[e.Path] != e.Blob {
			changed = append(changed, e.Path)
		}
	}
	sort.Strings(changed)
	return changed
}

func short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

func safeName(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '-' || r == '.':
		default:
			return false
		}
	}
	return true
}

var indexTmpl = template.Must(template.New("index").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><title>re_gent demo server</title>
<style>
 body{font:14px/1.5 -apple-system,Segoe UI,sans-serif;margin:2rem;color:#111;background:#fafafa}
 h1{font-size:1.4rem} h2{margin-top:1.5rem;border-bottom:1px solid #ddd;padding-bottom:.25rem}
 .session{margin:.75rem 0;padding:.75rem 1rem;background:#fff;border:1px solid #e3e3e3;border-radius:8px}
 .origin{display:inline-block;font-size:.75rem;padding:.1rem .5rem;border-radius:99px;background:#eef;color:#334;margin-left:.5rem}
 .step{margin:.35rem 0;padding-left:1rem;border-left:3px solid #cdd}
 code{background:#f0f0f0;padding:.05rem .3rem;border-radius:4px}
 .files{color:#555;font-size:.85rem}
 .empty{color:#999}
</style></head><body>
<h1>re_gent demo server</h1>
{{if not .}}<p class="empty">No repositories pushed yet.</p>{{end}}
{{range .}}
 <h2>{{.Name}}</h2>
 {{if not .Sessions}}<p class="empty">No sessions.</p>{{end}}
 {{range .Sessions}}
  <div class="session">
   <strong>{{.Name}}</strong><span class="origin">{{.Origin}}</span>
   {{range .Steps}}
    <div class="step">
     <code>{{.Short}}</code> turn <code>{{.TurnID}}</code> &middot; {{.Tools}}
     <div class="files">{{range .Changed}}{{.}} {{else}}<span class="empty">(no file changes)</span>{{end}}</div>
    </div>
   {{end}}
  </div>
 {{end}}
{{end}}
</body></html>`))
