package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"nimbleterminal.dev/ntfuzz/internal/store"
)

func FuzzParseOpenParamAndFixRequest(f *testing.F) {
	f.Add("true", "1", `{"issue_url":"https://github.com/org/repo/issues/1"}`)
	f.Add("false", "42", `{}`)
	f.Add("all", "-5", `{"unknown_field":true}`)
	f.Add("invalid", "abc", `not-json`)
	f.Add("", "0", "")

	snapSrv := NewSnapshotServer(nil, store.Snapshot{
		File:          "snapshot-1.duckdb",
		PublishedAt:   time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC),
		SchemaVersion: store.CurrentSchemaVersion,
	})
	liveSrv := NewServer(nil, nil)

	f.Fuzz(func(t *testing.T, openRaw, idRaw, body string) {
		if len(openRaw)+len(idRaw)+len(body) > 16*1024 {
			return
		}

		openOnly, err := parseOpenParam(openRaw)
		switch strings.TrimSpace(strings.ToLower(openRaw)) {
		case "", "true", "1":
			if err != nil || !openOnly {
				t.Fatalf("parseOpenParam(%q) = (%v, %v), want (true, nil)", openRaw, openOnly, err)
			}
		case "false", "0", "all":
			if err != nil || openOnly {
				t.Fatalf("parseOpenParam(%q) = (%v, %v), want (false, nil)", openRaw, openOnly, err)
			}
		default:
			if err == nil {
				t.Fatalf("parseOpenParam(%q) unexpectedly succeeded", openRaw)
			}
		}

		// Snapshot server must never allow POST /api/crashes/{id}/fix.
		escapedID := url.PathEscape(idRaw)
		if escapedID != "" && !strings.Contains(escapedID, "/") {
			req := httptest.NewRequest(http.MethodPost, "/api/crashes/"+escapedID+"/fix", strings.NewReader(body))
			rec := httptest.NewRecorder()
			snapSrv.ServeHTTP(rec, req)
			if rec.Code == http.StatusOK {
				t.Fatalf("snapshot server allowed POST /api/crashes/%s/fix", escapedID)
			}
		}

		// Exercise handleFixCrash input validation with a non-positive ID or malformed JSON.
		req := httptest.NewRequest(http.MethodPost, "/api/crashes/0/fix", bytes.NewReader([]byte(body)))
		req.SetPathValue("id", "0")
		rec := httptest.NewRecorder()
		liveSrv.handleFixCrash(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("handleFixCrash with id=0 returned status %d, want 400", rec.Code)
		}
	})
}
