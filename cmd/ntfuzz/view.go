package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"nimbleterminal.dev/ntfuzz/internal/booba"
	"nimbleterminal.dev/ntfuzz/internal/metrics"
	"nimbleterminal.dev/ntfuzz/internal/viewer"
)

func newViewCmd() *cobra.Command {
	var dir, listen, metricsListen string
	var metricsEnabled bool
	var brandFlags booba.Branding
	var listenFD int
	var interval time.Duration
	cmd := &cobra.Command{Use: "view", Short: "Serve immutable snapshots through a restricted read-only web terminal and API", RunE: func(cmd *cobra.Command, args []string) error {
		if interval <= 0 {
			return fmt.Errorf("refresh interval must be positive")
		}
		branding, err := booba.ResolveBranding(brandFlags, os.Getenv)
		if err != nil {
			return err
		}
		metricsAddr, err := metrics.ResolveListenAddr(metricsEnabled, metricsListen, metrics.DefaultViewMetricsPort)
		if err != nil {
			return err
		}
		var viewMetrics *metrics.Metrics
		if metricsAddr != "" {
			viewMetrics = metrics.New(metrics.ModeView)
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		v, err := viewer.OpenWithMetrics(dir, viewMetrics)
		if err != nil {
			return err
		}
		defer v.Close()
		done := make(chan struct{})
		go func() { defer close(done); v.Watch(ctx, interval) }()
		defer func() { stop(); <-done }()
		options := booba.BridgeOptions{
			ListenAddr:        listen,
			RefreshInterval:   interval,
			ReadOnly:          true,
			Branding:          branding,
			Metrics:           viewMetrics,
			MetricsListenAddr: metricsAddr,
		}
		if listenFD > 0 {
			file := os.NewFile(uintptr(listenFD), "viewer-listener")
			listener, err := net.FileListener(file)
			_ = file.Close()
			if err != nil {
				return err
			}
			defer listener.Close()
			options.Listener = listener
		}
		return booba.ListenAndServe(ctx, v, options)
	}}
	cmd.Flags().IntVar(&listenFD, "listen-fd", 0, "Use an inherited listening socket (e.g. systemd fd 3)")
	cmd.Flags().StringVar(&dir, "snapshots", "snapshots", "Published snapshot directory (read-only)")
	cmd.Flags().StringVar(&listen, "listen", "127.0.0.1:8081", "Viewer listen address")
	cmd.Flags().BoolVar(&metricsEnabled, "metrics", false, "Enable the loopback Prometheus /metrics listener (defaults to 127.0.0.1:9091)")
	cmd.Flags().StringVar(&metricsListen, "metrics-listen", "", "HTTP listen address for Prometheus /metrics endpoint (disabled by default; binds to 127.0.0.1 when host is omitted)")
	booba.AddBrandingFlags(cmd.Flags(), &brandFlags)
	cmd.Flags().DurationVar(&interval, "refresh", 10*time.Second, "Check for a new published generation")
	return cmd
}
