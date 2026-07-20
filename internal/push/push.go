// Package push implements the rgt push algorithm: collect all objects
// reachable from local session refs, upload the ones the remote lacks, then
// advance each remote session ref.
//
// Invariant (proven by construction): a ref is never advanced to a step whose
// objects are not all present on the remote.  All object uploads are confirmed
// before any ref update is attempted.
package push

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"

	"github.com/regent-vcs/regent/internal/remote"
	"github.com/regent-vcs/regent/internal/store"
)

// Result summarises what happened during a push.
type Result struct {
	// Uploaded is the number of objects sent to the remote.
	Uploaded int
	// RefsUpdated is the list of ref names whose tips were advanced.
	RefsUpdated []string
	// RefsSkipped is the list of ref names that were already up-to-date.
	RefsSkipped []string
}

// Push uploads all objects reachable from local session refs to r, then
// advances each session ref on the remote.
//
// Steps are uploaded oldest-ancestor-first so that, if the push is
// interrupted, the remote never holds a ref to a step whose ancestors are
// missing.  Ref updates happen only after ALL object uploads are complete.
func Push(local *store.Store, r remote.Remote) (Result, error) {
	// --- 1. Discover local session refs ---
	localRefs, err := local.ListRefs("sessions")
	if err != nil {
		return Result{}, fmt.Errorf("list local session refs: %w", err)
	}
	if len(localRefs) == 0 {
		return Result{}, nil
	}

	// --- 2. Collect objects needed per ref ---
	type refWork struct {
		refName  string     // full ref name, e.g. "sessions/my-session"
		localTip store.Hash // what the local ref points at
		toSend   []objectPayload
	}

	works := make([]refWork, 0, len(localRefs))
	for name, tip := range localRefs {
		refName := "sessions/" + name
		remoteTip, err := r.GetRef(refName)
		if err != nil {
			return Result{}, fmt.Errorf("get remote ref %s: %w", refName, err)
		}

		objs, err := collectObjects(local, r, tip, remoteTip)
		if err != nil {
			return Result{}, fmt.Errorf("collect objects for %s: %w", refName, err)
		}
		works = append(works, refWork{refName: refName, localTip: tip, toSend: objs})
	}

	// --- 3. Upload all missing objects (leaves first, across all refs) ---
	// Deduplicate so a blob shared by two refs is only sent once.
	uploaded := 0
	sentSet := make(map[store.Hash]struct{})
	for i := range works {
		for _, obj := range works[i].toSend {
			if _, done := sentSet[obj.hash]; done {
				continue
			}
			sentSet[obj.hash] = struct{}{}
			if err := r.SendObject(obj.hash, obj.data); err != nil {
				return Result{}, fmt.Errorf("send object %s: %w", obj.hash, err)
			}
			uploaded++
		}
	}

	// --- 4. Advance remote refs ONLY after all objects are confirmed sent ---
	var updated, skipped []string
	for _, w := range works {
		remoteTip, err := r.GetRef(w.refName)
		if err != nil {
			return Result{}, fmt.Errorf("re-read remote ref %s: %w", w.refName, err)
		}
		if remoteTip == w.localTip {
			skipped = append(skipped, w.refName)
			continue
		}
		if err := r.UpdateRef(w.refName, remoteTip, w.localTip); err != nil {
			return Result{}, fmt.Errorf("update remote ref %s: %w", w.refName, err)
		}
		updated = append(updated, w.refName)
	}

	return Result{
		Uploaded:    uploaded,
		RefsUpdated: updated,
		RefsSkipped: skipped,
	}, nil
}

// objectPayload pairs a hash with its raw bytes.
type objectPayload struct {
	hash store.Hash
	data []byte
}

