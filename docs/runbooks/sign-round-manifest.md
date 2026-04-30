# Runbook: Sign a round manifest

## Overview

After a Shielded-Vote round's TSS ceremony completes and `VoteRound.ea_pk` is
visible on-chain, an enrolled **manifest signer** publishes a `round_signatures`
attestation that binds `(round_id, ea_pk, valset_hash)` together. Wallets that
trust this signer's pubkey will then accept the round and encrypt ballot
amounts to the attested `ea_pk`.

If you don't sign the manifest, no Phase 2 wallet user can vote on this round —
they will hard-fail with `manifestSignaturesMissing`. Sign promptly after the
round's `EA_PK` is confirmed on-chain.

For the schema and threat-model context, see
[../config.md](../config.md). This runbook is the operator-facing
step-by-step.

## Prerequisites

- `manifest-signer` Go binary built from
  [`vote-sdk/cmd/manifest-signer/`](../../cmd/manifest-signer/):

  ```
  cd vote-sdk
  go install ./cmd/manifest-signer
  ```

- Your signer ed25519 private key — see [key-rotation.md](key-rotation.md) for
  custody guidance. The CLI accepts the key via:
  - `--privkey-file <path>` — base64-encoded 32-byte seed in a file (POC default)
  - `MANIFEST_SIGNER_PRIVKEY` environment variable (same encoding)
  - stdin via `--privkey-stdin`
- Read access to a trusted RPC endpoint that has the round's
  `created_at_height` block. Production: `https://vote-rpc-primary.valargroup.org:443`.
- Write access to the `token-holder-voting-config` GitHub repo (PR or push).

## Step 1 — Confirm the round is final on-chain

```sh
ROUND_ID=6823028ccc36f2fffc5a6d9af3e62a918a33913a8f37c2d3efe962a0357aa03f
API=https://vote-chain-primary.valargroup.org

curl -fsS "${API}/shielded-vote/v1/round/${ROUND_ID}" | jq '.round | {round_id: .vote_round_id, ea_pk, status, created_at_height}'
```

Required:

- `status` is `2` (`active`) or `3` (`tallying`) / `4` (`finalized`) — never sign
  while ceremony is still in flight (status `1`).
- `ea_pk` is non-empty.
- Save `created_at_height` for step 2.

## Step 2 — Look up the validator-set hash at `created_at_height`

```sh
HEIGHT=2814203
RPC=https://vote-rpc-primary.valargroup.org

# CometBFT's /header endpoint returns Header.ValidatorsHash for the block at
# height H, which is the validator set that signed block H.
curl -fsS "${RPC}/header?height=${HEIGHT}" \
  | jq -r '.result.header.validators_hash'
```

Save this 64-char hex string as `VALSET_HASH`.

> **Why the validator hash, not next-validators?** `ValidatorsHash` commits to
> the set that produced the block; that's the set whose membership the chain's
> consensus state agreed on at `created_at_height`. Cross-fork replay is
> prevented because a fork would have a different set at the same height.

## Step 3 — Sign

```sh
manifest-signer sign-round \
  --chain-id     svote-1 \
  --signer-id    valarg-vote-authority \
  --round-id     "${ROUND_ID}" \
  --ea-pk        "$(curl -fsS "${API}/shielded-vote/v1/round/${ROUND_ID}" | jq -r '.round.ea_pk')" \
  --valset-hash  "${VALSET_HASH}" \
  --privkey-file ~/.config/valar/manifest-signer.key \
  --output       round_signatures.json
```

The tool prints the canonical payload bytes (hex), the SHA-256 of the payload,
and the resulting signature, then writes the full `round_signatures.json` to
the output path.

```sh
cat round_signatures.json
```

```jsonc
{
  "round_id": "6823028ccc36f2fffc5a6d9af3e62a918a33913a8f37c2d3efe962a0357aa03f",
  "ea_pk": "<base64>",
  "valset_hash": "<64-char hex>",
  "signed_payload_hash": "<64-char hex>",
  "signatures": [
    {
      "signer": "valarg-vote-authority",
      "alg": "ed25519",
      "signature": "<base64>"
    }
  ]
}
```

