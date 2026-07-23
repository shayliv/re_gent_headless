package cli

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/regent-vcs/regent/internal/server"
	"github.com/spf13/cobra"
)

// ServeCmd returns the `rgt serve` cobra command.
func ServeCmd() *cobra.Command {
	var (
		addr    string
		dataDir string
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the regent demo HTTP server",
		Long: `Starts the regent demo object/ref store server.

The server stores objects and session refs per repository.
Data is written to --data-dir (default: ~/.regent-server), which must not be
inside your source repository.

API:
  POST /repos/{repo}/objects          upload an object (returns its hash)
  GET  /repos/{repo}/objects/{hash}   download an object
  GET  /repos/{repo}/refs/{ref...}    read a ref
  PUT  /repos/{repo}/refs/{ref...}    CAS-update a ref (JSON body: {"expected":"","new":"<hash>"})
  GET  /repos/{repo}/                 web view of a repository
  GET  /                              web view listing all repositories`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if dataDir == "" {
				home, err := os.UserHomeDir()
				if err != nil {
					return fmt.Errorf("could not determine home directory: %w", err)
				}
				dataDir = filepath.Join(home, ".regent-server")
			}

			logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
				Level: slog.LevelInfo,
			}))

			srv, err := server.New(dataDir, logger)
			if err != nil {
				return fmt.Errorf("init server: %w", err)
			}

			ln, err := net.Listen("tcp", addr)
			if err != nil {
				return fmt.Errorf("listen %s: %w", addr, err)
			}

			httpSrv := &http.Server{Handler: srv}

			logger.Info("regent demo server started",
				"addr", ln.Addr().String(),
				"data_dir", dataDir,
			)

			done := make(chan struct{})
			go func() {
				sig := make(chan os.Signal, 1)
				signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
				<-sig
				_ = httpSrv.Shutdown(context.Background())
				close(done)
			}()

			if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
				return fmt.Errorf("serve: %w", err)
			}
			<-done
			return nil
		},
	}

	cmd.Flags().StringVarP(&addr, "addr", "a", ":7654", "TCP address to listen on")
	cmd.Flags().StringVarP(&dataDir, "data-dir", "d", "", "directory for server data (default: ~/.regent-server)")

	return cmd
}
