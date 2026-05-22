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
| `DO_SPACES_BUCKET` | `shielded-vote` | `shielded-vote` | Spaces bucket name for reset uploads and release artifacts. Defaults to `shielded-vote`. |
| `DO_SPACES_REGION` | `nyc3` | `nyc3` | Spaces region used for release and reset uploads. Defaults to `nyc3`. |
| `DO_SPACES_BASE` | `https://shielded-vote.nyc3.digitaloceanspaces.com` | `https://shielded-vote.nyc3.digitaloceanspaces.com` | Spaces public base URL. Defaults to `https://${DO_SPACES_BUCKET}.${DO_SPACES_REGION}.digitaloceanspaces.com`. |
| `RELEASE_BASE_URL` | `https://shielded-vote.nyc3.digitaloceanspaces.com/binaries/vote-sdk` | `https://shielded-vote.nyc3.digitaloceanspaces.com/binaries/vote-sdk` | Release tarball prefix. Defaults to `${DO_SPACES_BASE}/binaries/vote-sdk`. |
| `GENESIS_KEY` | `genesis/svote-1/genesis.json` | `genesis/zvote-1/genesis.json` | Prevents resets from overwriting another environment's genesis. |
| `SNAPSHOTS_PREFIX` | `snapshots/svote-1` | `snapshots/zvote-1` | Prefix cleared during reset before fresh snapshots are published. |
| `PRIMARY_REST_URL` | derived from `DNS_PREFIX` + `DOMAIN` | optional cutover URL | REST base URL used by reset joiners to discover the primary peer. |
| `SECONDARY_REST_URL` | derived from `DNS_PREFIX` + `DOMAIN` | usually unused | REST base URL used by post-reset verification. |
| `EXPLORER_API_BASE_URL` | derived from `DNS_PREFIX` + `DOMAIN` | optional cutover URL | Explorer LCD base URL used by post-reset verification. |
| `SNAPSHOT_PUBLIC_HOST` | derived from `DNS_PREFIX` + `DOMAIN` | optional cutover host | Public snapshot hostname used for local Host-header checks. |
| `SNAPSHOT_BASE_URL` | derived from `SNAPSHOT_PUBLIC_HOST` | optional cutover URL | Snapshot frontend and metadata base URL used by post-reset verification. |
| `VERIFY_PUBLIC_ENDPOINTS` | `true` | `false` before DNS cutover | Set to `false` only for pre-DNS migration resets. SSH jobs still check local services, and public HTTPS checks must run after DNS cutover. |

The workflows have defaults for these values, but operators should set them
explicitly so the selected environment is visible in GitHub's UI.

Before production DNS cutover, set SSH host variables such as `PRIMARY_HOST`,
`EXPLORER_HOST`, and `SNAPSHOT_HOST` to the destination IPs. Set
`PRIMARY_REST_URL` to the destination primary's private VPC REST endpoint, for
example `http://<primary-private-ip>:1317`, so snapshot and archive reset jobs
peer with the new primary instead of whichever host public DNS still resolves
to.

`release.yml` is not tied to a GitHub Environment. If release artifacts should
be published to a non-default bucket or region, set the repository variables
`DO_SPACES_BUCKET` and `DO_SPACES_REGION` before pushing the release tag.

## Environment Secrets

Set these in both environments unless noted:

| Secret | Required for | Notes |
|--------|--------------|-------|
| `PRIMARY_HOST` | deploy, reset | SSH host/IP for the primary validator. May be an environment variable instead when not sensitive. |
| `SECONDARY_HOST` | staging deploy/reset | Omit in production when `HAS_SECONDARY=false`. May be an environment variable instead when not sensitive. |
| `EXPLORER_HOST` | deploy, reset | SSH host/IP for the explorer/archive node. May be an environment variable instead when not sensitive. |
| `SNAPSHOT_HOST` | deploy, reset | SSH host/IP for the snapshot node. May be an environment variable instead when not sensitive. |
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
