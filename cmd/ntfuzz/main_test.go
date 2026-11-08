package main

import (
	"bytes"
	"context"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"nimbleterminal.dev/ntfuzz/internal/api"
	"nimbleterminal.dev/ntfuzz/internal/store"
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

func TestCrashLinkListAndReportCommands(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "cli_crash.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	pID, err := st.SyncProject(ctx, store.ProjectSyncInput{
		Name:         "nt3d",
		Repo:         "https://github.com/NimbleTerminal/nt3d",
		GitSHA:       "abc123def456",
		PublicInputs: true,
		SyncedAt:     &now,
	})
	if err != nil {
		t.Fatal(err)
	}
	tID, err := st.UpsertTarget(ctx, pID, "nimbleterminal.dev/nt3d/mesh/gltf", "FuzzDecode")
	if err != nil {
		t.Fatal(err)
	}
	cID, err := st.RecordCrash(ctx, store.Crash{
		TargetID:         tID,
		FirstSeen:        now,
		LastSeen:         now,
		Stack:            "panic: slice bounds out of range [::-1]\ngithub.com/qmuntal/gltf@v0.29.0/decoder.go:151",
		StackFingerprint: "fp123",
		InputHash:        "deadbeef12345678",
		MinimizedInput:   "testdata/fuzz/FuzzDecode/deadbeef12345678",
		InputBytes:       []byte("go test fuzz v1\n[]byte(\"bad\")\n"),
		GitSHA:           "abc123def456",
		GoVersion:        "go1.25.0",
		BlamedModule:     "github.com/qmuntal/gltf",
		BlamedVersion:    "v0.29.0",
		BlamedFile:       "decoder.go",
		BlamedLine:       151,
	})
	if err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(api.NewServer(st, nil))
	defer ts.Close()

	// 1. ntfuzz crash link <id> --issue URL
	var linkOut bytes.Buffer
	linkCmd := newCrashCmd()
	linkCmd.SetOut(&linkOut)
	linkCmd.SetArgs([]string{"link", fmt.Sprint(cID), "--api", ts.URL, "--issue", "https://github.com/qmuntal/gltf/issues/99"})
	if err := linkCmd.Execute(); err != nil {
		t.Fatalf("crash link failed: %v", err)
	}
	if !strings.Contains(linkOut.String(), "Linked crash #1 to https://github.com/qmuntal/gltf/issues/99") {
		t.Fatalf("unexpected crash link output: %s", linkOut.String())
	}

	// 2. ntfuzz crash list shows REPORTED, blame, and issue URL
	var listOut bytes.Buffer
	listCmd := newCrashCmd()
	listCmd.SetOut(&listOut)
	listCmd.SetArgs([]string{"list", "--api", ts.URL})
	if err := listCmd.Execute(); err != nil {
		t.Fatalf("crash list failed: %v", err)
	}
	for _, want := range []string{
		"REPORTED",
		"blame=github.com/qmuntal/gltf@v0.29.0 (https://github.com/qmuntal/gltf/blob/v0.29.0/decoder.go#L151)",
		"issue=https://github.com/qmuntal/gltf/issues/99",
	} {
		if !strings.Contains(listOut.String(), want) {
			t.Errorf("crash list missing %q:\n%s", want, listOut.String())
		}
	}

	// 3. ntfuzz crash report <id> --public-url
	var reportOut bytes.Buffer
	reportCmd := newCrashCmd()
	reportCmd.SetOut(&reportOut)
	reportCmd.SetArgs([]string{"report", fmt.Sprint(cID), "--api", ts.URL, "--public-url", "https://fuzz.nimbleterminal.com"})
	if err := reportCmd.Execute(); err != nil {
		t.Fatalf("crash report failed: %v", err)
	}
	for _, want := range []string{
		"# Crash #1: nt3d FuzzDecode",
		"https://fuzz.nimbleterminal.com/crashes/1",
		"https://fuzz.nimbleterminal.com/api/crashes/1/input",
		"go test ./mesh/gltf -run 'FuzzDecode/deadbeef12345678'",
		"go test fuzz v1",
	} {
		if !strings.Contains(reportOut.String(), want) {
			t.Errorf("crash report missing %q:\n%s", want, reportOut.String())
		}
	}
}
