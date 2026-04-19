output "primary_droplet_ip" {
  description = "Public IPv4 address of the primary Droplet"
  value       = digitalocean_droplet.primary.ipv4_address
}

output "primary_droplet_ip_private" {
  description = "Private VPC IPv4 address of the primary Droplet"
  value       = digitalocean_droplet.primary.ipv4_address_private
}

output "secondary_droplet_ip" {
  description = "Public IPv4 address of the secondary Droplet"
  value       = digitalocean_droplet.secondary.ipv4_address
}

output "secondary_droplet_ip_private" {
  description = "Private VPC IPv4 address of the secondary Droplet"
  value       = digitalocean_droplet.secondary.ipv4_address_private
}

output "primary_url" {
  description = "Public HTTPS URL for the primary chain REST API"
  value       = "https://vote-chain-primary.${var.domain}"
}

output "secondary_url" {
  description = "Public HTTPS URL for the secondary chain REST API"
  value       = "https://vote-chain-secondary.${var.domain}"
}

# PIR hosts

output "pir_primary_ip" {
  description = "Public IPv4 address of the PIR primary Droplet"
  value       = digitalocean_droplet.pir_primary.ipv4_address
}

output "pir_backup_ip" {
  description = "Public IPv4 address of the PIR backup Droplet"
  value       = digitalocean_droplet.pir_backup.ipv4_address
}

output "pir_primary_url" {
  description = "Direct HTTPS URL for the PIR primary host"
  value       = "https://pir-primary.${var.domain}"
}

output "pir_backup_url" {
  description = "Direct HTTPS URL for the PIR backup host"
  value       = "https://pir-backup.${var.domain}"
}

output "pir_url" {
  description = "Cloudflare Load Balancer URL for PIR (use this in clients)"
  value       = "https://pir.${var.domain}"
}
