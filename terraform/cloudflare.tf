# -----------------------------------------------------------------------------
# DNS records
# -----------------------------------------------------------------------------

resource "cloudflare_record" "primary" {
  zone_id = var.cf_zone_id
  name    = "vote-chain-primary"
  content = digitalocean_droplet.primary.ipv4_address
  type    = "A"
  ttl     = 1
  proxied = false
}

resource "cloudflare_record" "secondary" {
  zone_id = var.cf_zone_id
  name    = "vote-chain-secondary"
  content = digitalocean_droplet.secondary.ipv4_address
  type    = "A"
  ttl     = 1
  proxied = false
}

resource "cloudflare_record" "rpc_primary" {
  zone_id = var.cf_zone_id
  name    = "vote-rpc-primary"
  content = digitalocean_droplet.primary.ipv4_address
  type    = "A"
  ttl     = 1
  proxied = false
}

# -----------------------------------------------------------------------------
# Data sources
# -----------------------------------------------------------------------------

data "cloudflare_zone" "main" {
  zone_id = var.cf_zone_id
}
