// Package viewer serves immutable snapshots without opening the live writer.
package viewer

import (
	"context"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"nimbleterminal.dev/ntfuzz/internal/api"
	"nimbleterminal.dev/ntfuzz/internal/metrics"
	"nimbleterminal.dev/ntfuzz/internal/store"
)

type Viewer struct {
	dir        string
	mu         sync.RWMutex
	current    store.Snapshot
	db         *store.Store
	handler    http.Handler
	metrics    *metrics.Metrics
	refreshes  int64
	refreshErr int64
}

func Open(dir string) (*Viewer, error) {
	return OpenWithMetrics(dir, nil)
}

func OpenWithMetrics(dir string, m *metrics.Metrics) (*Viewer, error) {
	v := &Viewer{dir: dir, metrics: m}
	if err := v.Refresh(); err != nil {
		return nil, err
	}
	return v, nil
}

// SetMetrics attaches a Prometheus metrics collector to the viewer and syncs
// the currently loaded snapshot timestamp and prior refresh counts.
func (v *Viewer) SetMetrics(m *metrics.Metrics) {
	if v == nil || m == nil {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.metrics == m {
		return
	}
	v.metrics = m
	if !v.current.PublishedAt.IsZero() {
		m.SetSnapshotPublishedAt(v.current.PublishedAt)
	}
	m.AddSnapshotRefreshes(v.refreshes, v.refreshErr)
}

func (v *Viewer) recordRefreshResult(publishedAt time.Time, err error) {
	v.mu.Lock()
	if err != nil {
		v.refreshErr++
	} else {
		v.refreshes++
	}
	m := v.metrics
	v.mu.Unlock()
	if m != nil {
		m.RecordSnapshotRefresh(publishedAt, err)
	}
}

func (v *Viewer) Refresh() (err error) {
	var publishedAt time.Time
	defer func() {
		v.recordRefreshResult(publishedAt, err)
	}()
	meta, err := store.ReadSnapshot(v.dir)
	if err != nil {
		return err
	}
	publishedAt = meta.PublishedAt
	v.mu.RLock()
	same := meta.File == v.current.File
	v.mu.RUnlock()
	if same {
		return nil
	}
	db, err := store.OpenReadOnly(filepath.Join(v.dir, meta.File))
	if err != nil {
		return err
	}
	// Validate all read surfaces before replacing the last known-good generation.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err = db.GetFleetStats(ctx); err == nil {
		_, err = db.ListProjects(ctx)
	}
	if _, e := db.ListTargets(ctx, ""); err == nil {
		err = e
	}
	if err != nil {
		_ = db.Close()
		return err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	old := v.db
	v.db = db
	v.current = meta
	v.handler = api.NewSnapshotServer(db, meta)
	if old != nil {
		_ = old.Close()
	}
	return nil
}
func (v *Viewer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	if len(r.URL.RawQuery) > 4096 {
		http.Error(w, "query too large", http.StatusRequestURITooLong)
		return
	}
	v.handler.ServeHTTP(w, r)
}
func (v *Viewer) Close() error { v.mu.Lock(); defer v.mu.Unlock(); return v.db.Close() }
func (v *Viewer) Watch(ctx context.Context, interval time.Duration) {
	timer := time.NewTicker(interval)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			if err := v.Refresh(); err != nil {
				slog.Warn("snapshot refresh failed; retaining previous generation", "error", err)
			}
		}
	}
}
