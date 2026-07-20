package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/regent-vcs/regent/internal/config"
	"github.com/regent-vcs/regent/internal/style"
	"github.com/spf13/cobra"
)

// LoginCmd returns the `rgt login <server-url>` command.
//
// Flow:
//  1. Accept the server URL as a positional arg.
//  2. Prompt for a token (echo-masked).  --token flag allows scripted use, but
//     the user is warned that flag values appear in shell history.
//  3. Validate the token locally (non-empty, minimum length).
//  4. Persist to ~/.regent/config.toml with mode 0600.
func LoginCmd() *cobra.Command {
	var tokenFlag string

	cmd := &cobra.Command{
		Use:          "login <server-url>",
		Short:        "Sign in to a re_gent server",
		Long:         "Authenticate to a re_gent server and store the token at ~/.regent/config.toml (mode 0600).",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runLogin(args[0], tokenFlag, config.Save)
		},
	}

	cmd.Flags().StringVar(&tokenFlag, "token", "",
		"Auth token (prefer the interactive prompt — flag values appear in shell history)")

	return cmd
}

// runLogin is the testable core of the login command.  saveFn abstracts the
// persistence layer so tests can supply a custom path.
func runLogin(serverURL, token string, saveFn func(*config.UserConfig) error) error {
	serverURL = strings.TrimRight(serverURL, "/")
	if serverURL == "" {
		return fmt.Errorf("server URL is required")
	}

	var inputToken string
	if token != "" {
		inputToken = token
		fmt.Printf("  %s --token flag set (this value appeared in your shell history)\n",
			style.Warning(""))
	} else {
		// Prompt interactively with hidden input.
		form := huh.NewForm(
			huh.NewGroup(
				huh.NewInput().
					Title("Auth token").
					Description("Paste the token from " + serverURL + " (input is hidden)").
					EchoMode(huh.EchoModePassword).
					Value(&inputToken),
			),
		)
		if err := form.Run(); err != nil {
			return fmt.Errorf("token prompt: %w", err)
		}
	}

	inputToken = strings.TrimSpace(inputToken)

	// Validate locally before writing to disk.
	cfg := &config.UserConfig{Auth: config.Auth{
		ServerURL: serverURL,
		Token:     inputToken,
	}}
	if err := config.CheckAuth(cfg); err != nil {
		if errors.Is(err, config.ErrNotSignedIn) {
			return fmt.Errorf("token must not be empty")
		}
		return fmt.Errorf("token validation failed: %w", err)
	}

	if err := saveFn(cfg); err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}

	fmt.Printf("  %s Signed in to %s\n", style.Success(""), serverURL)
	fmt.Printf("  %s Token stored at ~/.regent/config.toml (mode 0600)\n", style.DimText("-"))
	return nil
}
