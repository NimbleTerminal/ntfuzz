---
title: "Commands"
weight: 30
---

# Commands

The `ntfuzz` binary provides five subcommands (plus `ntfuzz --version` / `-v`):

- [`ntfuzz serve`](#ntfuzz-serve) — the single-writer fuzzing orchestrator, DuckDB store, local operator HTTP API, and Booba browser TUI.
- [`ntfuzz view`](#ntfuzz-view) — the sandboxed, read-only public snapshot viewer backed by `ntduckbox`.
- [`ntfuzz tui`](#ntfuzz-tui) — the native terminal Bubble Tea dashboard client.
- [`ntfuzz crash`](#ntfuzz-crash) — the CLI for listing, linking, reporting, and resolving crashes over the HTTP API.
- [`ntfuzz version`](#ntfuzz-version) — print the `ntfuzz` binary version (`ntfuzz <version>`).

## `serve` vs. `view`

DuckDB enforces a single-writer lock per database file, and a public internet service should never have write access to your fuzzing database, corpus directories, or Git credentials. `ntfuzz` separates the private writer from the public viewer:

| Property | `ntfuzz serve` (Writer) | `ntfuzz view` (Public Viewer) |
| --- | --- | --- |
| **Role** | Clones/pulls repos, runs `go test -fuzz` workers, writes `live.duckdb`, and optionally publishes immutable snapshots (`--snapshot-dir`). | Watches a directory of published snapshots (`--snapshots`) and serves the browser TUI and read-only HTTP API. |
| **Database access** | Read/write DuckDB connection with automatic schema migrations and 14-day run retention pruning. | Read-only via [`ntduckbox`](https://github.com/NimbleTerminal/ntduckbox): locked configuration, no migrations, no extension loading or external file/network reads, and bounded query rows (10,000), output (16 MiB), and duration (5s). |
| **Subprocesses** | Spawns `git` and `go test` child processes. | Never spawns child processes. |
| **Mutation endpoints** | Registers `POST /api/crashes/{id}/issue` and `POST /api/crashes/{id}/fix`; TUI `x` key marks crashes fixed. | Registers **no** mutation endpoints (`404` / `405`); TUI `x` key is disabled and header shows `read-only` + snapshot timestamp. |
| **Crash input downloads** | `GET /api/crashes/{id}/input` is always served for any crash with stored input bytes or corpus file. | `GET /api/crashes/{id}/input` is served **only** when the project opts in with `public_inputs: true` (`403 Forbidden` otherwise; non-public `input_bytes` are stripped from snapshot files at publish time). |
| **Intended network exposure** | Loopback only (`127.0.0.1:8080`), accessed locally or via SSH port-forwarding. | Public internet behind a TLS reverse proxy (such as Caddy), optionally inside a `PrivateNetwork=true` namespace via `--listen-fd 3`. |

---

## `ntfuzz serve`

Run the fuzzing orchestrator, DuckDB writer, HTTP JSON API, and Booba browser TUI.

```sh
ntfuzz serve [flags]
```

### Flags

| Flag | Short | Default | Description |
| --- | --- | --- | --- |
| `--config` | `-c` | `"configs/projects.yaml"` | Path to the projects YAML configuration file (read once at startup; uses the embedded default if `"configs/projects.yaml"` is not found on disk). |
| `--db` | `-d` | `"ntfuzz.duckdb"` | Path to the DuckDB database file. |
| `--listen` | `-l` | `"127.0.0.1:8080"` | HTTP listen address for the JSON API and Booba web TUI. |
| `--workers` | `-w` | `2` | Number of concurrent local fuzz workers (each capped at `GOMAXPROCS=1` and `-parallel=1`). |
| `--interval` | | `5s` | Pause interval between fuzzing rounds. |
| `--snapshot-dir` | | `""` (disabled) | Directory in which to publish immutable DuckDB snapshot generations (`snapshot-*.duckdb` and `current.json`). |
| `--snapshot-interval` | | `1m0s` | Interval between database snapshot publications (must be positive when `--snapshot-dir` is set). |
| `--no-orchestrator` | | `false` | Sync configured projects once and serve the HTTP API and Booba TUI without running background fuzz jobs. |
| `--metrics` | | `false` | Enable the loopback Prometheus `/metrics` listener (defaults to `127.0.0.1:9090` unless `--metrics-listen` is set). |
| `--metrics-listen` | | `""` (disabled) | Explicit HTTP listen address for `/metrics` (binds to `127.0.0.1` when host is omitted, e.g. `:9090`). |
| `--brand-name` | | `""` | Optional operator or fleet display name shown beside `NTFUZZ` in the web header (or `$NTFUZZ_BRAND_NAME`). |
| `--brand-url` | | `""` | Optional `http://` or `https://` website URL for `--brand-name` (or `$NTFUZZ_BRAND_URL`). |
| `--brand-accent` | | `""` (`#38bdf8`) | Optional header accent color in `#RRGGBB` format (or `$NTFUZZ_BRAND_ACCENT`). |
| `--debug` | | `false` | Enable verbose Booba server logging. |

---

## `ntfuzz view`

Serve immutable DuckDB snapshots published by `ntfuzz serve --snapshot-dir` through a restricted read-only web terminal and HTTP API.

```sh
ntfuzz view --snapshots /var/lib/ntfuzz/public --listen 127.0.0.1:8081 --refresh 10s
```

### Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--snapshots` | `"snapshots"` | Directory containing published snapshot generations (`current.json` and `snapshot-*.duckdb`). |
| `--listen` | `"127.0.0.1:8081"` | HTTP listen address for the viewer web TUI and read-only API. |
| `--listen-fd` | `0` | Inherit a pre-bound listening socket file descriptor (for example `--listen-fd 3` under `systemd` socket activation with `PrivateNetwork=true`). |
| `--refresh` | `10s` | Polling interval to check `current.json` for a new published snapshot generation (must be positive). |
| `--metrics` | `false` | Enable the loopback Prometheus `/metrics` listener (defaults to `127.0.0.1:9091` unless `--metrics-listen` is set). |
| `--metrics-listen` | `""` (disabled) | Explicit HTTP listen address for `/metrics` (binds to `127.0.0.1` when host is omitted, e.g. `:9091`). |
| `--brand-name` | `""` | Optional operator or fleet display name shown beside `NTFUZZ` in the web header (or `$NTFUZZ_BRAND_NAME`). |
| `--brand-url` | `""` | Optional `http://` or `https://` website URL for `--brand-name` (or `$NTFUZZ_BRAND_URL`). |
| `--brand-accent` | `""` (`#38bdf8`) | Optional header accent color in `#RRGGBB` format (or `$NTFUZZ_BRAND_ACCENT`). |

---

## `ntfuzz tui`

Run the interactive Bubble Tea dashboard in your local terminal against a running `ntfuzz serve` or `ntfuzz view` HTTP API.

```sh
ntfuzz tui --api http://127.0.0.1:8080
```

### Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--api` | `"http://127.0.0.1:8080"` | Base URL of the `ntfuzz` HTTP API (`serve` or `view`). |
| `--refresh` | `2s` | Dashboard polling interval. |

---

## `ntfuzz crash`

List or triage recorded crashes from the command line via the `ntfuzz` HTTP API.

```sh
ntfuzz crash <subcommand> [flags]
```

### Persistent flag

| Flag | Default | Description |
| --- | --- | --- |
| `--api` | `"http://127.0.0.1:8080"` | Base URL of the `ntfuzz` HTTP API. |

### Subcommands

#### `ntfuzz crash list`

List crashes from `GET /api/crashes`, printing crash ID, state (`OPEN`, `REPORTED`, or `FIXED (<timestamp>)`), `<project>/<target>@<git_sha>`, blamed component (`project` or `<module>@<version> (<url>)`), input hash, stack fingerprint, minimized input path, and linked issue URL.

```sh
ntfuzz crash list --api http://127.0.0.1:8080
ntfuzz crash list --open=false --api http://127.0.0.1:8080
```

| Flag | Default | Description |
| --- | --- | --- |
| `--open` | `true` | Show only open crashes (`--open=false` includes fixed crashes). |

#### `ntfuzz crash link <crash-id>`

Attach an upstream issue or security advisory URL to an open crash (`POST /api/crashes/{id}/issue`, `serve` only) without marking it fixed. The crash transitions to the **reported** state while remaining open for replay verification.

```sh
ntfuzz crash link 23 --issue https://github.com/qmuntal/gltf/security/advisories/GHSA-xxxx-xxxx-xxxx
```

| Flag | Default | Description |
| --- | --- | --- |
| `--issue` | *(required)* | Upstream `http://` or `https://` issue or advisory URL. |

#### `ntfuzz crash report <crash-id>`

Fetch and print a shareable Markdown bug report (`GET /api/crashes/{id}/report.md`) suitable for pasting into a GitHub issue or security advisory.

```sh
ntfuzz crash report 23 --api http://127.0.0.1:8080 --public-url https://fuzz.nimbleterminal.com
```

| Flag | Default | Description |
| --- | --- | --- |
| `--public-url` | `""` (uses `--api`) | Public base URL used for permalink (`/crashes/{id}`) and reproducer download (`/api/crashes/{id}/input`) links inside the generated Markdown report. |

#### `ntfuzz crash fixed <crash-id>`

Mark a crash as fixed (`POST /api/crashes/{id}/fix`, `serve` only) and optionally attach or update its `issue_url`.

```sh
ntfuzz crash fixed 23 --issue https://github.com/qmuntal/gltf/issues/123
```

| Flag | Default | Description |
| --- | --- | --- |
| `--issue` | `""` | Optional `http://` or `https://` issue URL to record on the crash. |

---

## `ntfuzz version`

Print the `ntfuzz` version string (stamped via `-ldflags "-X main.version=..."` during `task build`, or read from Go module build info when installed via `go install`).

```sh
ntfuzz version
ntfuzz --version
```
