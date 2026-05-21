# TSS EA Key Ceremony

This document describes the threshold secret sharing (TSS) ceremony for establishing the election authority public key `ea_pk` for each voting round. The ceremony uses Joint-Feldman distributed key generation (DKG) so that `ea_sk` is never known to any single party — only the aggregate tally is recoverable, and only with cooperation from at least `t` validators.

The minimum number of eligible validators is controlled by the `min_ceremony_validators` genesis parameter (stored as a KV singleton, default: 1). `CreateVotingSession` rejects rounds when fewer than `min_ceremony_validators` validators have registered Pallas keys. Set to 2 or higher on mainnet for real threshold security.

## Threshold Secret Sharing

**Trust model:** no trusted dealer. Each validator generates their own polynomial and distributes encrypted shares. The combined `ea_sk = sum(s_i)` is never known to any single party. Any `t` validators acting together can reconstruct `ea_sk * C1`, but fewer than `t` learn nothing.

### Threshold value

For a ceremony with `n` validators:

```
t(n) = 1                       (n = 1: trivial single-share, no threshold security)
t(n) = max(2, ceil(n/2))       (n >= 2)
```

The TSS reconstruction threshold `t` is deliberately separate from the
ceremony activation quorum. Timeout activation must leave enough active
validators above `t` so that the round can still tally if some survivor later
withholds their partial decryption.

For the quorum-hardening design:

```
f(n) = floor((n - t(n)) / 2)
x(n) = f(n)                    // tolerated later partial-decryption withholders
y(n) = f(n)                    // tolerated non-contributors / non-ackers
required(n) = max(t(n) + x(n), n - y(n))
```

`required(n)` is the minimum contribution/ack quorum for bounded-subset
finalization on timeout. The `n - y(n)` term bounds how many validators may be
missing before activation; the `t(n) + x(n)` term ensures enough survivors
remain for tally even if later withholders spend the same fault budget.

The safety invariant is:

```
required(n) - f(n) >= t(n)
n - 2f(n)       >= t(n)
```

Because of integer rounding, `t + f` and `n - f` are not always equal. The
implementation therefore takes the stricter side with `max(...)`.

| n | t | f = x = y | required | required - t | Notes |
|---|---|-----------|----------|--------------|---|
| 1 | 1 | 0 | 1 | 0 | Single share = full key; for local testing only |
| 2 | 2 | 0 | 2 | 0 | Both validators required |
| 3 | 2 | 0 | 3 | 1 | All validators required for activation |
| 4 | 2 | 1 | 3 | 1 | One activation-phase or tally-phase fault tolerated |
| 5 | 3 | 1 | 4 | 1 | Canonical 4/5 quorum |
| 6 | 3 | 1 | 5 | 2 | |
| 7 | 4 | 1 | 6 | 2 | |
| 8 | 4 | 2 | 6 | 2 | |
| 9 | 5 | 2 | 7 | 2 | |
| 10 | 5 | 2 | 8 | 3 | |
| 11 | 6 | 2 | 9 | 3 | |
| 12 | 6 | 3 | 9 | 3 | |
| 13 | 7 | 3 | 10 | 3 | |
| 14 | 7 | 3 | 11 | 4 | |
| 15 | 8 | 3 | 12 | 4 | |

**Warning:** with `n = 1, t = 1` the single validator holds the full `ea_sk` (the degree-0 polynomial makes `share = secret`). This provides no threshold security and should only be used for local development/testing.

### Ceremony state machine

```
PENDING (REGISTERING) ──[n × MsgContributeDKG]──> PENDING (DEALT) ──[n × MsgAck]──> ACTIVE
```

Each validator contributes once during REGISTERING. The all-honest fast path
combines all commitments and transitions to DEALT after all `n` contributions.
The bounded-subset timeout design only finalizes with a contributor/acker subset
when that subset has at least `required(n)` validators; otherwise the ceremony
fails and must be retried.

`VoteRound` fields set during the ceremony:

| Field | Type | Set when | Description |
|---|---|---|---|
| `dkg_contributions` | `repeated DKGContribution` | Each contribution | Per-validator commitments + encrypted payloads |
| `threshold` | `uint32` | DEALT transition | Minimum shares for reconstruction (`t`). Always >= 1. |
| `feldman_commitments` | `repeated bytes` | DEALT transition | Combined `C_j = sum_i(C_{i,j})` for `j=0..t-1` (`t` compressed Pallas points) |
| `ea_pk` | `bytes` | DEALT transition | `sum_i(C_{i,0}).Compress()` — the combined ElGamal public key |

