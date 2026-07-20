package cli

import (
	"fmt"

	"github.com/regent-vcs/regent/internal/push"
	"github.com/regent-vcs/regent/internal/remote"
	"github.com/spf13/cobra"
)

// PushCmd creates the push command.
func PushCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "push <remote-url>",
		Short: "Upload objects and advance session refs on a remote server",
		Long: `Upload all local objects the remote lacks, then advance each session ref.

Objects are uploaded before any ref is advanced, ensuring the remote is always
in a consistent state even if the push is interrupted.`,
		SilenceUsage: true,
		Args:         cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := openStoreFromCWD()
			if err != nil {
				return err
			}

			r := remote.NewHTTP(args[0], nil)

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
		},
	}
	return cmd
}