## Step 4 — Verify locally before publishing

```sh
manifest-signer verify \
  --chain-id     svote-1 \
  --pubkey-file  ~/.config/valar/manifest-signer.pub \
  --round-id     "${ROUND_ID}" \
  --ea-pk        "$(curl -fsS "${API}/shielded-vote/v1/round/${ROUND_ID}" | jq -r '.round.ea_pk')" \
  --valset-hash  "${VALSET_HASH}" \
  --input        round_signatures.json
```

Exit `0` = signature valid; non-zero = something is wrong. Investigate before
publishing — once a wallet user fetches a bad config, they hard-fail.

## Step 5 — Publish to the CDN

In a checkout of
[`token-holder-voting-config`](https://github.com/valargroup/token-holder-voting-config):

```sh
git checkout -b round-${ROUND_ID:0:8}-signatures

# Splice round_signatures.json into voting-config.json under top-level
# "round_signatures" key. The two checked-in helpers do this idempotently:
jq --slurpfile sigs round_signatures.json \
   '.round_signatures = $sigs[0]' \
   voting-config.json > voting-config.json.tmp \
   && mv voting-config.json.tmp voting-config.json

# CI validates the schema; run it locally first if you have node installed.
node scripts/validate-config.mjs

git add voting-config.json
git commit -m "round ${ROUND_ID:0:16}: add round_signatures"
git push -u origin HEAD
gh pr create --fill
```

Once merged, GitHub Pages picks up the change within ~30s and Phase 2 wallets
will start accepting the round.

## Multi-signer rounds (`k_required ≥ 2`)

If `manifest_signers[].length > 1` and `k_required ≥ 2`, **each** enrolled
signer runs steps 1–4 independently and produces their own
`round_signatures.json`. The publisher merges:

```sh
jq -s '.[0] as $a | .[1] as $b
       | $a + {signatures: ($a.signatures + $b.signatures)}' \
   sigs-valar.json sigs-observer.json \
   > round_signatures.json
```

The resulting `signatures[]` has one entry per signer. CI rejects the merge if
`signed_payload_hash` differs across the inputs (the canonical payload MUST be
byte-identical — same `chain_id`, `round_id`, `ea_pk`, `valset_hash`).

See [key-rotation.md](key-rotation.md) for the enrollment ceremony.

## Troubleshooting

| Symptom                                                                | Cause                                                                                          | Fix                                                                                                            |
| ---------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------- |
| `manifest-signer verify` returns `signature mismatch`                  | Wrong pubkey, or canonical payload was constructed differently (e.g. wrong `chain_id` casing). | Re-run `sign-round` with the same `--chain-id`, `--round-id`, `--ea-pk`, `--valset-hash`; bytes must match.    |
| Wallet fails `manifestSignatureInvalid` for a signature that verifies locally | Wallet bundle's `manifest_signers` doesn't include your signer id, or pubkey mismatch.         | Coordinate with wallet maintainers — your pubkey may need to ship in the next wallet release.                  |
| Wallet fails `eaPkMismatch`                                            | `ea_pk` you signed doesn't match what the vote-server returns.                                 | Re-fetch `ea_pk` from the chain (`/shielded-vote/v1/round/<id>`) and re-sign. Don't sign cached values.        |
| `valset_hash` doesn't match a vote-server's view                       | You queried a forked or out-of-sync RPC.                                                       | Use a primary RPC; cross-check against `${API}/cosmos/base/tendermint/v1beta1/blocks/${HEIGHT}` if in doubt.   |

## See also

- [../config.md](../config.md) — full spec.
- [publish-checkpoint.md](publish-checkpoint.md) — sibling runbook for the
  CometBFT checkpoint publisher.
- [key-rotation.md](key-rotation.md) — multi-signer enrollment / rotation.
