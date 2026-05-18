# Genesis primary bootstrap

This document covers how the **genesis validator** (`vote-primary`) is brought up. In production we bootstrap from CI against a Terraform-provisioned droplet — the manual steps that used to live here are no longer the canonical path. The same `scripts/init.sh` that CI runs is also the local-dev entrypoint, so the [Local single-host bootstrap](#local-single-host-bootstrap) at the bottom is a thin wrapper around it.

For joining additional validators to an already-running chain, see [join-chain.md](join-chain.md). For the running fleet's day-to-day operations, see [production-setup.md](../production-setup.md) and [deploy-setup.md](../deploy-setup.md).

## Production (CI + Terraform)

```mermaid
flowchart LR
    tag["git tag v*"] --> release["release.yml\nbuild + DO Spaces upload"]
    release --> artifacts["shared artifacts"]
    manual["workflow_dispatch\ntarget_environment"] --> reset["sdk-chain-reset.yml"]
    reset --> quiesceSnapshot["quiesce-snapshot\nstop old publisher"]
    quiesceSnapshot --> resetPrimary["reset-primary\ninstall-release.sh + scripts/init.sh"]
    resetPrimary --> uploadGenesis["upload-genesis\ngenesis per chain -> DO Spaces"]
    uploadGenesis --> fundSecondary["fund-secondary\nstaging only"]
    fundSecondary --> resetSecondary["reset-secondary\nstaging only"]
    uploadGenesis --> resetSnapshot["reset-snapshot\nreset-snapshot.sh"]
    uploadGenesis --> resetArchive["reset-archive\nproduction path"]
    resetSecondary --> resetArchive
    resetArchive --> verify["verify\nREST health checks"]
    resetSnapshot --> verify
```

### Where the primary lives

The primary is one DigitalOcean droplet (`vote-primary`, `s-4vcpu-16gb-amd`) defined in [vote-infrastructure/digitalocean.tf](../../../vote-infrastructure/digitalocean.tf). Its first-boot configuration lives in [cloud-init/primary.yaml](../../../vote-infrastructure/cloud-init/primary.yaml) and installs:

- **Caddy** (apt, from Cloudsmith) terminating TLS for `svote.<domain>`, `vote-chain-primary.<domain>`, and `vote-rpc-primary.<domain>` (Caddyfile + DNS records in [vote-infrastructure/](../../../vote-infrastructure/)).
- **`/etc/systemd/system/svoted.service`** with the primary drop-in `svoted.service.d/primary.conf`, which appends `--serve-ui --ui-dist /opt/shielded-vote/current/ui/dist` so this host serves the admin UI in-process.
- **`/etc/default/svoted`** with `SVOTE_PIR_URL=https://pir.<domain>` (PIR runs on the dedicated `vote-nullifier-pir-{primary,backup}` droplets, not co-located).
- **[install-release.sh](../../../vote-infrastructure/scripts/install-release.sh)** under `/opt/shielded-vote/`. The release tarball is unpacked to `/opt/shielded-vote/releases/<tag>/` and `/opt/shielded-vote/current` is an atomically-swapped symlink. Chain state lives on a block volume bind-mounted to `/opt/shielded-vote/.svoted/`.

After `terraform apply`, the droplet is up and `svoted` is installed but the chain is not yet initialized — there is no `genesis.json`. The chain bootstrap is a separate step, driven by CI.

### Bootstrap flow

The single workflow [`sdk-chain-reset.yml`](../../.github/workflows/sdk-chain-reset.yml) (`workflow_dispatch`, takes `tag` and `target_environment`) brings the selected fleet up from genesis:

1. **`resolve-environment`** — reads GitHub Environment variables such as `CHAIN_ID`, `DNS_PREFIX`, `HAS_SECONDARY`, `GENESIS_KEY`, and `SNAPSHOTS_PREFIX`.
2. **`quiesce-snapshot`** — SSHes to `<dns-prefix>snapshots.<domain>`, stops and disables `snapshot.timer`, stops any running `snapshot.service`, and stops old snapshot-node `svoted` before primary chain state changes.
3. **`reset-primary`** — SSHes to `PRIMARY_HOST`, runs `install-release.sh --tag <tag>`, stops `svoted`, wipes `/opt/shielded-vote/.svoted/`, then runs [`scripts/init.sh`](../../scripts/init.sh) with `CHAIN_ID`, `VAL_PRIVKEY=PRIMARY_VAL_PRIVKEY`, `VM_PRIVKEYS`, and `SVOTE_ADMIN_DISABLE=false`. Drops in `svoted.service.d/primary.conf`, starts `svoted`, polls `localhost:1317/shielded-vote/v1/rounds`.
4. **`upload-genesis`** — fetches `genesis.json` from `localhost:1317/shielded-vote/v1/genesis` on the primary, uploads it to `s3://vote/<genesis-key>` (for example `genesis/svote-1/genesis.json` or `genesis/zvote-1/genesis.json`), and clears `s3://vote/<snapshots-prefix>/` so joiners cannot restore a pre-reset snapshot from the same environment. This is the canonical source joining nodes pull from.
5. **`fund-secondary`** — staging only. Derives the secondary's address from `SECONDARY_VAL_PRIVKEY` (in a temp keyring) and funds it with a coordinator-approved `MsgAuthorizedSend` from the `vote_funding` module account.
6. **`reset-snapshot`** — SSHes to `<dns-prefix>snapshots.<domain>`, installs the same tag, runs [`scripts/reset-snapshot.sh`](../../scripts/reset-snapshot.sh) to bring up a pruned non-validator node, then enables `snapshot.timer`.
7. **`reset-secondary`** — staging only. SSHes to `SECONDARY_HOST`, installs the same tag, runs [`scripts/reset-join.sh`](../../scripts/reset-join.sh) (downloads environment-specific genesis from Spaces, discovers the primary's P2P NodeID via REST, syncs, calls `create-val-tx` to register).
8. **`reset-archive`** — SSHes to `<dns-prefix>explorer.<domain>`, renders the explorer config for the selected chain, then runs [`scripts/reset-archive.sh`](../../scripts/reset-archive.sh) to bring up a non-validator archive node (pruning=nothing) for the explorer.

Then `verify` polls all REST endpoints. On any failure the `notify-slack` job fires.

### Required GitHub secrets

Configure the `staging` and `production` GitHub Environments before reset.
Required variables and secrets are listed in [environments.md](environments.md).
Staging uses `CHAIN_ID=svote-1` with a secondary validator; production uses
`CHAIN_ID=zvote-1` with `HAS_SECONDARY=false`.

`VM_PRIVKEYS` is a comma-separated list of 64-char hex secp256k1 private keys; each derived address becomes a coordinator at genesis and the 1B usvote stake pool is split evenly across them. The coordinator threshold defaults to 1 unless configured otherwise. Generate one with `openssl rand -hex 32`.

The environment-prefixed snapshot hostname is required for every reset. A chain
reset invalidates old snapshot node state, so `sdk-chain-reset.yml` always
reinitializes `vote-snapshot` from the newly uploaded genesis.

### First-time bring-up

1. `cd vote-infrastructure && terraform apply` — provisions the validators, PIR hosts, explorer/archive host, snapshot host, Cloudflare DNS, and firewalls.
2. Set the GitHub Environment variables and secrets above (the `*_VAL_PRIVKEY` values are the deterministic identities the workflow expects to find on the corresponding droplets).
3. Trigger the **Reset SDK Chain** workflow with the desired release tag and target environment (the tag must already be published by [`release.yml`](../../.github/workflows/release.yml) — `validate-tag` HEAD-checks DO Spaces and aborts otherwise).
4. After the workflow goes green, sanity-check:
   - `https://<dns-prefix>vote-chain-primary.<domain>/shielded-vote/v1/rounds` returns `200`
   - `https://<dns-prefix>svote.<domain>/` serves the admin UI
   - `https://<dns-prefix>vote-chain-secondary.<domain>/shielded-vote/v1/rounds` returns `200` when `HAS_SECONDARY=true`
   - `https://<dns-prefix>explorer-api.<domain>/cosmos/base/tendermint/v1beta1/blocks/latest` returns `200`
   - `https://<dns-prefix>snapshots.<domain>/` serves the snapshot page

To wipe and reset the chain from genesis later, re-run the same workflow. The first release that adds a new store such as `x/upgrade` must use this reset path because existing live state does not contain that store. For binary swaps that preserve state, use [`sdk-chain-deploy.yml`](../../.github/workflows/sdk-chain-deploy.yml) instead — it installs the new tag across the primary, secondary, explorer/archive, and snapshot hosts, restarting `svoted` where chain state is already initialized. Future coordinated state-breaking upgrades should follow [software-upgrades.md](software-upgrades.md).

### What `scripts/init.sh` does

The same script runs in CI and locally. It wipes `$SVOTED_HOME` (preserving `nullifiers/`), runs `svoted init`, imports `validator` from `VAL_PRIVKEY` (or generates it if unset), selects the vote-manager set, allocates 10M usvote to the validator, creates auth-only vote-manager accounts, and funds the shared `vote_funding` module account with 1B usvote. It then runs `gentx` for the validator's self-delegation, patches `app_state.vote.vote_manager_addresses` and the coordinator threshold, sets the downtime slashing window to 72,800 blocks with 80% minimum signing and a 300s jail duration, and zeros out the slashing fractions in genesis. Staging/local chains import each `vote-manager-N` from `VM_PRIVKEYS`; production `zvote-1` preserves the default vote-manager address encoded in `x/vote/module.go` unless `SVOTE_USE_DEFAULT_GENESIS_VOTE_MANAGERS=false` is explicitly set. Production also refuses to continue unless that Go default matches `SVOTE_EXPECTED_PRODUCTION_VOTE_MANAGER` (default `sv1wyf8tuys2ussdqwc6ugnvq0x273j8wq8fm3jrj`). It then patches `app.toml` to enable the REST API on `:1317` with CORS, the gRPC ports off the Cosmos defaults (Cursor Remote-SSH conflicts with `9090`/`9091`), and writes `[helper]` / `[ui]` sections. It also generates the host's Pallas keypair (`pallas.sk`/`pallas.pk`); the per-round EA key is generated automatically by the auto-deal path during ceremony.

## EA key ceremony

The EA key ceremony runs **automatically per voting round** — when a round is created, eligible validators (bonded + registered Pallas key) are snapshotted and the block proposer auto-deals + auto-acks via `PrepareProposal`. There is no manual ceremony step. The primary's Pallas key is registered atomically when CI runs `init.sh` (inside the `gentx` self-delegation); subsequent validators register theirs via `MsgCreateValidatorWithPallasKey` during their join.

To create the first voting round, an operator opens the admin UI at `https://svote.<domain>/`, goes to **Rounds**, and uses **Create round**. See [tss-ceremony.md](../tss-ceremony.md) for the protocol details.

## Local single-host bootstrap

For local development, the same `init.sh` runs via mise. Provide a `VM_PRIVKEYS` value in `.env` (one hex key for single-vote-manager dev) and let the script generate a fresh validator key:

```bash
cp .env.example .env
echo "VM_PRIVKEYS=$(openssl rand -hex 32)" >> .env

mise install                  # toolchain (Go, Rust, Node)
mise run install              # build svoted + create-val-tx into $GOBIN
mise run chain:init           # wraps scripts/init.sh against ~/.svoted
mise run chain:start          # foreground; sets SVOTE_PIR_URL=http://localhost:3000
```

The single-host devnet binds REST on `0.0.0.0:1317`, RPC on `127.0.0.1:26657`, P2P on `0.0.0.0:26656`. Never commit `.env` — in CI the same value is provided via the `VM_PRIVKEYS` secret. For a 3-validator local devnet (val1+val2+val3 on one host with non-overlapping ports), use `mise run chain:init-multi` + `mise run chain:start-multi` and see [deploy-setup.md § Dev single-host setup](../deploy-setup.md#dev-single-host-setup-3-validators).

If you want PIR locally, run `nf-server` on `localhost:3000` per [vote-nullifier-pir](https://github.com/valargroup/vote-nullifier-pir) — `mise run chain:start` defaults `SVOTE_PIR_URL` there.

## See also

- [production-setup.md](../production-setup.md) — production layout, manual operations, failover runbook.
- [deploy-setup.md](../deploy-setup.md) — CI/CD workflow reference, GitHub secrets, helper/admin/UI configuration.
- [join-chain.md](join-chain.md) — joining the live chain as an additional validator (uses `join.sh`, not `init.sh`).
- [vote-infrastructure/README.md](https://github.com/valargroup/vote-infrastructure) — Terraform layout for droplets, DNS, and firewalls.
