variable "do_token" {
  description = "DigitalOcean API token"
  type        = string
  sensitive   = true
}

variable "cf_api_token" {
  description = "Cloudflare API token with DNS permissions"
  type        = string
  sensitive   = true
}

variable "cf_zone_id" {
  description = "Cloudflare zone ID for the domain"
  type        = string
}

variable "domain" {
  description = "Base domain (e.g. example.com). Used for vote-chain-primary.<domain>, vote-chain-secondary.<domain>."
  type        = string
}

variable "ssh_key_fingerprints" {
  description = "List of DigitalOcean SSH key fingerprints for admin access"
  type        = list(string)
}

variable "admin_ips" {
  description = "CIDR blocks allowed SSH access (e.g. [\"203.0.113.5/32\"]). No default -- must be explicitly set."
  type        = list(string)
}

variable "release_tag" {
  description = "Shielded-vote release tag to deploy (e.g. v1.2.3). Must match a tag published to DO Spaces by release.yml."
  type        = string
  default     = "latest"
}

variable "region" {
  description = "DigitalOcean region for all Droplets"
  type        = string
  default     = "fra1"
}

variable "primary_size" {
  description = "Droplet size slug for the primary (bootstrap) validator"
  type        = string
  default     = "s-4vcpu-16gb-amd"
}

variable "secondary_size" {
  description = "Droplet size slug for the secondary (joining) validator"
  type        = string
  default     = "s-2vcpu-8gb-amd"
}

variable "vpc_cidr" {
  description = "Private CIDR for the VPC connecting both Droplets"
  type        = string
  default     = "10.20.0.0/24"
}

variable "primary_volume_size" {
  description = "Size in GB for the primary chain-data block volume"
  type        = number
  default     = 100
}

variable "secondary_volume_size" {
  description = "Size in GB for the secondary chain-data block volume"
  type        = number
  default     = 50
}

variable "chain_id" {
  description = "Cosmos chain ID"
  type        = string
  default     = "svote-1"
}

# -----------------------------------------------------------------------------
# PIR hosts (vote-nullifier-pir)
# -----------------------------------------------------------------------------

variable "pir_primary_size" {
  description = "Droplet size slug for the PIR primary host (needs AVX-512 — Premium Intel)"
  type        = string
  default     = "g-8vcpu-32gb-intel"
}

variable "pir_backup_size" {
  description = "Droplet size slug for the PIR backup host (needs AVX-512 — Premium Intel)"
  type        = string
  default     = "m-4vcpu-32gb-intel"
}

variable "pir_volume_size" {
  description = "Size in GB for each PIR data block volume"
  type        = number
  default     = 100
}

variable "pir_release_tag" {
  description = "vote-nullifier-pir GitHub release tag for the nf-server binary"
  type        = string
  default     = "latest"
}

variable "pir_snapshot_url" {
  description = "Base URL of the DO Spaces bucket hosting nullifier snapshots"
  type        = string
  default     = "https://vote.fra1.digitaloceanspaces.com"
}

variable "pir_resync_on_calendar" {
  description = "systemd OnCalendar= spec for periodic snapshot re-pull on PIR hosts"
  type        = string
  default     = "*-*-* 03:00:00"
}
