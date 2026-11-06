output "droplet_id" {
  description = "ID of the provisioned DigitalOcean Droplet."
  value       = digitalocean_droplet.ntfuzz.id
}

output "ipv4_address" {
  description = "Public IPv4 address of the ntfuzz Droplet."
  value       = digitalocean_droplet.ntfuzz.ipv4_address
}

output "ipv6_address" {
  description = "Public IPv6 address of the ntfuzz Droplet."
  value       = digitalocean_droplet.ntfuzz.ipv6_address
}

output "volume_id" {
  description = "ID of the persistent DigitalOcean Block Storage Volume mounted at /var/lib/ntfuzz."
  value       = digitalocean_volume.data.id
}

output "public_url" {
  description = "Public URL of the read-only ntfuzz web terminal and JSON API."
  value       = trimspace(var.domain) != "" ? "https://${trimspace(var.domain)}" : "http://${digitalocean_droplet.ntfuzz.ipv4_address}"
}

output "ssh_tunnel_command" {
  description = "SSH command to forward the private writer API (127.0.0.1:8080) and Prometheus metrics (9090/9091) to your local workstation."
  value       = "ssh -N -L 8080:127.0.0.1:8080 -L 9090:127.0.0.1:9090 -L 9091:127.0.0.1:9091 root@${digitalocean_droplet.ntfuzz.ipv4_address}"
}

output "operator_tui_command" {
  description = "Command to run the local operator TUI against the SSH-tunneled writer service."
  value       = "ntfuzz tui --api http://127.0.0.1:8080"
}
