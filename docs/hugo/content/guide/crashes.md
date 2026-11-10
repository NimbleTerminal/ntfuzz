---
title: "Crashes"
weight: 50
---

# Crashes

When `go test -fuzz` discovers a failing input, `ntfuzz` captures the failure output, copies the minimized reproducer into the persistent corpus directory, scrubs server-local filesystem paths from the stack trace, attributes external dependency crashes, computes a normalized stack fingerprint, and stores the crash in DuckDB.

## Stack scrubbing and fingerprinting

Raw Go stack traces contain machine-specific paths (such as `/var/lib/ntfuzz/.ntfuzz/repos/nt3d/...` and `/var/lib/ntfuzz/.cache/go-mod/...`), ephemeral goroutine IDs, test durations, and hex memory addresses.

At record time (and during schema migrations for historical crashes), `ntfuzz` runs two passes:

1. **Path scrubbing (`ScrubStack`)**:
   - Replaces the `GOMODCACHE` prefix with `<module>@<version>/<rel-path>.go:<line>` (for example `github.com/qmuntal/gltf@v0.29.0/decoder.go:151`).
   - Replaces the local checkout directory (`work_dir` / `repoDir`) with the Go module import path (for example `nimbleterminal.dev/nt3d/mesh/gltf/gltf.go:108`).
   - Leaves `GOROOT` standard-library frames (`/usr/local/go/src/...`) intact.
2. **Normalized stack fingerprinting (`FingerprintStack`)**:
   - Normalizes test failure durations (`(3.52s)` → `(0s)`), goroutine numbers (`goroutine 11456` → `goroutine N`), `file:line +0xOFFSET` tokens (collapsed to the `.go` basename), and hex pointers (`0x...` → `0xADDR`).
   - Hashes the normalized stack with SHA-256 and encodes the first 12 bytes as a 24-character hex `stack_fingerprint`.
   - `ScrubStack` is invariant under `FingerprintStack`: scrubbing a stack never changes its fingerprint, and line-number shifts inside the same file do not create duplicate open crash rows.

A new crash deduplicates against existing crashes for the same target if **either** `input_hash` or `stack_fingerprint` (including the pre-v6 legacy fingerprint) matches. Matching an open crash updates `last_seen`; matching a previously fixed crash automatically **reopens** it (`fixed_at = NULL`) and records a `reopened` burndown event.

---

## Dependency blame

Many fuzz crashes in a higher-level Go package actually panic inside a third-party dependency in `go.mod`. `ntfuzz` inspects the stack top-down (`AttributeStack`):

1. Skips test-runner log prefixes (`testing.go:2078: panic: ...`) and frames in `runtime`, `testing`, `reflect`, and `internal/fuzz`.
2. Checks the first non-runtime frame:
   - If that frame is in an external module cache path (`<module>@<version>/<file>.go:<line>`) before any frame in the project's own repository or module, `ntfuzz` attributes the crash to that dependency:
     - `blamed_module`: e.g. `github.com/qmuntal/gltf`
     - `blamed_version`: e.g. `v0.29.0`
     - `blamed_file`: e.g. `decoder.go`
     - `blamed_line`: e.g. `151`
     - `blamed_url`: clickable source URL (`https://github.com/qmuntal/gltf/blob/v0.29.0/decoder.go#L151` for GitHub/GitLab modules, stripping `/vN` major-version suffixes and resolving pseudo-versions to their commit SHA, or `https://pkg.go.dev/<module>@<version>` for other hosts).
   - If the first non-runtime frame is inside the project's own module, dependency blame fields remain empty (`PROJECT` badge).
   - If a stack contains no recognizable project or dependency frames (for example a truncated log line), it is rendered as `not attributed` rather than falsely blaming project code.

---

## Reproducer downloads and `public_inputs`

When `go test -fuzz` writes a failing input (`Failing input written to testdata/fuzz/FuzzTarget/...`):

