package store

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func FuzzReadSnapshotManifest(f *testing.F) {
	f.Add([]byte(`{"file":"snapshot-100.duckdb","published_at":"2026-11-05T12:00:00Z","schema_version":6}`))
	f.Add([]byte(`{"file":"../snapshot-100.duckdb","published_at":"2026-11-05T12:00:00Z","schema_version":6}`))
	f.Add([]byte(`{"file":"sub\\snapshot-100.duckdb","published_at":"2026-11-05T12:00:00Z","schema_version":6}`))
	f.Add([]byte(`{"file":"snapshot-100.duckdb","published_at":"2026-11-05T12:00:00Z","schema_version":999}`))
	f.Add([]byte(`{}`))

	dir, err := os.MkdirTemp("", "ntfuzz-fuzz-snap-*")
	if err != nil {
		f.Fatalf("MkdirTemp: %v", err)
	}
	f.Cleanup(func() {
		_ = os.RemoveAll(dir)
	})

	validFile := filepath.Join(dir, "snapshot-100.duckdb")
	if err := os.WriteFile(validFile, []byte("placeholder"), 0o644); err != nil {
		f.Fatalf("WriteFile: %v", err)
	}
	manifestPath := filepath.Join(dir, "current.json")

	f.Fuzz(func(t *testing.T, manifest []byte) {
		if len(manifest) > 16*1024 {
			return
		}
		if err := os.WriteFile(manifestPath, manifest, 0o644); err != nil {
			t.Fatalf("WriteFile current.json: %v", err)
		}

		snap, err := ReadSnapshot(dir)
		if err != nil {
			return
		}

		if snap.SchemaVersion != CurrentSchemaVersion {
			t.Fatalf("ReadSnapshot accepted wrong SchemaVersion %d", snap.SchemaVersion)
		}
		if snap.PublishedAt.IsZero() {
			t.Fatalf("ReadSnapshot accepted zero PublishedAt")
		}
		if filepath.Base(snap.File) != snap.File || strings.ContainsAny(snap.File, `/\`) {
			t.Fatalf("ReadSnapshot accepted path with directory components: %q", snap.File)
		}
		if !strings.HasPrefix(snap.File, "snapshot-") || !strings.HasSuffix(snap.File, ".duckdb") {
			t.Fatalf("ReadSnapshot accepted unexpected filename %q", snap.File)
		}
	})
}

func FuzzRecordAndGetCrash(f *testing.F) {
	f.Add([]byte("go test fuzz v1\n[]byte(\"seed\")\n"), "github.com/qmuntal/gltf", "v0.29.0", "decoder.go", 151)
	f.Add([]byte(""), "", "", "", 0)

	st, err := Open(":memory:")
	if err != nil {
		f.Fatalf("Open: %v", err)
	}
	f.Cleanup(func() {
		_ = st.Close()
	})

	ctx := context.Background()
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	pID, err := st.SyncProject(ctx, ProjectSyncInput{
		Name:         "fuzz-proj",
		Repo:         "./repo",
		PublicInputs: true,
		SyncedAt:     &now,
	})
	if err != nil {
		f.Fatalf("SyncProject: %v", err)
	}
	tID, err := st.UpsertTarget(ctx, pID, "example.com/fuzz", "FuzzTarget")
	if err != nil {
		f.Fatalf("UpsertTarget: %v", err)
	}

	f.Fuzz(func(t *testing.T, input []byte, mod, ver, file string, line int) {
		if len(input) > 70*1024 || len(mod)+len(ver)+len(file) > 512 {
			return
		}
		if line < 0 {
			line = 0
		}
		cID, err := st.RecordCrash(ctx, Crash{
			TargetID:       tID,
			FirstSeen:      now,
			LastSeen:       now,
			Stack:          "panic: fuzz",
			InputHash:      "hash-" + mod,
			MinimizedInput: "path",
			InputBytes:     input,
			BlamedModule:   mod,
			BlamedVersion:  ver,
			BlamedFile:     file,
			BlamedLine:     line,
		})
		if err != nil {
			t.Fatalf("RecordCrash: %v", err)
		}
		got, err := st.GetCrash(ctx, cID)
		if err != nil {
			t.Fatalf("GetCrash: %v", err)
		}
		if len(got.InputBytes) > MaxCrashInputBytes {
			t.Fatalf("stored InputBytes exceeded MaxCrashInputBytes: %d", len(got.InputBytes))
		}
		if len(input) > MaxCrashInputBytes && !got.InputTruncated {
			t.Fatalf("expected InputTruncated=true for len=%d", len(input))
		}
	})
}
