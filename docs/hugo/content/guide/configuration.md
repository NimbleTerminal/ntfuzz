---
title: "Configuration"
weight: 20
---

# Configuration

`ntfuzz serve` reads a YAML configuration file (`--config`, default `configs/projects.yaml`) once at startup. If `--config` is left at its default path `configs/projects.yaml` and that file does not exist on disk, `ntfuzz` falls back to the embedded `configs/projects.yaml` bundled in the binary.

## Example `projects.yaml`

```yaml
# Root directory for persisted fuzz caches and crash inputs
corpus_dir: .ntfuzz/corpus

# Directory where remote Git repositories are cloned and pulled
work_dir: .ntfuzz/repos

# Default per-target fuzzing budget when a project omits fuzztime
default_fuzztime: 15s

projects:
  # Local Go module directory
  - name: demo-parser
    repo: ./examples/demo-parser
    fuzztime: 10s
    public_inputs: true

  # Remote Git repository on a specific branch
  - name: go-yaml
    repo: https://github.com/go-yaml/yaml.git
    branch: v3
    fuzztime: 20s

  # Remote Git repository with custom corpus path and public crash reproducers enabled
  - name: klauspost-compress
    repo: https://github.com/klauspost/compress.git
    branch: master
    fuzztime: 30s
    corpus_dir: .ntfuzz/corpus/klauspost-compress
    public_inputs: true
```

## Top-level fields

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `corpus_dir` | `string` | `.ntfuzz/corpus` | Base directory where `ntfuzz` stores evolved fuzz caches (`<project>/cache/...`) and minimized crash inputs (`<project>/crashes/...`). |
| `work_dir` | `string` | `.ntfuzz/repos` | Directory where remote Git repositories are cloned (`<work_dir>/<name>`). Local directory `repo` paths are used in place without cloning into `work_dir`. |
| `default_fuzztime` | `duration` | `10s` | Default `-fuzztime` duration passed to `go test` for any project that omits its own `fuzztime`. Must be a positive Go duration string (for example `10s`, `30s`, `2m`). |
| `projects` | `list` | `[]` | List of Go repositories or local module directories to discover and fuzz. |

