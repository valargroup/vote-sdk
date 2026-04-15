# TSS EA Key Ceremony

This document describes the threshold secret sharing (TSS) ceremony for establishing the election authority public key `ea_pk` for each voting round. The ceremony uses Joint-Feldman distributed key generation (DKG) so that `ea_sk` is never known to any single party — only the aggregate tally is recoverable, and only with cooperation from at least `t` validators.

The minimum number of eligible validators is controlled by the `min_ceremony_validators` genesis parameter (stored as a KV singleton, default: 1). `CreateVotingSession` rejects rounds when fewer than `min_ceremony_validators` validators have registered Pallas keys. Set to 2 or higher on mainnet for real threshold security.

## Threshold Secret Sharing

**Trust model:** no trusted dealer. Each validator generates their own polynomial and distributes encrypted shares. The combined `ea_sk = sum(s_i)` is never known to any single party. Any `t` validators acting together can reconstruct `ea_sk * C1`, but fewer than `t` learn nothing.

### Threshold value

For a ceremony with `n` validators:

```
t = 1                (n = 1: trivial single-share, no threshold security)
t = ceil(n/2)        (n >= 2, minimum 2)
```

| n | t | Notes |
|---|---|---|
| 1 | 1 | Single share = full key; for local testing only |
| 2 | 2 | Both validators required |
| 3 | 2 | |
| 4 | 2 | |
| 5 | 3 | |
| 6 | 3 | |
| 9 | 5 | |

**Warning:** with `n = 1, t = 1` the single validator holds the full `ea_sk` (the degree-0 polynomial makes `share = secret`). This provides no threshold security and should only be used for local development/testing.

### Ceremony state machine

```
PENDING (REGISTERING) ──[n × MsgContributeDKG]──> PENDING (DEALT) ──[n × MsgAck]──> ACTIVE
```

Each validator contributes once during REGISTERING. On the n-th contribution, the handler combines all commitments and transitions to DEALT. Acks proceed identically to the single-dealer design.

`VoteRound` fields set during the ceremony:

| Field | Type | Set when | Description |
|---|---|---|---|
| `dkg_contributions` | `repeated DKGContribution` | Each contribution | Per-validator commitments + encrypted payloads |
| `threshold` | `uint32` | DEALT transition | Minimum shares for reconstruction (`t`). Always >= 1. |
| `feldman_commitments` | `repeated bytes` | DEALT transition | Combined `C_j = sum_i(C_{i,j})` for `j=0..t-1` (`t` compressed Pallas points) |
| `ea_pk` | `bytes` | DEALT transition | `sum_i(C_{i,0}).Compress()` — the combined ElGamal public key |

### Contribution phase (`PrepareProposal` — auto-contribute)

When a block proposer detects a PENDING round in REGISTERING status, is a ceremony validator, and has not yet contributed:

1. Generate a random secret `s_i` and compute `t = ceil(n/2)`.
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

**On-chain `ContributeDKG` handler**: validates proposer identity, ceremony membership, no duplicate contribution, correct commitment/payload counts, valid Pallas points. Appends to `round.DkgContributions`. On the n-th contribution, calls `CombineCommitments` (point-wise sum of all commitment vectors), sets `ea_pk`, `feldman_commitments`, `threshold`, and transitions to DEALT.

### Ack phase (`PrepareProposal` — auto-ack with majority-vote skip set)

When a block proposer detects a PENDING round in DEALT status and has not yet acked:

1. Load own polynomial coefficients from `<ea_sk_dir>/coeffs.<hex(round_id)>`.
2. Compute own partial share: `own_partial = EvalPolynomial(coeffs, shamirIndex)`.
3. For each other validator's contribution in `round.DkgContributions`:
   - Find the ECIES envelope addressed to self.
   - Decrypt with own Pallas SK to recover `share_j`.
   - Verify against contributor `j`'s individual Feldman commitments via `VerifyFeldmanShare`.
   - If any step fails (decryption, deserialization, or Feldman verification), **skip** the contributor: add their address to a local `skipped_contributors` list and do not include their share in the sum.
4. Sum into combined share: `combined_share = own_partial + sum(non-skipped received shares)`.
5. Recompute combined Feldman commitments locally from only the non-skipped contributors. Verify `combined_share` against the recomputed commitments.
6. Write `combined_share` to disk as `<ea_sk_dir>/share.<hex(round_id)>`.
7. Delete the coefficients file (no longer needed).
8. Compute ack binding hash: `SHA256("ack" || ea_pk || validator_address || skipped_contributors...)`. The skip set is bound into the hash to prevent post-hoc modification. Note: this is a deterministic binding hash, not a cryptographic signature. Authentication relies on CometBFT proposer enforcement (`ValidateProposerIsCreator`).
9. Inject `MsgAckExecutiveAuthorityKey` with the sorted `skipped_contributors` list.

**On-chain ack handling:**

- `AckExecutiveAuthorityKey` validates the skip set (sorted, no duplicates, all addresses are ceremony contributors, acker not included).
- The ack binding hash is verified including the skip set in the domain.
- **Fast-path confirmation** (all validators acked) only triggers when all skip sets are empty. If any ack has a non-empty skip set, confirmation is deferred to the timeout path.

