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
