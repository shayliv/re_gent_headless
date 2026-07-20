package cli

import (
	"errors"
	"fmt"

	"github.com/regent-vcs/regent/internal/config"
	"github.com/regent-vcs/regent/internal/style"
	"github.com/spf13/cobra"
)

// PushCmd returns the `rgt push` command.
//
// Push sends local session steps to a re_gent server so they are persisted
// remotely and attributed to the authenticated user.  Unauthenticated push is
// always rejected (fails closed) — the server would reject it anyway, but we
// catch it client-side first to give a clear error message.
//
// Network transport is not yet implemented; this command validates auth and
// exits cleanly so the auth layer can be exercised before the transport is
// added.
func PushCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "push",
		Short:        "Push session steps to the remote re_gent server",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			return runPush(cfg)
		},
	}
}

// runPush is the testable core of the push command.  It enforces auth before
// any network operation is attempted so that unauthenticated calls fail closed.
func runPush(cfg *config.UserConfig) error {
	if err := config.CheckAuth(cfg); err != nil {
		if errors.Is(err, config.ErrNotSignedIn) {
			return fmt.Errorf("%w\n\nAuthenticate first:\n  rgt login <server-url>", err)
		}
		return fmt.Errorf("auth check failed: %w", err)
	}

	// Auth passed.  Remote transport is not yet implemented.
	fmt.Printf("  %s Authenticated as %s\n", style.Success(""), cfg.Auth.ServerURL)
	fmt.Printf("  %s Remote push not yet implemented (coming in a future release)\n",
		style.DimText("-"))
	return nil
}