// collectObjects returns the ordered list of objects that the remote is
// missing, starting from localTip down to (but not including) remoteBase.
// Order is leaf-first: file blobs → tree → cause blobs → transcript nodes →
// step.  Within a step chain, the oldest ancestor is fully processed before
// newer steps, ensuring every uploaded step can be reconstructed from objects
// already present on the remote.
func collectObjects(local *store.Store, r remote.Remote, localTip, remoteBase store.Hash) ([]objectPayload, error) {
	if localTip == "" || localTip == remoteBase {
		return nil, nil
	}

	// Walk ancestor chain newest-first, stopping when we reach a step the
	// remote already has (or remoteBase).
	var stepChain []store.Hash // newest first
	cur := localTip
	for cur != "" && cur != remoteBase {
		has, err := r.HasObject(cur)
		if err != nil {
			return nil, fmt.Errorf("HasObject %s: %w", cur, err)
		}
		if has {
			// Remote has this step and, by our invariant, all its ancestors.
			break
		}
		stepChain = append(stepChain, cur)
		step, err := local.ReadStep(cur)
		if err != nil {
			return nil, fmt.Errorf("read step %s: %w", cur, err)
		}
		cur = step.Parent
	}

	// Build payloads oldest-ancestor-first.
	var payloads []objectPayload
	haveSent := make(map[store.Hash]struct{})

	addBlob := func(h store.Hash) error {
		if h == "" {
			return nil
		}
		if _, ok := haveSent[h]; ok {
			return nil
		}
		has, err := r.HasObject(h)
		if err != nil {
			return fmt.Errorf("HasObject %s: %w", h, err)
		}
		haveSent[h] = struct{}{}
		if has {
			return nil
		}
		data, err := local.ReadBlob(h)
		if err != nil {
			return fmt.Errorf("read blob %s: %w", h, err)
		}
		payloads = append(payloads, objectPayload{hash: h, data: data})
		return nil
	}

	// Process steps oldest-first (reverse the newest-first slice).
	for i := len(stepChain) - 1; i >= 0; i-- {
		sh := stepChain[i]
		step, err := local.ReadStep(sh)
		if err != nil {
			return nil, fmt.Errorf("read step %s: %w", sh, err)
		}

		// 1. File blobs (leaves of the tree).
		if step.Tree != "" {
			tree, err := local.ReadTree(step.Tree)
			if err != nil {
				return nil, fmt.Errorf("read tree %s: %w", step.Tree, err)
			}
			for _, e := range tree.Entries {
				if err := addBlob(e.Blob); err != nil {
					return nil, err
				}
			}
		}

		// 2. Cause blobs.
		for _, c := range step.Causes {
			if err := addBlob(c.ArgsBlob); err != nil {
				return nil, err
			}
			if err := addBlob(c.ResultBlob); err != nil {
				return nil, err
			}
		}
		// Legacy single cause.
		if err := addBlob(step.Cause.ArgsBlob); err != nil {
			return nil, err
		}
		if err := addBlob(step.Cause.ResultBlob); err != nil {
			return nil, err
		}

		// 3. Config blob.
		if err := addBlob(step.Config); err != nil {
			return nil, err
		}

		// 4. Transcript chain.
		if err := addTranscriptChain(local, r, step.Transcript, addBlob, haveSent); err != nil {
			return nil, err
		}

		// 5. Tree blob (after its file blobs).
		if err := addBlob(step.Tree); err != nil {
			return nil, err
		}

		// 6. Step blob itself (last, after everything it references).
		stepData, err := local.ReadBlob(sh)
		if err != nil {
			return nil, fmt.Errorf("read step blob %s: %w", sh, err)
		}
		if _, ok := haveSent[sh]; !ok {
			haveSent[sh] = struct{}{}
			payloads = append(payloads, objectPayload{hash: sh, data: stepData})
		}
	}

	return payloads, nil
}

// addTranscriptChain uploads all transcript nodes and their message blobs.
func addTranscriptChain(
	local *store.Store,
	_ remote.Remote,
	head store.Hash,
	addBlob func(store.Hash) error,
	chainSeen map[store.Hash]struct{},
) error {
	cur := head
	for cur != "" {
		if _, ok := chainSeen[cur]; ok {
			break
		}
		chainSeen[cur] = struct{}{}

		data, err := local.ReadBlob(cur)
		if err != nil {
			return fmt.Errorf("read transcript blob %s: %w", cur, err)
		}

		var t struct {
			Prev        string   `json:"prev"`
			NewMessages []string `json:"new_messages"`
		}
		if jsonErr := json.Unmarshal(data, &t); jsonErr != nil {
			// Not a transcript node — treat as opaque blob.
			return addBlob(cur)
		}
		for _, mh := range t.NewMessages {
			if err := addBlob(store.Hash(mh)); err != nil {
				return err
			}
		}
		// The transcript node itself.
		if err := addBlob(cur); err != nil {
			return err
		}
		cur = store.Hash(t.Prev)
	}
	return nil
}

// LocalRemote wraps a *store.Store as a Remote for use in unit tests
// without an HTTP server.
func LocalRemote(s *store.Store) remote.Remote {
	return &localStoreRemote{s: s}
}

type localStoreRemote struct {
	s *store.Store
}

func (l *localStoreRemote) HasObject(h store.Hash) (bool, error) {
	return l.s.ObjectExists(h), nil
}

func (l *localStoreRemote) SendObject(_ store.Hash, data []byte) error {
	_, err := l.s.WriteBlob(data)
	return err
}

func (l *localStoreRemote) GetRef(name string) (store.Hash, error) {
	h, err := l.s.ReadRef(name)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return h, nil
}

func (l *localStoreRemote) UpdateRef(name string, old, new store.Hash) error {
	return l.s.UpdateRef(name, old, new)
}

func (l *localStoreRemote) ListRefs(dir string) (map[string]store.Hash, error) {
	return l.s.ListRefs(dir)
}
