---
title: "ntfuzz"
type: docs
bookToC: false
---

# ntfuzz

`ntfuzz` (`nimbleterminal.dev/ntfuzz`) is a single-binary continuous fuzzing orchestrator and dashboard for Go repositories. It discovers native `FuzzXxx(f *testing.F)` targets across local modules and remote Git repositories, schedules `go test -fuzz` workers, tracks corpus coverage and crash history in **DuckDB**, and serves a **[Bubble Tea v2](https://github.com/charmbracelet/bubbletea)** terminal dashboard both in your terminal and in the browser via **[Booba](https://github.com/NimbleTerminal/booba)** and **[Ghostty](https://ghostty.org/) / [ntwebterm](https://github.com/NimbleTerminal/ntwebterm)**.

![ntfuzz TUI dashboard showing fleet overview, target coverage sparklines, and status cards](images/dashboard.png)

## Live dashboard

Explore the public read-only snapshot dashboard for the NimbleTerminal Go fleet at **[fuzz.nimbleterminal.com](https://fuzz.nimbleterminal.com/)**.

## Quick start

```sh
# Install or build ntfuzz (requires Go 1.27+ and CGO_ENABLED=1 for DuckDB)
go install nimbleterminal.dev/ntfuzz/cmd/ntfuzz@latest

# Start the orchestrator, DuckDB writer, HTTP API, and browser TUI
ntfuzz serve --config configs/projects.yaml --db ntfuzz.duckdb --listen 127.0.0.1:8080

# Or open the native terminal dashboard against the running API
ntfuzz tui --api http://127.0.0.1:8080
```

When `configs/projects.yaml` is not present on disk, `ntfuzz serve` falls back to its embedded default configuration so you can try the server immediately. In a checkout of the repository, `configs/projects.yaml` points at two self-contained example modules (`examples/demo-parser` and `examples/demo-codec`).

## Where to look

- **[Guide](guide/)**:
  - [Getting started](guide/getting-started/) — installation, running the bundled demos, and navigating the terminal and browser TUI.
  - [Configuration](guide/configuration/) — the `projects.yaml` schema, private repository authentication, and browser branding.
  - [Commands](guide/commands/) — `serve`, `view`, `tui`, and `crash`, and how the private writer differs from the public snapshot viewer.
  - [HTTP API](guide/http-api/) — `/api/*` JSON endpoints, `/crashes/{id}` permalinks, OpenAPI discovery, and Prometheus metrics.
  - [Crashes](guide/crashes/) — stack scrubbing and fingerprinting, dependency blame, automatic replay and recovery, corpus downloads (`public_inputs`), and upstream Markdown reports.
  - [Deployment](guide/deployment/) — running `ntfuzz serve` and `ntfuzz view` under separate OS users with `systemd`, Caddy, and Terraform.
- **[Live public dashboard](https://fuzz.nimbleterminal.com/)**: see `ntfuzz view` serving the NimbleTerminal fleet in production.
- **[GitHub repository](https://github.com/NimbleTerminal/ntfuzz)**: source code, issues, reference `systemd` units, and DigitalOcean Terraform module.

## Source

`ntfuzz` is MIT-licensed and part of the [Nimble Terminal](https://nimbleterminal.com) platform. Source, issues, and releases are on [GitHub](https://github.com/NimbleTerminal/ntfuzz).
