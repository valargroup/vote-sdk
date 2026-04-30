# Runbook: Publish a signed CometBFT checkpoint

## Overview

`checkpoints/latest.json` is the rolling pointer to the most recent signed
chain checkpoint that Phase 3 wallets use to refresh their light-client trust
anchor. The publisher runs every 12–24h and writes:

- `checkpoints/latest.json` — pointer to the just-signed checkpoint
- `checkpoints/<height>.json` — append-only archive of every published checkpoint

Format and signing details are in
[../config.md §Signed checkpoint schema](../config.md#signed-checkpoint-schema).

In production this is automated via a scheduled GitHub Action in
`token-holder-voting-config/.github/workflows/publish-checkpoint.yml`. This
runbook covers:

1. The exact steps the action runs (and how to run them by hand for debugging).
2. Key custody guidance — same private key as round-manifest signing, see
   [key-rotation.md](key-rotation.md).
3. The two-RPC cross-check that prevents a single hijacked publisher RPC from
   poisoning the trust anchor.

## Inputs

| Input                | Source                              | Production value (svote-1)                                                |
| -------------------- | ----------------------------------- | ------------------------------------------------------------------------- |
| `chain_id`           | constant                            | `svote-1`                                                                 |
| Primary RPC          | constant                            | `https://vote-rpc-primary.valargroup.org`                                 |
| Secondary RPC        | constant (independent of primary)   | `https://vote-rpc-secondary.valargroup.org` (or a community-run mirror)   |
| Signer private key   | GitHub Actions secret               | `MANIFEST_SIGNER_PRIVKEY` — base64 32-byte ed25519 seed                   |
| Signer id            | constant                            | `valarg-vote-authority`                                                   |

The secondary RPC MUST be run by an organizationally distinct operator (or
hosted on different infrastructure with no shared TLS or DNS) — that is the
single point that protects against a hijacked-publisher attack on the trust
anchor.

## Manual flow (debugging or first-time setup)

```sh
CHAIN_ID=svote-1
RPC_PRIMARY=https://vote-rpc-primary.valargroup.org
RPC_SECONDARY=https://vote-rpc-secondary.valargroup.org

# 1. Fetch the latest commit height from the primary, choosing a height a few
#    blocks back from tip to avoid race with new blocks.
LATEST_HEIGHT=$(curl -fsS "${RPC_PRIMARY}/status" | jq -r '.result.sync_info.latest_block_height')
HEIGHT=$((LATEST_HEIGHT - 5))

# 2. Fetch (header_hash, valset_hash, app_hash) from the primary at HEIGHT.
PRIMARY=$(curl -fsS "${RPC_PRIMARY}/header?height=${HEIGHT}" | jq '.result.header')
HEADER_HASH=$(curl -fsS "${RPC_PRIMARY}/block?height=${HEIGHT}" | jq -r '.result.block_id.hash' | tr '[:upper:]' '[:lower:]')
VALSET_HASH=$(echo "${PRIMARY}" | jq -r '.validators_hash' | tr '[:upper:]' '[:lower:]')
APP_HASH=$(echo "${PRIMARY}" | jq -r '.app_hash' | tr '[:upper:]' '[:lower:]')

# 3. CROSS-CHECK against the secondary RPC. Same height, must agree.
SECONDARY_HEADER_HASH=$(curl -fsS "${RPC_SECONDARY}/block?height=${HEIGHT}" \
  | jq -r '.result.block_id.hash' | tr '[:upper:]' '[:lower:]')
SECONDARY_VALSET_HASH=$(curl -fsS "${RPC_SECONDARY}/header?height=${HEIGHT}" \
  | jq -r '.result.header.validators_hash' | tr '[:upper:]' '[:lower:]')

if [ "${HEADER_HASH}" != "${SECONDARY_HEADER_HASH}" ] || [ "${VALSET_HASH}" != "${SECONDARY_VALSET_HASH}" ]; then
  echo "FATAL: primary/secondary RPC divergence at height ${HEIGHT}" >&2
  echo "  primary header_hash=${HEADER_HASH} valset_hash=${VALSET_HASH}" >&2
  echo "  secondary header_hash=${SECONDARY_HEADER_HASH} valset_hash=${SECONDARY_VALSET_HASH}" >&2
  exit 1
fi

# 4. Sign.
manifest-signer sign-checkpoint \
  --chain-id     "${CHAIN_ID}" \
  --signer-id    valarg-vote-authority \
  --height       "${HEIGHT}" \
  --header-hash  "${HEADER_HASH}" \
  --valset-hash  "${VALSET_HASH}" \
  --app-hash     "${APP_HASH}" \
  --privkey-file ~/.config/valar/manifest-signer.key \
  --output       /tmp/checkpoint-${HEIGHT}.json

# 5. Stage the file under both names.
cp /tmp/checkpoint-${HEIGHT}.json checkpoints/${HEIGHT}.json
cp /tmp/checkpoint-${HEIGHT}.json checkpoints/latest.json

# 6. Commit and push.
git add checkpoints/
git commit -m "checkpoint @${HEIGHT}"
git push origin main
```

GitHub Pages picks up the change within ~30s.

## Scheduled GitHub Action

The publisher is a workflow named `publish-checkpoint.yml` in
`token-holder-voting-config/.github/workflows/`. It:

1. Runs on `cron: '0 */12 * * *'` (every 12 hours, UTC) and on
   `workflow_dispatch`.
2. Installs Go and builds `manifest-signer` from `vote-sdk` at a pinned ref.
3. Loads `MANIFEST_SIGNER_PRIVKEY` from the
   `production-manifest-signer` GitHub Environment (which gates access to the
   secret behind a manual approval for `workflow_dispatch` runs).
4. Runs the manual-flow commands above.
5. Commits and pushes to `main`. GitHub Pages auto-deploys.
6. On any failure (RPC unreachable, primary/secondary divergence, signature
   step fails), opens an issue tagged `incident:checkpoint-publisher`.

A reference YAML lives at
[`token-holder-voting-config/.github/workflows/publish-checkpoint.yml`](https://github.com/valargroup/token-holder-voting-config/blob/main/.github/workflows/publish-checkpoint.yml).

## Cadence and freshness

- `trust_period_secs = 1209600` (14 days) is mirrored from the chain's
  `unbonding_period / 2`. Wallets refuse to adopt a checkpoint older than this.
- The publisher's 12h cadence gives ~28 cycles of headroom inside the trust
  period — a single failed run is not user-visible; ten consecutive failures
  start to be.
- Sentry alert: `checkpoint-freshness > 24h` (warning) / `> 72h` (page).
  Configured in `vote-sdk/sentry/` (see [observability.md](../observability.md)).

## Operator escalation

If the publisher fails for > 7 days:

1. **First**: triage the failure issue auto-opened by the workflow. Most often
   this is a transient RPC outage.
2. **If RPCs are healthy** but signing fails: assume signer-key compromise or
   rotation in progress — go to [key-rotation.md](key-rotation.md).
3. **If wallets are already past the trust window** (Phase 3 only): user
   recovery is "update wallet"; the bundled checkpoint in the next wallet
   release re-bootstraps the anchor. Phase 2-only wallets are unaffected by
   checkpoint freshness.

## Key custody

Same key as round-manifest signing. Per the trust model, this key signs both
the per-round attestations and the periodic checkpoints. Custody guidance:

- **POC / dev**: `~/.config/valar/manifest-signer.key` (file, mode 0600). OK
  for first-round dry-runs.
- **Pre-mainnet**: GitHub Environment secret with manual approval gate. The key
  never leaves the GitHub-hosted runner; the secret is base64'd, never logged.
- **Mainnet target**: cloud KMS (AWS KMS, GCP KMS) or a hardware security key
  (YubiHSM, Ledger) — `manifest-signer` will gain a `--kms` mode whose
  reference implementation is a follow-up to Phase 1. Do NOT ship a long-lived
  `MANIFEST_SIGNER_PRIVKEY` plaintext secret to mainnet.

See [key-rotation.md](key-rotation.md) for the rotation flow that the cloud-KMS
mode will plug into.

## See also

- [../config.md](../config.md) — full spec.
- [sign-round-manifest.md](sign-round-manifest.md) — sibling runbook for
  per-round signatures.
- [key-rotation.md](key-rotation.md) — multi-signer enrollment / rotation.
