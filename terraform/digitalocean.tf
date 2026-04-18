# -----------------------------------------------------------------------------
# VPC
# -----------------------------------------------------------------------------

resource "digitalocean_vpc" "main" {
  name     = "vote-sdk-vpc"
  region   = var.region
  ip_range = var.vpc_cidr
}

# -----------------------------------------------------------------------------
# Block volumes
# -----------------------------------------------------------------------------

resource "digitalocean_volume" "primary_data" {
  region                  = var.region
  name                    = "chain-data-primary"
  size                    = var.primary_volume_size
  initial_filesystem_type = "ext4"
  description             = "Persistent chain state for vote-primary validator"
}

resource "digitalocean_volume" "secondary_data" {
  region                  = var.region
  name                    = "chain-data-secondary"
  size                    = var.secondary_volume_size
  initial_filesystem_type = "ext4"
  description             = "Persistent chain state for vote-secondary validator"
}

# -----------------------------------------------------------------------------
# Droplet 1: vote-primary (bootstrap validator)
# -----------------------------------------------------------------------------

resource "digitalocean_droplet" "primary" {
  name     = "vote-primary"
  region   = var.region
  size     = var.primary_size
  image    = "ubuntu-24-04-x64"
  vpc_uuid = digitalocean_vpc.main.id

  ssh_keys = var.ssh_key_fingerprints
  user_data = templatefile("${path.module}/cloud-init/primary.yaml", {
    release_tag    = var.release_tag
    domain         = var.domain
    chain_id       = var.chain_id
    volume_name    = "chain-data-primary"
    install_script = file("${path.module}/../scripts/install-release.sh")
    systemd_unit   = file("${path.module}/../deploy/systemd/svoted-chain.service")
    caddyfile      = file("${path.module}/../deploy/caddy/primary.Caddyfile")
  })

  volume_ids = [
    digitalocean_volume.primary_data.id,
  ]

  tags = ["vote-sdk", "primary"]

  lifecycle {
    ignore_changes = [user_data]
  }
}

# -----------------------------------------------------------------------------
# Droplet 2: vote-secondary (joining validator)
# -----------------------------------------------------------------------------

resource "digitalocean_droplet" "secondary" {
  name     = "vote-secondary"
  region   = var.region
  size     = var.secondary_size
  image    = "ubuntu-24-04-x64"
  vpc_uuid = digitalocean_vpc.main.id

  ssh_keys = var.ssh_key_fingerprints
  user_data = templatefile("${path.module}/cloud-init/secondary.yaml", {
    release_tag    = var.release_tag
    domain         = var.domain
    chain_id       = var.chain_id
    volume_name    = "chain-data-secondary"
    install_script = file("${path.module}/../scripts/install-release.sh")
    systemd_unit   = file("${path.module}/../deploy/systemd/svoted-chain.service")
    caddyfile      = file("${path.module}/../deploy/caddy/secondary.Caddyfile")
  })

  volume_ids = [
    digitalocean_volume.secondary_data.id,
  ]

  tags = ["vote-sdk", "secondary"]

  lifecycle {
    ignore_changes = [user_data]
  }
}

# -----------------------------------------------------------------------------
# Project (billing group)
# -----------------------------------------------------------------------------

resource "digitalocean_project" "vote_sdk" {
  name        = "vote-sdk"
  description = "Shielded Vote Chain infrastructure"
  purpose     = "Service or API"
  environment = "Production"

  resources = [
    digitalocean_droplet.primary.urn,
    digitalocean_droplet.secondary.urn,
    digitalocean_volume.primary_data.urn,
    digitalocean_volume.secondary_data.urn,
  ]
}

# -----------------------------------------------------------------------------
# Firewalls
# -----------------------------------------------------------------------------

resource "digitalocean_firewall" "primary" {
  name        = "vote-primary-fw"
  droplet_ids = [digitalocean_droplet.primary.id]

  dynamic "inbound_rule" {
    for_each = var.admin_ips
    content {
      protocol         = "tcp"
      port_range       = "22"
      source_addresses = [inbound_rule.value]
    }
  }

  # CometBFT P2P
  inbound_rule {
    protocol         = "tcp"
    port_range       = "26656"
    source_addresses = ["0.0.0.0/0", "::/0"]
  }

  # HTTPS (Caddy)
  inbound_rule {
    protocol         = "tcp"
    port_range       = "443"
    source_addresses = ["0.0.0.0/0", "::/0"]
  }

  # HTTP (Caddy ACME challenge)
  inbound_rule {
    protocol         = "tcp"
    port_range       = "80"
    source_addresses = ["0.0.0.0/0", "::/0"]
  }

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

resource "digitalocean_firewall" "secondary" {
  name        = "vote-secondary-fw"
  droplet_ids = [digitalocean_droplet.secondary.id]

  dynamic "inbound_rule" {
    for_each = var.admin_ips
    content {
      protocol         = "tcp"
      port_range       = "22"
      source_addresses = [inbound_rule.value]
    }
  }

  inbound_rule {
    protocol         = "tcp"
    port_range       = "26656"
    source_addresses = ["0.0.0.0/0", "::/0"]
  }

  inbound_rule {
    protocol         = "tcp"
    port_range       = "443"
    source_addresses = ["0.0.0.0/0", "::/0"]
  }

  inbound_rule {
    protocol         = "tcp"
    port_range       = "80"
    source_addresses = ["0.0.0.0/0", "::/0"]
  }

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
