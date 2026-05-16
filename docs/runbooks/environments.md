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
| `SENTRY_AUTH_TOKEN` | deploy, reset | Optional Sentry token for `sentry-cli` deploy markers. If omitted, deploys skip marker creation. |
| `CONFIG_PR_GITHUB_TOKEN` | deploy, reset | Optional token written to the primary's `svoted` environment. |
| `VM_PRIVKEYS` | staging reset | Comma-separated vote-manager private keys for genesis. Production `zvote-1` resets use the default vote-manager address encoded in `x/vote/module.go` unless `SVOTE_USE_DEFAULT_GENESIS_VOTE_MANAGERS=false` is explicitly set. |
| `SVOTE_EXPECTED_PRODUCTION_VOTE_MANAGER` | production reset | Optional safety override for the expected production default vote manager. Defaults to `sv1wyf8tuys2ussdqwc6ugnvq0x273j8wq8fm3jrj`; `scripts/init.sh` refuses a production reset if the release binary's Go default does not match. |
| `PRIMARY_VAL_PRIVKEY` | reset | Deterministic primary validator key. |
| `SECONDARY_VAL_PRIVKEY` | staging reset | Omit in production when `HAS_SECONDARY=false`. |
| `DO_ACCESS_KEY` | release, reset | Spaces access key. Release still uses repository-level secrets. |
| `DO_SECRET_KEY` | release, reset | Spaces secret key. Release still uses repository-level secrets. |
| `SLACK_WEBHOOK_URL` | deploy, reset | Failure notifications. |

## Sentry setup

Use `sentry-cli` with an explicit org because local developer machines may not
have a default org configured:

```bash
sentry-cli info
sentry-cli projects list --org valar-group
```

The chain fleet uses the `svote-helper-vm` project. Keep `SENTRY_DSN` scoped to
the GitHub Environment. The workflows derive the host-side
`SENTRY_ENVIRONMENT` from `target_environment` and write it to
`/etc/default/svoted`. If you rotate DSNs, update the selected GitHub
Environment secret, then run **Deploy SDK** for that environment so
`/etc/default/svoted` is rewritten on every host.

After a deploy, record or verify the Sentry deploy marker with the same tag and
environment:

```bash
sentry-cli deploys new --org valar-group --project svote-helper-vm --release "$TAG" -e staging
sentry-cli deploys new --org valar-group --project svote-helper-vm --release "$TAG" -e production
```

Verification order:

1. Deploy or reset staging first, then search Sentry for
   `project:svote-helper-vm environment:staging`.
2. Deploy production only after staging events are tagged correctly, then search
   for `project:svote-helper-vm environment:production`.
3. Do not use a production reset as a Sentry test; production resets are
   destructive and require a separate operator decision.

## Manual Operations

To deploy a published tag, run **Deploy SDK** and select the environment. To
wipe and re-bootstrap a fleet, run **Reset SDK Chain** and select the environment.
Production reset is a single-validator flow; staging reset funds and joins the
secondary validator before archive verification.