**DEALT timeout — majority-vote confirmation (EndBlocker):**

When the DEALT phase times out, the chain runs majority-vote logic:

1. **Canonical skip set**: for each contributor address appearing in any ack's skip set, count how many acks include it. If `count * 2 > len(acks)`, the contributor is in the canonical set.
2. **Compatible acks**: only acks whose `skipped_contributors` exactly equals the canonical set are counted.
3. If compatible acks >= threshold AND strictly > n/2: recompute `ea_pk` and `feldman_commitments` excluding the canonical skip set. Strip all non-compatible validators. Confirm and activate.
4. Otherwise: reset to REGISTERING.

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

### Corrupted shares detected and tolerated (Feldman + majority-vote skip set)

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

When a contributor's share fails verification, the acking validator **skips** that contributor (rather than failing entirely) and reports them in their ack's `skipped_contributors` field. At confirmation time, the chain computes a canonical skip set by majority vote across all acks, ensuring all surviving validators agree on exactly which contributors were excluded. This prevents a single malicious validator from permanently stalling the ceremony.

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
| Malicious contributor sends bad shares | Detected and tolerated: skipped via majority-vote, ceremony confirms without them |
| Malicious validator sabotages tally | No — DLEQ proof required per partial decryption |
| Offline validator | REGISTERING phase times out after `DefaultContributionTimeout` (30 min), contributions are cleared and the phase restarts |
| Compromised Pallas key | Validator rotates via `MsgRotatePallasKey` (blocked during in-flight ceremonies). Future rounds use the new key. Past ECIES ciphertexts in completed `DkgContributions` remain encrypted to the old key. |
| Honest-majority threshold | Strictly more than `n/2` honest validators required (`floor(n/2) + 1`). Both skip-set majority and confirmation quorum use strict `>`. |
| Liveness (all honest, n validators) | ~2n blocks (n contributions + n acks) |

## Roadmap

### DKG Liveness Hardening

Two liveness gaps were identified during DKG review.

#### Issue 1: Offline validator stalls REGISTERING (implemented)

**Problem.** The DKG requires all `n` contributions before transitioning REGISTERING → DEALT (`len(round.DkgContributions) == nValidators` in `ContributeDKG`). If any validator is offline and never proposes a block, the ceremony hangs indefinitely.

**Fix (implemented).** `DefaultContributionTimeout` (30 minutes) is set on `CeremonyPhaseStart` / `CeremonyPhaseTimeout` when a round enters REGISTERING (both on initial creation and when resetting from a DEALT timeout). EndBlocker checks for expired REGISTERING rounds and unconditionally clears contributions, resetting `CeremonyPhaseStart` to the current block time. The ceremony validators are preserved, giving everyone a fresh window to contribute. The same reset-only approach applies when a DEALT timeout resets back to REGISTERING.

#### Issue 2: Corrupted-share DoS vector (implemented)

**Problem.** A malicious validator could send shares that fail Feldman verification to all other validators. Previously, `ackDKGRound` returned an error on the first failed `VerifyFeldmanShare`, preventing the validator from acking. If every honest validator's ack failed, the DEALT timeout fired and reset to REGISTERING. The malicious validator could repeat this on every cycle they proposed, stalling the ceremony indefinitely.

**Why naive skipping doesn't work.** If `ackDKGRound` simply skipped a bad contributor and summed the remaining shares, validators would need to agree on who was skipped. A sophisticated attacker can send bad shares to *some* validators and valid shares to others. Validators who received good shares include the attacker in their sum; those who received bad shares exclude them. The two groups end up with shares of different combined polynomials — threshold decryption would fail later.

**Fix (implemented): majority-vote skip set.** Each ack carries a `skipped_contributors` field listing which contributors failed Feldman verification (or any decryption step) for that validator. A binding hash commits the skip set to the ack, preventing post-hoc modification. At confirmation time (DEALT timeout in EndBlocker), the chain:

1. Computes a canonical skip set by strict majority vote: a contributor is skipped if `count * 2 > len(acks)`.
2. Filters to compatible acks (those whose skip set exactly matches canonical).
3. If compatible acks >= threshold and strictly > n/2: recomputes `ea_pk` and `feldman_commitments` excluding skipped contributors, strips incompatible validators, confirms and activates.
4. Otherwise: resets to REGISTERING.

The fast path (all validators acked) only confirms when all skip sets are empty. Any non-empty skip set defers to the timeout path.

**Correctness.** For a single attacker `M` sending bad shares to `k` out of `n-1` honest validators:

- `k < n/2`: majority says no skip. The `k` validators who skipped `M` have incompatible shares and get stripped. Remaining `n-k > n/2 >= t`. Confirms.
- `k >= n/2`: majority says skip `M`. The `n-1-k` validators who did NOT skip `M` have incompatible shares and get stripped. Remaining `k >= n/2 >= t`. Confirms without `M`.
- `k = n-1`: unanimous skip. All `n-1` honest validators have compatible shares. Confirms.

