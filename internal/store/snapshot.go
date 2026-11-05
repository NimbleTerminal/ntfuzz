package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Snapshot identifies a completed, immutable database generation. The manifest
// is published only after DuckDB has detached and closed the copied database.
type Snapshot struct {
	File          string    `json:"file"`
	PublishedAt   time.Time `json:"published_at"`
	SchemaVersion int       `json:"schema_version"`
}

// SnapshotMetricsRecorder records snapshot publication latency and outcomes.
type SnapshotMetricsRecorder interface {
	RecordSnapshotPublication(duration time.Duration, err error)
}

// SetMetrics attaches an optional recorder for snapshot publication metrics.
func (s *Store) SetMetrics(m SnapshotMetricsRecorder) {
	if s == nil {
		return
	}
	s.metricsMu.Lock()
	s.metrics = m
	s.metricsMu.Unlock()
}

func (s *Store) getMetrics() SnapshotMetricsRecorder {
	if s == nil {
		return nil
	}
	s.metricsMu.RLock()
	m := s.metrics
	s.metricsMu.RUnlock()
	return m
}

// PublishSnapshot copies the database through the writer's own connection.
// It never copies a live database file or its WAL. Store mutations briefly wait
// while COPY takes its consistent source view; fuzz subprocesses keep running.
func (s *Store) PublishSnapshot(ctx context.Context, dir string) (snap Snapshot, err error) {
	start := time.Now()
	defer func() {
		if m := s.getMetrics(); m != nil {
			m.RecordSnapshotPublication(time.Since(start), err)
		}
	}()
	if s.readonly != nil {
		return Snapshot{}, ErrReadOnly
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return Snapshot{}, err
	}
	meta := Snapshot{File: fmt.Sprintf("snapshot-%d.duckdb", time.Now().UTC().UnixNano()), PublishedAt: time.Now().UTC(), SchemaVersion: CurrentSchemaVersion}
	path := filepath.Join(dir, meta.File)
	s.mu.Lock()
	err = s.copySnapshotLocked(ctx, path)
	s.mu.Unlock()
	if err != nil {
		_ = os.Remove(path)
		_ = os.Remove(path + ".wal")
		return Snapshot{}, err
	}
	if err := os.Chmod(path, 0444); err != nil {
		return Snapshot{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return Snapshot{}, err
	}
	err = f.Sync()
	_ = f.Close()
	if err != nil {
		return Snapshot{}, err
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return Snapshot{}, err
	}
	manifest, err := os.CreateTemp(dir, ".manifest-*")
	if err != nil {
		return Snapshot{}, err
	}
	defer func() { _ = os.Remove(manifest.Name()) }()
	if _, err = manifest.Write(data); err == nil {
		err = manifest.Chmod(0644)
	}
	if err == nil {
		err = manifest.Sync()
	}
	closeErr := manifest.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return Snapshot{}, err
	}
	if err = os.Rename(manifest.Name(), filepath.Join(dir, "current.json")); err != nil {
		return Snapshot{}, err
	}
	if directory, e := os.Open(dir); e == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	// Keep a predecessor for recovery and bound disk usage. Open readers retain
	// their generation until refresh; a failed refresh keeps the last good reader.
	files, _ := filepath.Glob(filepath.Join(dir, "snapshot-*.duckdb"))
	sort.Strings(files)
	for len(files) > 2 {
		_ = os.Remove(files[0])
		files = files[1:]
	}
	return meta, nil
}
func (s *Store) copySnapshotLocked(ctx context.Context, path string) error {
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close() }()
	var source string
	if err := conn.QueryRowContext(ctx, "SELECT current_database()").Scan(&source); err != nil {
		return err
	}
	if _, err := conn.ExecContext(ctx, "ATTACH "+sqlLiteral(path)+" AS ntfuzz_publish"); err != nil {
		return err
	}
	attached := true
	defer func() {
		if attached {
			_, _ = conn.ExecContext(context.Background(), "DETACH ntfuzz_publish")
		}
	}()
	if _, err := conn.ExecContext(ctx, `COPY FROM DATABASE "`+strings.ReplaceAll(source, `"`, `""`)+`" TO ntfuzz_publish`); err != nil {
		return err
	}
	// Explicit detach errors matter: the file cannot be published while open.
	_, err = conn.ExecContext(ctx, "DETACH ntfuzz_publish")
	if err == nil {
		attached = false
	}
	return err
}

// ReadSnapshot rejects paths escaping the publish directory. Only a regular
// generation file with the current schema may become a viewer candidate.
func ReadSnapshot(dir string) (Snapshot, error) {
	var meta Snapshot
	data, err := os.ReadFile(filepath.Join(dir, "current.json"))
	if err != nil {
		return meta, err
	}
	if len(data) > 4096 {
		return meta, errors.New("snapshot manifest exceeds limit")
	}
	if err = json.Unmarshal(data, &meta); err != nil {
		return meta, err
	}
	if filepath.Base(meta.File) != meta.File || strings.ContainsAny(meta.File, `/\`) || !strings.HasPrefix(meta.File, "snapshot-") || !strings.HasSuffix(meta.File, ".duckdb") || meta.PublishedAt.IsZero() || meta.SchemaVersion != CurrentSchemaVersion {
		return meta, errors.New("invalid snapshot manifest")
	}
	info, err := os.Lstat(filepath.Join(dir, meta.File))
	if err != nil {
		return meta, err
	}
	if !info.Mode().IsRegular() {
		return meta, errors.New("snapshot is not a regular file")
	}
	return meta, nil
}
