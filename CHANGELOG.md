# `ntfuzz` CHANGELOG

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
