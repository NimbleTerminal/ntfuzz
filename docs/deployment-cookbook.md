# `ntfuzz` Self-Hosting & Deployment Cookbook

This cookbook walks through deploying your own public or internal `ntfuzz` continuous Go fuzzing dashboard—modeled after [`fuzz.nimbleterminal.com`](https://fuzz.nimbleterminal.com)—with strict privilege separation between the private fuzzing orchestrator and the public browser terminal.

---

## Table of Contents

1. [Production Architecture & Security Model](#1-production-architecture--security-model)
2. [Recipe 1: DigitalOcean Terraform Quickstart](#2-recipe-1-digitalocean-terraform-quickstart)
3. [Recipe 2: Manual / Bare-Metal Linux Setup (`systemd` + Caddy)](#3-recipe-2-manual--bare-metal-linux-setup-systemd--caddy)
4. [Recipe 3: Maximum Isolation with `systemd` Socket Activation](#4-recipe-3-maximum-isolation-with-systemd-socket-activation)
5. [Day-2 Operator Workflows](#5-day-2-operator-workflows)
   - [Triage & Mark Crashes Fixed via SSH Tunnel](#51-triage--mark-crashes-fixed-via-ssh-tunnel)
   - [Adding Repositories & Tuning Fuzz Budgets](#52-adding-repositories--tuning-fuzz-budgets)
   - [Private repositories](#53-private-repositories)
   - [Customizing Browser Dashboard Branding](#54-customizing-browser-dashboard-branding)
   - [Upgrading `ntfuzz` & Schema Migrations](#55-upgrading-ntfuzz--schema-migrations)
   - [Prometheus Metrics & Alerting](#56-prometheus-metrics--alerting)
   - [Backups & Volume Recovery](#57-backups--volume-recovery)

---

## 1. Production Architecture & Security Model

DuckDB enforces a single-writer lock per database file, and a public internet listener should never have write access to your fuzzing database, raw crash inputs, or Git worktrees.

In production, `ntfuzz` splits responsibility across two separate OS processes and users:

```mermaid
flowchart LR
    subgraph Internet["Public Internet"]
        Browser["Browser / API Client<br/>(WebSocket + REST)"]
        Operator["Operator Workstation<br/>(ntfuzz tui / ntfuzz crash)"]
    end

    subgraph Host["Linux Host / Droplet"]
        Caddy["Caddy Reverse Proxy<br/>:80 / :443 (Auto TLS)"]

        subgraph ViewerBox["User: ntfuzz-view (Read-Only Sandbox)"]
            View["ntfuzz view<br/>127.0.0.1:8081<br/>metrics: 127.0.0.1:9091"]
        end

        subgraph Volume["Persistent Volume (/var/lib/ntfuzz)"]
            PublicDir["/var/lib/ntfuzz/public (0755)<br/>current.json + snapshot-*.duckdb"]
            PrivateDir["/var/lib/ntfuzz/private (0750)<br/>live.duckdb"]
            CorpusDir["/var/lib/ntfuzz/corpus (0750)<br/>Fuzz Corpora & Crash Inputs"]
            ReposDir["/var/lib/ntfuzz/repos (0750)<br/>Git Worktrees"]
        end

        subgraph WriterBox["User: ntfuzz (Private Writer)"]
            Serve["ntfuzz serve<br/>127.0.0.1:8080<br/>metrics: 127.0.0.1:9090"]
        end
    end

    Browser -->|"HTTPS :443"| Caddy
    Caddy -->|"reverse_proxy"| View
    View -->|"Read-only open"| PublicDir
    Serve -->|"Atomic snapshot publish<br/>(every 1m)"| PublicDir
    Serve -->|"Read / Write"| PrivateDir
    Serve -->|"Read / Write"| CorpusDir
    Serve -->|"git clone / pull +<br/>go test -fuzz"| ReposDir
    Operator -.->|"SSH Tunnel<br/>-L 8080:127.0.0.1:8080"| Serve
```

### Why This Split Matters

| Property | Private Writer (`ntfuzz serve`) | Public Viewer (`ntfuzz view`) |
| :--- | :--- | :--- |
| **Linux User** | `ntfuzz` | `ntfuzz-view` |
| **Network Binding** | `127.0.0.1:8080` (loopback only) | `127.0.0.1:8081` behind Caddy `:443` |
| **Database Access** | Reads/writes `/var/lib/ntfuzz/private/live.duckdb` | Reads immutable snapshots in `/var/lib/ntfuzz/public` |
| **Filesystem Access** | Read/write to `/var/lib/ntfuzz` | `ReadOnlyPaths=/var/lib/ntfuzz/public`; `InaccessiblePaths` hides `private`, `corpus`, `repos`, `cache` |
| **Mutation APIs** | `POST /api/crashes/{id}/fix` enabled | Mutation routes not registered (`404`/`405`); TUI `x` key disabled |
| **Subprocesses** | Runs `git` and `go test -fuzz` workers | Never spawns child processes |

---

## 2. Recipe 1: DigitalOcean Terraform Quickstart

The fastest way to stand up a complete instance is the ready-made DigitalOcean Terraform module in [`deploy/terraform/digitalocean/`](../deploy/terraform/digitalocean/).

It provisions:
- A **DigitalOcean Block Storage Volume** (`ext4`, mounted at `/var/lib/ntfuzz`) so your database and fuzzing corpora survive Droplet rebuilds.
- An **Ubuntu 24.04 LTS Droplet** bootstrapped via [`cloud-init.yaml.tftpl`](../deploy/terraform/digitalocean/cloud-init.yaml.tftpl) with Go, `ntfuzz`, `ntfuzz-serve.service`, `ntfuzz-view.service`, and Caddy.
- A **DigitalOcean Cloud Firewall** allowing only SSH (`22`) and HTTP/HTTPS/HTTP3 (`80`, `443`).
- Optional **DigitalOcean DNS `A`/`AAAA` records** when `manage_dns = true`.

### Step 1: Configure `terraform.tfvars`

```bash
cd deploy/terraform/digitalocean
cp terraform.tfvars.example terraform.tfvars
```

Edit `terraform.tfvars` with your SSH key fingerprint, domain name, branding, and repositories to fuzz:

```hcl
name                 = "ntfuzz"
region               = "nyc3"
droplet_size         = "s-4vcpu-8gb"
volume_size_gib      = 50
ssh_key_fingerprints = ["aa:bb:cc:dd:ee:ff:00:11:22:33:44:55:66:77:88:99"]

domain      = "fuzz.example.com"
manage_dns  = true
dns_zone    = "example.com"
record_name = "fuzz"

workers      = 2
brand_name   = "Example Security"
brand_url    = "https://example.com"
brand_accent = "#58A6FF"

projects_yaml = <<-YAML
  corpus_dir: /var/lib/ntfuzz/corpus
  work_dir: /var/lib/ntfuzz/repos
  default_fuzztime: 15s

  projects:
    - name: ntfuzz
      repo: https://github.com/NimbleTerminal/ntfuzz.git
      branch: main
      fuzztime: 15s
YAML
```

### Step 2: Apply with Terraform

```bash
export DIGITALOCEAN_TOKEN="dop_v1_..."
terraform init
terraform apply
```

### Step 3: Verify First-Boot Provisioning

Wait 2–3 minutes for `cloud-init` to finish installing Go, building `ntfuzz`, and starting the services:

```bash
ssh root@$(terraform output -raw ipv4_address) \
  'cloud-init status --wait && systemctl status ntfuzz-serve ntfuzz-view caddy'
```

Once the first snapshot is published (within 60 seconds of `ntfuzz-serve` starting), visit `https://fuzz.example.com` in your browser.

---

## 3. Recipe 2: Manual / Bare-Metal Linux Setup (`systemd` + Caddy)

You can deploy the exact same architecture on any Ubuntu/Debian VM, bare-metal server, or cloud provider using the reference files in [`deploy/systemd/`](../deploy/systemd/).

### Step 1: Install Go, Build Dependencies, and `ntfuzz`

```bash
sudo apt-get update
sudo apt-get install -y build-essential ca-certificates curl git jq pkg-config

# Install Go 1.27+ (amd64 or arm64)
ARCH="amd64"
[ "$(uname -m)" = "aarch64" ] && ARCH="arm64"
curl -fsSL "https://go.dev/dl/go1.27.3.linux-${ARCH}.tar.gz" -o /tmp/go.tar.gz
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf /tmp/go.tar.gz
sudo ln -sf /usr/local/go/bin/go /usr/local/bin/go
sudo ln -sf /usr/local/go/bin/gofmt /usr/local/bin/gofmt

# Clone into /opt/ntfuzz-src (persists across reboots for in-place upgrades)
# and build ntfuzz (CGO_ENABLED=1 is required for DuckDB)
sudo rm -rf /opt/ntfuzz-src
sudo git clone https://github.com/NimbleTerminal/ntfuzz.git /opt/ntfuzz-src
sudo env PATH="/usr/local/go/bin:${PATH}" HOME="/root" CGO_ENABLED=1 \
  go -C /opt/ntfuzz-src build -trimpath -o /usr/local/bin/ntfuzz ./cmd/ntfuzz
sudo chmod 0755 /usr/local/bin/ntfuzz
```

### Step 2: Create Isolated Users and Data Directories

```bash
sudo useradd --system --user-group --home-dir /var/lib/ntfuzz --no-create-home --shell /usr/sbin/nologin ntfuzz
sudo useradd --system --user-group --home-dir /var/lib/ntfuzz/public --no-create-home --shell /usr/sbin/nologin ntfuzz-view

# /var/lib/ntfuzz is 0755 so ntfuzz-view can traverse to /var/lib/ntfuzz/public,
# while private/, corpus/, repos/, and cache/ are 0750 (accessible only to ntfuzz).
sudo install -d -m 0755 -o root -g root /var/lib/ntfuzz
sudo install -d -m 0750 -o ntfuzz -g ntfuzz /var/lib/ntfuzz/private
sudo install -d -m 0750 -o ntfuzz -g ntfuzz /var/lib/ntfuzz/corpus
sudo install -d -m 0750 -o ntfuzz -g ntfuzz /var/lib/ntfuzz/repos
sudo install -d -m 0750 -o ntfuzz -g ntfuzz /var/lib/ntfuzz/cache
sudo install -d -m 0755 -o ntfuzz -g ntfuzz /var/lib/ntfuzz/public
sudo install -d -m 0755 -o root -g root /etc/ntfuzz
```

### Step 3: Install Configuration and `systemd` Units

Copy the templates from [`deploy/systemd/`](../deploy/systemd/):

```bash
sudo cp deploy/systemd/projects.yaml.example /etc/ntfuzz/projects.yaml
sudo cp deploy/systemd/ntfuzz.env.example /etc/ntfuzz/ntfuzz.env
sudo cp deploy/systemd/ntfuzz-serve.service /etc/systemd/system/ntfuzz-serve.service
sudo cp deploy/systemd/ntfuzz-view.service /etc/systemd/system/ntfuzz-view.service

sudo systemctl daemon-reload
sudo systemctl enable --now ntfuzz-serve.service
sudo systemctl enable --now ntfuzz-view.service
```

Check that `ntfuzz-serve` published an initial snapshot and `ntfuzz-view` opened it:

```bash
ls -la /var/lib/ntfuzz/public/
curl -s http://127.0.0.1:8081/api/status | jq .
```

### Step 4: Install and Configure Caddy for Automatic HTTPS

Install the official Caddy package for Debian/Ubuntu:

```bash
sudo apt-get install -y debian-keyring debian-archive-keyring apt-transport-https curl
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' | sudo gpg --dearmor --yes -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' | sudo tee /etc/apt/sources.list.d/caddy-stable.list >/dev/null
sudo apt-get update
sudo apt-get install -y caddy
```

Copy [`deploy/systemd/Caddyfile.example`](../deploy/systemd/Caddyfile.example) to `/etc/caddy/Caddyfile` and replace `fuzz.example.com` with your domain:

```caddyfile
fuzz.example.com {
	encode zstd gzip

	reverse_proxy 127.0.0.1:8081 {
		flush_interval -1
	}

	header {
		X-Content-Type-Options "nosniff"
		X-Frame-Options "SAMEORIGIN"
		Referrer-Policy "strict-origin-when-cross-origin"
	}
}
```

Reload Caddy:

```bash
sudo systemctl reload caddy
```

> **Using Cloudflare Proxy (Orange Cloud)?**
> Set your Cloudflare SSL/TLS encryption mode to **Full (strict)** so Cloudflare validates Caddy's origin certificate, and ensure **WebSockets** are enabled in your Cloudflare Network settings so the interactive Booba web terminal streams cleanly.

---

## 4. Recipe 3: Maximum Isolation with `systemd` Socket Activation

`ntfuzz view` supports inheriting a pre-bound socket descriptor from `systemd` via `--listen-fd 3`. Combining [`deploy/systemd/ntfuzz-view.socket`](../deploy/systemd/ntfuzz-view.socket) with `PrivateNetwork=true` places the public viewer process inside an isolated network namespace with **zero** network interfaces—not even loopback—while still serving traffic over the inherited socket descriptor from the host namespace.

1. Install [`deploy/systemd/ntfuzz-view.socket`](../deploy/systemd/ntfuzz-view.socket) to `/etc/systemd/system/ntfuzz-view.socket`.
2. In `/etc/systemd/system/ntfuzz-view.service`, update `ExecStart` and add `PrivateNetwork=true`:
   ```ini
   Sockets=ntfuzz-view.socket
   PrivateNetwork=true
   ExecStart=/usr/local/bin/ntfuzz view \
     --snapshots /var/lib/ntfuzz/public \
     --listen-fd 3 \
     --refresh ${NTFUZZ_VIEW_REFRESH} \
     --metrics=false
   ```
3. Reload and enable the socket:
   ```bash
   sudo systemctl daemon-reload
   sudo systemctl stop ntfuzz-view.service
   sudo systemctl enable --now ntfuzz-view.socket
   sudo systemctl start ntfuzz-view.service
   ```

---

## 5. Day-2 Operator Workflows

### 5.1 Triage & Mark Crashes Fixed via SSH Tunnel

The public viewer (`:8081` / `https://fuzz.example.com`) is strictly read-only. To mark crashes fixed (`POST /api/crashes/{id}/fix` or `x` in the TUI) or inspect live orchestrator state before the next snapshot interval, open an SSH port-forward to the private writer on `127.0.0.1:8080` (use `root@` on DigitalOcean images, or your SSH login user such as `ubuntu@`/`debian@` on other hosts):

```bash
# Terminal 1: forward local port 8080 to the remote private writer
ssh -N -L 8080:127.0.0.1:8080 root@fuzz.example.com
```

In a second terminal on your workstation:

```bash
# Launch the interactive operator TUI (supports pressing 'x' in Open Crashes view)
ntfuzz tui --api http://127.0.0.1:8080

# Or list and mark crashes fixed from the CLI
ntfuzz crash list --api http://127.0.0.1:8080
ntfuzz crash fixed 42 --api http://127.0.0.1:8080
```

Once a crash is marked fixed on the writer, the next published snapshot (default `1m`) updates the public dashboard automatically. If a future fuzz run reproduces the same input hash or normalized stack fingerprint, `ntfuzz` automatically reopens the crash.

### 5.2 Adding Repositories & Tuning Fuzz Budgets

Edit `/etc/ntfuzz/projects.yaml` on the host:

```yaml
corpus_dir: /var/lib/ntfuzz/corpus
work_dir: /var/lib/ntfuzz/repos
default_fuzztime: 15s

projects:
  - name: ntfuzz
    repo: https://github.com/NimbleTerminal/ntfuzz.git
    branch: main
    fuzztime: 15s

  - name: my-library
    repo: https://github.com/example/my-library.git
    branch: main
    fuzztime: 30s
```

Then restart the writer service:

```bash
sudo systemctl restart ntfuzz-serve.service
```

`ntfuzz serve` will clone any new repositories into `/var/lib/ntfuzz/repos/<name>`, discover all native `FuzzXxx(f *testing.F)` targets across all Go modules in the repo, and schedule them onto the worker pool.

### 5.3 Private repositories

#### The Model: `git` Holds the Credentials, Not `ntfuzz`

`ntfuzz` never stores tokens, SSH keys, or passwords in `projects.yaml` or in DuckDB—`git` and `ssh` in the service user's environment handle authentication directly. At startup, `ntfuzz` rejects any `repo` URL with embedded userinfo (`https://user:token@host/...`) so credentials never leak into YAML, Terraform state, or the public snapshot viewer.

Every `git` subprocess runs with `GIT_TERMINAL_PROMPT=0` and (when `GIT_SSH_COMMAND` is unset) `GIT_SSH_COMMAND=ssh -o BatchMode=yes`, overridable via `NTFUZZ_GIT_SSH_COMMAND`. Misconfigured or missing credentials therefore fail immediately instead of hanging on an interactive prompt, and surface as a redacted project `sync_error` in `/api/projects` and the TUI.

#### SSH Deploy Key (Preferred)

Generate an `ed25519` key as the `ntfuzz` service user under `/var/lib/ntfuzz/.ssh`, set `0700`/`0600` permissions, pre-seed `known_hosts` with GitHub's host keys so `BatchMode=yes` does not fail on first connection, and register the public key as a **read-only** deploy key on the repository:

```bash
sudo install -d -m 0700 -o ntfuzz -g ntfuzz /var/lib/ntfuzz/.ssh
sudo runuser -u ntfuzz -- env HOME=/var/lib/ntfuzz \
  ssh-keygen -t ed25519 -N "" -C "ntfuzz@$(hostname -f)" -f /var/lib/ntfuzz/.ssh/id_ed25519
sudo runuser -u ntfuzz -- sh -c 'ssh-keyscan -t ed25519,rsa github.com >> /var/lib/ntfuzz/.ssh/known_hosts'
sudo chmod 0600 /var/lib/ntfuzz/.ssh/id_ed25519 /var/lib/ntfuzz/.ssh/known_hosts
sudo chmod 0644 /var/lib/ntfuzz/.ssh/id_ed25519.pub
```

Then reference the repository in `/etc/ntfuzz/projects.yaml` using standard SSH syntax:

```yaml
projects:
  - name: private-service
    repo: git@github.com:Org/repo.git
    branch: main
    fuzztime: 20s
```

> **Multiple private repositories on GitHub**: GitHub allows a given SSH key to be attached as a deploy key to only one repository. To fuzz several private repositories:
> 1. **Machine user key**: add a single key to a dedicated read-only machine user account with access to those repositories, or
> 2. **Per-repo deploy keys with `~/.ssh/config` host aliases**: generate one key per repository (`/var/lib/ntfuzz/.ssh/id_repo_a`, `/var/lib/ntfuzz/.ssh/id_repo_b`), define a host alias per repo in `/var/lib/ntfuzz/.ssh/config` (`chmod 0600`, owned by `ntfuzz:ntfuzz`), and use `git@github-repo-a:Org/repo-a.git` in `projects.yaml`:
>    ```sshconfig
>    Host github-repo-a
>        HostName github.com
>        User git
>        IdentityFile /var/lib/ntfuzz/.ssh/id_repo_a
>        IdentitiesOnly yes
>    ```

#### HTTPS with a Credential Helper or `insteadOf`

If you prefer HTTPS over SSH, use a fine-grained read-only Personal Access Token (PAT) stored in the `ntfuzz` user's global Git configuration—either via `git credential-store` or a `url.<base>.insteadOf` rewrite rule:

```bash
# Option A: git credential-store (~/.git-credentials, mode 0600)
sudo runuser -u ntfuzz -- env HOME=/var/lib/ntfuzz \
  git config --global credential.helper 'store --file /var/lib/ntfuzz/.git-credentials'
sudo install -m 0600 -o ntfuzz -g ntfuzz /dev/null /var/lib/ntfuzz/.git-credentials
sudo sh -c 'printf "https://x-access-token:ghp_REPLACE_WITH_READONLY_PAT@github.com\n" > /var/lib/ntfuzz/.git-credentials'

# Option B: global URL rewrite in /var/lib/ntfuzz/.gitconfig
sudo runuser -u ntfuzz -- env HOME=/var/lib/ntfuzz \
  git config --global url."https://x-access-token:ghp_REPLACE_WITH_READONLY_PAT@github.com/".insteadOf "https://github.com/"
sudo chmod 0600 /var/lib/ntfuzz/.gitconfig
```

Both approaches keep `/etc/ntfuzz/projects.yaml` clean (`repo: https://github.com/Org/repo.git`). Placing a token directly inside the YAML `repo` URL is rejected at startup.

#### Private Go Module Dependencies

`go test` also runs as `User=ntfuzz`, so when a fuzz target imports private Go modules from your organization, set `GOPRIVATE` in `/etc/ntfuzz/ntfuzz.env` (loaded via `EnvironmentFile` in `ntfuzz-serve.service`):

```dotenv
GOPRIVATE="github.com/Org/*"
```

`go` automatically derives `GONOPROXY` and `GONOSUMDB` from `GOPRIVATE` unless overridden, and uses the same SSH keys or HTTPS Git credentials configured above when fetching private module dependencies into `/var/lib/ntfuzz/cache/gomod`.

#### `systemd` Specifics & Infrastructure Persistence

In production, `ntfuzz-serve.service` runs as `User=ntfuzz` with `HOME=/var/lib/ntfuzz`. All SSH keys, `known_hosts`, `.gitconfig`, and `.git-credentials` files must be owned by `ntfuzz:ntfuzz` under `/var/lib/ntfuzz` (use `sudo runuser -u ntfuzz -- env HOME=/var/lib/ntfuzz ...` rather than configuring `/root`).

Anything placed on the host by hand—or outside the persistent `/var/lib/ntfuzz` volume—should also be recorded in your provisioning script, Terraform / `cloud-init` template (`deploy/terraform/digitalocean/cloud-init.yaml.tftpl`), or configuration management so a rebuilt machine retains its configuration and `/etc/ntfuzz/ntfuzz.env` settings.

#### Verification

Before restarting `ntfuzz-serve`, verify that `git ls-remote` succeeds non-interactively as the `ntfuzz` user:

```bash
sudo runuser -u ntfuzz -- env HOME=/var/lib/ntfuzz git ls-remote git@github.com:Org/repo.git
```

Then restart the writer and verify that `/api/projects` shows a clean `last_synced` timestamp and empty `sync_error` for the private project:

```bash
sudo systemctl restart ntfuzz-serve.service
curl -s http://127.0.0.1:8080/api/projects | jq .
```

#### Security Notes

- **Public visibility of `repo`**: The public `ntfuzz view` process serves `/api/projects` to anonymous readers, so the `repo` URL string (and project name) is visible to anyone who can access the viewer.
- **Least privilege**: Always use read-only deploy keys or fine-grained read-only PATs scoped only to the repositories being fuzzed.
- **Rotation**: Rotate credentials by replacing `/var/lib/ntfuzz/.ssh/id_ed25519` or updating `/var/lib/ntfuzz/.git-credentials` / `.gitconfig`, revoking the old key or PAT upstream, and running `sudo systemctl restart ntfuzz-serve.service`.

### 5.4 Customizing Browser Dashboard Branding

Both `ntfuzz serve` and `ntfuzz view` read branding settings from `/etc/ntfuzz/ntfuzz.env` (or `--brand-*` CLI flags):

```dotenv
NTFUZZ_BRAND_NAME="NimbleTerminal"
NTFUZZ_BRAND_URL="https://nimbleterminal.com"
NTFUZZ_BRAND_ACCENT="#58A6FF"
# Optional: override the git SSH command used by ntfuzz serve (defaults to "ssh -o BatchMode=yes")
# NTFUZZ_GIT_SSH_COMMAND="ssh -o BatchMode=yes -F /var/lib/ntfuzz/.ssh/config"
```

Restart both services after editing `/etc/ntfuzz/ntfuzz.env`:

```bash
sudo systemctl restart ntfuzz-serve.service ntfuzz-view.service
```

### 5.5 Upgrading `ntfuzz` & Schema Migrations

When upgrading `ntfuzz` to a new release (especially across a DuckDB schema version bump):

1. Fetch and build the new `/usr/local/bin/ntfuzz` binary from `/opt/ntfuzz-src` (replace `origin/main` with a release tag or commit SHA if pinning):
   ```bash
   sudo git -C /opt/ntfuzz-src fetch --tags --prune origin
   sudo git -C /opt/ntfuzz-src checkout origin/main
   sudo env PATH="/usr/local/go/bin:${PATH}" HOME="/root" CGO_ENABLED=1 \
     go -C /opt/ntfuzz-src build -trimpath -o /usr/local/bin/ntfuzz ./cmd/ntfuzz
   ```
2. **Restart `ntfuzz-serve` first** so it migrates `/var/lib/ntfuzz/private/live.duckdb` and publishes a fresh snapshot using the new schema version:
   ```bash
   sudo systemctl restart ntfuzz-serve.service
   ```
3. Wait for a new snapshot to be written (or check `journalctl -u ntfuzz-serve -n 20`), then restart `ntfuzz-view`:
   ```bash
   sudo systemctl restart ntfuzz-view.service
   ```

### 5.6 Prometheus Metrics & Alerting

By default in the systemd units:
- `ntfuzz-serve` exposes `/metrics` on `http://127.0.0.1:9090/metrics`
- `ntfuzz-view` exposes `/metrics` on `http://127.0.0.1:9091/metrics`

Recommended alerts for production monitoring:
- **Stale public snapshot**: `ntfuzz_snapshot_age_seconds > 300` (indicates `ntfuzz-serve` stopped publishing snapshots or `ntfuzz-view` failed to refresh).
- **Snapshot publication errors**: `rate(ntfuzz_snapshot_publications_total{result="error"}[5m]) > 0`.
- **Viewer refresh errors**: `rate(ntfuzz_snapshot_refreshes_total{result="error"}[5m]) > 0`.
- **Terminal session saturation**: `rate(ntfuzz_terminal_sessions_rejected_total[5m]) > 0`.

### 5.7 Backups & Volume Recovery

All persistent state lives under `/var/lib/ntfuzz`:
- **Published snapshots (`/var/lib/ntfuzz/public/snapshot-*.duckdb`)** are already point-in-time, self-contained DuckDB files created via `COPY FROM DATABASE` (with the two newest snapshot files retained automatically). You can copy the latest `snapshot-*.duckdb` directly to object storage (S3 / DigitalOcean Spaces) on a cron schedule without stopping `ntfuzz-serve`!
- **Fuzzing corpora (`/var/lib/ntfuzz/corpus/`)** contain the evolved seed inputs (`<project>/cache/<package-segment>/<target>/`) and crash reproducers (`<project>/crashes/<package-segment>/<target>/<hash>`, where `<hash>` is the hex-encoded first 16 bytes of the SHA-256 digest) and can be backed up incrementally with `restic`, `rsync`, or DigitalOcean Volume Snapshots.