### Contribution phase (`PrepareProposal` — auto-contribute)

When a block proposer detects a PENDING round in REGISTERING status, is a ceremony validator, and has not yet contributed:

1. Generate a random secret `s_i` and compute `t = max(2, ceil(n/2))` for `n >= 2` (`t = 1` for the single-validator dev path).
2. Build a degree-`(t-1)` polynomial `f_i(x)` over Pallas Fq with `f_i(0) = s_i`:
   ```
   f_i(x) = s_i + a_1*x + a_2*x^2 + ... + a_{t-1}*x^{t-1}
   ```
   where `a_1 ... a_{t-1}` are uniformly random scalars.
3. Evaluate shares: `share_j = f_i(j)` for each other validator `j`. The proposer's own share is computed later from the persisted coefficients.
4. Compute Feldman commitments: `C_{i,j} = a_j * G` for `j = 0..t-1`.
5. Persist polynomial coefficients to disk: `<ea_sk_dir>/coeffs.<hex(round_id)>` (`t` × 32 bytes, mode 0600).
6. ECIES-encrypt `share_j` to validator `j`'s registered Pallas key (n-1 envelopes, excluding self).
7. Zero coefficients and shares from memory.
8. Inject `MsgContributeDKG` containing the Feldman commitments and ECIES envelopes.

**On-chain `ContributeDKG` handler**: validates proposer identity, ceremony membership, no duplicate contribution, correct commitment/payload counts, valid Pallas points. Appends to `round.DkgContributions`. On the all-contributor fast path, calls `CombineCommitments` (point-wise sum of all commitment vectors), sets `ea_pk`, `feldman_commitments`, `threshold`, and transitions to DEALT. The bounded-subset timeout design applies the same transition to a contributor subset only when `len(contributors) >= required(n)`.

### Ack phase (`PrepareProposal` — auto-ack)

When a block proposer detects a PENDING round in DEALT status and has not yet acked:

1. Load own polynomial coefficients from `<ea_sk_dir>/coeffs.<hex(round_id)>`.
2. Compute own partial share: `own_partial = EvalPolynomial(coeffs, shamirIndex)`.
3. For each other validator's contribution in `round.DkgContributions`:
   - Find the ECIES envelope addressed to self.
   - Decrypt with own Pallas SK to recover `share_j` (the share that contributor `j` computed for this validator).
   - Verify against contributor `j`'s individual Feldman commitments via `VerifyFeldmanShare`.
   - If any verification fails, skip ack (the ceremony will timeout and reset).
4. Sum into combined share: `combined_share = own_partial + sum(received_shares)`.
5. Verify `combined_share` against the combined Feldman commitments on `round.FeldmanCommitments`.
6. Write `combined_share` to disk as `<ea_sk_dir>/share.<hex(round_id)>`.
7. Delete the coefficients file (no longer needed).
8. Inject `MsgAckExecutiveAuthorityKey`.

### On-disk key files

| File | When written | When deleted | Contents |
|---|---|---|---|
| `coeffs.<hex(round_id)>` | Contribution (PrepareProposal) | Ack (after combined share computed) | `t` × 32 bytes: polynomial coefficients |
| `share.<hex(round_id)>` | Ack (PrepareProposal) | After tally finalized | 32 bytes: combined Shamir share |

Files are written mode `0600`. The tally injector reads the share file for the round.

### ECIES encryption scheme

