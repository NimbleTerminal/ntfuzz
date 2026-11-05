package main

import (
	"testing"
)

func TestServeAndViewMetricsFlagsDefaultsAndValidation(t *testing.T) {
	serveCmd := newServeCmd()
	mFlag := serveCmd.Flags().Lookup("metrics")
	if mFlag == nil || mFlag.DefValue != "false" {
		t.Fatalf("serve --metrics flag missing or default != false: %+v", mFlag)
	}
	mlFlag := serveCmd.Flags().Lookup("metrics-listen")
	if mlFlag == nil || mlFlag.DefValue != "" {
		t.Fatalf("serve --metrics-listen flag missing or default != empty: %+v", mlFlag)
	}

	viewCmd := newViewCmd()
	vmFlag := viewCmd.Flags().Lookup("metrics")
	if vmFlag == nil || vmFlag.DefValue != "false" {
		t.Fatalf("view --metrics flag missing or default != false: %+v", vmFlag)
	}
	vmlFlag := viewCmd.Flags().Lookup("metrics-listen")
	if vmlFlag == nil || vmlFlag.DefValue != "" {
		t.Fatalf("view --metrics-listen flag missing or default != empty: %+v", vmlFlag)
	}

	// Malformed --metrics-listen fails immediately before touching database or network.
	viewCmd.SetArgs([]string{"--metrics-listen", "invalid-addr"})
	if err := viewCmd.Execute(); err == nil {
		t.Fatal("expected error for invalid --metrics-listen on view")
	}
}
