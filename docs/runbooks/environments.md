# Vote SDK GitHub Environments

`release.yml` is environment-agnostic: tag pushes build artifacts, create the
GitHub Release, and upload shared tarballs to DigitalOcean Spaces. Fleet changes
happen only through manual `sdk-chain-deploy.yml` or `sdk-chain-reset.yml`
dispatches with `target_environment` set to `staging` or `production`.

## Environment Variables

Create GitHub Environments named `staging` and `production`.

| Variable | Staging | Production | Notes |
|----------|---------|------------|-------|
| `CHAIN_ID` | `svote-1` | `zvote-1` | Passed into `scripts/init.sh`, reset joiners, and explorer config. |
| `HAS_SECONDARY` | `true` | `false` | Skips secondary funding, reset, deploy, and verification in production. |
| `DNS_PREFIX` | `stage.` | `prod.` | Prefixes public DNS names, e.g. `stage.explorer.<domain>`. |
| `DO_SPACES_BASE` | `https://vote.fra1.digitaloceanspaces.com` | `https://vote.fra1.digitaloceanspaces.com` | Shared Spaces public base URL. |
| `RELEASE_BASE_URL` | `https://vote.fra1.digitaloceanspaces.com/binaries/vote-sdk` | `https://vote.fra1.digitaloceanspaces.com/binaries/vote-sdk` | Shared release tarball prefix. |
| `GENESIS_KEY` | `genesis/svote-1/genesis.json` | `genesis/zvote-1/genesis.json` | Prevents resets from overwriting another environment's genesis. |
| `SNAPSHOTS_PREFIX` | `snapshots/svote-1` | `snapshots/zvote-1` | Prefix cleared during reset before fresh snapshots are published. |

The workflows have defaults for these values, but operators should set them
explicitly so the selected environment is visible in GitHub's UI.

## Environment Secrets

Set these in both environments unless noted:

| Secret | Required for | Notes |
|--------|--------------|-------|
| `PRIMARY_HOST` | deploy, reset | SSH host/IP for the primary validator. |
| `SECONDARY_HOST` | staging deploy/reset | Omit in production when `HAS_SECONDARY=false`. |
| `DOMAIN` | deploy, reset | Base domain. Workflows prepend `DNS_PREFIX` to service hostnames. |
| `DEPLOY_USER` | deploy, reset | SSH user for fleet hosts. |
| `SSH_PRIVATE_KEY` | deploy, reset | SSH key for fleet access. |
| `SENTRY_DSN` | deploy, reset | Written to `/etc/default/svoted`. |
| `CONFIG_PR_GITHUB_TOKEN` | deploy, reset | Optional token written to the primary's `svoted` environment. |
| `VM_PRIVKEYS` | reset | Comma-separated vote-manager private keys for genesis. |
| `PRIMARY_VAL_PRIVKEY` | reset | Deterministic primary validator key. |
| `SECONDARY_VAL_PRIVKEY` | staging reset | Omit in production when `HAS_SECONDARY=false`. |
| `DO_ACCESS_KEY` | release, reset | Spaces access key. Release still uses repository-level secrets. |
| `DO_SECRET_KEY` | release, reset | Spaces secret key. Release still uses repository-level secrets. |
| `SLACK_WEBHOOK_URL` | deploy, reset | Failure notifications. |

## Manual Operations

To deploy a published tag, run **Deploy SDK** and select the environment. To
wipe and re-bootstrap a fleet, run **Reset SDK Chain** and select the environment.
Production reset is a single-validator flow; staging reset funds and joins the
secondary validator before archive verification.
