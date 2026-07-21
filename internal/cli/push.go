package cli

import (
	"fmt"
	"os"

	"github.com/regent-vcs/regent/internal/config"
	"github.com/spf13/cobra"
)

// PushCmd returns the `rgt push` command.
func PushCmd() *cobra.Command {
	var cfgPath string

	cmd := &cobra.Command{
		Use:   "push",
		Short: "Push local steps to the configured remote",
		Long: `Push local steps and refs to the remote server configured by 'rgt connect'.

Authentication is required. Run 'rgt login <server-url>' first.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			var cfg *config.UserConfig
			var err error
			if cfgPath != "" {
				cfg, err = config.LoadFrom(cfgPath)
			} else {
				cfg, err = config.Load()
			}
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			return runPush(cfg)
		},
	}

	cmd.Flags().StringVar(&cfgPath, "config", "", "path to user config file (default: ~/.regent/config.toml)")
	return cmd
}

func runPush(cfg *config.UserConfig) error {
	if err := config.CheckAuth(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return err
	}

	// Stub: network transport is wired in the remote package (RE-7).
	// The access-control gate (CheckAuth) is the deliverable here.
	fmt.Println("push: authenticated as", config.Redact(cfg.Auth.Token))
	fmt.Println("push: remote push not yet implemented — see rgt connect")
	return nil
}
