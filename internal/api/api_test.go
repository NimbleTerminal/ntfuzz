package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"nimbleterminal.dev/ntfuzz/internal/orchestrator"
	"nimbleterminal.dev/ntfuzz/internal/store"
)

type stubWorkerStatus struct {
	jobs []orchestrator.ActiveJob
}

func (s stubWorkerStatus) ActiveJobs() []orchestrator.ActiveJob { return s.jobs }
func (s stubWorkerStatus) WorkerCount() int                     { return 2 }

func TestAPIServerAndClient(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "api_test.duckdb"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer func() { _ = st.Close() }()

	now := time.Now().UTC()
	pID, err := st.SyncProject(ctx, store.ProjectSyncInput{
		Name:     "demo-codec",
		Repo:     "./examples/demo-codec",
		Branch:   "main",
		GitSHA:   "a1b2c3d4e5f6",
		Dirty:    false,
		SyncedAt: &now,
	})
	if err != nil {
		t.Fatalf("SyncProject: %v", err)
	}
	tID, err := st.UpsertTarget(ctx, pID, "example.com/demo/codec", "FuzzDecodePacket")
	if err != nil {
		t.Fatalf("UpsertTarget: %v", err)
	}
	_, err = st.InsertRun(ctx, store.Run{
		Diagnostic:     "bounded failure detail",
		TargetID:       tID,
		Started:        now,
		DurationSec:    4.2,
		Execs:          84000,
		ExecsPerSec:    20000,
		NewInteresting: 5,
		CorpusCount:    19,
		Crashes:        1,
		CoveragePct:    81.4,
		Status:         "crash",
		Worker:         "local-1",
		GitSHA:         "a1b2c3d4e5f6",
		GoVersion:      "go1.27",
	})
	if err != nil {
		t.Fatalf("InsertRun: %v", err)
	}
	crashID, err := st.RecordCrash(ctx, store.Crash{
		TargetID:         tID,
		FirstSeen:        now,
		LastSeen:         now,
		Stack:            "panic: malformed packet header",
		StackFingerprint: "fp-malformed-header",
		InputHash:        "cafebabe00112233",
		MinimizedInput:   ".ntfuzz/corpus/demo-codec/crashes/FuzzDecodePacket/cafebabe00112233",
		GitSHA:           "a1b2c3d4e5f6",
		GoVersion:        "go1.27",
	})
	if err != nil {
		t.Fatalf("RecordCrash: %v", err)
	}

	srv := NewServer(st, stubWorkerStatus{
		jobs: []orchestrator.ActiveJob{
			{
				WorkerID:  "local-1",
				Project:   "demo-codec",
				Package:   "example.com/demo/codec",
				Target:    "FuzzDecodePacket",
				FuzzTime:  "10s",
				GitSHA:    "a1b2c3d4e5f6",
				StartedAt: now,
				Status:    "fuzzing",
			},
		},
	})
	ts := httptest.NewServer(srv)
	defer ts.Close()

	client := NewClient(ts.URL)

	// Verify no wildcard CORS header is emitted
	rawResp, err := http.Get(ts.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	_ = rawResp.Body.Close()
	if got := rawResp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf("expected no Access-Control-Allow-Origin header, got %q", got)
	}

	status, err := client.GetStatus(ctx)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if status.OpenCrashes != 1 || status.RunsLast24h != 1 || status.ActiveWorkers != 1 || status.SchemaVersion != store.CurrentSchemaVersion {
		t.Fatalf("unexpected status: %+v", status)
	}

	projects, err := client.GetProjects(ctx)
	if err != nil {
		t.Fatalf("GetProjects: %v", err)
	}
	if len(projects) != 1 || projects[0].Name != "demo-codec" || projects[0].GitSHA != "a1b2c3d4e5f6" {
		t.Fatalf("unexpected projects: %+v", projects)
	}

	targets, err := client.GetTargets(ctx, "demo-codec")
	if err != nil {
		t.Fatalf("GetTargets: %v", err)
	}
	if len(targets) != 1 || targets[0].Name != "FuzzDecodePacket" || targets[0].CorpusCount != 19 {
		t.Fatalf("unexpected targets: %+v", targets)
	}

	runs, err := client.GetRuns(ctx, strconv.FormatInt(tID, 10), 20)
	if err != nil {
		t.Fatalf("GetRuns: %v", err)
	}
	if len(runs) != 1 || runs[0].Diagnostic != "bounded failure detail" || runs[0].Execs != 84000 || runs[0].GitSHA != "a1b2c3d4e5f6" {
		t.Fatalf("unexpected runs: %+v", runs)
	}

	crashes, err := client.GetCrashes(ctx, true, "")
	if err != nil {
		t.Fatalf("GetCrashes: %v", err)
	}
	if len(crashes) != 1 || crashes[0].InputHash != "cafebabe00112233" {
		t.Fatalf("unexpected crashes: %+v", crashes)
	}

	// Test invalid ?open= parameter returns 400
	badOpenResp, err := http.Get(ts.URL + "/api/crashes?open=invalid_bool")
	if err != nil {
		t.Fatal(err)
	}
	_ = badOpenResp.Body.Close()
	if badOpenResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid ?open= parameter, got %d", badOpenResp.StatusCode)
	}

	// Test malformed JSON on POST /api/crashes/{id}/fix returns 400
	badJSONResp, err := http.Post(ts.URL+"/api/crashes/1/fix", "application/json", strings.NewReader("{bad json"))
	if err != nil {
		t.Fatal(err)
	}
	_ = badJSONResp.Body.Close()
	if badJSONResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 on malformed JSON body, got %d", badJSONResp.StatusCode)
	}

	// Test unknown crash ID on POST /api/crashes/{id}/fix returns 404
	notFoundResp, err := http.Post(ts.URL+"/api/crashes/99999/fix", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	_ = notFoundResp.Body.Close()
	if notFoundResp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown crash ID, got %d", notFoundResp.StatusCode)
	}

	// Mark crash fixed via POST /api/crashes/{id}/fix
	if err := client.MarkCrashFixed(ctx, crashID, "https://github.com/example/codec/issues/7"); err != nil {
		t.Fatalf("MarkCrashFixed: %v", err)
	}
	openAfterFix, err := client.GetCrashes(ctx, true, "")
	if err != nil || len(openAfterFix) != 0 {
		t.Fatalf("expected 0 open crashes after MarkCrashFixed, got %+v (err=%v)", openAfterFix, err)
	}
	allCrashes, err := client.GetCrashes(ctx, false, "")
	if err != nil || len(allCrashes) != 1 || allCrashes[0].FixedAt == nil {
		t.Fatalf("expected 1 total crash when open=false, got %+v (err=%v)", allCrashes, err)
	}

	history, err := client.GetCoverageHistory(ctx, strconv.FormatInt(tID, 10))
	if err != nil {
		t.Fatalf("GetCoverageHistory: %v", err)
	}
	if len(history) != 1 || history[0].CoveragePct != 81.4 || history[0].CorpusCount != 19 {
		t.Fatalf("unexpected history: %+v", history)
	}

	for _, q := range []string{StatsQueryThroughput, StatsQueryCoverage, StatsQueryOutcomes} {
		stats, err := client.GetStats(ctx, q, StatsWindow24h, "demo-codec", "")
		if err != nil {
			t.Fatalf("GetStats(%s): %v", q, err)
		}
		if stats.Query != q || stats.Window != StatsWindow24h || stats.TotalRuns != 1 || stats.TotalExecs != 84000 || len(stats.Buckets) != 1 {
			t.Fatalf("unexpected stats for %s: %+v", q, stats)
		}
	}

	// Invalid stats query or window returns 400
	badStatsResp, err := http.Get(ts.URL + "/api/stats/not-a-query?window=24h")
	if err != nil {
		t.Fatal(err)
	}
	_ = badStatsResp.Body.Close()
	if badStatsResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid stats query, got %d", badStatsResp.StatusCode)
	}
}

