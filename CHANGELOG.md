# `ntfuzz` CHANGELOG

## [Unreleased]

### Added

- **TUI column sorting**: press `s` in the fleet view to cycle the sort column (`STATUS`, `PROJECT`, `TARGET`, `CRASH`, `EXECS/SEC`, `LAST RUN`, `COV %`, `REVISION`, `CORP`) and `S` to reverse sort direction, with `▲`/`▼` indicators in column headers and the filter bar.
- **Crash report drill-down**: press `enter` on any crash in the Open Crashes view (`c`) to open its target detail view with the full multi-line crash stack trace expanded, and `esc` to return to the crashes list.

### Changed

- **Readable coverage sparklines and tighter `TARGET` column width**: cap the fleet `TARGET` column width so `COV TREND` is visible alongside `COV %` at standard terminal widths (`>= 101` cols), scale `RenderCoverageTrend` consistently against 0–100% coverage with a trailing `↑`/`→`/`↓` direction indicator (and `·` for 0% unmeasured runs instead of auto-zooming tiny fluctuations), and add `Now / Δ / Min–Max / Corpus` context above the target-detail sparkline with vertical wrapping on narrower terminals.

### Fixed

- `FuzzValidateAndRenderBranding`: check the rendered `.brand-operator` badge instead of searching the entire HTML document for `clean.Name`, fixing false-positive crashes when the brand name is `<`, `>`, or another substring of the static HTML template.
- Crash stack extraction and fingerprinting: strip `FAIL\t<pkg>\t<duration>` and `[diagnostic tail]` footer lines in `extractCrashStack` and normalize package failure durations in `FingerprintStack` so identical failures with different package run times do not create duplicate crash records.
- Corpus coverage replay (`measureCorpusCoverage`): increase the `-cover` subprocess timeout from 15s to 45s to match the runner's build overhead allowance so packages with CGo/large dependency graphs or larger corpora do not time out at 0% coverage.

## [0.2.0] - 2026-11-05

### Added

- **API discovery**: `/api/docs` (HTML reference), `/openapi.json` (OpenAPI description of the read endpoints shared by `serve` and `view`), and `/llms.txt` (agent guide), advertised via `Link` headers on every response.
- **Run diagnostics**: failed runs retain a bounded, redacted capture of the fuzz process output, persisted in schema v3 and exposed through `GET /api/runs` and the target-detail TUI, including in published snapshots.
- **Run outcome classification**: runs now report `ok` only after coordinator progress and a standalone `PASS`; `missing_target`, `incomplete`, `canceled`, `error`, and `crash` are distinguished explicitly.
- Live subprocess regression tests covering subpackages with and without root Go files, corpus cache delivery, outcome classification, and optional representative project campaigns.
- Snapshot viewer TUI shows a **Snapshot Age** card in read-only mode in place of the always-zero Active Workers card, and the Fleet Status card derives health from the published data (CRASH or OK) instead of a PEND badge.

### Changed

- **Schema v3** adds the run `diagnostic` column. Writer migration is automatic; viewers require a matching v3 snapshot, so upgrade the writer and viewer together and publish a new snapshot before starting the new viewer.
- Migrate from the archived `github.com/NimbleMarkets/go-booba` v0.11.0 to `nimbleterminal.dev/booba` v0.12.1, which bundles ntwebterm 0.4.2 and its browser text-selection fixes (wrapped-line copy, wide-character word selection, listener cleanup).
- Canceled runs no longer count as failed targets in fleet health or project status.
- Shorten the incomplete, missing, and canceled TUI badges to fit the 7-column status column.
- Bump indirect dependencies (go-json, cpuid, mcp-go, terminfo) and the checkout/setup-go GitHub Actions to v7. quic-go stays at v0.64.1 until webtransport-go supports v0.65.
- README: correct schema version, remove the duplicate license section, move run-outcome documentation into the main body, and drop internal deployment notes.

### Fixed

- Subpackage fuzz targets were never selected: the package path now precedes forwarded test flags for both the fuzz run and the coverage replay.
- Bounded diagnostic capture keeps small outputs verbatim and only inserts the omission marker on line boundaries, so `PASS` and `--- FAIL` lines are never split by the truncation.
- Crashes whose panic text contains coordinator phrases such as "failed to unmarshal" or "no tests to run" are classified as crashes; infrastructure and missing-target detection are anchored on Go's own output lines.
- Crash records whose failing input was written before the runner deadline fired are kept instead of being discarded as errors.

## [0.1.0] - 2026-11-05

### Added

