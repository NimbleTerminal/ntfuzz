# ntfuzz — Continuous Go Fuzzing Orchestrator and Dashboard

<p>
    <a href="https://github.com/NimbleTerminal/ntfuzz/tags"><img src="https://img.shields.io/github/tag/NimbleTerminal/ntfuzz.svg" alt="Latest Release"></a>
    <a href="https://pkg.go.dev/nimbleterminal.dev/ntfuzz"><img src="https://pkg.go.dev/badge/nimbleterminal.dev/ntfuzz.svg" alt="Go Reference"></a>
    <a href="https://github.com/NimbleTerminal/ntfuzz/actions/workflows/ci.yml"><img src="https://github.com/NimbleTerminal/ntfuzz/actions/workflows/ci.yml/badge.svg" alt="CI"></a>
    <a href="./LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="License: MIT"></a>
    <a href="./CODE_OF_CONDUCT.md"><img src="https://img.shields.io/badge/Contributor%20Covenant-2.1-4baaaa.svg" alt="Code Of Conduct"></a>
</p>

`ntfuzz` (`nimbleterminal.dev/ntfuzz`) is a single-binary continuous fuzzing orchestrator for Go repositories, backed by **DuckDB** ([`duckdb-go/v2`](https://github.com/duckdb/duckdb-go) and [`ntduckbox`](https://github.com/NimbleTerminal/ntduckbox)) and a **[Bubble Tea v2](https://github.com/charmbracelet/bubbletea)** TUI dashboard served both natively in your terminal and in the browser via **Booba** ([`nimbleterminal.dev/booba`](https://github.com/NimbleTerminal/booba) + Ghostty/[`ntwebterm`](https://github.com/NimbleTerminal/ntwebterm)).

---

## Features

- **Continuous Native Go Fuzzing Orchestrator**:
  - Syncs local Go modules or clones/pulls remote Git repositories, recording `git_sha`, `dirty` state, and `go_version` on every project, run, and crash. Failed pulls are surfaced visibly as `sync_error` without advancing `last_synced`.
  - Discovers `FuzzXxx(f *testing.F)` targets across multi-module repositories while respecting `//go:build` constraints and reconciling removed targets (`active = FALSE`).
  - Runs `go test -parallel=1 -fuzz=^FuzzXxx$ -fuzztime=...` with `GOMAXPROCS=1` per worker and per-module exclusion so `--workers N` runs exactly `N` fuzz processes without contending on the same module tree.
  - Prioritizes targets that recently added corpus entries and backs off flat targets.
  - Copies minimized crash inputs into `.ntfuzz/corpus/<project>/crashes/...`, hashes the actual input bytes, and computes a normalized stack fingerprint (stripping paths, line numbers, and hex addresses).
  - Replays the cached fuzz corpus (`-test.fuzzcachedir`) under `-cover` to track real corpus statement coverage alongside corpus size (`corpus_count`).
- **DuckDB Metadata & History Store**:
  - Versioned schema migrations (`schema_migrations` table, currently v3) for `projects`, `targets`, `runs`, `crashes`, indexes, and a single-query `latest_target_status` view that aggregates 10-point coverage trends via DuckDB `LIST`.
  - Automatic 14-day run retention pruning (preserving at least the latest 10 runs per target).
- **HTTP JSON API (`/api/*`)**:
  - `GET /api/projects` — project list with revision, sync status, and latest summary
  - `GET /api/targets?project=...` — active targets with latest run metrics, corpus count, and coverage trend
  - `GET /api/runs?target=...&limit=100` — recent run history with `git_sha`, `dirty`, and `go_version`
  - `GET /api/crashes?open=true` — open (or all) crashes with stack traces, stack fingerprints, and durable corpus paths
  - `POST /api/crashes/{id}/fix` — mark a crash fixed (and optionally attach `issue_url`)
  - `GET /api/coverage/history?target=...` — chronological coverage & corpus growth series
  - `GET /api/status` — fleet health, 24h run counts, schema version, and active worker jobs
- **Shared Bubble Tea TUI + Booba Browser Bridge**:
  - The TUI talks strictly to the HTTP API DTOs in `internal/api` (never imports `internal/store` or opens DuckDB directly).
  - Surfaces keybindings as **Booba Terminal Actions** (OSC 7501) via `booba/actions/bubblehelp`.
  - Accessible both locally via `ntfuzz tui` and live in a browser Ghostty terminal at `http://127.0.0.1:8080/` during `ntfuzz serve`.

---

## Quick Start

### 1. Build & Test

Using [Task](https://taskfile.dev), `make`, or `go` directly:

```bash
# Build ./bin/ntfuzz
task build
# or: make build
# or: go build -o bin/ntfuzz ./cmd/ntfuzz

# Run the test suite
task test
```

### 2. Run the Orchestrator + API + Browser TUI (`serve`)

Out of the box, `configs/projects.yaml` is pre-configured with two self-contained Go modules under [`examples/demo-parser`](./examples/demo-parser) and [`examples/demo-codec`](./examples/demo-codec) (and is also embedded directly into the `ntfuzz` binary):

```bash
task serve
# or: ./bin/ntfuzz serve --config configs/projects.yaml --db ntfuzz.duckdb --listen 127.0.0.1:8080
```

> **Note**: `configs/projects.yaml` is read once at process startup (restart `ntfuzz serve` after editing). Only one `ntfuzz serve` process should open a given `.duckdb` file at a time. The HTTP API is an unauthenticated local operator console bound to `127.0.0.1` by default — do not expose it as a shared public service.

Then:
- **Browser TUI (Booba / Ghostty)**: Open **http://127.0.0.1:8080/** in your browser.
- **Terminal TUI**: In another terminal, run:
  ```bash
  task tui
  # or: ./bin/ntfuzz tui --api http://127.0.0.1:8080
  ```
- **Crash Triage CLI**:
  ```bash
  ./bin/ntfuzz crash list --api http://127.0.0.1:8080
  ./bin/ntfuzz crash fixed 1 --issue https://github.com/org/repo/issues/42
  ```
- **HTTP JSON API**:
  ```bash
  curl -s http://127.0.0.1:8080/api/status | jq
  curl -s http://127.0.0.1:8080/api/projects | jq
  curl -s http://127.0.0.1:8080/api/targets | jq
  ```

---

## Pointing `ntfuzz` at Your Own Repositories

Edit `configs/projects.yaml` (or pass `--config /path/to/projects.yaml`) to list any combination of local Go module paths and remote Git URLs:

```yaml
corpus_dir: .ntfuzz/corpus
work_dir: .ntfuzz/repos
default_fuzztime: 15s

projects:
  # Local Go module path
  - name: demo-parser
    repo: ./examples/demo-parser
    fuzztime: 10s

  # Remote Git repositories with Go fuzz targets
  - name: go-yaml
    repo: https://github.com/go-yaml/yaml.git
    branch: v3
    fuzztime: 20s

  - name: klauspost-compress
    repo: https://github.com/klauspost/compress.git
    branch: master
    fuzztime: 30s
    corpus_dir: .ntfuzz/corpus/klauspost-compress
```

---

## TUI Keybindings & Booba Terminal Actions

| Key | Action | Description |
| --- | --- | --- |
| `↑` / `k`, `↓` / `j` | Move up / down | Navigate targets, runs, or crashes |
| `enter` | Target detail | Open selected target's run history, `ntcharts` coverage sparkline, and crashes |
| `esc` / `backspace` | Back to fleet | Return to the main fleet overview |
| `p` / `tab` | Filter project | Cycle through projects (`All Projects` → project 1 → ...) |
| `c` | Open crashes | Toggle the fleet-wide open crashes view |
| `x` | Mark crash fixed | Mark the selected open crash as fixed (in Crashes view) |
| `r` | Refresh now | Poll the HTTP API immediately |
| `?` | Full help | Expand/collapse full keybinding help |
| `h` | Hide/Show help | Toggle in-terminal help footer |
| `q` / `ctrl+c` | Quit | Exit the TUI client |

---

## Public read-only snapshots

Keep the orchestrator private and publish immutable generations:

```sh
ntfuzz serve --config fleet.yaml --db live.duckdb --listen 127.0.0.1:8080 \
  --snapshot-dir /var/lib/ntfuzz/public --snapshot-interval 1m
ntfuzz view --snapshots /var/lib/ntfuzz/public --listen 127.0.0.1:8081 --refresh 10s
```

`serve` uses DuckDB `COPY FROM DATABASE` through the writer's own connection,
with store mutations serialized during the copy. It detaches/closes the copied
file before publishing `current.json` by atomic rename. The database file is
immutable after publication, includes a schema version and publication timestamp,
and is separate from the live database/WAL. The publisher retains two completed
generations; active readers retain their file until refresh. Snapshot publication
errors are logged and do not stop fuzz workers.

`view` opens existing files through `nimbleterminal.dev/ntduckbox`: read-only
access, no migrations, locked configuration, no extension loading/external file
or network reads, and bounded query time/output. The viewer runs no orchestrator,
clones no repositories, and registers no mutation endpoint. Its terminal disables
crash-fixing actions and labels the snapshot timestamp. Failed refreshes retain
the last validated generation; failure to open the initial generation prevents
startup. The API reports `read_only`, `snapshot_at`, and `fleet_health=snapshot`;
it does not invent live worker activity from stale data.

Run the viewer under a different OS identity with only read access to the publish
directory. It must have no access to the writer database, corpus, repository
credentials, or private operator API. `view --listen-fd 3` accepts an inherited
listener (for example a systemd socket). Browser terminal API requests are handled
in process, so the viewer can run with `PrivateNetwork=true` while serving that
inherited listener. This is useful for OS-level confinement; ntduckbox's SQL
restrictions are not a substitute for a process boundary. ntconfine integration
is not implemented here; do not assume its closed networking policy can launch
an ordinary HTTP listener without inherited descriptors.

The default viewer limits browser sessions to 32 and idle sessions to ten minutes.
Queries are capped at 10,000 rows, 16 MiB output, and five seconds. The HTTP API
exposes fixed dashboard queries, not arbitrary user SQL. Set process memory/CPU
limits in deployment as well. Public snapshots expose the configured fleet's
project/run/crash details; publish only data intended for public viewing.

### Self-Hosting & Deployment Cookbook

For end-to-end production deployment recipes—including isolated `systemd` service units, Caddy automatic HTTPS, SSH operator tunnels, and automated provisioning on DigitalOcean—see:

- **[Self-Hosting & Deployment Cookbook](docs/deployment-cookbook.md)**: architecture guide, manual Linux setup, socket activation (`--listen-fd 3`), day-2 operator workflows, and Prometheus alerting.
- **[DigitalOcean Terraform Module](deploy/terraform/digitalocean/)**: reusable Terraform configuration provisioning a Droplet, persistent Block Storage Volume (`/var/lib/ntfuzz`), Cloud Firewall, and `cloud-init` bootstrap.
- **[Reference `systemd` & Caddy Units](deploy/systemd/)**: standalone `ntfuzz-serve.service`, `ntfuzz-view.service`, `ntfuzz-view.socket`, `Caddyfile.example`, and `projects.yaml.example`.

---

## Browser Dashboard Branding

Both `ntfuzz serve` and `ntfuzz view` support optional operator branding in the web terminal header while keeping the upstream **NTFUZZ** wordmark linked to `https://github.com/NimbleTerminal/ntfuzz`.

### Configuration & Precedence

Branding is configured via CLI flags or environment variables (independent of `projects.yaml` and snapshot database files, so standalone `ntfuzz view` processes require no access to the writer's config). Settings are resolved in the following order of precedence:

1. **CLI flags** (`--brand-name`, `--brand-url`, `--brand-accent`)
2. **Environment variables** (`NTFUZZ_BRAND_NAME`, `NTFUZZ_BRAND_URL`, `NTFUZZ_BRAND_ACCENT`)
3. **Built-in defaults** (unconfigured operator badge, default header accent `#38bdf8`)

| CLI Flag | Environment Variable | Default | Validation / Behavior |
| --- | --- | --- | --- |
| `--brand-name` | `NTFUZZ_BRAND_NAME` | `""` (omitted) | Rendered as escaped plain text beside `NTFUZZ` (truncates with ellipsis on narrow screens) |
| `--brand-url` | `NTFUZZ_BRAND_URL` | `""` (unlinked) | Optional website link for `--brand-name`; must be a valid `http://` or `https://` URL |
| `--brand-accent` | `NTFUZZ_BRAND_ACCENT` | `#38bdf8` | Scoped `--ntfuzz-accent` color on `.topbar`; must match 6-digit `#RRGGBB` hex |

### Generic Example

```sh
# Via CLI flags (works identically for serve and view)
ntfuzz view --snapshots /var/lib/ntfuzz/public --listen 127.0.0.1:8081 \
  --brand-name "Example Security" \
  --brand-url "https://security.example.com" \
  --brand-accent "#38bdf8"

# Or via environment variables
export NTFUZZ_BRAND_NAME="Example Security"
export NTFUZZ_BRAND_URL="https://security.example.com"
export NTFUZZ_BRAND_ACCENT="#38bdf8"
ntfuzz serve --config fleet.yaml --db live.duckdb --listen 127.0.0.1:8080
```

---

## Observability & Prometheus Metrics

Both `ntfuzz serve` and `ntfuzz view` support an optional, isolated Prometheus `/metrics` listener. Metrics are **disabled by default**, are **never exposed on the dashboard/API listener (`--listen`)**, and **never write access data into DuckDB or published snapshots**.

### CLI Flags

| Command | Flag | Default | Description |
| --- | --- | --- | --- |
| `ntfuzz serve` | `--metrics` | `false` | Enable the loopback Prometheus `/metrics` listener on `127.0.0.1:9090` (unless `--metrics-listen` is set) |
| `ntfuzz serve` | `--metrics-listen` | `""` (disabled) | Explicit listen address for `/metrics` (binds to `127.0.0.1` when host is omitted, e.g. `:9090`) |
| `ntfuzz view` | `--metrics` | `false` | Enable the loopback Prometheus `/metrics` listener on `127.0.0.1:9091` (unless `--metrics-listen` is set) |
| `ntfuzz view` | `--metrics-listen` | `""` (disabled) | Explicit listen address for `/metrics` (binds to `127.0.0.1` when host is omitted, e.g. `:9091`) |

### Exposed Metrics

All metrics use bounded labels (`mode`, `route`, `method`, `status_class`, `reason`, `result`). High-cardinality or sensitive values—such as IP addresses, session IDs, arbitrary URLs, query strings, target names, repository names, or terminal input/output contents—are never recorded in labels. Internal in-process API calls made by browser terminal sessions are excluded from external HTTP request metrics.

| Metric Name | Type | Unit | Labels | Modes | Description |
| --- | --- | --- | --- | --- | --- |
| `ntfuzz_http_requests_total` | Counter | requests | `mode`, `route`, `method`, `status_class` | `serve`, `view` | External HTTP requests handled (`route` templates: `/`, `/ws`, `/wt`, `/cert-hash`, `/static/*`, `/api/projects`, `/api/targets`, `/api/runs`, `/api/crashes`, `/api/crashes/{id}/fix`, `/api/coverage/history`, `/api/status`, `other`) |
| `ntfuzz_http_request_duration_seconds` | Histogram | seconds | `mode`, `route`, `method`, `status_class` | `serve`, `view` | External HTTP request latency in seconds (for `/ws`, measures HTTP upgrade handshake latency) |
| `ntfuzz_terminal_sessions_active` | Gauge | sessions | `mode` | `serve`, `view` | Currently active browser-terminal sessions |
| `ntfuzz_terminal_sessions_opened_total` | Counter | sessions | `mode` | `serve`, `view` | Total browser-terminal sessions opened |
| `ntfuzz_terminal_sessions_closed_total` | Counter | sessions | `mode`, `reason` | `serve`, `view` | Closed browser-terminal sessions (`reason`: `client_disconnect`, `idle_timeout`, `server_shutdown`, `error`) |
| `ntfuzz_terminal_sessions_rejected_total` | Counter | connections | `mode`, `reason` | `serve`, `view` | Rejected terminal connection attempts (`reason`: `connection_limit`, `unauthorized`, `origin`, `handshake`, `error`) |
| `ntfuzz_terminal_session_duration_seconds` | Histogram | seconds | `mode`, `reason` | `serve`, `view` | Duration of closed browser-terminal sessions in seconds |
| `ntfuzz_terminal_bytes_received_total` | Counter | bytes | `mode` | `serve`, `view` | Terminal input payload bytes written from clients to the PTY session |
| `ntfuzz_terminal_bytes_sent_total` | Counter | bytes | `mode` | `serve`, `view` | Terminal output payload bytes read from the PTY session and sent to clients |
| `ntfuzz_snapshot_publications_total` | Counter | publications | `mode`, `result` | `serve` | Immutable snapshot publication attempts (`result`: `success`, `error`) |
| `ntfuzz_snapshot_publish_duration_seconds` | Histogram | seconds | `mode`, `result` | `serve` | Duration of immutable snapshot publication attempts in seconds |
| `ntfuzz_orchestrator_workers_active` | Gauge | workers | `mode` | `serve` | Currently active fuzzing worker jobs in live orchestrator mode |
| `ntfuzz_orchestrator_workers_configured` | Gauge | workers | `mode` | `serve` | Configured fuzzing worker concurrency in live orchestrator mode |
| `ntfuzz_snapshot_age_seconds` | Gauge | seconds | `mode` | `view` | Age in seconds of the currently loaded immutable snapshot generation |
| `ntfuzz_snapshot_refreshes_total` | Counter | refreshes | `mode`, `result` | `view` | Snapshot refresh checks in viewer mode (`result`: `success`, `error`) |

> **Transport & Process Boundaries**:
> - Counters and histograms are held in process memory and **reset on process restart**. Historical retention, alerting, and long-term aggregation require an external Prometheus-compatible collector.
> - `ntfuzz_terminal_bytes_received_total` and `ntfuzz_terminal_bytes_sent_total` measure terminal PTY payload bytes via `booba/serve` `SessionMiddleware` (`InputWriter`/`OutputReader`). WebSocket/WebTransport framing bytes and control messages (`MsgResize`, `MsgPing`/`MsgPong`, `MsgOptions`, `MsgClose`) are handled internally by the transport layer and are not included in PTY byte counters.
> - `ntfuzz view` only exposes metrics for the viewer process and the age/refresh health of its loaded snapshot; it never fabricates live orchestrator worker activity from snapshot data.

### Local Scraping Example

Start `serve` and `view` with loopback metrics listeners:

```sh
ntfuzz serve --config fleet.yaml --db live.duckdb --listen 127.0.0.1:8080 \
  --snapshot-dir /var/lib/ntfuzz/public --snapshot-interval 1m \
  --metrics-listen 127.0.0.1:9090

ntfuzz view --snapshots /var/lib/ntfuzz/public --listen 127.0.0.1:8081 \
  --refresh 10s --metrics-listen 127.0.0.1:9091
```

Inspect metrics locally:

```sh
curl -s http://127.0.0.1:9090/metrics | grep '^ntfuzz_'
curl -s http://127.0.0.1:9091/metrics | grep '^ntfuzz_'
```

Example `prometheus.yml` scrape configuration:

```yaml
scrape_configs:
  - job_name: ntfuzz
    scrape_interval: 15s
    static_configs:
      - targets:
          - 127.0.0.1:9090
          - 127.0.0.1:9091
```

### Service Metrics vs. Edge / CDN Analytics

- **`ntfuzz` service metrics (`/metrics`)** measure origin-level process health: HTTP requests reaching the Go server, WebSocket terminal session concurrency, connection rejections (`connection_limit`, `origin`, `handshake`), session durations and disconnect causes (`client_disconnect`, `idle_timeout`, `server_shutdown`), PTY terminal byte throughput, and DuckDB snapshot publication/refresh latency and staleness.
- **CDN / reverse-proxy analytics** (such as edge CDN or ingress logs configured in deployment infrastructure) measure edge traffic before it reaches `ntfuzz`: CDN cache hits on static assets, WAF/rate-limit blocks at the edge, TLS handshake versions, geographic/ASN distributions, and unique visitor estimates. Keep CDN configuration, collectors, dashboards, and retention policies in deployment infrastructure rather than inside `ntfuzz`.

## Run Outcomes & Diagnostics

Package selection precedes all test flags: `go test ./subpackage -parallel=1
-run=^FuzzName$ -fuzz=^FuzzName$ -fuzztime=3s -test.fuzzcachedir=PATH`.
The coverage replay command uses the same package-first convention. Other
subprocesses are Git checkout/revision commands and `go env GOVERSION`; none
forward test-binary flags.

New runs report `ok` only after coordinator execution progress and a standalone
`PASS` with a successful exit. Zero reported executions can still be valid when
the coordinator reports progress; a positive mutation count is not the sole
success criterion. `missing_target` identifies Go's no-target/no-test warning,
`incomplete` identifies clean exits without completed fuzzing evidence (including
seed-only runs), `canceled` identifies interrupted jobs, `error` covers build,
startup, and runner timeout failures, and `crash` requires fuzz failure evidence.
Unknown output formats fail closed as incomplete. Shutdown cancellations are
recorded with a short independent persistence timeout when possible.

Failed runs expose `diagnostic` in `GET /api/runs?target=TARGET_ID&limit=2`.
The target detail TUI displays the selected run's last 12 diagnostic lines.
Capture retains approximately 16 KiB of beginning/end output, even for noisy
targets; successful runs do not retain output. Local repository, corpus, and
home paths, URL credentials, and common labeled secrets are redacted; terminal
control characters are removed. This is best-effort redaction, **not a guarantee
against arbitrary secrets printed by target code**. Run only projects whose
diagnostic output is suitable for the audience of the API. Public snapshots
include these diagnostics. Existing crash stacks and historical rows retain
their existing contents and retention policy; this update does not rewrite them.

Schema v3 adds nullable/default-empty run diagnostics. Writer migration is
automatic; viewers require a matching v3 snapshot. Upgrade the writer and viewer
together and publish a new snapshot before starting the new viewer. Old viewers
cannot refresh to v3 snapshots.

---

## API discovery

The browser dashboard includes server-rendered links to open crashes, projects,
status, API documentation (`/api/docs`), and an agent guide (`/llms.txt`).
`/openapi.json` describes the six read endpoints shared by `serve` and `view`,
including response schemas and query parameters. These embedded resources need
no JavaScript or external documentation service.

HTML link relations and HTTP `Link` headers advertise `service-doc`,
`service-desc`, and `describedby`. The guide explains crash investigation,
numeric target IDs, snapshot freshness, and server-local corpus paths.
Private live-server crash-fixing remains an operator action outside this read
API description; public snapshot viewers still expose no mutation endpoint.

## Open Collaboration

Contributions and feedback are welcome. Please adhere to our [Code of Conduct](./CODE_OF_CONDUCT.md) when engaging our community.

- [GitHub Issues](https://github.com/NimbleTerminal/ntfuzz/issues)
- [GitHub Pull Requests](https://github.com/NimbleTerminal/ntfuzz/pulls)

## Acknowledgements

Thanks to [DuckDB](https://duckdb.org/) and [`duckdb-go`](https://github.com/duckdb/duckdb-go), [Charm](https://charm.land) for [Bubble Tea](https://github.com/charmbracelet/bubbletea) and [Lip Gloss](https://github.com/charmbracelet/lipgloss), [Ghostty](https://ghostty.org/), and the [`ntwebterm`](https://github.com/NimbleTerminal/ntwebterm), [`ntduckbox`](https://github.com/NimbleTerminal/ntduckbox), and [`ntcharts`](https://github.com/NimbleMarkets/ntcharts) libraries.

## License

Released under the [MIT License](./LICENSE). Part of the [Nimble Terminal](https://nimbleterminal.com) platform.

Copyright (c) 2026 [Neomantra Corp](https://www.neomantra.com).

----
Made with :heart: and :fire: by the team behind [Nimble Terminal](https://nimbleterminal.com).
