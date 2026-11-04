package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/NimbleTerminal/ntfuzz/internal/booba"
	"github.com/NimbleTerminal/ntfuzz/internal/viewer"
	"github.com/spf13/cobra"
)

func newViewCmd() *cobra.Command {
	var dir, listen string
	var listenFD int
	var interval time.Duration
	cmd := &cobra.Command{Use: "view", Short: "Serve immutable snapshots through a restricted read-only web terminal and API", RunE: func(cmd *cobra.Command, args []string) error {
		if interval <= 0 {
			return fmt.Errorf("refresh interval must be positive")
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		v, err := viewer.Open(dir)
		if err != nil {
			return err
		}
		defer v.Close()
		done := make(chan struct{})
		go func() { defer close(done); v.Watch(ctx, interval) }()
		defer func() { stop(); <-done }()
		options := booba.BridgeOptions{ListenAddr: listen, RefreshInterval: interval, ReadOnly: true}
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
	cmd.Flags().DurationVar(&interval, "refresh", 10*time.Second, "Check for a new published generation")
	return cmd
}
