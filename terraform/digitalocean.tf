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

resource "digitalocean_volume" "pir_primary_data" {
  region                  = var.region
  name                    = "chain-data-pir-primary"
  size                    = var.pir_volume_size
  initial_filesystem_type = "ext4"
  description             = "PIR data for vote-nullifier-pir-primary"
}

resource "digitalocean_volume" "pir_backup_data" {
  region                  = var.region
  name                    = "chain-data-pir-backup"
  size                    = var.pir_volume_size
  initial_filesystem_type = "ext4"
  description             = "PIR data for vote-nullifier-pir-backup"
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
# Droplet 3: vote-nullifier-pir-primary
# -----------------------------------------------------------------------------

resource "digitalocean_droplet" "pir_primary" {
  name     = "vote-nullifier-pir-primary"
  region   = var.region
  size     = var.pir_primary_size
  image    = "ubuntu-24-04-x64"
  vpc_uuid = digitalocean_vpc.main.id

  ssh_keys = var.ssh_key_fingerprints
  user_data = templatefile("${path.module}/cloud-init/pir.yaml", {
    role               = "primary"
    volume_name        = "chain-data-pir-primary"
    release_tag        = var.pir_release_tag
    snapshot_url       = var.pir_snapshot_url
    resync_on_calendar = var.pir_resync_on_calendar
    hostname           = "pir-primary.${var.domain}"
    caddyfile          = file("${path.module}/../deploy/caddy/pir.Caddyfile")
    systemd_unit       = file("${path.module}/../deploy/systemd/nullifier-query-server.service")
  })

  volume_ids = [
    digitalocean_volume.pir_primary_data.id,
  ]

  tags = ["vote-sdk", "pir", "primary"]

  lifecycle {
    ignore_changes = [user_data]
  }
}

# -----------------------------------------------------------------------------
# Droplet 4: vote-nullifier-pir-backup
# -----------------------------------------------------------------------------

resource "digitalocean_droplet" "pir_backup" {
  name     = "vote-nullifier-pir-backup"
  region   = var.region
  size     = var.pir_backup_size
  image    = "ubuntu-24-04-x64"
  vpc_uuid = digitalocean_vpc.main.id

  ssh_keys = var.ssh_key_fingerprints
  user_data = templatefile("${path.module}/cloud-init/pir.yaml", {
    role               = "backup"
    volume_name        = "chain-data-pir-backup"
    release_tag        = var.pir_release_tag
    snapshot_url       = var.pir_snapshot_url
    resync_on_calendar = var.pir_resync_on_calendar
    hostname           = "pir-backup.${var.domain}"
    caddyfile          = file("${path.module}/../deploy/caddy/pir.Caddyfile")
    systemd_unit       = file("${path.module}/../deploy/systemd/nullifier-query-server.service")
  })

  volume_ids = [
    digitalocean_volume.pir_backup_data.id,
  ]

  tags = ["vote-sdk", "pir", "backup"]

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
    digitalocean_droplet.pir_primary.urn,
    digitalocean_droplet.pir_backup.urn,
    digitalocean_volume.primary_data.urn,
    digitalocean_volume.secondary_data.urn,
    digitalocean_volume.pir_primary_data.urn,
    digitalocean_volume.pir_backup_data.urn,
  ]
}

# -----------------------------------------------------------------------------
# Firewalls
# -----------------------------------------------------------------------------

resource "digitalocean_firewall" "primary" {
  name        = "vote-primary-fw"
  droplet_ids = [digitalocean_droplet.primary.id]

  # SSH - open to all (key-based auth provides security; GitHub Actions
  # runners use unpredictable IPs so IP whitelisting is not feasible)
  inbound_rule {
    protocol         = "tcp"
    port_range       = "22"
    source_addresses = ["0.0.0.0/0", "::/0"]
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

  inbound_rule {
    protocol         = "tcp"
    port_range       = "22"
    source_addresses = ["0.0.0.0/0", "::/0"]
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

resource "digitalocean_firewall" "pir" {
  name = "vote-pir-fw"
  droplet_ids = [
    digitalocean_droplet.pir_primary.id,
    digitalocean_droplet.pir_backup.id,
  ]

  inbound_rule {
    protocol         = "tcp"
    port_range       = "22"
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