- **Single-binary CLI (`ntfuzz`)** with `serve`, `view`, `tui`, and `crash` (`list`, `fixed`) subcommands (`module nimbleterminal.dev/ntfuzz`).
- **Continuous Go Fuzzing Orchestrator (`internal/orchestrator`)**:
  - Local path and remote Git repository checkout/pull management with `git_sha`, `dirty` status, `go_version`, and visible `sync_error` tracking.
  - Multi-module native Go fuzz target discovery (`FuzzXxx(f *testing.F)`) honoring `//go:build` constraints, skipping nested `examples/` directories when discovering from a repository root, and reconciling removed targets.
  - Non-starving per-module worker queue (`moduleQueue`) that prevents concurrent `go test` runs inside the same module while keeping workers busy across other modules, with `GOMAXPROCS=1` per child process and graceful `SIGINT` + `WaitDelay` cancellation.
  - Priority scheduling for targets that recently added corpus inputs and flat-streak backoff.
  - Persistent corpus cache management (`-test.fuzzcachedir` with automatic legacy corpus layout migration), durable crash input persistence and SHA-256 content hashing, normalized stack trace fingerprinting, and isolated temporary-copy corpus statement coverage replay (`-cover`).
- **DuckDB History & Status Store (`internal/store`)**:
  - Versioned schema migrations (`schema_migrations`, v2) for `projects`, `targets`, `runs`, `crashes`, indexes, and the `latest_target_status` view with single-query `LIST` coverage trend aggregation.
  - Crash deduplication by `(target_id, input_hash)` and `(target_id, stack_fingerprint)` with automatic reopen on regression after `fixed_at`.
  - Automatic 14-day run retention pruning (preserving at least the 10 most recent runs per target).
- **Immutable Snapshot Publication & Read-Only Public Viewer (`ntfuzz view`, `internal/viewer`)**:
  - Periodic immutable DuckDB snapshot publication through the live writer, atomic manifests, and bounded generation retention.
  - Separate read-only Booba web terminal and fixed JSON API backed by `ntduckbox`, with safe generation refresh, publication timestamps, inherited listener descriptor support (`--listen-fd`), in-process terminal API transport, and removal of mutation routes and crash-fixing terminal actions in snapshot mode.
- **HTTP JSON API (`internal/api`)**:
  - Dedicated wire DTOs and converters for `GET /api/projects`, `GET /api/targets`, `GET /api/runs`, `GET /api/crashes`, `POST /api/crashes/{id}/fix`, `GET /api/coverage/history`, and `GET /api/status`.
- **Bubble Tea v2 Dashboard & Booba Browser Bridge (`internal/tui`, `internal/booba`)**:
  - Shared Bubble Tea v2 TUI model that communicates exclusively with the HTTP API (fleet overview, per-target detail view with `ntcharts` Braille coverage sparkline, and crash triage view) and adapts to resized terminal dimensions.
  - Booba browser bridge (`go-booba` + Ghostty/`ntwebterm`) serving the live TUI at `/` alongside `/api/*`, with OSC 7501 Terminal Actions via `bubblehelp`, automatic renderer selection (`renderer: 'auto'`), and live WebSocket resize forwarding through idle-timeout sessions.
  - Optional browser dashboard branding (`--brand-name`, `--brand-url`, `--brand-accent` and `NTFUZZ_BRAND_NAME`, `NTFUZZ_BRAND_URL`, `NTFUZZ_BRAND_ACCENT`) for both `ntfuzz serve` and `ntfuzz view`, linking the `NTFUZZ` wordmark to `https://github.com/NimbleTerminal/ntfuzz` while displaying an HTML-escaped operator/fleet name, validated `http`/`https` link, and `#RRGGBB` `--ntfuzz-accent` override.
- **Prometheus-Compatible Observability (`internal/metrics`)**:
  - Optional dedicated loopback-by-default `/metrics` listener (`--metrics` / `--metrics-listen`) for both `ntfuzz serve` and `ntfuzz view`, isolated from the public dashboard listener.
  - Bounded-label HTTP request counts and latency histograms (`mode`, `route`, `method`, `status_class`), excluding in-process TUI API calls while preserving `http.Hijacker`, `http.Flusher`, and `Unwrap`.
  - Browser-terminal session gauges, open/close/reject counters, session duration histograms, bounded rejection/disconnect reason categories, and PTY terminal byte counters.
  - Orchestrator snapshot publication counters/duration histograms and live worker gauges in `serve` mode, plus current snapshot age and refresh success/error counters in `view` mode.
- **Self-Fuzzing Suite**:
  - Native Go fuzz targets across `internal/orchestrator` (`FuzzParseConfig`, `FuzzParseFuzzOutput`, `FuzzFingerprintStack`), `internal/booba` (`FuzzValidateAndRenderBranding`), `internal/metrics` (`FuzzClassifyLabels`, `FuzzResolveListenAddr`), `internal/store` (`FuzzReadSnapshotManifest`), `internal/api` (`FuzzParseOpenParamAndFixRequest`), and `internal/tui` (`FuzzFormatAndRenderHelpers`).
