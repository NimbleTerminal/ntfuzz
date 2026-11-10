---
title: "HTTP API"
weight: 40
---

# HTTP API

Both `ntfuzz serve` and `ntfuzz view` serve the same read-only JSON, HTML, and Markdown endpoints on their main `--listen` address alongside the Booba browser terminal (`/`). Mutation endpoints (`POST /api/crashes/{id}/issue` and `POST /api/crashes/{id}/fix`) are registered only on `ntfuzz serve`.

## Endpoint summary

| Method & Path | Modes | Content-Type | Description |
| --- | --- | --- | --- |
| `GET /` | `serve`, `view` | `text/html` | Interactive Booba / Ghostty browser TUI dashboard with server-rendered discovery links. |
| `GET /api/status` | `serve`, `view` | `application/json` | Fleet health (`ok`, `crash`, `error`, `idle`, or `snapshot`), 24h run counts, open/reported crash counts, schema version, `read_only`, `snapshot_at`, and active worker jobs. |
| `GET /api/projects` | `serve`, `view` | `application/json` | Configured projects with `id`, `name`, `repo`, `branch`, `public_inputs`, `git_sha`, `dirty`, `go_version`, `last_synced`, `sync_error`, target count, and open crash counts. |
| `GET /api/targets` | `serve`, `view` | `application/json` | Active fuzz targets with latest run metrics, `corpus_count`, `coverage_pct`, 10-point `coverage_trend`, `open_crashes`, and `total_runs`. |
| `GET /api/runs` | `serve`, `view` | `application/json` | Recent fuzz runs (newest first) with `execs`, `execs_per_sec`, `new_interesting`, `corpus_count`, `coverage_pct`, `status`, `git_sha`, `dirty`, `go_version`, and redacted `diagnostic` on failed runs. |
| `GET /api/crashes` | `serve`, `view` | `application/json` | Crash records with scrubbed `stack`, `stack_fingerprint`, `input_hash`, dependency blame (`blamed_*`), `issue_url`, and replay status (`replay_*`). |
| `GET /api/crashes/{id}` | `serve`, `view` | `application/json` | Single crash JSON record by numeric ID (`404` JSON error if not found). |
| `GET /crashes/{id}` | `serve`, `view` | `text/html` | Server-rendered HTML crash permalink (no JavaScript required) with stack trace, dependency blame link, replay status, reproducer download link, and a button to open the Web Terminal Dashboard. |
| `GET /api/crashes/{id}/input` | `serve`, `view` (opt-in) | `text/plain` | Raw `go test fuzz v1` corpus reproducer file (`Content-Disposition: attachment; filename="<input_hash>"`). Always available in `serve`; available in `view` when `public_inputs: true` on the project (`403` otherwise). |
| `GET /api/crashes/{id}/report.md` | `serve`, `view` | `text/markdown` | Shareable Markdown crash report for upstream maintainers (accepts optional `?public_url=https://...`). |
| `POST /api/crashes/{id}/issue` | `serve` only | `application/json` | Attach or update `issue_url` on a crash without marking it fixed. Body: `{"issue_url": "https://..."}`. |
| `POST /api/crashes/{id}/fix` | `serve` only | `application/json` | Mark a crash as fixed and optionally set `issue_url`. Body: `{"issue_url": "https://..."}` (or empty body). |
| `GET /api/coverage/history` | `serve`, `view` | `application/json` | Chronological `coverage_pct` and `corpus_count` series for a target (`?target=<id-or-name>&limit=50`). |
| `GET /api/stats/{query}` | `serve`, `view` | `application/json` | Time-bucketed analytical series (`throughput`, `coverage`, `outcomes`, `revisions`, `burndown`). Also accessible at `GET /api/stats?query=...`. |
| `GET /api/crash-replays` | `serve`, `view` | `application/json` | Saved-input replay verification rows across revisions (`?project=...&target=...&crash_id=...&status=...&limit=100`). |
| `GET /api/docs` | `serve`, `view` | `text/html` | Embedded browser-readable API documentation page. |
| `GET /openapi.json` | `serve`, `view` | `application/json` | Embedded OpenAPI 3.1 specification for all read endpoints. |
| `GET /llms.txt` | `serve`, `view` | `text/markdown` | Embedded agent and LLM guide describing endpoints, parameters, and crash triage workflows. |

---

