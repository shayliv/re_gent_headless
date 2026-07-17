package cli

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/regent-vcs/regent/internal/store"
	"github.com/regent-vcs/regent/internal/style"
	"github.com/spf13/cobra"
)

// PushCmd uploads the local object store and session refs to a demo server so
// captured changes from different agents can be stored and viewed centrally.
func PushCmd() *cobra.Command {
	var serverURL string
	var repo string

	cmd := &cobra.Command{
		Use:   "push",
		Short: "Push local objects and session refs to a re_gent server",
		Long: `Upload the local .regent object store and session refs to a re_gent demo server.

Objects are content-addressed, so only objects the server does not already have
are uploaded. Each session ref (one per agent branch) is then pointed at its tip,
which lets the server show how different agents recorded their changes.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if serverURL == "" {
				return fmt.Errorf("--server is required (e.g. --server http://localhost:8099)")
			}
			serverURL = strings.TrimRight(serverURL, "/")

			s, err := openStoreFromCWD()
			if err != nil {
				return err
			}
			if repo == "" {
				repo = filepath.Base(filepath.Dir(s.Root))
			}

			client := &http.Client{Timeout: 30 * time.Second}
			out := cmd.OutOrStdout()

			uploaded, skipped, err := pushObjects(client, serverURL, repo, s)
			if err != nil {
				return err
			}

			refs, err := s.ListRefs("sessions")
			if err != nil {
				return fmt.Errorf("list session refs: %w", err)
			}
			for name, tip := range refs {
				if err := pushRef(client, serverURL, repo, "sessions/"+name, tip); err != nil {
					return fmt.Errorf("push ref %s: %w", name, err)
				}
			}

			fmt.Fprintf(out, "%s Pushed to %s (repo %q)\n", style.Success(""), serverURL, repo)
			fmt.Fprintf(out, "  objects uploaded: %d (skipped %d already present)\n", uploaded, skipped)
			fmt.Fprintf(out, "  session refs:     %d\n", len(refs))
			fmt.Fprintf(out, "\nView: %s/\n", serverURL)
			return nil
		},
	}

	cmd.Flags().StringVar(&serverURL, "server", "", "server base URL (e.g. http://localhost:8099)")
	cmd.Flags().StringVar(&repo, "repo", "", "repo name on the server (default: workspace directory name)")
	return cmd
}

// pushObjects uploads every local object the server is missing. Returns the
// number uploaded and the number skipped because the server already had them.
func pushObjects(client *http.Client, serverURL, repo string, s *store.Store) (int, int, error) {
	var uploaded, skipped int
	err := s.WalkObjects(func(h store.Hash) error {
		if serverHasObject(client, serverURL, repo, h) {
			skipped++
			return nil
		}
		content, err := s.ReadBlob(h)
		if err != nil {
			return fmt.Errorf("read object %s: %w", h, err)
		}
		if err := putObject(client, serverURL, repo, h, content); err != nil {
			return err
		}
		uploaded++
		return nil
	})
	return uploaded, skipped, err
}

func serverHasObject(client *http.Client, serverURL, repo string, h store.Hash) bool {
	url := fmt.Sprintf("%s/repos/%s/objects/%s", serverURL, repo, h)
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func putObject(client *http.Client, serverURL, repo string, h store.Hash, content []byte) error {
	url := fmt.Sprintf("%s/repos/%s/objects/%s", serverURL, repo, h)
	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(content))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("upload object %s: %w", h, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("upload object %s: %s: %s", h, resp.Status, readBody(resp.Body))
	}
	return nil
}

func pushRef(client *http.Client, serverURL, repo, name string, tip store.Hash) error {
	url := fmt.Sprintf("%s/repos/%s/refs/%s", serverURL, repo, name)
	req, err := http.NewRequest(http.MethodPut, url, strings.NewReader(string(tip)))
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", resp.Status, readBody(resp.Body))
	}
	return nil
}

func readBody(r io.Reader) string {
	data, _ := io.ReadAll(io.LimitReader(r, 512))
	return strings.TrimSpace(string(data))
}
