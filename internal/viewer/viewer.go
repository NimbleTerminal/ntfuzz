// Package viewer serves immutable snapshots without opening the live writer.
package viewer

import (
	"context"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"github.com/NimbleTerminal/ntfuzz/internal/api"
	"github.com/NimbleTerminal/ntfuzz/internal/store"
)

type Viewer struct {
	dir     string
	mu      sync.RWMutex
	current store.Snapshot
	db      *store.Store
	handler http.Handler
}

func Open(dir string) (*Viewer, error) {
	v := &Viewer{dir: dir}
	if err := v.Refresh(); err != nil {
		return nil, err
	}
	return v, nil
}
func (v *Viewer) Refresh() error {
	meta, err := store.ReadSnapshot(v.dir)
	if err != nil {
		return err
	}
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
		db.Close()
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
