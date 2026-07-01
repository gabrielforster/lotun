// Command lotund is the lotun server: it loads configuration, opens the claim
// store, and runs the data plane until interrupted by a signal.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/gabrielrocha/lotun/internal/config"
	"github.com/gabrielrocha/lotun/internal/server"
	"github.com/gabrielrocha/lotun/internal/store"
	"github.com/spf13/cobra"
)

func main() {
	var configPath string

	root := &cobra.Command{
		Use:           "lotund",
		Short:         "lotun server",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.LoadServer(configPath)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			return run(cmd.Context(), cfg)
		},
	}
	root.Flags().StringVar(&configPath, "config", "lotund.yaml", "path to config file")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := root.ExecuteContext(ctx); err != nil {
		log.Fatalf("lotund: %v", err)
	}
}

// run opens the claim store and runs the server until ctx is cancelled. It logs
// the bound control and HTTP addresses once the listeners are up.
func run(ctx context.Context, cfg config.ServerConfig) error {
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}

	claims, err := store.Open(filepath.Join(cfg.DataDir, "claims.json"))
	if err != nil {
		return fmt.Errorf("open claim store: %w", err)
	}

	srv, err := server.New(cfg, claims)
	if err != nil {
		return fmt.Errorf("init server: %w", err)
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.Run(ctx) }()

	// The listeners bind inside Run; poll briefly so we can log the resolved
	// addresses (which matter when configured with :0).
	logBoundAddrs(ctx, srv)

	return <-errc
}

// logBoundAddrs waits for the server's listeners to bind, then logs the bound
// control and HTTP addresses. It gives up quietly if ctx is cancelled first.
func logBoundAddrs(ctx context.Context, srv *server.Server) {
	t := time.NewTicker(10 * time.Millisecond)
	defer t.Stop()
	for {
		if ctrl, http := srv.ControlAddr(), srv.HTTPAddr(); ctrl != "" && http != "" {
			log.Printf("lotund: control listening on %s", ctrl)
			log.Printf("lotund: http listening on %s", http)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
