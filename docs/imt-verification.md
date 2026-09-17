# IMT verification acknowledgment

The admin dashboard requires an acknowledgment before signing a round's config
attestation, opening its config PR, or endorsing it. The acknowledgment says the
person independently rebuilt the Ironwood nullifier tree for the displayed Zcash
snapshot and matched its circuit root to the root on chain. The linked AI guide
and Copy AI prompt provide tools to do that. No report upload or proof of running
the verifier is required.

## Round query

Build `svoted` with its existing Rust circuit library, then run:

```sh
svoted query vote verify-round --latest \
  --node "$VOTE_NODE" --expected-chain-id "$VOTE_CHAIN_ID" --output json
```

To inspect a specific round, replace `--latest` with its full hex ID. The default
is the greatest `created_at_height` among all registered rounds, including
pending rounds without an EA key. Ties require an explicit ID. The command
checks the node's chain ID and sync status, pins the query height, and recomputes
the round ID using `ffi/roundid.DeriveRoundID`. A mismatch exits nonzero.

The JSON result includes the on-chain and computed round IDs, snapshot height
and hash, IMT root, and other round identity inputs. This command checks the
round ID's commitment to the root. It does **not** rebuild the tree. Use the
linked `verify-round-imt.sh` wrapper to combine this query with `nf-server sync`
in a fresh directory. The default mode repeats PIR's normal lightwalletd sync
and tree construction, requires the exact snapshot height, and compares the
exported circuit root with the on-chain root. It trusts the selected lightwalletd
source and shares PIR's tree implementation. Authenticated raw-block rebuilding
remains available with `--mode raw-blocks` for an explicit stronger check. Use a
voting node you trust. This query does not independently authenticate voting
consensus.

## Dashboard flows

A checked box belongs to the displayed round, snapshot, chain, and connected
wallet. The chain ID comes from the selected voting server. An unknown chain or
a wallet connected to a different chain blocks acknowledgment and signing.
Changing that context clears it. Changing the account in Keplr or clearing
the box also invalidates an in-flight signing action. Batch creation pauses after
the rounds are created so the person can review and acknowledge the listed
rounds before attesting them.

Endorsement uses the same acknowledgment and AI guide, with no GitHub step. This
is a dashboard gate. It does not change `MsgEndorseRound`, CLI endorsement, or
consensus rules.

## Config PR API

`POST /api/config-prs` requires this object inside the canonical ADR-036 signed
intent, immediately after `entry_sha256`. In a batch, each signed intent round
contains the object.

```json
"imt_verification": {"acknowledged": true, "statement_version": 1}
```

Version 1 acknowledges independently rebuilding the round's snapshot IMT and
matching its circuit root to the root committed by that round. Missing, false,
unknown-version, or unsigned acknowledgments are rejected before GitHub calls.
Older dashboard clients must refresh before using this endpoint. The existing
round authorization signature and published config schema remain unchanged.

The PR body records the authenticated vote-manager address, round ID, and signed
round-authorization hash with the acknowledgment. It records the person's
statement, not an independently certified test result. Reusing an open PR
preserves its existing branch signatures and body, appends the new acknowledgment,
and avoids duplicate records on retry. Preserved and incoming signatures are
combined before validation so an entry can gain a valid replacement signature
after a trusted-key rotation. An unchanged config skips the file write
while still recording a new manager's acknowledgment. Updates are serialized
within one admin server. GitHub content conflicts fail and can be retried.
