# Runbook: Manifest-signer key rotation & multi-signer enrollment

## Why this runbook exists

The wallet bundle's `manifest_signers[]` is a **trust root**: rotating it is a
wallet release, not a CDN push. This document covers:

1. **Initial enrollment ceremony** for a brand-new signer.
2. **Adding a second signer** so we can move from `k_required = 1` (single
   point of failure) to `k_required = 2` (one signer's key compromise no
   longer breaks privacy).
3. **Rotation** (planned or emergency) of an existing signer's key.

For schema / threat model context see [../config.md](../config.md).

## Rotation window — the rule that constrains everything

Wallet bundles bake `manifest_signers[]` at build time. A rotation is
"complete" only after every active wallet user has updated to a build that
contains the new signer set. The release-to-rotation lag is bounded below by:

- **iOS/macOS App Store review**: 1–7 days, plus update adoption tail (50%
  within 24h, 95% within 14d, long-tail beyond).
- **Android (if applicable)**: similar.
- **Self-hosted / dev wallets**: out-of-band notification.

We treat **30 days** as the minimum rotation window: from the day a wallet
release with the new signer ships, an old signer's key is still considered
valid for 30 days during which both old and new signers continue publishing
attestations. Only after 30 days do we drop the old signer's contributions
from the active set (server-side: stop signing with the old key). Wallets
that haven't updated by then will hard-fail `manifestSignaturesMissing` until
they do — that's the intended UX; it's what makes rotation enforceable.

## Initial enrollment (signer #1)

Done once per chain at first round. Skip if already done.

1. **Generate the key** on the operator's offline / hardened machine:

   ```sh
   manifest-signer keygen \
     --signer-id  valarg-vote-authority \
     --out-priv   ~/.config/valar/manifest-signer.key \
     --out-pub    ~/.config/valar/manifest-signer.pub
   ```

   `keygen` prints the pubkey (base64) and the SHA-256 fingerprint (hex). Both
   should be communicated to wallet maintainers via two independent channels
   (e.g. signed email + in-person hash readout).

2. **Wallet maintainers** add the signer to the wallet bundle:

   ```jsonc
   // zodl-ios/secant/Resources/manifest-signers.json (or equivalent)
   {
     "manifest_signers": [
       {
         "id": "valarg-vote-authority",
         "alg": "ed25519",
         "pubkey": "<base64 32-byte pubkey from keygen>"
       }
     ],
     "k_required": 1
   }
   ```

   The wallet build pipeline pins this file; CI verifies the SHA-256 against
   the value the operator communicated out-of-band.

3. **Cut a wallet release.** Until users adopt this build, no Phase 2
   verification happens — older builds either don't speak `round_signatures`
   yet or have an empty trust anchor.

4. **Operator publishes the first `round_signatures`** per
   [sign-round-manifest.md](sign-round-manifest.md).

## Adding a second signer (`k_required: 1 → 2`)

The goal: any single signer-key compromise no longer lets an attacker forge a
round_signatures that wallets accept.

### Pre-requisites

- A second, **organizationally distinct** operator (community member, external
  partner, separate-team employee). Co-located keys defeat the purpose.
- The second operator generates their key on their own machine and never
  shares the private half.

### Enrollment ceremony

1. **Operator B (new signer)** runs `manifest-signer keygen`:

   ```sh
   manifest-signer keygen \
     --signer-id  community-observer-1 \
     --out-priv   ~/.config/observer/manifest-signer.key \
     --out-pub    ~/.config/observer/manifest-signer.pub
   ```

2. **Operator B sends the pubkey to wallet maintainers** via two independent
   channels (email + voice readout of SHA-256).

3. **Wallet maintainers** stage the change in a feature branch:

   ```diff
   {
     "manifest_signers": [
       {
         "id": "valarg-vote-authority",
         "alg": "ed25519",
         "pubkey": "..."
       },
   +   {
   +     "id": "community-observer-1",
   +     "alg": "ed25519",
   +     "pubkey": "<base64 from keygen>"
   +   }
     ],
   - "k_required": 1
   + "k_required": 2
   }
   ```

   Note: `k_required` is bumped in the **same commit** that adds the second
   signer. A wallet that ships the new signer but still has `k_required: 1` is
   pointless — one signer suffices and the second never gets exercised.

