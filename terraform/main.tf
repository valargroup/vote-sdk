terraform {
  required_version = ">= 1.5"

  required_providers {
    digitalocean = {
      source  = "digitalocean/digitalocean"
      version = "~> 2.36"
    }
    cloudflare = {
      source  = "cloudflare/cloudflare"
      version = "~> 4.40"
    }
  }

  # Store state in Terraform Cloud or DO Spaces; configure as needed.
  # backend "s3" {
  #   endpoint                    = "https://fra1.digitaloceanspaces.com"
  #   bucket                      = "vote-sdk-tfstate"
  #   key                         = "prod/terraform.tfstate"
  #   region                      = "us-east-1"  # required by S3 backend, ignored by DO
  #   skip_credentials_validation = true
  #   skip_metadata_api_check     = true
  #   skip_requesting_account_id  = true
  #   skip_s3_checksum            = true
  # }
}

provider "digitalocean" {
  token = var.do_token
}

provider "cloudflare" {
  api_token = var.cf_api_token
}
