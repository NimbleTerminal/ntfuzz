package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	booba "nimbleterminal.dev/booba"

	"nimbleterminal.dev/ntfuzz/internal/api"
	boobabridge "nimbleterminal.dev/ntfuzz/internal/booba"
	"nimbleterminal.dev/ntfuzz/internal/metrics"
	"nimbleterminal.dev/ntfuzz/internal/orchestrator"
	"nimbleterminal.dev/ntfuzz/internal/store"
	"nimbleterminal.dev/ntfuzz/internal/tui"
)

func main() {
	rootCmd := &cobra.Command{
		Use:          "ntfuzz",
		Short:        "Continuous Go fuzzing orchestrator with DuckDB store and Booba/Bubble Tea TUI",
		SilenceUsage: true,
	}

	rootCmd.AddCommand(newServeCmd(), newViewCmd(), newTUICmd(), newCrashCmd())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func newServeCmd() *cobra.Command {
	var (
		configPath       string
		snapshotDir      string
		snapshotInterval time.Duration
		dbPath           string
		listenAddr       string
		metricsEnabled   bool
		metricsListen    string
		brandFlags       boobabridge.Branding
		workers          int
		roundInterval    time.Duration
		noOrchestrator   bool
		debug            bool
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the fuzzing orchestrator, DuckDB store, HTTP JSON API, and Booba browser TUI",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			if snapshotDir != "" && snapshotInterval <= 0 {
				return fmt.Errorf("snapshot interval must be positive")
			}
			branding, err := boobabridge.ResolveBranding(brandFlags, os.Getenv)
			if err != nil {
				return err
			}
			metricsAddr, err := metrics.ResolveListenAddr(metricsEnabled, metricsListen, metrics.DefaultServeMetricsPort)
			if err != nil {
				return err
			}
			var srvMetrics *metrics.Metrics
			if metricsAddr != "" {
				srvMetrics = metrics.New(metrics.ModeServe)
			}

			logger := slog.New(slog.NewTextHandler(os.Stderr, nil))

			cfg, err := orchestrator.LoadConfig(configPath)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}

			st, err := store.Open(dbPath)
			if err != nil {
				return fmt.Errorf("open duckdb store %q: %w", dbPath, err)
			}
			defer func() { _ = st.Close() }()
			if srvMetrics != nil {
				st.SetMetrics(srvMetrics)
			}
			orch := orchestrator.New(cfg, st, orchestrator.Options{
				Workers:       workers,
				RoundInterval: roundInterval,
				Logger:        logger,
			})
			// Apply project config flags (such as public_inputs) before publishing the initial snapshot.
			orch.SyncProjectConfig(ctx)

			if snapshotDir != "" {
				snapshotCtx, cancelSnapshots := context.WithCancel(ctx)
				done := make(chan struct{})
				go func() {
					defer close(done)
					timer := time.NewTicker(snapshotInterval)
					defer timer.Stop()
					publish := func() {
						if _, err := st.PublishSnapshot(snapshotCtx, snapshotDir); err != nil {
							logger.Error("publish snapshot failed", "err", err)
						}
					}
					publish()
					for {
						select {
						case <-snapshotCtx.Done():
							return
						case <-timer.C:
							publish()
						}
					}
				}()
				defer func() { cancelSnapshots(); <-done }()
			}

			var orchWG sync.WaitGroup
			defer func() { stop(); orchWG.Wait() }()

			if !noOrchestrator {
				orchWG.Add(1)
				go func() {
					defer orchWG.Done()
					if err := orch.Run(ctx); err != nil {
						logger.Error("orchestrator exited with error", "component", "serve", "err", err)
					}
				}()
			} else {
				orch.SyncAll(ctx)
			}

			apiServer := api.NewServer(st, orch)
			logger.Info("serving HTTP API (/api/*) and Booba Ghostty TUI",
				"component", "serve",
				"listen", listenAddr,
				"url", fmt.Sprintf("http://%s/", listenAddr),
				"metrics_listen", metricsAddr,
				"db", dbPath,
				"projects", len(cfg.Projects),
				"workers", workers,
			)

			return boobabridge.ListenAndServe(ctx, apiServer, boobabridge.BridgeOptions{
				ListenAddr:        listenAddr,
				RefreshInterval:   2 * time.Second,
				Debug:             debug,
				Branding:          branding,
				Metrics:           srvMetrics,
				MetricsListenAddr: metricsAddr,
			})
		},
	}

	cmd.Flags().StringVarP(&configPath, "config", "c", "configs/projects.yaml", "Path to projects YAML config (read once at startup; uses embedded default if not found)")
	cmd.Flags().StringVarP(&dbPath, "db", "d", "ntfuzz.duckdb", "Path to DuckDB database file")
	cmd.Flags().StringVarP(&listenAddr, "listen", "l", "127.0.0.1:8080", "HTTP listen address for API and Booba web TUI")
	cmd.Flags().BoolVar(&metricsEnabled, "metrics", false, "Enable the loopback Prometheus /metrics listener (defaults to 127.0.0.1:9090)")
	cmd.Flags().StringVar(&metricsListen, "metrics-listen", "", "HTTP listen address for Prometheus /metrics endpoint (disabled by default; binds to 127.0.0.1 when host is omitted)")
	boobabridge.AddBrandingFlags(cmd.Flags(), &brandFlags)
	cmd.Flags().IntVarP(&workers, "workers", "w", 2, "Number of concurrent local fuzz workers (each capped at GOMAXPROCS=1)")
	cmd.Flags().DurationVar(&roundInterval, "interval", 5*time.Second, "Pause interval between fuzzing rounds")
	cmd.Flags().StringVar(&snapshotDir, "snapshot-dir", "", "Publish immutable DuckDB generations in this directory")
	cmd.Flags().DurationVar(&snapshotInterval, "snapshot-interval", time.Minute, "Interval between database snapshots")
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

	cmd.Flags().StringVar(&apiURL, "api", "http://127.0.0.1:8080", "Base URL of the ntfuzz HTTP API")
	cmd.Flags().DurationVar(&refresh, "refresh", 2*time.Second, "Dashboard polling interval")

	return cmd
}

