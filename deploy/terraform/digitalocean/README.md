# DigitalOcean Terraform Module for `ntfuzz`

This Terraform configuration provisions a production-ready `ntfuzz` instance on DigitalOcean using the isolated **private writer (`ntfuzz serve`) + public snapshot viewer (`ntfuzz view`)** architecture running behind **Caddy** automatic HTTPS.

## What Gets Provisioned

1. **DigitalOcean Block Storage Volume (`digitalocean_volume.data`)**:
   - Persistent `ext4` volume mounted at `/var/lib/ntfuzz` (`defaults,nofail,discard`).
   - Preserves the live DuckDB database (`/var/lib/ntfuzz/private/live.duckdb`), persistent fuzzing corpora (`/var/lib/ntfuzz/corpus`), Git checkouts (`/var/lib/ntfuzz/repos`), Go build/module caches (`/var/lib/ntfuzz/cache`), and published read-only snapshots (`/var/lib/ntfuzz/public`).
2. **DigitalOcean Droplet (`digitalocean_droplet.ntfuzz`)**:
   - Ubuntu 24.04 LTS (`s-4vcpu-8gb` by default).
   - Bootstrapped via [`cloud-init.yaml.tftpl`](cloud-init.yaml.tftpl) with two isolated Linux system users:
     - `ntfuzz` runs `ntfuzz-serve.service` on `127.0.0.1:8080` (private writer + orchestrator) and publishes immutable DuckDB snapshots to `/var/lib/ntfuzz/public`.
     - `ntfuzz-view` runs `ntfuzz-view.service` on `127.0.0.1:8081` (read-only snapshot viewer) with systemd filesystem sandboxing (`InaccessiblePaths` hiding `/var/lib/ntfuzz/private`, `/var/lib/ntfuzz/corpus`, and `/var/lib/ntfuzz/repos`).
   - **Caddy** reverse-proxies public HTTP/HTTPS/WebSocket traffic on `:80`/`:443` exclusively to `127.0.0.1:8081` (`ntfuzz-view`).
3. **DigitalOcean Cloud Firewall (`digitalocean_firewall.ntfuzz`)**:
   - Allows inbound TCP `22` (SSH + operator loopback tunnel) from `var.allowed_ssh_cidrs`.
   - Allows inbound TCP `80`, TCP `443`, and UDP `443` from `0.0.0.0/0` and `::/0`.
   - Blocks direct external access to `8080`, `8081`, `9090`, and `9091`.
4. **Optional DigitalOcean DNS Records (`digitalocean_record.a` / `digitalocean_record.aaaa`)**:
   - Created when `manage_dns = true` and `dns_zone` is set.

## Quickstart

### 1. Configure Variables

```bash
cd deploy/terraform/digitalocean
cp terraform.tfvars.example terraform.tfvars
$EDITOR terraform.tfvars
```

Export your DigitalOcean API token:

```bash
export DIGITALOCEAN_TOKEN="dop_v1_..."
```

### 2. Deploy

```bash
terraform init
terraform plan
terraform apply
```

### 3. Monitor First-Boot Provisioning

After the Droplet boots, `cloud-init` installs the Go toolchain, compiles `ntfuzz`, mounts `/var/lib/ntfuzz`, and starts `ntfuzz-serve`, `ntfuzz-view`, and `caddy` (typically 2–3 minutes):

```bash
ssh root@$(terraform output -raw ipv4_address) 'cloud-init status --wait && systemctl status ntfuzz-serve ntfuzz-view caddy'
```

### 4. Operator Workflow (SSH Tunnel)

Because `ntfuzz serve` binds exclusively to `127.0.0.1:8080`, use the generated SSH tunnel command to connect your local `ntfuzz tui` or `ntfuzz crash` CLI to the private writer:

```bash
# Terminal 1: open loopback tunnel
$(terraform output -raw ssh_tunnel_command)

# Terminal 2: launch local operator TUI or manage crashes
ntfuzz tui --api http://127.0.0.1:8080
ntfuzz crash list --api http://127.0.0.1:8080
ntfuzz crash fixed 1 --api http://127.0.0.1:8080
```

## Using as a Reusable Module

You can also reference `deploy/terraform/digitalocean` as a Terraform module from your own infrastructure repository:

```hcl
module "ntfuzz" {
  source = "git::https://github.com/NimbleTerminal/ntfuzz.git//deploy/terraform/digitalocean?ref=main"

  name                 = "acme-ntfuzz"
  region               = "nyc3"
  droplet_size         = "s-4vcpu-8gb"
  volume_size_gib      = 100
  ssh_key_fingerprints = ["aa:bb:cc:dd:..."]

  domain       = "fuzz.acme.example"
  manage_dns   = true
  dns_zone     = "acme.example"
  record_name  = "fuzz"

  brand_name   = "Acme Security"
  brand_url    = "https://acme.example"
  brand_accent = "#58A6FF"

  projects_yaml = file("${path.module}/projects.yaml")
}
```

See the full [Self-Hosting & Deployment Cookbook](../../../docs/deployment-cookbook.md) for manual bare-metal recipes, schema upgrade runbooks, and Prometheus scraping setup.
