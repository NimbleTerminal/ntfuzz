variable "name" {
  description = "Name prefix for the DigitalOcean Droplet, Block Storage Volume, and Cloud Firewall."
  type        = string
  default     = "ntfuzz"
}

variable "region" {
  description = "DigitalOcean region slug (e.g. nyc3, sfo3, ams3, fra1)."
  type        = string
  default     = "nyc3"
}

variable "droplet_size" {
  description = "DigitalOcean Droplet size slug. Minimum recommended for Go compilation + DuckDB fuzzing is s-2vcpu-4gb (s-4vcpu-8gb recommended for multi-worker fleets)."
  type        = string
  default     = "s-4vcpu-8gb"
}

variable "droplet_image" {
  description = "DigitalOcean Droplet OS image slug."
  type        = string
  default     = "ubuntu-24-04-x64"
}

variable "volume_size_gib" {
  description = "Size in GiB of the persistent DigitalOcean Block Storage Volume mounted at /var/lib/ntfuzz (holds live DuckDB, published snapshots, corpora, and git checkouts)."
  type        = number
  default     = 50
}

variable "ssh_key_fingerprints" {
  description = "List of SSH key fingerprints or IDs registered in your DigitalOcean account to authorize on the Droplet."
  type        = list(string)
}

variable "allowed_ssh_cidrs" {
  description = "List of IPv4/IPv6 CIDR blocks allowed to connect to TCP port 22 (SSH and operator tunnel)."
  type        = list(string)
  default     = ["0.0.0.0/0", "::/0"]
}

variable "domain" {
  description = "Public FQDN for the dashboard (e.g. fuzz.example.com). When set, Caddy provisions automatic HTTPS certificates. When empty, Caddy serves HTTP on :80."
  type        = string
  default     = ""
}

variable "manage_dns" {
  description = "When true and var.dns_zone is set, create DigitalOcean DNS A and AAAA records pointing var.record_name in var.dns_zone to the Droplet."
  type        = bool
  default     = false
}

variable "dns_zone" {
  description = "DigitalOcean DNS domain zone name (e.g. example.com) used when manage_dns = true."
  type        = string
  default     = ""
}

variable "record_name" {
  description = "DNS record name within var.dns_zone (e.g. fuzz for fuzz.example.com, or @ for apex) used when manage_dns = true."
  type        = string
  default     = "fuzz"
}

variable "workers" {
  description = "Number of concurrent fuzzing workers passed to ntfuzz serve --workers."
  type        = number
  default     = 2
}

variable "snapshot_interval" {
  description = "Interval between immutable DuckDB snapshot publications from ntfuzz serve to /var/lib/ntfuzz/public."
  type        = string
  default     = "1m"
}

variable "view_refresh" {
  description = "How often ntfuzz view polls /var/lib/ntfuzz/public for a newer snapshot."
  type        = string
  default     = "10s"
}

variable "brand_name" {
  description = "Optional operator/fleet name displayed in the browser dashboard header (NTFUZZ_BRAND_NAME)."
  type        = string
  default     = ""
}

variable "brand_url" {
  description = "Optional http/https URL linked from the operator badge in the browser header (NTFUZZ_BRAND_URL)."
  type        = string
  default     = ""
}

variable "brand_accent" {
  description = "Optional #RRGGBB CSS accent color override for the browser header (NTFUZZ_BRAND_ACCENT)."
  type        = string
  default     = ""
}

variable "go_version" {
  description = "Go toolchain version to install from go.dev/dl on first boot."
  type        = string
  default     = "1.27.3"
}

variable "ntfuzz_repo" {
  description = "Git repository URL used to build and install ntfuzz."
  type        = string
  default     = "https://github.com/NimbleTerminal/ntfuzz.git"
}

variable "ntfuzz_ref" {
  description = "Git branch, tag, or commit SHA of ntfuzz to build on first boot."
  type        = string
  default     = "main"
}

variable "projects_yaml" {
  description = "Full contents of /etc/ntfuzz/projects.yaml. When empty, a default configuration fuzzing ntfuzz itself is installed."
  type        = string
  default     = ""
}

variable "tags" {
  description = "Tags applied to the DigitalOcean Droplet, Volume, and Firewall."
  type        = list(string)
  default     = ["ntfuzz"]
}