The same scheme is used in both modes. The generator `G` is SpendAuthG (Orchard's `spend_auth_g`), shared with the ElGamal encryption used for votes.

```
E   = e * G                        (ephemeral public key, fresh per payload)
S   = e * pk_i                     (ECDH shared secret)
k   = SHA256(E_compressed || S.x)  (32-byte symmetric key)
ct  = ChaCha20-Poly1305(k, nonce=0, plaintext)
```

The plaintext is `share_i.Bytes()` (32 bytes).

### Tally phase

After a round enters TALLYING, partial decryptions are collected and combined.

#### Step 1: submit partial decryptions (`PrepareProposal`)

When a validator is the block proposer and a TALLYING round exists, and the proposer has not yet submitted for that round:

1. Load `<ea_sk_dir>/share.<hex(round_id)>` from disk (written during ack phase).
2. For each non-empty ElGamal accumulator `(C1, C2)` on-chain:
   - Compute `D_i = share_i * C1`.
3. Inject `MsgSubmitPartialDecryption` with all `(proposal_id, vote_decision, D_i)` entries.

**On-chain `MsgSubmitPartialDecryption` handler**:
- Validates round is TALLYING.
- Validates `validator_index` is 1-based and matches `creator`.
- Rejects duplicate submissions (one per validator per round).
- Validates each entry: 32-byte `partial_decrypt`, valid `proposal_id` and `vote_decision`.
- Stores all entries via `SetPartialDecryptions` under key `0x12 || round_id || validator_index || proposal_id || decision`.

#### Step 2: combine and finalize (`PrepareProposal`)

When the block proposer detects that `CountPartialDecryptionValidators >= threshold`:

1. Load all stored partial decryptions grouped by accumulator via `GetPartialDecryptionsForRound`.
2. For each accumulator `(C1, C2)`:
   - Build `[{Index: i, Di: D_i}]` from all stored entries.
   - Call `shamir.CombinePartials(partials, threshold)` → `skC1 = ea_sk * C1`.
   - Compute `v*G = C2 - skC1`.
   - Run BSGS to solve `v*G → v`.
3. Inject `MsgSubmitTally` with `(proposal_id, decision, total_value)` per accumulator. No `DecryptionProof` in Step 1.

**On-chain `MsgSubmitTally` handler — threshold verification** (Step 1):
- For each entry with a non-nil accumulator, re-runs the Lagrange combination from stored partials.
- Checks `C2 - combined == totalValue * G` by comparing compressed Pallas points.
- On success, stores `TallyResult`, transitions round to FINALIZED.

#### KV storage layout for partial decryptions

```
0x12 || round_id (32 bytes) || uint32 BE validator_index
     || uint32 BE proposal_id || uint32 BE vote_decision
  → PartialDecryptionEntry (protobuf)
```

Prefix scans:
- `0x12 || round_id` — all entries for a round (used by tally combiner)
- `0x12 || round_id || validator_index` — check if a validator already submitted

## Security Guarantees

### No trusted dealer (Joint-Feldman DKG)

Each validator generates their own polynomial, publishes Feldman commitments, and distributes ECIES-encrypted shares to all other validators. The combined public key `ea_pk = sum(C_{i,0})` is computed on-chain; `ea_sk = sum(s_i)` is never assembled by any party. No single validator can unilaterally determine or learn the group secret.

The state machine reuses the same ceremony statuses — the only structural change vs. a single-dealer model is that REGISTERING → DEALT requires `n` contributions instead of 1:

```
REGISTERING ──[n × MsgContributeDKG]──> DEALT ──[n × MsgAck]──> CONFIRMED ──> ACTIVE
```

The tally pipeline (partial decryptions, Lagrange interpolation, BSGS) is unchanged. Combined Feldman commitments are a drop-in replacement: `VK_i = EvalCommitmentPolynomial(combined_commitments, shamirIndex)` works identically because point-wise addition of commitment vectors corresponds to addition of the underlying polynomials.

#### Why single-phase (no separate COMMITTING phase)

Standard Pedersen DKG separates commitment publication from share distribution to prevent the last participant from biasing the combined public key. On this chain, contributions are sequential (one per proposer turn), so the last contributor can see prior commitments and adapt their `C_{i,0}`.

However, **biasing `ea_pk` does not help the attacker** in this protocol:

- **The attacker cannot learn `ea_sk`.** An attacker who contributes last knows their own `s_A` and can see the prior commitments `C_{j,0}` for `j ≠ A`, giving them `R_pk = sum(C_{j,0})`. But `ea_sk = s_A + R` where `R = sum(s_j, j ≠ A)` is a random scalar unknown to the attacker (protected by the discrete log assumption against `R_pk`). The attacker can shift `ea_pk` to a chosen point, but doing so does not reveal `R` or `ea_sk`.

- **`ea_pk` is used solely for ElGamal vote encryption.** IND-CPA security of ElGamal holds for any valid generator/key, regardless of how the key was chosen. The attacker gains no advantage in decrypting votes by biasing `ea_pk`.

- **Gennaro et al. (2007) proved** that Joint-Feldman DKG is secure for threshold decryption despite the key bias. The bias matters only in protocols that require a provably uniform public key (e.g., common reference string generation for ZK proofs); this system has no such requirement.

A separate COMMITTING phase would add an extra state, an extra message type, and ~n extra blocks of latency for no practical security gain. Even a two-phase design (commit-then-reveal) does not fully prevent bias on a sequential blockchain — the last committer still sees prior commitments. Full prevention requires a three-phase hash-commit-reveal, tripling the latency.

#### Why not vote extensions (CometBFT ExtendVote)

CometBFT's ABCI `ExtendVote` / `VerifyVoteExtension` would allow all validators to contribute simultaneously within a single consensus round, collapsing the contribution phase from `n` blocks to 1 and naturally eliminating the last-contributor bias.

This approach was rejected because:

1. **New ABCI surface.** ExtendVote and VerifyVoteExtension are called during consensus, not during FinalizeBlock. This introduces a new execution context with different safety invariants (no state writes, must be deterministic for verification).

2. **In-memory state across re-rounds.** If consensus fails to finalize on the first attempt, CometBFT may call ExtendVote multiple times. The polynomial coefficients must be cached in memory and reused (regenerating would change commitments across rounds), adding complexity for idempotent behavior.

3. **Deferred disk writes.** Coefficients cannot be persisted in ExtendVote (no disk I/O guarantees during consensus). They must be written later, creating a window where a crash loses the polynomial.

4. **The bias is harmless.** As analyzed above, `ea_pk` bias provides no advantage to the attacker in this protocol. The additional engineering complexity of vote extensions solves a non-problem.

For the current validator set size (n ≤ 9), the `2n`-block contribution + ack latency is negligible relative to the voting period.

### Corrupted shares detected at ack time (Feldman commitments)

Each contributor publishes `t` Feldman polynomial commitments alongside their encrypted shares:

```
C_j = a_j * G    for j = 0..t-1
```

During the ack phase, each validator verifies every received share against the contributor's commitments:
```
share_i * G == sum(C_j * i^j)    for j = 0..t-1
```

This proves consistency — a contributor cannot send conflicting or corrupted shares to different validators without being detected. The commitments reveal nothing about the actual coefficients (discrete log hardness).

`ea_pk` is derivable as `C_0` (the constant term commitment), so it does not need to be published separately.

### Tally sabotage prevented (DLEQ proofs)

Each `PartialDecryptionEntry` includes a Chaum-Pedersen DLEQ proof proving that the validator used the same scalar for their verification key and their partial decryption:

```
DLEQ: log_G(VK_i) == log_{C1}(D_i)
```

The chain verifies the proof in `SubmitPartialDecryption` (FinalizeBlock) before storing the partial decryption. A malicious validator with a fake share cannot forge a valid proof against their published `VK_i`.

Implementation:
- `crypto/elgamal/dleq.go`: `GeneratePartialDecryptDLEQ` / `VerifyPartialDecryptDLEQ` with domain tag `"svote-pd-dleq-v1"`.
- `app/prepare_proposal_partial_decrypt.go`: generates proof alongside each `D_i`.
- `x/vote/keeper/msg_server_tally_decrypt.go`: derives `VK_i` via `EvalCommitmentPolynomial(round.FeldmanCommitments, shamirIndex)` and verifies DLEQ proof against it.

### Summary

| Property | Guarantee |
|---|---|
| Who knows `ea_sk` | **Nobody** — `ea_sk = sum(s_i)` is never assembled |
| Single party can decrypt votes | No — requires `t` partial decryptions |
| Malicious contributor sends bad shares | Detected at ack time (Feldman verification per contributor); bounded ack quorum prevents activation with only exactly `t` usable survivors |
| Malicious validator sabotages tally | Invalid partial decryptions are rejected by DLEQ; withholding is tolerated while at least `t` honest survivors remain |
| Offline validator | REGISTERING phase times out after `DefaultContributionTimeout` (10 min). The bounded-subset design proceeds only if contributors/ackers meet `required(n)`; otherwise non-contributors are jailed through `x/slashing`, the pending round is marked `SESSION_STATUS_CEREMONY_FAILED`, and a later create transaction can retry the same vote metadata with a fresh height-derived `vote_round_id` |
| Compromised Pallas key | Validator rotates via `MsgRotatePallasKey` (blocked during in-flight ceremonies). Future rounds use the new key. Past ECIES ciphertexts in completed `DkgContributions` remain encrypted to the old key. |
| Liveness (all honest, n validators) | ~2n blocks (n contributions + n acks) |

## Roadmap

### DKG Liveness Hardening

Two liveness gaps were identified during DKG review.

#### Issue 1: Offline validator stalls REGISTERING (implemented)

**Problem.** The DKG requires all `n` contributions before transitioning REGISTERING → DEALT (`len(round.DkgContributions) == nValidators` in `ContributeDKG`). If any validator is offline and never proposes a block, the ceremony hangs indefinitely.

**Fix (implemented).** `DefaultContributionTimeout` (10 minutes) is set on `CeremonyPhaseStart` / `CeremonyPhaseTimeout` when a round enters REGISTERING. EndBlocker checks for expired REGISTERING rounds, jails validators that did not contribute, and marks the pending round `SESSION_STATUS_CEREMONY_FAILED` rather than retrying with the same ceremony validator snapshot. A later create transaction can reuse the same vote metadata, derive a fresh `vote_round_id` from its creation height, and snapshot the currently eligible validators.

#### Issue 2: Corrupted-share DoS vector (documented, deferred)

**Problem.** A malicious validator can send shares that fail Feldman verification to selected validators. Currently, `ackDKGRound` returns an error on the first failed `VerifyFeldmanShare`, preventing the validator from acking. If too few validators ack, the DEALT timeout fires and resets to REGISTERING. If the timeout path activates with exactly `t` ackers and one survivor later withholds partial decryptions, the round can reach TALLYING and finalize with `TallyTimedOut=true`.

**Why naive skipping doesn't work.** If `ackDKGRound` simply skipped a bad contributor and summed the remaining shares, validators would need to agree on who was skipped. A sophisticated attacker can send bad shares to *some* validators and valid shares to others. Validators who received good shares include the attacker in their sum; those who received bad shares exclude them. The two groups end up with shares of different combined polynomials — threshold decryption would fail later.

**Initial quorum hardening.** Use a higher activation quorum than the tally
reconstruction threshold:

```
f(n) = floor((n - t(n)) / 2)
required(n) = max(t(n) + f(n), n - f(n))
```

For `n = 5`, this gives `t = 3`, `required = 4`: if one surviving validator
withholds during tally, the remaining three can still finalize. This does not
prove encrypted-share correctness; it only prevents activation with no liveness
slack.

**Proposed solution: majority-vote skip set.** Each ack carries a `SkippedContributors` list identifying which contributors failed Feldman verification for that validator. At confirmation time (fast path or timeout), the chain determines the majority skip set and only counts acks compatible with it. Combined Feldman commitments and `ea_pk` are recomputed excluding the skipped contributors.

Analysis for a single malicious validator `j` who sends bad shares to `k` out
of `n-1` honest validators under a future skip-set protocol must use the same
`required(n)` quorum, not a bare majority:

- `n-k >= required(n)`: the no-skip view has enough validators to confirm with `j` included, while still preserving tally slack.
- `k >= required(n)`: the skip-`j` view has enough validators to recompute without `j`, while still preserving tally slack.
- Neither side reaches `required(n)`: the ceremony fails rather than activating a threshold-sized or view-split survivor set.
- `k = n-1` (bad to everyone): unanimous `{skip j}`. Confirms trivially.

A single attacker cannot force activation with no liveness margin. The attack
degrades to either a bounded failed ceremony or to requiring enough coordinated
selective targeting that no view reaches `required(n)`.

**Why we defer.** The majority-vote mechanism requires:
- Proto change: new `SkippedContributors` field on `MsgAckExecutiveAuthorityKey`.
- `ackDKGRound` rewrite: skip bad contributors instead of failing, track skip set, recompute combined commitments locally.
- Confirm/timeout handler: majority-vote logic to determine canonical skip set, filter compatible acks, recompute on-chain commitments and `ea_pk`.
- Extensive test coverage for all edge cases.

This adds meaningful complexity for an attack that requires a compromised bonded validator. The attack is detectable: every honest validator's logs record the offender's address on Feldman verification failure. Attribution is straightforward.

**Current mitigation.** If the attack is observed in production:
1. Identify the offending validator from node logs (all honest validators will log the same contributor address).
2. Chain upgrade to jail the attacker and exclude them from future ceremonies.
3. Optionally implement the majority-vote mechanism at that point.
