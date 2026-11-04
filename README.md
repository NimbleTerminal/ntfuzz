# ntfuzz

`ntfuzz` is a single-binary continuous fuzzing orchestrator for Go repositories, backed by **DuckDB** (`duckdb-go/v2`) and a **Bubble Tea v2** TUI dashboard served both natively in your terminal and in the browser via **Booba** (`go-booba` + Ghostty/`ntwebterm`).

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
  - Versioned schema migrations (`schema_migrations` table, currently v2) for `projects`, `targets`, `runs`, `crashes`, indexes, and a single-query `latest_target_status` view that aggregates 10-point coverage trends via DuckDB `LIST`.
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
  - Surfaces keybindings as **Booba Terminal Actions** (OSC 7501) via `go-booba/actions/bubblehelp`.
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

## License

Released under the [MIT License](./LICENSE).



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