4. **Cut a wallet release.** During the 30-day rotation window, both signers
   sign every round-manifest and every checkpoint. Wallets running an old
   build (`k_required = 1`) accept either signer; wallets running the new
   build (`k_required = 2`) require both.

   Important: during this window, if either signer's pipeline fails, wallets
   on the new build hard-fail. Both pipelines MUST be operational before
   shipping the release. Validate by running both
   `sign-round-manifest.md` flows for a non-production round and verifying
   the merged `round_signatures` against the new wallet build.

5. **End of rotation window**: nothing changes operationally for `k_required`
   unless we need to drop a signer (see "rotation" below).

## Planned rotation (signer X → X′)

Triggered by: scheduled rotation (every 6–12 months), suspected weakness,
operator handover. Timeline:

| Day  | Action                                                                                                |
| ---- | ----------------------------------------------------------------------------------------------------- |
| T    | New key X′ generated. Pubkey communicated to wallet maintainers out-of-band.                          |
| T+0  | Operator starts dual-signing: every round_signatures and checkpoint includes both X and X′ signatures. |
| T+0  | Wallet release cut: `manifest_signers` adds X′ alongside X. `k_required` unchanged.                   |
| T+30 | At least 95% of users have adopted; cut a follow-up wallet release that drops X from `manifest_signers`. |
| T+30 | Operator stops signing with X. Final destruction of X's private key (zeroize the file / delete KMS). |

Until the second wallet release ships, both old and new keys must keep
signing. If either fails for > 24h during the dual-sign window, alert and
restart the operator's signing pipeline; wallets won't notice as long as one
of the two signs.

## Emergency rotation (suspected key compromise)

Compromise is an explicit Sentry-pageable event. The user-visible privacy
guarantee is broken from the moment of compromise until users update to a
build that no longer trusts the compromised key.

1. **Hot-fix wallet release**: drop the compromised signer from
   `manifest_signers` immediately. If `k_required = 1` and there's no second
   signer to fall back on, this is a dark-window outage: voting is blocked
   until both the new key is enrolled and the new build ships.
2. **Generate replacement key offline.** Do not reuse the compromised key for
   any future signing.
3. **Publish a revocation notice**: a signed file `signer-revocations/<id>.json`
   in `token-holder-voting-config` documenting the timestamp of revocation.
   This is for operator forensics, not wallet enforcement.
4. **Cut a hot-fix wallet release** with the new (or remaining) trust anchor.
5. **Post-mortem** in `vote-sdk/audit/`.

This is the failure mode `k_required ≥ 2` is supposed to prevent — at
`k_required = 2`, a single key compromise does not break the privacy
guarantee, only the availability of one of the two signing pipelines.

## Verifying a signer pubkey out-of-band

Pubkeys MUST be confirmed via two independent channels before they ship in a
wallet bundle. The reference protocol:

```sh
# Operator side
manifest-signer keygen --signer-id <id> --out-priv ... --out-pub <path>
shasum -a 256 <path>
# 6f4a... <path>
```

```sh
# Wallet maintainer side
shasum -a 256 path/to/imported/manifest-signer.pub
# 6f4a... <path>
# Verbal/video confirmation of the SHA-256 with the operator.
```

The pubkey itself is base64-encoded 32 bytes (decode with `base64 -D`).

## Custody hardening (target for production)

| Custody mode                                                        | Status (Phase 1 POC) | Production target                                         |
| ------------------------------------------------------------------- | -------------------- | --------------------------------------------------------- |
| File on operator laptop                                             | OK for dev / first round | NOT acceptable                                          |
| GitHub Actions secret (`MANIFEST_SIGNER_PRIVKEY`, environment-gated) | Phase 1 default     | OK for non-mainnet rounds                                  |
| Cloud KMS (AWS KMS, GCP KMS)                                        | Future flag         | Recommended for mainnet — `manifest-signer --kms <uri>` is the planned interface |
| Hardware security key (YubiHSM, Ledger)                             | Future flag         | Acceptable; signing latency is fine for 12h cadence        |

The CLI's `--privkey-file` interface is intentionally simple to make migration
to KMS / HSM straightforward — the public-facing canonical-payload computation
and JSON output are independent of where the signature is computed.

## See also

- [../config.md](../config.md) — full spec.
- [sign-round-manifest.md](sign-round-manifest.md) — per-round signing flow.
- [publish-checkpoint.md](publish-checkpoint.md) — checkpoint publisher.
