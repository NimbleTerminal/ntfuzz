package store

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func FuzzReadSnapshotManifest(f *testing.F) {
	f.Add([]byte(`{"file":"snapshot-100.duckdb","published_at":"2026-11-05T12:00:00Z","schema_version":2}`))
	f.Add([]byte(`{"file":"../snapshot-100.duckdb","published_at":"2026-11-05T12:00:00Z","schema_version":2}`))
	f.Add([]byte(`{"file":"sub\\snapshot-100.duckdb","published_at":"2026-11-05T12:00:00Z","schema_version":2}`))
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
