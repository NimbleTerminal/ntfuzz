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

	// Both serve and view register --brand-name, --brand-url, and --brand-accent with empty defaults,
	// and reject invalid branding URLs/colors early.
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"invalid-url", []string{"--brand-name", "Org", "--brand-url", "javascript:alert(1)"}},
		{"invalid-accent", []string{"--brand-name", "Org", "--brand-accent", "not-a-color"}},
	} {
		vCmd := newViewCmd()
		vCmd.SetArgs(tc.args)
		if err := vCmd.Execute(); err == nil {
			t.Fatalf("view %s: expected error for %v", tc.name, tc.args)
		}
		sCmd := newServeCmd()
		sCmd.SetArgs(tc.args)
		if err := sCmd.Execute(); err == nil {
			t.Fatalf("serve %s: expected error for %v", tc.name, tc.args)
		}
	}
}
