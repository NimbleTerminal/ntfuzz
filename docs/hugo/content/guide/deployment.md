---
title: "Deployment"
weight: 60
---

# Deployment

> [!WARNING]
> **`ntfuzz serve` is an unauthenticated local operator console.** It binds to `127.0.0.1:8080` by default, executes `git` and `go test -fuzz` subprocesses, and exposes mutation endpoints (`POST /api/crashes/{id}/fix` and `POST /api/crashes/{id}/issue`). Never expose `ntfuzz serve` directly to the public internet. For public or shared dashboards, publish immutable snapshots with `--snapshot-dir` and expose [`ntfuzz view`](../commands/#ntfuzz-view) instead.

This page condenses the full [`docs/deployment-cookbook.md`](https://github.com/NimbleTerminal/ntfuzz/blob/main/docs/deployment-cookbook.md) in the repository, which mirrors how **[fuzz.nimbleterminal.com](https://fuzz.nimbleterminal.com/)** is deployed.

## Production architecture

In production, `ntfuzz` runs as two separate OS processes under two unprivileged Linux users sharing only a world-readable snapshot directory (`/var/lib/ntfuzz/public`):

| Component | User | Listen address | Filesystem access |
| --- | --- | --- | --- |
| **`ntfuzz-serve.service`** (Private Writer) | `ntfuzz` | `127.0.0.1:8080` (metrics `127.0.0.1:9090`) | Read/write to `/var/lib/ntfuzz/{private,corpus,repos,cache,public}` |
| **`ntfuzz-view.service`** (Public Viewer) | `ntfuzz-view` | `127.0.0.1:8081` behind Caddy `:443` (metrics `127.0.0.1:9091`) | `ReadOnlyPaths=/var/lib/ntfuzz/public`; `InaccessiblePaths` hides `private`, `corpus`, `repos`, and `cache` |

Every minute (`--snapshot-interval 1m`), `ntfuzz serve` runs DuckDB `COPY FROM DATABASE` into a fresh `snapshot-<timestamp>.duckdb` file (stripping `input_bytes` for projects where `public_inputs` is `false`), closes it, and atomically updates `current.json`. The publisher retains the two most recent snapshot generations. `ntfuzz view` polls `current.json` every 10 seconds (`--refresh 10s`) and opens the immutable file through `ntduckbox`.

---

## Option 1: DigitalOcean Terraform quickstart

The repository includes a complete Terraform module in [`deploy/terraform/digitalocean/`](https://github.com/NimbleTerminal/ntfuzz/tree/main/deploy/terraform/digitalocean) that provisions:

- A persistent DigitalOcean Block Storage Volume (`ext4`, mounted at `/var/lib/ntfuzz`).
- An Ubuntu 24.04 LTS Droplet bootstrapped via `cloud-init` with Go, `ntfuzz`, `ntfuzz-serve.service`, `ntfuzz-view.service`, and Caddy.
- A Cloud Firewall allowing only SSH (`22`) and HTTP/HTTPS/HTTP3 (`80`, `443`).
- Optional DNS `A`/`AAAA` records.

```sh
cd deploy/terraform/digitalocean
cp terraform.tfvars.example terraform.tfvars
# Edit terraform.tfvars with your domain, ssh_key_fingerprints, and projects_yaml

export DIGITALOCEAN_TOKEN="dop_v1_..."
terraform init
terraform apply
```

---

## Option 2: Manual Linux setup (`systemd` + Caddy)

Reference unit files and configuration templates live in [`deploy/systemd/`](https://github.com/NimbleTerminal/ntfuzz/tree/main/deploy/systemd).

### 1. Build `/usr/local/bin/ntfuzz`

```sh
sudo git clone https://github.com/NimbleTerminal/ntfuzz.git /opt/ntfuzz-src
sudo env PATH="/usr/local/go/bin:${PATH}" HOME="/root" CGO_ENABLED=1 \
  go -C /opt/ntfuzz-src build -trimpath -o /usr/local/bin/ntfuzz ./cmd/ntfuzz
sudo chmod 0755 /usr/local/bin/ntfuzz
```

### 2. Create users and `/var/lib/ntfuzz` directories

```sh
sudo useradd --system --user-group --home-dir /var/lib/ntfuzz --no-create-home --shell /usr/sbin/nologin ntfuzz
sudo useradd --system --user-group --home-dir /var/lib/ntfuzz/public --no-create-home --shell /usr/sbin/nologin ntfuzz-view

sudo install -d -m 0755 -o root -g root /var/lib/ntfuzz
sudo install -d -m 0750 -o ntfuzz -g ntfuzz /var/lib/ntfuzz/private
sudo install -d -m 0750 -o ntfuzz -g ntfuzz /var/lib/ntfuzz/corpus
sudo install -d -m 0750 -o ntfuzz -g ntfuzz /var/lib/ntfuzz/repos
sudo install -d -m 0750 -o ntfuzz -g ntfuzz /var/lib/ntfuzz/cache
sudo install -d -m 0755 -o ntfuzz -g ntfuzz /var/lib/ntfuzz/public
sudo install -d -m 0755 -o root -g root /etc/ntfuzz
```

### 3. Install configs and enable `systemd` services

```sh
sudo cp deploy/systemd/projects.yaml.example /etc/ntfuzz/projects.yaml
sudo cp deploy/systemd/ntfuzz.env.example /etc/ntfuzz/ntfuzz.env
sudo cp deploy/systemd/ntfuzz-serve.service /etc/systemd/system/ntfuzz-serve.service
sudo cp deploy/systemd/ntfuzz-view.service /etc/systemd/system/ntfuzz-view.service

sudo systemctl daemon-reload
sudo systemctl enable --now ntfuzz-serve.service
sudo systemctl enable --now ntfuzz-view.service
```

### 4. Reverse proxy `127.0.0.1:8081` with Caddy

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

### 5. Optional: Socket activation with `PrivateNetwork=true`

`ntfuzz view` accepts an inherited socket descriptor via `--listen-fd 3`. Pairing [`deploy/systemd/ntfuzz-view.socket`](https://github.com/NimbleTerminal/ntfuzz/blob/main/deploy/systemd/ntfuzz-view.socket) with `PrivateNetwork=true` in `ntfuzz-view.service` runs the public viewer inside a network namespace with **no** network interfaces while serving traffic over the socket passed by `systemd`.

---

## Operator triage via SSH tunnel

Because the public viewer on `:8081` is read-only, operators connect to the private writer on `127.0.0.1:8080` over an SSH port-forward to link issues or mark crashes fixed:

```sh
# Terminal 1: forward local port 8080 to the remote writer
ssh -N -L 8080:127.0.0.1:8080 root@fuzz.example.com

# Terminal 2: run the operator TUI or crash CLI against the tunnel
ntfuzz tui --api http://127.0.0.1:8080
ntfuzz crash list --api http://127.0.0.1:8080
ntfuzz crash link 23 --issue https://github.com/org/repo/issues/42 --api http://127.0.0.1:8080
ntfuzz crash fixed 23 --issue https://github.com/org/repo/issues/42 --api http://127.0.0.1:8080
```

---

## Private repositories

`ntfuzz-serve.service` runs as `User=ntfuzz` with `HOME=/var/lib/ntfuzz`. Configure Git or SSH credentials under `/var/lib/ntfuzz` owned by `ntfuzz:ntfuzz`:

### SSH deploy key (preferred)

```sh
sudo install -d -m 0700 -o ntfuzz -g ntfuzz /var/lib/ntfuzz/.ssh
sudo runuser -u ntfuzz -- env HOME=/var/lib/ntfuzz \
  ssh-keygen -t ed25519 -N "" -C "ntfuzz@$(hostname -f)" -f /var/lib/ntfuzz/.ssh/id_ed25519
sudo runuser -u ntfuzz -- sh -c 'ssh-keyscan -t ed25519,rsa github.com >> /var/lib/ntfuzz/.ssh/known_hosts'
sudo chmod 0600 /var/lib/ntfuzz/.ssh/id_ed25519 /var/lib/ntfuzz/.ssh/known_hosts
sudo chmod 0644 /var/lib/ntfuzz/.ssh/id_ed25519.pub
```

Add `id_ed25519.pub` as a read-only deploy key on the repository (or use a read-only machine user / `~/.ssh/config` host aliases for multiple GitHub repositories) and reference `repo: git@github.com:Org/repo.git` in `/etc/ntfuzz/projects.yaml`.

### HTTPS with `git credential-store` or `insteadOf`

```sh
sudo runuser -u ntfuzz -- env HOME=/var/lib/ntfuzz \
  git config --global credential.helper 'store --file /var/lib/ntfuzz/.git-credentials'
sudo install -m 0600 -o ntfuzz -g ntfuzz /dev/null /var/lib/ntfuzz/.git-credentials
sudo sh -c 'printf "https://x-access-token:ghp_REPLACE_WITH_READONLY_PAT@github.com\n" > /var/lib/ntfuzz/.git-credentials'
```

If fuzzed packages import private Go module dependencies, add `GOPRIVATE="github.com/Org/*"` to `/etc/ntfuzz/ntfuzz.env`. Verify non-interactive access before restarting `ntfuzz-serve`:

```sh
sudo runuser -u ntfuzz -- env HOME=/var/lib/ntfuzz git ls-remote git@github.com:Org/repo.git
sudo systemctl restart ntfuzz-serve.service
```

---

## Upgrades, migrations, and backups

- **Upgrading across schema versions**: Rebuild `/usr/local/bin/ntfuzz`, restart **`ntfuzz-serve.service` first** so it migrates `/var/lib/ntfuzz/private/live.duckdb` and publishes a new snapshot generation with the updated schema version, and then restart **`ntfuzz-view.service`**.
- **Backups**: Published snapshot files (`/var/lib/ntfuzz/public/snapshot-*.duckdb`) are self-contained point-in-time DuckDB files created via `COPY FROM DATABASE` and can be copied directly to object storage on a schedule without stopping `ntfuzz-serve`. Back up `/var/lib/ntfuzz/corpus/` incrementally with `restic`, `rsync`, or volume snapshots.
