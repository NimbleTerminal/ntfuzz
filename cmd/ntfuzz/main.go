package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	booba "github.com/NimbleMarkets/go-booba"
	"github.com/spf13/cobra"

	"github.com/NimbleTerminal/ntfuzzy/internal/api"
	boobabridge "github.com/NimbleTerminal/ntfuzzy/internal/booba"
	"github.com/NimbleTerminal/ntfuzzy/internal/orchestrator"
	"github.com/NimbleTerminal/ntfuzzy/internal/store"
	"github.com/NimbleTerminal/ntfuzzy/internal/tui"
)

func main() {
	log.SetOutput(os.Stderr)
	log.SetFlags(log.LstdFlags)

	rootCmd := &cobra.Command{
		Use:   "ntfuzz",
		Short: "Continuous Go fuzzing orchestrator with DuckDB store and Booba/Bubble Tea TUI",
		SilenceUsage: true,
	}

	rootCmd.AddCommand(newServeCmd(), newTUICmd())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func newServeCmd() *cobra.Command {
	var (
		configPath     string
		dbPath         string
		listenAddr     string
		workers        int
		roundInterval  time.Duration
		noOrchestrator bool
		debug          bool
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the fuzzing orchestrator, DuckDB store, HTTP JSON API, and Booba browser TUI",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			cfg, err := orchestrator.LoadConfig(configPath)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			st, err := store.Open(dbPath)
			if err != nil {
				return fmt.Errorf("open duckdb store %q: %w", dbPath, err)
			}
			defer func() { _ = st.Close() }()

			orch := orchestrator.New(cfg, st, orchestrator.Options{
				Workers:       workers,
				RoundInterval: roundInterval,
				Logger:        log.New(os.Stderr, "", log.LstdFlags),
			})

			if !noOrchestrator {
				go func() {
					if err := orch.Run(ctx); err != nil {
						log.Printf("level=ERROR component=serve msg=%q err=%q", "orchestrator exited with error", err)
					}
				}()
			} else {
				// Still perform an initial project/target sync so the store has project metadata
				orch.SyncAll(ctx)
			}

			apiServer := api.NewServer(st, orch)
			log.Printf("level=INFO component=serve listen=%s db=%s projects=%d workers=%d msg=%q",
				listenAddr, dbPath, len(cfg.Projects), workers,
				fmt.Sprintf("serving HTTP API (/api/*) and Booba Ghostty TUI (http://%s/)", listenAddr))

			return boobabridge.ListenAndServe(ctx, apiServer, boobabridge.BridgeOptions{
				ListenAddr:      listenAddr,
				RefreshInterval: 2 * time.Second,
				Debug:           debug,
			})
		},
	}

	cmd.Flags().StringVarP(&configPath, "config", "c", "configs/projects.yaml", "Path to projects YAML config (uses embedded default if not found)")
	cmd.Flags().StringVarP(&dbPath, "db", "d", "ntfuzz.duckdb", "Path to DuckDB database file")
	cmd.Flags().StringVarP(&listenAddr, "listen", "l", "127.0.0.1:8080", "HTTP listen address for API and Booba web TUI")
	cmd.Flags().IntVarP(&workers, "workers", "w", 2, "Number of concurrent local fuzz workers")
	cmd.Flags().DurationVar(&roundInterval, "interval", 5*time.Second, "Pause interval between fuzzing rounds")
	cmd.Flags().BoolVar(&noOrchestrator, "no-orchestrator", false, "Serve API and Booba TUI without running background fuzz jobs")
	cmd.Flags().BoolVar(&debug, "debug", false, "Enable verbose Booba server logging")

	return cmd
}

func newTUICmd() *cobra.Command {
	var (
		apiURL  string
		refresh time.Duration
	)

	cmd := &cobra.Command{
		Use:   "tui",
		Short: "Run the Bubble Tea dashboard against a remote ntfuzz HTTP API",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := api.NewClient(apiURL)
			model, err := tui.NewActionModel(client, refresh)
			if err != nil {
				model = tui.NewModel(client, refresh)
			}
			return booba.Run(model)
		},
	}

	cmd.Flags().StringVar(&apiURL, "api", "http://localhost:8080", "Base URL of the ntfuzz HTTP API")
	cmd.Flags().DurationVar(&refresh, "refresh", 2*time.Second, "Dashboard polling interval")

	return cmd
}
