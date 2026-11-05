# `ntfuzz` CHANGELOG

## Unreleased — 2026-11-05

- Add native Go fuzz targets across `ntfuzz` packages so `ntfuzz` can fuzz itself:
  - `internal/orchestrator`: `FuzzParseConfig`, `FuzzParseFuzzOutput`, and
    `FuzzFingerprintStack` (hardening project name validation against path
    traversal/control characters, `ParseFuzzOutput` against >64KB output lines
    and `NaN`/`Inf` durations, `extractCrashStack` UTF-8 rune boundaries, and
    skipping nested `examples/` directories when discovering targets from a
    repository root).
  - `internal/booba`: `FuzzValidateAndRenderBranding` (verifying HTML escaping,
    URL scheme/host/credential validation, `#RRGGBB` normalization, and
    validation idempotence).
  - `internal/metrics`: `FuzzClassifyLabels` and `FuzzResolveListenAddr`
    (verifying bounded Prometheus label cardinality and listener address parsing).
  - `internal/store`: `FuzzReadSnapshotManifest` (verifying manifest size/schema
    checks and path-traversal rejection across `/` and `\` separators).
  - `internal/api`: `FuzzParseOpenParamAndFixRequest` (verifying query parameter
    parsing, read-only snapshot route isolation, and JSON body validation).
  - `internal/tui`: `FuzzFormatAndRenderHelpers` (hardening coverage trend bar
    rendering and execution-rate formatting against `NaN`/`Inf` and UTF-8
    truncation).
- Change the Go module path from `github.com/NimbleTerminal/ntfuzz` to
  `nimbleterminal.dev/ntfuzz` for consistency with `nimbleterminal.dev/ntduckbox`.
- Add optional browser dashboard branding (`--brand-name`, `--brand-url`,
  `--brand-accent` and `NTFUZZ_BRAND_NAME`, `NTFUZZ_BRAND_URL`,
  `NTFUZZ_BRAND_ACCENT`) for both `ntfuzz serve` and `ntfuzz view`:
  - Link the `NTFUZZ` header wordmark to `https://github.com/NimbleTerminal/ntfuzz`
    while allowing operators to display an HTML-escaped organization/fleet name
    beside it, optionally linked to a validated `http`/`https` website.
  - Support a validated `#RRGGBB` accent override via a scoped `--ntfuzz-accent`
    CSS custom property while preserving contrast, visible `:focus-visible`
    outlines, responsive truncation on narrow viewports, automatic renderer
    selection (`renderer: 'auto'`), and removal of the renderer/FPS HUD overlay.
- Add optional Prometheus-compatible observability (`internal/metrics`) for both
  `ntfuzz serve` and `ntfuzz view` on a dedicated loopback-by-default listener
  (`--metrics` / `--metrics-listen`), isolated from the dashboard listener:
  - External HTTP request counts and latency histograms with bounded labels
    (`mode`, `route`, `method`, `status_class`), excluding in-process TUI API
    calls while preserving `http.Hijacker`, `http.Flusher`, and `Unwrap` for
    WebSocket upgrades and `http.ResponseController`.
  - Browser-terminal session gauges, open/close/reject counters, session
    duration histograms, bounded rejection/disconnect reason categories, and
    PTY terminal byte counters via `go-booba/serve` middleware hooks while
    preserving idle timeouts, connection limits, and live terminal resize events.
  - Orchestrator snapshot publication counters/duration histograms and live
    worker gauges in `serve` mode, plus current snapshot age and refresh
    success/error counters in `view` mode without writing access metrics into
    DuckDB or fabricating worker activity in viewer mode.
- Preserve live WebSocket resize notifications through the public viewer's
  idle-timeout wrapper, with a real-PTY resize regression test.
- Keep populated fleet tables within resized terminal dimensions.

## Unreleased — 2026-11-04

- Add periodic immutable DuckDB snapshot publication through the live writer,
  atomic manifests, and bounded generation retention.
- Add `ntfuzz view`: a separate read-only Booba web terminal and fixed JSON API
  backed by ntduckbox, with safe generation refresh and publication timestamps.
- Remove mutation routes and crash-fixing terminal actions in snapshot mode.
- Support inherited listener descriptors and in-process terminal API transport
  so a deployment can isolate the viewer from the private writer's network.
- Test snapshot/read API parity, writer isolation, injection-resistant filter
  binding, read-only actions, and recovery after invalid snapshot publication.

## [0.1.0] - 2026-11-04

### Added

- **Single-binary CLI (`ntfuzz`)** with `serve`, `tui`, and `crash` (`list`, `fixed`) subcommands.
- **Continuous Go Fuzzing Orchestrator (`internal/orchestrator`)**:
  - Local path and remote Git repository checkout/pull management with `git_sha`, `dirty` status, `go_version`, and visible `sync_error` tracking.
  - Multi-module native Go fuzz target discovery (`FuzzXxx(f *testing.F)`) honoring `//go:build` constraints and reconciling removed targets.
  - Non-starving per-module worker queue (`moduleQueue`) that prevents concurrent `go test` runs inside the same module while keeping workers busy across other modules, with `GOMAXPROCS=1` per child process and graceful `SIGINT` + `WaitDelay` cancellation.
  - Priority scheduling for targets that recently added corpus inputs and flat-streak backoff.
  - Persistent corpus cache management (`-test.fuzzcachedir` with automatic legacy corpus layout migration), durable crash input persistence and SHA-256 content hashing, normalized stack trace fingerprinting, and isolated temporary-copy corpus statement coverage replay (`-cover`).
- **DuckDB History & Status Store (`internal/store`)**:
  - Versioned schema migrations (`schema_migrations`, v2) for `projects`, `targets`, `runs`, `crashes`, indexes, and the `latest_target_status` view with single-query `LIST` coverage trend aggregation.
  - Crash deduplication by `(target_id, input_hash)` and `(target_id, stack_fingerprint)` with automatic reopen on regression after `fixed_at`.
  - Automatic 14-day run retention pruning (preserving at least the 10 most recent runs per target).
- **HTTP JSON API (`internal/api`)**:
  - Dedicated wire DTOs and converters for `GET /api/projects`, `GET /api/targets`, `GET /api/runs`, `GET /api/crashes`, `POST /api/crashes/{id}/fix`, `GET /api/coverage/history`, and `GET /api/status`.
- **Bubble Tea v2 Dashboard & Booba Bridge (`internal/tui`, `internal/booba`)**:
  - Shared Bubble Tea v2 TUI model that communicates exclusively with the HTTP API (fleet overview, per-target detail view with `ntcharts` Braille coverage sparkline, and crash triage view).
  - Booba browser bridge (`go-booba` + Ghostty/`ntwebterm`) serving the live TUI at `/` alongside `/api/*`, with OSC 7501 Terminal Actions via `bubblehelp`.
