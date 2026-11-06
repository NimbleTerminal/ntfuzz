locals {
  volume_name = "${replace(var.name, "_", "-")}-data"

  default_projects_yaml = <<-YAML
    corpus_dir: /var/lib/ntfuzz/corpus
    work_dir: /var/lib/ntfuzz/repos
    default_fuzztime: 15s

    projects:
      - name: ntfuzz
        repo: ${var.ntfuzz_repo}
        branch: ${var.ntfuzz_ref}
        fuzztime: 15s
  YAML

  effective_projects_yaml = trimspace(var.projects_yaml) != "" ? var.projects_yaml : local.default_projects_yaml
  caddy_site_address      = trimspace(var.domain) != "" ? trimspace(var.domain) : ":80"
}

resource "digitalocean_volume" "data" {
  region                  = var.region
  name                    = local.volume_name
  size                    = var.volume_size_gib
  initial_filesystem_type = "ext4"
  description             = "Persistent DuckDB, snapshots, corpora, and git worktrees for ${var.name}"
  tags                    = var.tags
}

resource "digitalocean_droplet" "ntfuzz" {
  name       = var.name
  region     = var.region
  size       = var.droplet_size
  image      = var.droplet_image
  ipv6       = true
  monitoring = true
  ssh_keys   = var.ssh_key_fingerprints
  tags       = var.tags

  user_data = templatefile("${path.module}/cloud-init.yaml.tftpl", {
    volume_name        = digitalocean_volume.data.name
    go_version         = var.go_version
    ntfuzz_repo        = var.ntfuzz_repo
    ntfuzz_ref         = var.ntfuzz_ref
    workers            = var.workers
    snapshot_interval  = var.snapshot_interval
    view_refresh       = var.view_refresh
    brand_name         = var.brand_name
    brand_url          = var.brand_url
    brand_accent       = var.brand_accent
    projects_yaml      = local.effective_projects_yaml
    caddy_site_address = local.caddy_site_address
  })
}

resource "digitalocean_volume_attachment" "data" {
  droplet_id = digitalocean_droplet.ntfuzz.id
  volume_id  = digitalocean_volume.data.id
}

resource "digitalocean_firewall" "ntfuzz" {
  name        = "${var.name}-fw"
  droplet_ids = [digitalocean_droplet.ntfuzz.id]
  tags        = var.tags

  # SSH + operator loopback tunnel (ssh -L 8080:127.0.0.1:8080)
  inbound_rule {
    protocol         = "tcp"
    port_range       = "22"
    source_addresses = var.allowed_ssh_cidrs
  }

  # HTTP (ACME HTTP-01 challenge + HTTP-to-HTTPS redirect)
  inbound_rule {
    protocol         = "tcp"
    port_range       = "80"
    source_addresses = ["0.0.0.0/0", "::/0"]
  }

  # HTTPS (WebSocket + JSON API)
  inbound_rule {
    protocol         = "tcp"
    port_range       = "443"
    source_addresses = ["0.0.0.0/0", "::/0"]
  }

  # HTTP/3 / QUIC / WebTransport
  inbound_rule {
    protocol         = "udp"
    port_range       = "443"
    source_addresses = ["0.0.0.0/0", "::/0"]
  }

  # ICMP ping / path MTU discovery
  inbound_rule {
    protocol         = "icmp"
    source_addresses = ["0.0.0.0/0", "::/0"]
  }

  # Outbound traffic for git clone/fetch, Go module proxy, and Let's Encrypt
  outbound_rule {
    protocol              = "tcp"
    port_range            = "1-65535"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }

  outbound_rule {
    protocol              = "udp"
    port_range            = "1-65535"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }

  outbound_rule {
    protocol              = "icmp"
    destination_addresses = ["0.0.0.0/0", "::/0"]
  }
}

resource "digitalocean_record" "a" {
  count  = var.manage_dns && trimspace(var.dns_zone) != "" ? 1 : 0
  domain = var.dns_zone
  type   = "A"
  name   = var.record_name
  value  = digitalocean_droplet.ntfuzz.ipv4_address
  ttl    = 300
}

resource "digitalocean_record" "aaaa" {
  count  = var.manage_dns && trimspace(var.dns_zone) != "" ? 1 : 0
  domain = var.dns_zone
  type   = "AAAA"
  name   = var.record_name
  value  = digitalocean_droplet.ntfuzz.ipv6_address
  ttl    = 300
}
