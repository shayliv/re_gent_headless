package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/regent-vcs/regent/internal/remote"
	"github.com/regent-vcs/regent/internal/store"
	"github.com/regent-vcs/regent/internal/style"
	"github.com/spf13/cobra"
)

type pushParams struct {
	URL      string
	RepoID   string
	Sessions []string
}

// PushCmd creates the push command: upload local history to one repo on a server.
func PushCmd() *cobra.Command {
	var p pushParams

	cmd := &cobra.Command{
		Use:   "push",
		Short: "Push session history to a repo on a re_gent server",
		Long: "Uploads every object reachable from the selected session refs into\n" +
			"one repo on the server, then advances that repo's refs.\n\n" +
			"The repo id scopes everything: two repos pushed to the same server\n" +
			"keep separate objects, separate refs and separate histories.\n\n" +
			"The first push records the server url and repo id in\n" +
			".regent/config.toml, so later pushes need no flags.",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := openStoreFromCWD()
			if err != nil {
				return err
			}
			target, err := resolveTarget(st, p)
			if err != nil {
				return err
			}
			stats, err := runPush(cmd.Context(), st, target)
			if err != nil {
				return err
			}
			if err := rememberTarget(st, target); err != nil {
				return err
			}
			fmt.Printf("%s pushed to %s (repo %s)\n",
				style.Brand("re_gent"), target.URL, style.Label(target.RepoID))
			fmt.Printf("  objects: %d sent, %d already present\n", stats.ObjectsSent, stats.ObjectsSkipped)
			fmt.Printf("  refs:    %d updated\n", stats.RefsUpdated)
			if len(stats.Missing) > 0 {
				fmt.Printf("  warning: %d referenced object(s) missing locally and not pushed\n", len(stats.Missing))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&p.URL, "url", "", "server url, e.g. http://127.0.0.1:7654 (default: the url recorded in .regent/config.toml)")
	cmd.Flags().StringVar(&p.RepoID, "repo", "", "repo id on the server (default: the repo id recorded in .regent/config.toml)")
	cmd.Flags().StringSliceVar(&p.Sessions, "session", nil, "session ref(s) to push (default: all sessions)")

	return cmd
}

// resolveTarget fixes the (server, repo) identity of one push. Flags win over
// the identity recorded in .regent/config.toml, so a repo that has been pushed
// once can be pushed again with no flags at all, while a one-off push to a
// different repo id is still possible.
func resolveTarget(st *store.Store, p pushParams) (pushParams, error) {
	cfg, err := st.ReadRepoConfig()
	if err != nil {
		return p, err
	}
	if p.URL == "" {
		p.URL = cfg.Remote.URL
	}
	if p.RepoID == "" {
		p.RepoID = cfg.Remote.RepoID
	}
	if p.URL == "" || p.RepoID == "" {
		return p, fmt.Errorf("no server recorded for this repo\n\n  Run: rgt push --url <server-url> --repo <repo-id>")
	}
	return p, nil
}

// rememberTarget binds this working copy to the repo it was first pushed to.
//
// A repo that already carries an identity keeps it: an explicit --repo sends
// this one push elsewhere but must not silently re-point the working copy, or a
// single mistyped flag would move every later push into another repo's history.
func rememberTarget(st *store.Store, p pushParams) error {
	cfg, err := st.ReadRepoConfig()
	if err != nil {
		return err
	}
	if cfg.Remote.URL != "" || cfg.Remote.RepoID != "" {
		return nil
	}
	cfg.Remote.URL = p.URL
	cfg.Remote.RepoID = p.RepoID
	return st.WriteRepoConfig(cfg)
}

// runPush resolves which refs to push and performs the push. It is separated
// from the cobra wiring so tests can drive it against any store and server.
func runPush(ctx context.Context, st *store.Store, p pushParams) (remote.PushStats, error) {
	var stats remote.PushStats

	client, err := remote.NewClient(p.URL, p.RepoID)
	if err != nil {
		return stats, err
	}

	refs, err := selectSessionRefs(st, p.Sessions)
	if err != nil {
		return stats, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return remote.Push(ctx, client, st, refs)
}

// selectSessionRefs turns the --session selection into full ref names. With no
// selection every recorded session is pushed.
func selectSessionRefs(st *store.Store, sessions []string) ([]string, error) {
	if len(sessions) > 0 {
		refs := make([]string, 0, len(sessions))
		for _, s := range sessions {
			name := strings.TrimSpace(s)
			if name == "" {
				return nil, fmt.Errorf("empty session id")
			}
			// Accept both "sessions/<id>" and a bare "<id>".
			if !strings.HasPrefix(name, "sessions/") {
				name = "sessions/" + name
			}
			if _, err := st.ReadRef(name); err != nil {
				return nil, fmt.Errorf("unknown session %q: %w", s, err)
			}
			refs = append(refs, name)
		}
		return refs, nil
	}

	all, err := st.ListRefs("sessions")
	if err != nil {
		return nil, fmt.Errorf("list session refs: %w", err)
	}
	if len(all) == 0 {
		return nil, fmt.Errorf("no sessions recorded yet; nothing to push")
	}
	refs := make([]string, 0, len(all))
	for name := range all {
		refs = append(refs, "sessions/"+filepath.ToSlash(name))
	}
	sort.Strings(refs) // deterministic push order
	return refs, nil
}