A single attacker cannot prevent confirmation under honest majority. The attack degrades to requiring two or more colluding validators doing coordinated selective targeting, which is a strictly harder attack.

**Honest-majority requirement (strict).** Safety requires **strictly more than half** of the validators to be honest (`floor(n/2) + 1`). Both the canonical skip set and the confirmation quorum use strict majority (`* 2 > n`), so exactly `n/2` colluding validators cannot confirm a ceremony — their compatible acks fail the strict `>` check, triggering a reset instead.

For a single attacker `M` who sends bad shares to `k` out of `n-1` honest validators, the analysis above shows confirmation always succeeds under honest majority. The honest-majority bound is tight: if `floor(n/2) + 1` or more validators are compromised, they can confirm a ceremony with only attacker contributions and recover `ea_sk`.

**Implementation:**
- Proto: `skipped_contributors` field on `MsgAckExecutiveAuthorityKey` and `AckEntry`.
- `app/prepare_proposal_ceremony.go`: `ackDKGRound` skips bad contributors, returns skip set; `types.ComputeAckBinding` produces the binding hash.
- `x/vote/keeper/msg_server_ceremony.go`: `AckExecutiveAuthorityKey` validates skip set, stores in `AckEntry`, gates fast path.
- `x/vote/keeper/keeper_ceremony.go`: `ComputeCanonicalSkipSet`, `FilterCompatibleAcks`, `RecomputeCommitmentsExcluding`, `StripRoundToCompatible`.
- `x/vote/module.go`: EndBlocker DEALT timeout uses majority-vote confirmation logic.
- `app/process_proposal.go`: `validateInjectedAck` validates skip set structural integrity.

## Known Limitations

The following limitations are inherent to the current design. None compromise safety under honest majority, but they affect liveness or introduce operational constraints that are worth understanding.

### Coordinated two-validator liveness attack

Two colluding validators can stall the ceremony indefinitely by doing complementary selective share poisoning. Attacker M1 sends bad shares to one subset of honest validators; M2 sends bad shares to the complementary subset. Neither attacker reaches strict majority in the canonical skip set, so the canonical set is empty. The only compatible acks are the two attackers' — far below quorum. The ceremony resets every cycle.

This requires only 2 out of n validators (well below the `floor(n/2) + 1` honest-majority threshold) and repeats on every ceremony cycle without on-chain penalty.

**Detection:** all honest validators log the offending contributor address on Feldman verification failure. The two offenders are identifiable from any honest node's logs.

**Current mitigation:** manual governance action (jail the attacker, exclude from future ceremonies). A future protocol extension could add on-chain Feldman re-verification proofs to enable automated slashing.

### Single malicious validator forces timeout path

The fast-path confirmation (all validators acked, immediate DEALT → CONFIRMED) requires every ack's skip set to be empty. A single malicious validator can inject a structurally valid ack with a non-empty skip set — `ValidateSkippedContributors` checks structural properties (sorted, no duplicates, valid contributors) but does not verify that the acker actually failed Feldman verification for the claimed contributors.

This forces every ceremony to wait for the full `CeremonyPhaseTimeout` (30 minutes) even when all shares are valid. It is a latency degradation, not a safety or liveness issue.

### Skip claims are not verifiable on-chain

Skip sets are honesty-assumed signals. A malicious proposer who received perfectly valid shares can still claim to skip any contributor (up to `n - threshold`). The majority-vote mechanism mitigates this: a lone false accuser gets outvoted, and their incompatible ack causes them to be stripped from the round. However, there is no cryptographic proof that the reported skip was justified.

Adding verifiable dispute proofs (e.g., on-chain Feldman re-verification via challenge-response) would make skip claims provable but adds significant protocol complexity. The current design is safe under honest majority because false accusers are self-penalizing.

### Threshold is not recomputed after stripping

After `StripRoundToCompatible` removes incompatible validators, `round.Threshold` remains the original `ceil(n/2)`. If many validators are stripped, the effective tally fault tolerance (`remaining - threshold`) can be very small or zero.

Example: n=9, threshold=5, 4 validators stripped → 5 remain. All 5 must provide partial decryptions during tally. If even one goes offline, the tally times out.

The strict `>` confirmation quorum guarantees at least `ceil(n/2) + 1` compatible validators when confirmation succeeds (providing at least one tally-phase buffer), but heavily stripped rounds are fragile. This is an acceptable tradeoff for current validator set sizes (n ≤ 9).

### Ack binding hash is not a cryptographic signature

The proto field `ack_signature` is a misnomer. `ComputeAckBinding` produces a deterministic SHA-256 hash over entirely public inputs (`ea_pk`, validator address, skip set) with no secret-key material. It prevents post-hoc modification of the ack's fields but provides no authentication — anyone who knows the public inputs can compute a valid binding.

Authentication relies entirely on CometBFT's proposer enforcement (`ValidateProposerIsCreator`), which ensures only the current block proposer can inject ack messages. The binding hash is defense-in-depth (a programmatic cross-check between builder and verifier), not an independent security boundary.

The field should be renamed to `ack_binding` in a future proto-breaking change window to avoid misleading future contributors.