## Project fields (`projects[]`)

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `name` | `string` | *(required)* | Unique identifier for the project in DuckDB, the TUI, `/api/projects`, and directory paths (`<work_dir>/<name>` and `<corpus_dir>/<name>`). Must not contain path separators (`/` or `\`), `..`, or control characters. |
| `repo` | `string` | *(required)* | Local directory path (such as `./examples/demo-parser` or `/srv/src/myrepo`) or remote Git URL (`https://github.com/org/repo.git` or `git@github.com:org/repo.git`). Must not contain embedded credentials (`https://user:token@host/...`). |
| `branch` | `string` | `""` (remote default branch) | Optional Git branch to clone (`git clone --depth 1 --branch <branch>`) and track (`git checkout <branch>` + `git pull --ff-only`) on each discovery round. Ignored for local non-Git paths. |
| `fuzztime` | `duration` | `default_fuzztime` (`10s`) | Per-target `go test -fuzztime` budget for this project. Must be a positive Go duration string (such as `6s`, `20s`, `1m`). A runner timeout of `fuzztime + 45s` is enforced per target invocation. |
| `corpus_dir` | `string` | `<top-level corpus_dir>/<name>` | Override the persistent corpus directory for this specific project. Inside this directory, `ntfuzz` maintains `cache/<pkg-segment>/<Target>` (passed to `-test.fuzzcachedir`) and `crashes/<pkg-segment>/<Target>/<input-hash>`. |
| `public_inputs` | `bool` | `false` | Opt-in flag allowing the read-only snapshot viewer (`ntfuzz view`) to serve raw crash reproducer files from `GET /api/crashes/{id}/input` and retain `input_bytes` in published snapshot databases. When `false`, published snapshots strip `input_bytes` for the project and `ntfuzz view` returns `403 Forbidden` on `/api/crashes/{id}/input`. |

> [!NOTE]
> On `ntfuzz serve` startup, `public_inputs` values from `projects.yaml` are synchronized into DuckDB immediately before the initial snapshot is published, so toggling `public_inputs` and restarting `ntfuzz serve` takes effect on the first snapshot generation.

## Multi-module discovery and target scheduling

During each round, `ntfuzz`:
1. Syncs each project (`git clone --depth 1` on first sight, or `git pull --ff-only` on subsequent rounds) and records `git_sha`, `dirty` state, and `go_version`. If a pull fails, `ntfuzz` records the redacted error in `sync_error` without advancing `last_synced`.
2. Walks the repository tree to find every `go.mod` file (skipping hidden directories, `vendor`, `node_modules`, `testdata`, and `bin`), parses `*_test.go` files while respecting `//go:build` constraints, and registers every `FuzzXxx(f *testing.F)` function as a target. Removed targets are marked `active = FALSE`.
3. Schedules targets across `--workers N` concurrent local workers (`GOMAXPROCS=1` and `-parallel=1` per worker) with per-module exclusion so two workers never contend on the same Go module directory at the same time. Targets with pending crash replays run first; targets that recently discovered new corpus entries are prioritized ahead of flat targets.

## Private repositories

Do **not** place tokens, passwords, or SSH key paths in `projects.yaml`. If a `repo` URL contains embedded userinfo (`https://user:token@host/...`), `ntfuzz` refuses to start so secrets never leak into YAML, Terraform state, DuckDB, or `/api/projects`.

Instead, configure authentication in the OS user's Git and SSH environment:

- **Non-interactive Git environment**: `ntfuzz` runs `git` subprocesses with `GIT_TERMINAL_PROMPT=0` and (when `GIT_SSH_COMMAND` is unset) `GIT_SSH_COMMAND="ssh -o BatchMode=yes"`, overridable via `NTFUZZ_GIT_SSH_COMMAND`. Missing credentials fail fast instead of hanging on a prompt and are reported (with URLs and tokens redacted) in `sync_error`.
- **SSH deploy keys (recommended)**: Configure an `ed25519` key and `known_hosts` in `~/.ssh` for the `ntfuzz` service user and use `repo: git@github.com:Org/repo.git` (or per-repo host aliases in `~/.ssh/config`).
- **HTTPS credential helper or `insteadOf`**: Configure `git config --global credential.helper 'store --file ~/.git-credentials'` or `git config --global url."https://x-access-token:<PAT>@github.com/".insteadOf "https://github.com/"`, keeping `repo: https://github.com/Org/repo.git` clean in `projects.yaml`.
- **Private Go module dependencies**: When a fuzzed repository imports private Go modules, set `GOPRIVATE="github.com/Org/*"` in the service environment so `go test` uses the same Git credentials when populating `GOMODCACHE`.

See [Deployment — Private repositories](../deployment/#private-repositories) for step-by-step `systemd` commands.

## Browser dashboard branding and environment variables

Both `ntfuzz serve` and `ntfuzz view` support optional operator branding in the web terminal header while keeping the upstream **NTFUZZ** wordmark linked to `https://github.com/NimbleTerminal/ntfuzz`. CLI flags take precedence over environment variables:

| CLI Flag | Environment Variable | Default | Validation / Behavior |
| --- | --- | --- | --- |
| `--brand-name` | `NTFUZZ_BRAND_NAME` | `""` (omitted) | Operator or fleet name rendered as escaped plain text beside `NTFUZZ`. |
| `--brand-url` | `NTFUZZ_BRAND_URL` | `""` (unlinked) | Optional link for `--brand-name`; must be a valid `http://` or `https://` URL. |
| `--brand-accent` | `NTFUZZ_BRAND_ACCENT` | `#38bdf8` | Header accent color (`--ntfuzz-accent` on `.topbar`); must match 6-digit `#RRGGBB` hex. |
| *(env only)* | `NTFUZZ_GIT_SSH_COMMAND` | `ssh -o BatchMode=yes` | Overrides `GIT_SSH_COMMAND` for `git` subprocesses in `ntfuzz serve` when `GIT_SSH_COMMAND` is not already set. |
