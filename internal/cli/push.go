package cli

import (
	"errors"
	"fmt"

	"github.com/regent-vcs/regent/internal/config"
	"github.com/regent-vcs/regent/internal/push"
	"github.com/regent-vcs/regent/internal/remote"
	"github.com/regent-vcs/regent/internal/style"
	"github.com/spf13/cobra"
)

// PushCmd returns the `rgt push` command.
//
// Push sends local session steps to a re_gent server so they are persisted
// remotely and attributed to the authenticated user. Unauthenticated push is
// always rejected (fails closed) — the server would reject it anyway, but we
// catch it client-side first to give a clear error message.
//
// Two modes are supported:
//
//   - rgt push            — auth-gated push to the configured server. Transport
//     over the configured server URL is still being wired up (see runPush).
//   - rgt push <url>      — after the same auth gate, upload all local objects
//     the remote lacks and advance each session ref against <url>.
func PushCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "push [remote-url]",
		Short: "Push session steps to the remote re_gent server",
		Long: `Upload all local objects the remote lacks, then advance each session ref.

Objects are uploaded before any ref is advanced, ensuring the remote is always
in a consistent state even if the push is interrupted.

Authentication is required: run 'rgt login <server-url>' first.`,
		SilenceUsage: true,
		Args:         cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			if len(args) == 1 {
				return runPushTransport(cfg, args[0])
			}
			return runPush(cfg)
		},
	}
}

// runPush is the testable core of the auth-gated push command. It enforces auth
// before any network operation is attempted so that unauthenticated calls fail
// closed. Transport against the configured server URL is not yet implemented.
func runPush(cfg *config.UserConfig) error {
	if err := config.CheckAuth(cfg); err != nil {
		if errors.Is(err, config.ErrNotSignedIn) {
			return fmt.Errorf("%w\n\nAuthenticate first:\n  rgt login <server-url>", err)
		}
		return fmt.Errorf("auth check failed: %w", err)
	}

	// Auth passed. Remote transport over the configured server URL is not yet
	// implemented; pass an explicit <remote-url> to use the object/ref uploader.
	fmt.Printf("  %s Authenticated as %s\n", style.Success(""), cfg.Auth.ServerURL)
	fmt.Printf("  %s Remote push not yet implemented (coming in a future release)\n",
		style.DimText("-"))
	return nil
}

// runPushTransport enforces the same auth gate, then uploads objects and
// advances session refs against an explicit remote URL.
func runPushTransport(cfg *config.UserConfig, remoteURL string) error {
	if err := config.CheckAuth(cfg); err != nil {
		if errors.Is(err, config.ErrNotSignedIn) {
			return fmt.Errorf("%w\n\nAuthenticate first:\n  rgt login <server-url>", err)
		}
		return fmt.Errorf("auth check failed: %w", err)
	}

	s, err := openStoreFromCWD()
	if err != nil {
		return err
	}

	r := remote.NewHTTP(remoteURL, nil)

	result, err := push.Push(s, r)
	if err != nil {
		return fmt.Errorf("push failed: %w", err)
	}

	if result.Uploaded == 0 && len(result.RefsUpdated) == 0 {
		fmt.Println("Everything up-to-date.")
		return nil
	}

	if result.Uploaded > 0 {
		fmt.Printf("Uploaded %d object(s).\n", result.Uploaded)
	}
	for _, name := range result.RefsUpdated {
		fmt.Printf("Updated %s\n", name)
	}
	for _, name := range result.RefsSkipped {
		fmt.Printf("Up-to-date %s\n", name)
	}
	return nil
}