## Query parameters and examples

### `GET /api/targets`

| Parameter | Default | Description |
| --- | --- | --- |
| `project` | `""` (all projects) | Filter by project name (e.g. `nt3d`) or numeric project ID. |

```sh
curl -s "http://127.0.0.1:8080/api/targets?project=demo-parser" | jq
```

### `GET /api/runs`

| Parameter | Default | Description |
| --- | --- | --- |
| `target` | `""` (all targets) | Filter by numeric target ID, target name (`FuzzDecode`), or `<project>/<target>`. |
| `limit` | `100` | Maximum number of recent runs to return. |

```sh
curl -s "http://127.0.0.1:8080/api/runs?target=1&limit=5" | jq
```

> [!NOTE]
> Failed runs (`status` of `error`, `crash`, `incomplete`, or `missing_target`) include a redacted `diagnostic` field containing up to ~16 KiB of head/tail output from `go test`. Successful (`ok`) runs omit `diagnostic`.

### `GET /api/crashes`

| Parameter | Default | Description |
| --- | --- | --- |
| `open` | `true` | `true` or `1` returns only open crashes (`fixed_at IS NULL`); `false`, `0`, or `all` returns both open and fixed crashes. Any other value returns `400 Bad Request`. |
| `target` | `""` (all targets) | Filter by numeric target ID, target name, or `<project>/<target>`. |

```sh
# Open crashes only
curl -s "http://127.0.0.1:8080/api/crashes?open=true" | jq

# All crashes (open + fixed) for target ID 98
curl -s "http://127.0.0.1:8080/api/crashes?open=all&target=98" | jq
```

### `GET /api/crashes/{id}` and `GET /crashes/{id}`

Fetch a single crash as JSON or as a standalone server-rendered HTML page:

```sh
curl -s http://127.0.0.1:8080/api/crashes/23 | jq
curl -s http://127.0.0.1:8080/crashes/23
```

### `GET /api/crashes/{id}/input`

Download the minimized Go fuzz corpus file (`go test fuzz v1` format) to drop directly into `testdata/fuzz/<FuzzTarget>/<input_hash>`:

```sh
curl -fOJ http://127.0.0.1:8080/api/crashes/23/input
```

If the stored input exceeded the 64 KiB database cap (`input_truncated: true`), the response includes the header `X-Ntfuzz-Input-Truncated: true`.

### `GET /api/crashes/{id}/report.md`

Generate a Markdown report for an upstream issue or security advisory:

| Parameter | Default | Description |
| --- | --- | --- |
| `public_url` | `""` | Optional `http://` or `https://` base URL used to construct the permalink and reproducer download URLs in the report. Validated to reject non-HTTP(S) schemes, userinfo, query strings, fragments, and Markdown injection characters. |

```sh
curl -s "http://127.0.0.1:8080/api/crashes/23/report.md?public_url=https://fuzz.nimbleterminal.com"
```

### `POST /api/crashes/{id}/issue` and `POST /api/crashes/{id}/fix` (`serve` only)

```sh
# Attach an upstream issue URL while keeping the crash open ("reported" state)
curl -s -X POST http://127.0.0.1:8080/api/crashes/23/issue \
  -H "Content-Type: application/json" \
  -d '{"issue_url": "https://github.com/org/repo/issues/42"}' | jq

# Mark a crash fixed (with an optional issue_url)
curl -s -X POST http://127.0.0.1:8080/api/crashes/23/fix \
  -H "Content-Type: application/json" \
  -d '{"issue_url": "https://github.com/org/repo/issues/42"}' | jq
```

### `GET /api/coverage/history`

| Parameter | Default | Description |
| --- | --- | --- |
| `target` | *(required)* | Numeric target ID, target name, or `<project>/<target>`. Missing `target` returns `[]`. |
| `limit` | `50` | Maximum number of chronological points to return. |

```sh
curl -s "http://127.0.0.1:8080/api/coverage/history?target=1&limit=50" | jq
```

### `GET /api/stats/{query}`

Run one of the five canned time-bucketed analytical queries:

