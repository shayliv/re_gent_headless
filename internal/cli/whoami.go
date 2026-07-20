package cli

import (
	"fmt"

	"github.com/regent-vcs/regent/internal/config"
	"github.com/regent-vcs/regent/internal/style"
	"github.com/spf13/cobra"
)

// WhoamiCmd returns the `rgt whoami` command.  It shows the currently
// authenticated server and a redacted token so the user can confirm their
// identity without the token being exposed in terminal output.
func WhoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:          "whoami",
		Short:        "Show the current sign-in identity",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			if err := config.CheckAuth(cfg); err != nil {
				fmt.Printf("%s Not signed in\n\n", style.Warning(""))
				fmt.Println("Run: rgt login <server-url>")
				return nil
			}

			fmt.Printf("%s %s\n", style.Label("Server:"), cfg.Auth.ServerURL)
			fmt.Printf("%s %s\n", style.Label("Token: "), config.Redact(cfg.Auth.Token))
			return nil
		},
	}
}
