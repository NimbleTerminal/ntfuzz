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
   - [Customizing Browser Dashboard Branding](#53-customizing-browser-dashboard-branding)
   - [Upgrading `ntfuzz` & Schema Migrations](#54-upgrading-ntfuzz--schema-migrations)
   - [Prometheus Metrics & Alerting](#55-prometheus-metrics--alerting)
   - [Backups & Volume Recovery](#56-backups--volume-recovery)

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

### 5.3 Customizing Browser Dashboard Branding

Both `ntfuzz serve` and `ntfuzz view` read branding settings from `/etc/ntfuzz/ntfuzz.env` (or `--brand-*` CLI flags):

```dotenv
NTFUZZ_BRAND_NAME="NimbleTerminal"
NTFUZZ_BRAND_URL="https://nimbleterminal.com"
NTFUZZ_BRAND_ACCENT="#58A6FF"
```

Restart both services after editing `/etc/ntfuzz/ntfuzz.env`:

```bash
sudo systemctl restart ntfuzz-serve.service ntfuzz-view.service
```

### 5.4 Upgrading `ntfuzz` & Schema Migrations

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

### 5.5 Prometheus Metrics & Alerting

By default in the systemd units:
- `ntfuzz-serve` exposes `/metrics` on `http://127.0.0.1:9090/metrics`
- `ntfuzz-view` exposes `/metrics` on `http://127.0.0.1:9091/metrics`

Recommended alerts for production monitoring:
- **Stale public snapshot**: `ntfuzz_snapshot_age_seconds > 300` (indicates `ntfuzz-serve` stopped publishing snapshots or `ntfuzz-view` failed to refresh).
- **Snapshot publication errors**: `rate(ntfuzz_snapshot_publications_total{result="error"}[5m]) > 0`.
- **Viewer refresh errors**: `rate(ntfuzz_snapshot_refreshes_total{result="error"}[5m]) > 0`.
- **Terminal session saturation**: `rate(ntfuzz_terminal_sessions_rejected_total[5m]) > 0`.

### 5.6 Backups & Volume Recovery

All persistent state lives under `/var/lib/ntfuzz`:
- **Published snapshots (`/var/lib/ntfuzz/public/snapshot-*.duckdb`)** are already point-in-time, self-contained DuckDB files created via `COPY FROM DATABASE` (with the two newest snapshot files retained automatically). You can copy the latest `snapshot-*.duckdb` directly to object storage (S3 / DigitalOcean Spaces) on a cron schedule without stopping `ntfuzz-serve`!
- **Fuzzing corpora (`/var/lib/ntfuzz/corpus/`)** contain the evolved seed inputs (`<project>/cache/<package-segment>/<target>/`) and crash reproducers (`<project>/crashes/<package-segment>/<target>/<hash>`, where `<hash>` is the hex-encoded first 16 bytes of the SHA-256 digest) and can be backed up incrementally with `restic`, `rsync`, or DigitalOcean Volume Snapshots.
