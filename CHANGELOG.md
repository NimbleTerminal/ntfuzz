# `ntfuzz` CHANGELOG

## [Unreleased]

- Snapshot viewer TUI: the Fleet Status card now reflects the published data (CRASH when open crashes exist, otherwise OK) instead of a misleading PEND badge, and the always-zero Active Workers card is replaced by a Snapshot Age card in read-only mode.
- Fix bounded diagnostic capture: small outputs are kept verbatim and the omission marker is only inserted on line boundaries, so `PASS` and `--- FAIL` lines are never split.
- Classify crashes correctly when target output contains coordinator phrases such as "failed to unmarshal" or "no tests to run"; infrastructure and missing-target detection are anchored on Go's own lines.
- Keep crash records whose failing input was written before the runner deadline fired instead of discarding them as errors.
- Canceled runs no longer count as failed targets in fleet health or project status.
- Shorten the incomplete, missing, and canceled TUI badges to fit the 7-column status column.
- README: correct schema version, remove the duplicate license section, move run-outcome documentation into the main body, and drop internal deployment notes.
- Fix subpackage fuzz selection by placing the package before forwarded test flags; apply package-first ordering to coverage replay.
- Require fuzz coordinator progress and PASS before reporting success; distinguish missing targets, incomplete runs, cancellation, infrastructure errors, and fuzz crashes.
- Bound failed-run diagnostics, redact common sensitive values, persist them in schema v3, and expose them in the runs API and target-detail TUI, including snapshots.
- Add always-on live subprocess regressions for subpackages with and without root Go files, cache delivery, outcome classification, and optional representative project campaigns.
- Document coordinated writer/viewer upgrade and fresh-run acceptance. Historical records and open crashes are unchanged.

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