1. `ntfuzz` verifies the path resolves inside the module or corpus tree, copies the file to `<corpus_dir>/crashes/<pkg-segment>/<Target>/<input_hash>` (where `<input_hash>` is the first 16 bytes of the file's SHA-256 digest in hex), and removes the working-tree copy so the Git checkout stays clean.
2. Up to **64 KiB** of the raw `go test fuzz v1` file is stored in the `crashes.input_bytes` column in DuckDB (`input_truncated = true` if the file exceeded 64 KiB).
3. `GET /api/crashes/{id}/input` serves those bytes with `Content-Type: text/plain; charset=utf-8` and `Content-Disposition: attachment; filename="<input_hash>"`.

### Privacy gate in snapshot `view` mode

- In **`ntfuzz serve`** (private operator mode), `GET /api/crashes/{id}/input` is always enabled.
- In **`ntfuzz view`** (public snapshot mode), `GET /api/crashes/{id}/input` is gated per project by `public_inputs: true` in `projects.yaml`:
  - When `public_inputs: false` (the default), `PublishSnapshot` strips `input_bytes` for that project from the published snapshot database file, and `GET /api/crashes/{id}/input` returns `403 Forbidden`.
  - When `public_inputs: true`, the snapshot retains `input_bytes`, `/crashes/{id}` displays the **Download Corpus Input** button, and `/api/crashes/{id}/report.md` includes the direct reproducer download link and `go test -run=<Target>/<input_hash>` reproduction instructions.

---

## Sharing crashes with upstream maintainers

Every crash has three shareable representations:

1. **HTML permalink (`GET /crashes/{id}`)**:
   - Server-rendered HTML (no JavaScript required) showing status (`OPEN`, `REPORTED`, or `FIXED`), project, package, target, revision, Go version, first/last seen timestamps, blamed dependency with a link to the exact source line, upstream issue link, replay verification status, scrubbed stack trace (wrapped in `<!--email_off-->` comments so Cloudflare Email Address Obfuscation never mangles `@v0.x.y` module versions), and buttons to download the corpus input, view the Markdown report, or open the interactive **Web Terminal Dashboard (`/`)**.
2. **Markdown advisory report (`GET /api/crashes/{id}/report.md` or `ntfuzz crash report <id>`)**:
   - Produces a self-contained Markdown section with a metadata table, blamed dependency link, scrubbed stack trace, and step-by-step reproduction commands:
     ```sh
     ntfuzz crash report 23 --api http://127.0.0.1:8080 --public-url https://fuzz.nimbleterminal.com
     ```
3. **JSON record (`GET /api/crashes/{id}`)**:
   - Machine-readable crash metadata for scripts and automation.

### Tracking upstream reports (`OPEN` → `REPORTED` → `FIXED`)

When you report an open crash upstream and are waiting for a maintainer fix, attach the issue or security advisory URL without closing the crash:

```sh
ntfuzz crash link 23 --issue https://github.com/qmuntal/gltf/security/advisories/GHSA-xxxx-xxxx-xxxx
```

This sets `issue_url` on the open crash. Both the TUI Crashes view (`c`), the **Crash Burndown** chart (`g` → **Crash Burndown**), and `/crashes/23` distinguish **unreported open crashes** from **reported open crashes** waiting on an upstream fix.

---

## Automatic saved-input replay and crash recovery

When a project advances to a new clean Git commit (`git_sha`), `ntfuzz` does **not** assume old crashes are fixed just because a short fuzz slice passed. Instead:

1. Targets with open or previously resolved crashes take priority over normal fuzzing backoff.
2. Before fuzzing the target on the new revision, `ntfuzz` copies the module to a disposable temporary directory, places each crash's saved reproducer file into `testdata/fuzz/<Target>/<input_hash>`, and runs `go test ./pkg -parallel=1 -run=^<Target>/<input_hash>$` with a 45-second timeout.
3. Outcomes are recorded in `crash_replays` (`GET /api/crash-replays`) and summarized on the crash record (`replay_status`, `replay_git_sha`, `replay_checked_at`, `replay_diagnostic`):
   - **`passed`**: the named subtest executed and passed on the new revision. If the crash was open, `ntfuzz` automatically marks it fixed (`fixed_at`) and records a `resolved` burndown event.
   - **`failed`**: the saved input still reproduces on the new revision. If the crash had been marked fixed manually, `ntfuzz` automatically reopens it.
   - **`blocked`**: the input file is missing, the checkout is dirty, the package failed to build, the target was removed, or the replay timed out. Blocked checks never close a crash and are retried after 1 hour.
   - **`pending`**: awaiting replay on the current revision.