func newCrashCmd() *cobra.Command {
	var apiURL string

	crashCmd := &cobra.Command{
		Use:   "crash",
		Short: "List or triage recorded fuzzing crashes via the HTTP API",
	}
	crashCmd.PersistentFlags().StringVar(&apiURL, "api", "http://127.0.0.1:8080", "Base URL of the ntfuzz HTTP API")

	var openOnly bool
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List crashes from the ntfuzz API",
		RunE: func(cmd *cobra.Command, args []string) error {
			client := api.NewClient(apiURL)
			crashes, err := client.GetCrashes(cmd.Context(), openOnly, "")
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(crashes) == 0 {
				_, _ = fmt.Fprintln(out, "No matching crashes.")
				return nil
			}
			for _, c := range crashes {
				state := "OPEN"
				if c.FixedAt != nil {
					state = "FIXED (" + c.FixedAt.Local().Format(time.RFC3339) + ")"
				} else if strings.TrimSpace(c.IssueURL) != "" {
					state = "REPORTED"
				}
				dep := "project"
				if c.BlamedModule != "" {
					dep = c.BlamedModule
					if c.BlamedVersion != "" {
						dep += "@" + c.BlamedVersion
					}
					if c.BlamedURL != "" {
						dep += " (" + c.BlamedURL + ")"
					}
				}
				extra := ""
				if c.IssueURL != "" {
					extra = "  issue=" + c.IssueURL
				}
				_, _ = fmt.Fprintf(out, "[#%d] %s  %s/%s@%s  blame=%s  hash=%s  fp=%s  input=%s%s\n",
					c.ID, state, c.ProjectName, c.TargetName, c.GitSHA, dep, c.InputHash, c.StackFingerprint, c.MinimizedInput, extra)
			}
			return nil
		},
	}
	listCmd.Flags().BoolVar(&openOnly, "open", true, "Show only open crashes")

	var issueURL string
	fixedCmd := &cobra.Command{
		Use:   "fixed <crash-id>",
		Short: "Mark a crash as fixed (and optionally attach an issue URL)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			crashID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil || crashID <= 0 {
				return fmt.Errorf("invalid crash ID %q", args[0])
			}
			client := api.NewClient(apiURL)
			if err := client.MarkCrashFixed(cmd.Context(), crashID, issueURL); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Marked crash #%d as fixed.\n", crashID)
			return nil
		},
	}
	fixedCmd.Flags().StringVar(&issueURL, "issue", "", "Optional issue URL to attach to the crash record")

	var linkIssueURL string
	linkCmd := &cobra.Command{
		Use:   "link <crash-id>",
		Short: "Attach an upstream issue URL to an open crash without marking it fixed",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			crashID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil || crashID <= 0 {
				return fmt.Errorf("invalid crash ID %q", args[0])
			}
			if strings.TrimSpace(linkIssueURL) == "" {
				return fmt.Errorf("--issue URL is required")
			}
			client := api.NewClient(apiURL)
			if err := client.SetCrashIssue(cmd.Context(), crashID, linkIssueURL); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Linked crash #%d to %s.\n", crashID, strings.TrimSpace(linkIssueURL))
			return nil
		},
	}
	linkCmd.Flags().StringVar(&linkIssueURL, "issue", "", "Upstream issue or security advisory URL")

	var publicURL string
	reportCmd := &cobra.Command{
		Use:   "report <crash-id>",
		Short: "Print a shareable Markdown crash report for upstream maintainers",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			crashID, err := strconv.ParseInt(args[0], 10, 64)
			if err != nil || crashID <= 0 {
				return fmt.Errorf("invalid crash ID %q", args[0])
			}
			client := api.NewClient(apiURL)
			md, err := client.GetCrashReport(cmd.Context(), crashID, publicURL)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprint(cmd.OutOrStdout(), md)
			return nil
		},
	}
	reportCmd.Flags().StringVar(&publicURL, "public-url", "", "Public base URL for permalink and input download links (defaults to --api)")

	crashCmd.AddCommand(listCmd, fixedCmd, linkCmd, reportCmd)
	return crashCmd
}
