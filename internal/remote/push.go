package remote

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/regent-vcs/regent/internal/store"
)

// ErrDiverged means the server's ref points at a step that is not an ancestor
// of the local tip. Pushing would drop history the server already has, so the
// push is refused instead.
var ErrDiverged = errors.New("remote ref has diverged from local history")

// PushStats summarises one push.
type PushStats struct {
	ObjectsSent    int          // objects uploaded by this push
	ObjectsSkipped int          // objects this repo already had (deduped)
	BytesSent      int64        // total bytes uploaded
	RefsUpdated    int          // refs advanced on the server
	Missing        []store.Hash // referenced objects absent from the local store
}

// Push uploads everything reachable from each named local ref into the client's
// repo and then advances that ref on the server.
//
// Ordering matters: objects are uploaded first and the ref is moved last, so a
// failed push never leaves the server with a ref pointing at a history it does
// not hold. Dedupe is per repo — an object is skipped only when *this* repo
// already holds it, which is what keeps two repos on one server independent.
func Push(ctx context.Context, c *Client, st *store.Store, refNames []string) (PushStats, error) {
	var stats PushStats
	if len(refNames) == 0 {
		return stats, errors.New("push: no refs selected")
	}
	if _, err := c.EnsureRepo(ctx); err != nil {
		return stats, err
	}

	for _, name := range refNames {
		localTip, err := st.ReadRef(name)
		if err != nil {
			return stats, fmt.Errorf("read local ref %s: %w", name, err)
		}
		if localTip == "" {
			return stats, fmt.Errorf("local ref %s is empty", name)
		}

		remoteTip, err := c.GetRef(ctx, name)
		switch {
		case err == nil:
		case errors.Is(err, ErrRefNotFound), errors.Is(err, ErrRepoNotFound):
			remoteTip = ""
		default:
			return stats, err
		}

		if remoteTip != "" && remoteTip != localTip {
			ok, err := isAncestor(st, remoteTip, localTip)
			if err != nil {
				return stats, fmt.Errorf("check ancestry of %s: %w", name, err)
			}
			if !ok {
				return stats, fmt.Errorf("%w: %s is at %s", ErrDiverged, name, remoteTip)
			}
		}

		objects, missing, err := ReachableObjects(st, localTip)
		if err != nil {
			return stats, err
		}
		stats.Missing = append(stats.Missing, missing...)

		for _, h := range objects {
			has, err := c.HasObject(ctx, h)
			if err != nil {
				return stats, err
			}
			if has {
				stats.ObjectsSkipped++
				continue
			}
			data, err := st.ReadBlob(h)
			if err != nil {
				return stats, fmt.Errorf("read object %s: %w", h, err)
			}
			if err := c.PutObject(ctx, h, data); err != nil {
				return stats, err
			}
			stats.ObjectsSent++
			stats.BytesSent += int64(len(data))
		}

		if remoteTip == localTip {
			continue // already up to date; nothing to advance
		}
		if err := c.UpdateRef(ctx, name, remoteTip, localTip); err != nil {
			return stats, err
		}
		stats.RefsUpdated++
	}

	return stats, nil
}

// ReachableObjects returns every object reachable from the step at tip, in
// deterministic order, together with the hashes that are referenced but absent
// from the local store.
//
// A referenced object that cannot be read locally is reported as missing rather
// than failing the whole push: history captured before a feature existed (or
// pruned since) must not make a repo unpushable. The tip step itself must be
// readable — without it there is nothing meaningful to push.
func ReachableObjects(st *store.Store, tip store.Hash) (objects []store.Hash, missing []store.Hash, err error) {
	if tip == "" {
		return nil, nil, errors.New("reachable: empty tip hash")
	}
	tipStep, err := st.ReadStep(tip)
	if err != nil {
		return nil, nil, fmt.Errorf("read tip step %s: %w", tip, err)
	}
	// Unmarshalling success is not type validation: a tree or message blob
	// decodes into a Step with every field zero. Every captured step carries a
	// tree, so an empty one means the hash is not a step at all.
	if tipStep.Tree == "" {
		return nil, nil, fmt.Errorf("object %s is not a step (no tree)", tip)
	}

	seen := make(map[store.Hash]bool)
	missingSet := make(map[store.Hash]bool)

	// add records a plain object (one whose own references we do not follow).
	add := func(h store.Hash) {
		if h == "" || seen[h] {
			return
		}
		seen[h] = true
		if !st.ObjectExists(h) {
			missingSet[h] = true
			return
		}
		objects = append(objects, h)
	}

	steps := []store.Hash{tip}
	for len(steps) > 0 {
		stepHash := steps[0]
		steps = steps[1:]
		if seen[stepHash] {
			continue
		}
		seen[stepHash] = true

		if !st.ObjectExists(stepHash) {
			missingSet[stepHash] = true
			continue
		}
		step, err := st.ReadStep(stepHash)
		if err != nil {
			// Unreadable ancestor: record it and stop walking that branch
			// rather than aborting a push of otherwise-intact history.
			missingSet[stepHash] = true
			continue
		}
		objects = append(objects, stepHash)

		// Workspace snapshot: the tree object plus every file blob it names.
		if step.Tree != "" {
			add(step.Tree)
			if tree, err := st.ReadTree(step.Tree); err == nil {
				for _, entry := range tree.Entries {
					add(entry.Blob)
				}
			}
		}
		add(step.Config)

		// Tool call payloads.
		for _, cause := range append([]store.Cause{step.Cause}, step.Causes...) {
			add(cause.ArgsBlob)
			add(cause.ResultBlob)
		}

		// Conversation chain: each transcript node plus its message blobs.
		for t := step.Transcript; t != ""; {
			if seen[t] {
				break
			}
			seen[t] = true
			if !st.ObjectExists(t) {
				missingSet[t] = true
				break
			}
			node, err := st.ReadTranscript(t)
			if err != nil {
				missingSet[t] = true
				break
			}
			objects = append(objects, t)
			for _, m := range node.NewMessages {
				add(m)
			}
			t = node.Prev
		}

		if step.Parent != "" {
			steps = append(steps, step.Parent)
		}
		if step.SecondaryParent != "" {
			steps = append(steps, step.SecondaryParent)
		}
	}

	for h := range missingSet {
		missing = append(missing, h)
	}
	sort.Slice(missing, func(i, j int) bool { return missing[i] < missing[j] })
	return objects, missing, nil
}

// isAncestor reports whether candidate is reachable from tip through parent
// links. Both primary and secondary parents are followed, because a merge step
// records its second lineage there.
func isAncestor(st *store.Store, candidate, tip store.Hash) (bool, error) {
	if candidate == "" {
		return true, nil
	}
	seen := make(map[store.Hash]bool)
	queue := []store.Hash{tip}
	for len(queue) > 0 {
		h := queue[0]
		queue = queue[1:]
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		if h == candidate {
			return true, nil
		}
		step, err := st.ReadStep(h)
		if err != nil {
			continue // unreadable ancestor: cannot extend this branch
		}
		queue = append(queue, step.Parent, step.SecondaryParent)
	}
	return false, nil
}
