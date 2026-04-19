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

resource "cloudflare_record" "svote_ui" {
  zone_id = var.cf_zone_id
  name    = "svote"
  content = digitalocean_droplet.primary.ipv4_address
  type    = "A"
  ttl     = 1
  proxied = false
}

# -----------------------------------------------------------------------------
# PIR DNS records (per-host, unproxied — Caddy needs ACME HTTP-01)
# -----------------------------------------------------------------------------

resource "cloudflare_record" "pir_primary" {
  zone_id = var.cf_zone_id
  name    = "pir-primary"
  content = digitalocean_droplet.pir_primary.ipv4_address
  type    = "A"
  ttl     = 1
  proxied = false
}

resource "cloudflare_record" "pir_backup" {
  zone_id = var.cf_zone_id
  name    = "pir-backup"
  content = digitalocean_droplet.pir_backup.ipv4_address
  type    = "A"
  ttl     = 1
  proxied = false
}

# -----------------------------------------------------------------------------
# PIR Load Balancer (pir.<domain>)
# -----------------------------------------------------------------------------

resource "cloudflare_load_balancer_monitor" "pir_health" {
  account_id     = data.cloudflare_zone.main.account_id
  type           = "https"
  method         = "GET"
  path           = "/health"
  expected_codes = "2xx"
  interval       = 10
  timeout        = 5
  retries        = 2
  description    = "PIR nf-server health check"
  allow_insecure = false
}

resource "cloudflare_load_balancer_pool" "pir_primary" {
  account_id = data.cloudflare_zone.main.account_id
  name       = "pir-primary-pool"
  monitor    = cloudflare_load_balancer_monitor.pir_health.id

  origins {
    name    = "pir-primary"
    address = "pir-primary.${var.domain}"
    enabled = true
  }
}

resource "cloudflare_load_balancer_pool" "pir_backup" {
  account_id = data.cloudflare_zone.main.account_id
  name       = "pir-backup-pool"
  monitor    = cloudflare_load_balancer_monitor.pir_health.id

  origins {
    name    = "pir-backup"
    address = "pir-backup.${var.domain}"
    enabled = true
  }
}

resource "cloudflare_load_balancer" "pir" {
  zone_id          = var.cf_zone_id
  name             = "pir.${var.domain}"
  default_pool_ids = [cloudflare_load_balancer_pool.pir_primary.id]
  fallback_pool_id = cloudflare_load_balancer_pool.pir_backup.id
  proxied          = true
  description      = "PIR failover: primary with backup fallback"
}

# -----------------------------------------------------------------------------
# Data sources
# -----------------------------------------------------------------------------

data "cloudflare_zone" "main" {
  zone_id = var.cf_zone_id
}