| `query` | Description |
| --- | --- |
| `throughput` | Total executions, average/peak `execs_per_sec`, and run counts per time bucket. |
| `coverage` | Average and maximum statement `coverage_pct`, `corpus_count`, and `new_interesting` inputs per bucket. |
| `outcomes` | Breakdown of `ok_runs`, `crash_runs`, `error_runs`, `incomplete_runs`, and `canceled_runs` per bucket. |
| `revisions` | Newly observed Git revisions split into `fuzzed_revisions` and `awaiting_fuzz_revisions`, plus a paginated `revisions` table with replay verification counts (`replays_passed`, `replays_failed`, `replays_blocked`, `replays_pending`). |
| `burndown` | Open crash backlog at the end of each bucket (`open_crashes`), split into `unreported_open_crashes` and `reported_open_crashes`, plus `new_crashes`, `reopened_crashes`, `resolved_crashes`, and dependency vs. project blame totals. |

| Parameter | Default | Allowed values |
| --- | --- | --- |
| `window` | `24h` | `1h` (1m buckets), `6h` (5m buckets), `24h` (15m buckets), `7d` (2h buckets), `14d` (6h buckets). |
| `project` | `""` | Optional project name or ID filter. |
| `target` | `""` | Optional target filter (for `throughput`, `coverage`, and `outcomes`). |

```sh
curl -s "http://127.0.0.1:8080/api/stats/throughput?window=24h" | jq
curl -s "http://127.0.0.1:8080/api/stats/revisions?window=7d&project=nt3d" | jq
curl -s "http://127.0.0.1:8080/api/stats/burndown?window=14d" | jq
```

---

## API discovery headers

Responses for `/`, `/api/docs`, `/llms.txt`, and `/openapi.json` include RFC 8288 `Link` headers advertising:

- `rel="service-doc"` → `/api/docs`
- `rel="service-desc"` → `/openapi.json`
- `rel="describedby"` → `/llms.txt`

---

## Prometheus `/metrics` listener

When enabled with `--metrics` or `--metrics-listen`, `ntfuzz` serves Prometheus metrics on a separate loopback listener (`127.0.0.1:9090` for `serve`, `127.0.0.1:9091` for `view`)—never on the main `--listen` port. All labels are strictly bounded (`mode`, `route`, `method`, `status_class`, `reason`, `result`).

| Metric Name | Type | Modes | Description |
| --- | --- | --- | --- |
| `ntfuzz_http_requests_total` | Counter | `serve`, `view` | External HTTP requests by `mode`, `route`, `method`, and `status_class`. |
| `ntfuzz_http_request_duration_seconds` | Histogram | `serve`, `view` | External HTTP request latency in seconds. |
| `ntfuzz_terminal_sessions_active` | Gauge | `serve`, `view` | Currently active browser-terminal sessions. |
| `ntfuzz_terminal_sessions_opened_total` | Counter | `serve`, `view` | Total browser-terminal sessions opened. |
| `ntfuzz_terminal_sessions_closed_total` | Counter | `serve`, `view` | Closed browser-terminal sessions by `reason` (`client_disconnect`, `idle_timeout`, `server_shutdown`, `error`). |
| `ntfuzz_terminal_sessions_rejected_total` | Counter | `serve`, `view` | Rejected terminal connections by `reason` (`connection_limit`, `unauthorized`, `origin`, `handshake`, `error`). |
| `ntfuzz_terminal_session_duration_seconds` | Histogram | `serve`, `view` | Duration of closed browser-terminal sessions in seconds. |
| `ntfuzz_terminal_bytes_received_total` | Counter | `serve`, `view` | Terminal input payload bytes written from clients to the PTY session. |
| `ntfuzz_terminal_bytes_sent_total` | Counter | `serve`, `view` | Terminal output payload bytes read from the PTY session and sent to clients. |
| `ntfuzz_snapshot_publications_total` | Counter | `serve` | Immutable snapshot publication attempts (`result`: `success`, `error`). |
| `ntfuzz_snapshot_publish_duration_seconds` | Histogram | `serve` | Duration of snapshot publication attempts in seconds. |
| `ntfuzz_orchestrator_workers_active` | Gauge | `serve` | Currently active fuzzing worker jobs. |
| `ntfuzz_orchestrator_workers_configured` | Gauge | `serve` | Configured fuzzing worker concurrency. |
| `ntfuzz_snapshot_age_seconds` | Gauge | `view` | Age in seconds of the currently loaded immutable snapshot generation. |
| `ntfuzz_snapshot_refreshes_total` | Counter | `view` | Snapshot refresh checks in viewer mode (`result`: `success`, `error`). |
