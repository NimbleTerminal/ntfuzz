---
title: "Getting started"
weight: 10
---

# Getting started

`ntfuzz` compiles to a single executable that includes the fuzzing orchestrator, DuckDB storage engine, HTTP JSON API, native Bubble Tea terminal client, and Booba browser terminal server.

## Prerequisites

- **Go 1.27 or later** (`go` must also be on `PATH` at runtime so `ntfuzz serve` can invoke `go test -fuzz` and `go env GOVERSION`).
- **A C/C++ toolchain** (`CGO_ENABLED=1`) when building from source, required by [`duckdb-go/v2`](https://github.com/duckdb/duckdb-go).
- **`git`** on `PATH` when fuzzing remote Git repositories or inspecting working-tree revisions.

## Install

Install the binary directly with `go install`:

```sh
go install nimbleterminal.dev/ntfuzz/cmd/ntfuzz@latest
```

Or clone the repository to use the bundled example Go modules (`examples/demo-parser` and `examples/demo-codec`) and `Taskfile.yml`:

```sh
git clone https://github.com/NimbleTerminal/ntfuzz.git
cd ntfuzz

# Build ./bin/ntfuzz
task build
# or: make build
# or: go build -o bin/ntfuzz ./cmd/ntfuzz

# Run the unit and integration test suite
task test
```

## Quick start with the bundled examples

Out of the box, `configs/projects.yaml` in the repository is configured with two self-contained Go modules under `examples/demo-parser` and `examples/demo-codec`:

```sh
task serve
# or: ./bin/ntfuzz serve --config configs/projects.yaml --db ntfuzz.duckdb --listen 127.0.0.1:8080
```

> [!IMPORTANT]
> `configs/projects.yaml` is read once at process startup—restart `ntfuzz serve` after editing it. Only one `ntfuzz serve` process may open a given `.duckdb` file at a time. The `serve` HTTP listener is an unauthenticated local operator console bound to `127.0.0.1` by default; never expose it directly to the public internet. Use [`ntfuzz view`](../commands/#ntfuzz-view) for public dashboards.

Once `ntfuzz serve` is running, you can interact with it in four ways:

### 1. Browser TUI (Booba + Ghostty)

Open **`http://127.0.0.1:8080/`** in your browser. Booba streams the Bubble Tea dashboard into a WebAssembly Ghostty terminal (`ntwebterm`) over WebSocket/WebTransport and renders clickable **Terminal Actions** buttons for every active keybinding.

### 2. Native terminal TUI

In another terminal window, connect the native Bubble Tea client to the local API:

```sh
task tui
# or: ./bin/ntfuzz tui --api http://127.0.0.1:8080
```

### 3. Crash triage CLI

List open crashes, attach an upstream issue or advisory URL, generate a Markdown bug report, or mark a crash fixed:

```sh
./bin/ntfuzz crash list --api http://127.0.0.1:8080
./bin/ntfuzz crash link 1 --issue https://github.com/org/repo/issues/42
./bin/ntfuzz crash report 1 --api http://127.0.0.1:8080 --public-url https://fuzz.example.com
./bin/ntfuzz crash fixed 1 --issue https://github.com/org/repo/issues/42
```

### 4. HTTP JSON API

Query fleet health, projects, targets, runs, and crashes directly with `curl`:

```sh
curl -s http://127.0.0.1:8080/api/status | jq
curl -s http://127.0.0.1:8080/api/projects | jq
curl -s http://127.0.0.1:8080/api/targets | jq
```

## TUI keybindings

The same Bubble Tea model runs in both `ntfuzz tui` and the browser dashboard. In the browser, keybindings are also advertised as **Booba Terminal Actions** (OSC 7501) so you can tap or click them without opening a mobile keyboard.

| Key | Action | Description |
| --- | --- | --- |
| `↑` / `k`, `↓` / `j` | Move up / down | Navigate targets, runs, or crashes |
| `enter` | Target detail / Crash detail | Open the selected target's run history and coverage sparkline, or drill into a crash |
| `esc` / `backspace` | Back | Return to the previous view or main fleet overview |
| `s` | Sort column | Cycle the active sort column in the fleet table |
| `d` | Sort direction | Toggle ascending / descending sort order |
| `p` / `tab` | Filter project | Cycle through projects (`All Projects` → project 1 → ...) |
| `g` | Stats graphs | Toggle the canned time-series `ntcharts` view (`throughput`, `coverage`, `outcomes`, `revisions`, `burndown`) |
| `w` | Time window | Cycle the time window (`1h` → `6h` → `24h` → `7d` → `14d`) in Stats view |
| `i` | Chart mode | Toggle between Kitty graphics image charts (`chartpicture`) and text charts in Stats view |
| `[` / `]` | Page | Page through revision records or wrapped compact cards |
| `c` | Open crashes | Toggle the fleet-wide open crashes view |
| `x` | Mark crash fixed | Mark the selected open crash as fixed (enabled in `serve` mode; disabled in read-only `view` mode) |
| `r` | Refresh now | Poll the HTTP API immediately |
| `?` | Full help | Expand or collapse full keybinding help |
| `h` | Hide/Show help | Toggle the in-terminal help footer |
| `q` / `ctrl+c` | Quit | Exit the TUI session |

## Kitty graphics and responsive layouts

- **Kitty image charts (`i`)**: In the **Stats graphs** (`g`) view, pressing `i` toggles high-resolution image charts rendered via `ntcharts/picture/chartpicture/v2`. Each terminal session is probed independently before sending Kitty graphics; terminals that do not support Kitty graphics keep the text charts. In native terminals where capability queries are blocked, set `NTCHARTS_KITTY=supported` (or `unsupported` to force text mode). Under `tmux`, enable `allow-passthrough` (`NTCHARTS_TMUX_PASSTHROUGH` controls passthrough wrapping).
- **Compact mobile and narrow viewports**: Below 80 columns or 24 rows, the Fleet and Stats views switch automatically to compact card and single-series presentations rather than shrinking the font size. On touch devices, tapping or swiping the browser terminal does not pop up the soft keyboard unless you explicitly tap the **Keyboard** action button.