func TestRevisionAndReplayEndpoints(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "revisions.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()
	now := time.Now().UTC()
	if _, err = st.SyncProject(ctx, store.ProjectSyncInput{Name: "repo", Repo: "local", GitSHA: "abc", SyncedAt: &now}); err != nil {
		t.Fatal(err)
	}
	for _, handler := range []http.Handler{NewServer(st, nil), NewSnapshotServer(st, store.Snapshot{PublishedAt: now})} {
		for _, path := range []string{"/api/stats/revisions?window=24h", "/api/crash-replays"} {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: %d %s", path, rec.Code, rec.Body.String())
			}
			if strings.Contains(path, "revisions") && !strings.Contains(rec.Body.String(), `"baseline":true`) {
				t.Fatalf("missing baseline: %s", rec.Body.String())
			}
		}
		for _, path := range []string{"/api/stats/revisions?window=bogus", "/api/stats/revisions?target=1"} {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
			if rec.Code != 400 {
				t.Fatalf("invalid stats query accepted: %s", path)
			}
		}
	}
}

func TestCrashSharePermalinkInputIssueAndReport(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "crash_share.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

	// Project 1: nt3d with public_inputs = true
	pPublicID, err := st.SyncProject(ctx, store.ProjectSyncInput{
		Name:         "nt3d",
		Repo:         "https://github.com/NimbleTerminal/nt3d",
		GitSHA:       "9a8b7c6d5e4f",
		PublicInputs: true,
		SyncedAt:     &now,
	})
	if err != nil {
		t.Fatal(err)
	}
	tPublicID, err := st.UpsertTarget(ctx, pPublicID, "nimbleterminal.dev/nt3d/mesh/gltf", "FuzzDecode")
	if err != nil {
		t.Fatal(err)
	}

	corpusData := []byte("go test fuzz v1\n[]byte(\"glTF\\x02\\x00\\x00\\x00\\xff\\xff\")\n")
	rawCrash23Stack := "--- FAIL: FuzzDecode (0.04s)\n" +
		"    --- FAIL: FuzzDecode (0.00s)\n" +
		"        testing.go:2078: panic: runtime error: slice bounds out of range [::-1]\n" +
		"        goroutine 22 [running]:\n" +
		"        testing.tRunner.func1.2({0x6d9960, 0xc00018a018})\n" +
		"        \t/usr/local/go/src/testing/testing.go:1967 +0x219\n" +
		"        panic({0x6d9960?, 0xc00018a018?})\n" +
		"        \t/usr/local/go/src/runtime/panic.go:792 +0x132\n" +
		"        github.com/qmuntal/gltf.(*Decoder).decodeBuffer(0xc0001a2000, 0x0)\n" +
		"        \t/var/lib/ntfuzz/.cache/go-mod/github.com/qmuntal/gltf@v0.29.0/decoder.go:151 +0x359\n" +
		"        github.com/qmuntal/gltf.(*Decoder).Decode(0xc0001a2000, 0xc00019c000)\n" +
		"        \t/var/lib/ntfuzz/.cache/go-mod/github.com/qmuntal/gltf@v0.29.0/decoder.go:74 +0x165\n" +
		"        nimbleterminal.dev/nt3d/mesh/gltf.Decode({0x7f9a040d5088?, 0xc000182048?})\n" +
		"        \t/var/lib/ntfuzz/.ntfuzz/repos/nt3d/mesh/gltf/gltf.go:108 +0x9f"

	// Insert with raw /var/lib/ntfuzz/... stack and empty BlamedModule to verify render-time scrubbing and attribution.
	pubCrashID, err := st.RecordCrash(ctx, store.Crash{
		TargetID:         tPublicID,
		FirstSeen:        now.Add(-time.Hour),
		LastSeen:         now,
		Stack:            rawCrash23Stack,
		StackFingerprint: "aabbccddeeff001122334455",
		InputHash:        "deadbeefcafebabe0123456789abcdef",
		MinimizedInput:   ".ntfuzz/corpus/nt3d/crashes/FuzzDecode/deadbeefcafebabe0123456789abcdef",
		InputBytes:       corpusData,
		GitSHA:           "9a8b7c6d5e4f",
		Dirty:            false,
		GoVersion:        "go1.25.0",
	})
	if err != nil {
		t.Fatal(err)
	}

	// Project 2: private-proj with public_inputs = false
	pPrivID, err := st.SyncProject(ctx, store.ProjectSyncInput{
		Name:         "private-proj",
		Repo:         "https://github.com/example/private-proj",
		GitSHA:       "111122223333",
		PublicInputs: false,
		SyncedAt:     &now,
	})
	if err != nil {
		t.Fatal(err)
	}
	tPrivID, err := st.UpsertTarget(ctx, pPrivID, "example.com/private-proj/pkg", "FuzzPriv")
	if err != nil {
		t.Fatal(err)
	}
	privCrashID, err := st.RecordCrash(ctx, store.Crash{
		TargetID:         tPrivID,
		FirstSeen:        now,
		LastSeen:         now,
		Stack:            "panic: local panic\nexample.com/private-proj/pkg/priv.go:12",
		StackFingerprint: "11223344556677889900aabb",
		InputHash:        "privhash12345678",
		MinimizedInput:   ".ntfuzz/corpus/private-proj/crashes/FuzzPriv/privhash12345678",
		InputBytes:       corpusData,
		GitSHA:           "111122223333",
		GoVersion:        "go1.25.0",
	})
	if err != nil {
		t.Fatal(err)
	}

	liveSrv := httptest.NewServer(NewServer(st, nil))
	defer liveSrv.Close()
	snapSrv := httptest.NewServer(NewSnapshotServer(st, store.Snapshot{
		File:          "snapshot-1.duckdb",
		PublishedAt:   now,
		SchemaVersion: store.CurrentSchemaVersion,
	}))
	defer snapSrv.Close()

	liveClient := NewClient(liveSrv.URL)
	snapClient := NewClient(snapSrv.URL)

	// 1. GET /api/crashes/{id} returns one crash record or 404 JSON error.
	gotCrash, err := snapClient.GetCrash(ctx, pubCrashID)
	if err != nil {
		t.Fatalf("GetCrash(%d): %v", pubCrashID, err)
	}
	if gotCrash.BlamedModule != "github.com/qmuntal/gltf" ||
		gotCrash.BlamedURL != "https://github.com/qmuntal/gltf/blob/v0.29.0/decoder.go#L151" ||
		!gotCrash.PublicInputs {
		t.Fatalf("unexpected crash DTO: %+v", gotCrash)
	}
	notFoundRec, err := http.Get(snapSrv.URL + "/api/crashes/99999")
	if err != nil {
		t.Fatal(err)
	}
	_ = notFoundRec.Body.Close()
	if notFoundRec.StatusCode != http.StatusNotFound || !strings.HasPrefix(notFoundRec.Header.Get("Content-Type"), "application/json") {
		t.Fatalf("expected 404 JSON error for missing crash, got %d %s", notFoundRec.StatusCode, notFoundRec.Header.Get("Content-Type"))
	}

	// 2. POST /api/crashes/{id}/issue sets issue_url on open crash in serve mode without fixing it; rejected in view mode.
	advisoryURL := "https://github.com/qmuntal/gltf/security/advisories/GHSA-1234-5678-90ab"
	if err := snapClient.SetCrashIssue(ctx, pubCrashID, advisoryURL); err == nil {
		t.Fatal("expected SetCrashIssue to be rejected in read-only snapshot mode")
	}
	if err := liveClient.SetCrashIssue(ctx, pubCrashID, "javascript:alert(1)"); err == nil {
		t.Fatal("expected invalid scheme in SetCrashIssue to be rejected")
	}
	if err := liveClient.SetCrashIssue(ctx, pubCrashID, advisoryURL); err != nil {
		t.Fatalf("liveClient.SetCrashIssue: %v", err)
	}
	afterIssue, err := liveClient.GetCrash(ctx, pubCrashID)
	if err != nil {
		t.Fatal(err)
	}
	if afterIssue.FixedAt != nil || afterIssue.IssueURL != advisoryURL {
		t.Fatalf("expected open-reported crash with FixedAt=nil and IssueURL=%q, got %+v", advisoryURL, afterIssue)
	}

	// 3. GET /api/crashes/{id}/input honors public_inputs in view mode and always serves in serve mode.
	inputResp, err := http.Get(fmt.Sprintf("%s/api/crashes/%d/input", snapSrv.URL, pubCrashID))
	if err != nil {
		t.Fatal(err)
	}
	_ = inputResp.Body.Close()
	if inputResp.StatusCode != http.StatusOK ||
		inputResp.Header.Get("Content-Disposition") != "attachment; filename=deadbeefcafebabe0123456789abcdef" {
		t.Fatalf("unexpected public input headers: status=%d cd=%q", inputResp.StatusCode, inputResp.Header.Get("Content-Disposition"))
	}
	downloaded, err := snapClient.GetCrashInput(ctx, pubCrashID)
	if err != nil || string(downloaded) != string(corpusData) {
		t.Fatalf("GetCrashInput public mismatch: %q (err=%v)", downloaded, err)
	}

	// Private project input returns 403 in view mode, but 200 in serve mode.
	privSnapResp, err := http.Get(fmt.Sprintf("%s/api/crashes/%d/input", snapSrv.URL, privCrashID))
	if err != nil {
		t.Fatal(err)
	}
	_ = privSnapResp.Body.Close()
	if privSnapResp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 Forbidden for public_inputs=false in view mode, got %d", privSnapResp.StatusCode)
	}
	privLiveBytes, err := liveClient.GetCrashInput(ctx, privCrashID)
	if err != nil || string(privLiveBytes) != string(corpusData) {
		t.Fatalf("expected serve mode to always serve input bytes, got %q (err=%v)", privLiveBytes, err)
	}

	// 4. GET /crashes/{id} serves server-rendered HTML in both serve and view modes.
	for modeName, baseURL := range map[string]string{"serve": liveSrv.URL, "view": snapSrv.URL} {
		resp, err := http.Get(fmt.Sprintf("%s/crashes/%d", baseURL, pubCrashID))
		if err != nil {
			t.Fatal(err)
		}
		var pageBuf strings.Builder
		_, _ = io.Copy(&pageBuf, resp.Body)
		_ = resp.Body.Close()
		page := pageBuf.String()
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
			t.Fatalf("%s GET /crashes/%d: status=%d ct=%s", modeName, pubCrashID, resp.StatusCode, resp.Header.Get("Content-Type"))
		}
		for _, wantToken := range []string{
			"nt3d",
			"nimbleterminal.dev/nt3d/mesh/gltf",
			"FuzzDecode",
			"9a8b7c6d5e4f",
			"go1.25.0",
			"OPEN · REPORTED",
			advisoryURL,
			"github.com/qmuntal/gltf@v0.29.0",
			"https://github.com/qmuntal/gltf/blob/v0.29.0/decoder.go#L151",
			"<!--email_off-->",
			"<!--/email_off-->",
			"<h2>Stack Trace</h2>",
			fmt.Sprintf("/api/crashes/%d/input", pubCrashID),
			`href="/"`,
			"go test ./mesh/gltf -run &#39;FuzzDecode/deadbeefcafebabe0123456789abcdef&#39;",
		} {
			if !strings.Contains(page, wantToken) {
				t.Errorf("%s HTML page missing %q:\n%s", modeName, wantToken, page)
			}
		}
		if strings.Contains(page, "/var/lib/ntfuzz") {
			t.Errorf("%s HTML page leaked server-local /var/lib/ntfuzz path:\n%s", modeName, page)
		}
	}

	// 5. GET /api/crashes/{id}/report.md renders shareable Markdown report.
	md, err := snapClient.GetCrashReport(ctx, pubCrashID, "https://fuzz.nimbleterminal.com")
	if err != nil {
		t.Fatalf("GetCrashReport: %v", err)
	}
	for _, wantMD := range []string{
		fmt.Sprintf("# Crash #%d: nt3d FuzzDecode", pubCrashID),
		"[`github.com/qmuntal/gltf@v0.29.0`](https://github.com/qmuntal/gltf/blob/v0.29.0/decoder.go#L151)",
		"github.com/qmuntal/gltf@v0.29.0/decoder.go:151",
		"nimbleterminal.dev/nt3d/mesh/gltf/gltf.go:108",
		"go1.25.0",
		"9a8b7c6d5e4f",
		fmt.Sprintf("https://fuzz.nimbleterminal.com/crashes/%d", pubCrashID),
		fmt.Sprintf("https://fuzz.nimbleterminal.com/api/crashes/%d/input", pubCrashID),
		"go test ./mesh/gltf -run 'FuzzDecode/deadbeefcafebabe0123456789abcdef'",
		"go test fuzz v1",
	} {
		if !strings.Contains(md, wantMD) {
			t.Errorf("Markdown report missing %q:\n%s", wantMD, md)
		}
	}
	if strings.Contains(md, "/var/lib/ntfuzz") {
		t.Errorf("Markdown report leaked /var/lib/ntfuzz path:\n%s", md)
	}

	// Verify empty blame with no recognizable project frame renders as "not attributed" instead of "project code".
	unattribMD := FormatCrashReportMarkdown(Crash{
		ID:          99,
		ProjectName: "nt3d",
		Package:     "nimbleterminal.dev/nt3d/mesh/gltf",
		TargetName:  "FuzzDecode",
		Stack:       "--- FAIL: FuzzDecode (0.04s)\n    testing.go:2078: panic: boom",
	}, nil, "", false)
	if !strings.Contains(unattribMD, "- **Blamed Component:** not attributed") {
		t.Fatalf("expected unattributed crash report to say 'not attributed', got:\n%s", unattribMD)
	}

	// Reject injected public_url values (newlines, non-http schemes, markdown delimiters).
	badReportResp, err := http.Get(fmt.Sprintf("%s/api/crashes/%d/report.md?public_url=https://evil.example%%0a%%0ainjected", snapSrv.URL, pubCrashID))
	if err != nil {
		t.Fatal(err)
	}
	_ = badReportResp.Body.Close()
	if badReportResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 Bad Request for newline-injected public_url, got %d", badReportResp.StatusCode)
	}

	// When public_url is omitted, derive base URL from validated request Host + X-Forwarded-Proto.
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/crashes/%d/report.md", snapSrv.URL, pubCrashID), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = "fuzz.nimbleterminal.com"
	req.Header.Set("X-Forwarded-Proto", "https")
	hostResp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	hostBody, _ := io.ReadAll(hostResp.Body)
	_ = hostResp.Body.Close()
	if !strings.Contains(string(hostBody), fmt.Sprintf("https://fuzz.nimbleterminal.com/crashes/%d", pubCrashID)) {
		t.Fatalf("expected Host + X-Forwarded-Proto to derive https://fuzz.nimbleterminal.com/crashes/%d:\n%s", pubCrashID, string(hostBody))
	}

	// Malformed Host header containing Markdown delimiters falls back to relative URL.
	badHostReq, err := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/crashes/%d/report.md", snapSrv.URL, pubCrashID), nil)
	if err != nil {
		t.Fatal(err)
	}
	badHostReq.Host = "evil.example/)[injected](https://bad.example"
	badHostResp, err := http.DefaultClient.Do(badHostReq)
	if err != nil {
		t.Fatal(err)
	}
	badHostBody, _ := io.ReadAll(badHostResp.Body)
	_ = badHostResp.Body.Close()
	if strings.Contains(string(badHostBody), "evil.example") {
		t.Fatalf("did not expect malformed Host header to be reflected into report.md:\n%s", string(badHostBody))
	}
}

func TestProjectsSyncErrorAndRepoRedaction(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "redact.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = st.Close() }()

	_, err = st.SyncProject(ctx, store.ProjectSyncInput{
		Name:      "private-lib",
		Repo:      "https://user:ghp_abc@github.com/org/private-lib.git",
		Branch:    "main",
		SyncError: "git clone https://user:ghp_abc@github.com/org/private-lib.git: Authorization: token ghp_secret999",
	})
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(NewServer(st, nil))
	defer srv.Close()

	client := NewClient(srv.URL)
	projects, err := client.GetProjects(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(projects) != 1 {
		t.Fatalf("expected 1 project, got %d", len(projects))
	}
	p := projects[0]
	if strings.Contains(p.Repo, "ghp_abc") || p.Repo != "https://<redacted>@github.com/org/private-lib.git" {
		t.Fatalf("expected redacted Repo, got %q", p.Repo)
	}
	if strings.Contains(p.SyncError, "ghp_abc") || strings.Contains(p.SyncError, "ghp_secret999") {
		t.Fatalf("expected redacted SyncError, got %q", p.SyncError)
	}
}
