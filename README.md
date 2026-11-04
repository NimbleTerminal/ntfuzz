# ntfuzz (`ntfuzzy`)

`ntfuzz` is a single-binary continuous fuzzing orchestrator for Go repositories, backed by **DuckDB** (`duckdb-go/v2`) and a **Bubble Tea v2** TUI dashboard served both natively in your terminal and in the browser via **Booba** (`go-booba` + Ghostty/`ntwebterm`).

---

## Features

- **Continuous Native Go Fuzzing Orchestrator**:
  - Syncs local Go modules or clones/pulls remote Git repositories.
  - Automatically discovers `FuzzXxx(f *testing.F)` targets across packages.
  - Runs `go test -fuzz=^FuzzXxx$ -fuzztime=...` with persistent per-target corpus directories (`-test.fuzzcachedir`).
  - Parses execution counts, throughput (`execs/sec`), new corpus additions, statement coverage (`%`), and crash artifacts/stacks.
- **DuckDB Metadata & History Store**:
  - Automatic schema migration (`projects`, `targets`, `runs`, `crashes`, indexes, and `latest_target_status` view).
  - Keeps fuzz corpus on the filesystem while storing structured run history, coverage trends, and deduplicated crashes in DuckDB.
- **HTTP JSON API (`/api/*`)**:
  - `GET /api/projects` — project list with latest status summary
  - `GET /api/targets?project=...` — targets with latest run metrics and coverage trend
  - `GET /api/runs?target=...&limit=100` — recent run history
  - `GET /api/crashes?open=true` — open (or all) crashes with stack traces and input paths
  - `GET /api/coverage/history?target=...` — chronological coverage & throughput series
  - `GET /api/status` — fleet health, 24h run counts, and active worker jobs
- **Shared Bubble Tea TUI + Booba Browser Bridge**:
  - The TUI talks strictly to the HTTP API (never opens DuckDB directly).
  - Surfaces keybindings as **Booba Terminal Actions** (OSC 7501) via `go-booba/actions/bubblehelp`.
  - Accessible both locally via `ntfuzz tui` and live in a browser Ghostty terminal at `http://localhost:8080/` during `ntfuzz serve`.

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

Then:
- **Browser TUI (Booba / Ghostty)**: Open **http://127.0.0.1:8080/** in your browser.
- **Terminal TUI**: In another terminal, run:
  ```bash
  task tui
  # or: ./bin/ntfuzz tui --api http://127.0.0.1:8080
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

Start the fleet orchestrator:

```bash
./bin/ntfuzz serve --config configs/projects.yaml --workers 2
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
| `r` | Refresh now | Poll the HTTP API immediately |
| `?` | Full help | Expand/collapse full keybinding help |
| `h` | Hide/Show help | Toggle in-terminal help footer |
| `q` / `ctrl+c` | Quit | Exit the TUI client |

---

## Project Layout

```text
cmd/ntfuzz/main.go         # CLI entry point (`ntfuzz serve` and `ntfuzz tui`)
configs/
  projects.yaml            # Default fleet configuration (also embedded)
  embed.go                 # go:embed directive for default config
internal/
  orchestrator/            # Git checkout/sync, AST fuzz target discovery, local runner
  store/                   # DuckDB schema migrations, latest_target_status view, queries
  api/                     # HTTP JSON API server (/api/*) and TUI HTTP client
  tui/                     # Pure Bubble Tea v2 model, Lipgloss styles, ntcharts sparkline
  booba/                   # go-booba WebSocket/Ghostty web bridge and embedded index.html
examples/
  demo-parser/             # Sample Go module with FuzzParseKV and FuzzEvalSum
  demo-codec/              # Sample Go module with FuzzFrameRoundTrip and FuzzDecodeArbitrary
```
