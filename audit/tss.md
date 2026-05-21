# Audit Checklist — EA Key Ceremony (Joint-Feldman DKG + Threshold ElGamal)

Construction-specific audit checklist for the election-authority key ceremony:
no-dealer Joint-Feldman DKG over **Pallas** (Orchard `spend_auth_g`), threshold
**ElGamal decryption** (not signing), homomorphic vote tally, DLEQ-proven partial
decryptions, BSGS recovery, ECIES share transport, and CometBFT
`PrepareProposal` / `FinalizeBlock` / `EndBlocker` integration.

Function/handler names below mirror the design doc (`ContributeDKG`,
`VerifyFeldmanShare`, `CombinePartials`, `EvalCommitmentPolynomial`,
`GetPartialDecryptionsForRound`, etc.) so items map directly onto the code.

**Governing principle:** every defense against a malicious *validator* is invisible
under all-honest testing. For each proof and validation confirm (a) it is called,
(b) failure aborts or routes to the dispute/timeout path, and (c) it is bound to the
correct statement, round, and party. Green CI proves nothing about Byzantine safety.

Severity tags reflect the prior review: **[P0]** design-level, change-now;
**[P1]** correctness/liveness; **[P2]** hardening/hygiene.

---

## Audit pass results — DKG/TSS focus

Date: 2026-05-20. Scope: source review of the per-round Joint-Feldman DKG,
ECIES share transport, Feldman ack path, threshold partial decryption, DLEQ
verification, and CometBFT proposal integration.

### Findings

#### TSS-01 — [P0] Even-sized validator sets allow exactly half the validators to decrypt

`ThresholdForN` implements `t = ceil(n/2)` for `n >= 2`, not `floor(n/2)+1`.
That means even-sized validator sets have `t = n/2`; for example `n=4 => t=2`
and `n=6 => t=3`. Any threshold-sized coalition can reconstruct
`ea_sk * C1` for every accumulator, so a half-sized coalition can decrypt the
round's aggregate tallies and can decrypt any individual ballot ciphertext it
observed before aggregation.

This is explicitly consistent with `docs/tss-ceremony.md`, but it is a privacy
policy decision, not just a liveness formula. If the intended threshold-security
model is "strict honest majority" or "fewer than half cannot decrypt", the
implementation should use `t = floor(n/2)+1` for even `n`, or restrict ceremony
sizes to odd `n`. If the current formula is intentional, document the exact
privacy bound in operator-facing material and avoid describing the threshold as
`t > n/2`.

Evidence:
- `x/vote/keeper/keeper_ceremony.go`: `ThresholdForN` returns `(n+1)/2` with a
  minimum of 2 for `n >= 2`.
- `docs/tss-ceremony.md`: the threshold table documents `n=4 => t=2` and
  `n=6 => t=3`.

#### TSS-02 — [P1] Production can launch with one ceremony validator by default

The chain defaults `min_ceremony_validators` to `1` and treats an unset or zero
genesis value as `1`. With `n=1`, `ThresholdForN(1)=1`, so the single validator
holds the full Shamir share and can decrypt without cooperation. The docs warn
that this is local-testing-only, but the code does not gate it by chain id,
environment, or an explicit unsafe flag.

Remediation: make production genesis validation reject
`min_ceremony_validators < 2` or `< 3` unless an explicit dev/test mode is set.
At minimum, emit a startup warning that cannot be missed when singleton
ceremony mode is active.

Evidence:
- `x/vote/keeper/keeper_min_validators.go`: default is `1`.
- `x/vote/keeper/genesis.go`: `0` imports as `1`.
- `x/vote/module.go`: default genesis sets `MinCeremonyValidators: 1`.

#### TSS-03 — [P1] No on-chain proof that ECIES payloads encrypt Feldman-valid shares

`ContributeDKG` validates commitment and envelope shape, but the chain cannot
verify that each ciphertext decrypts to the share committed by the contributor's
Feldman vector. Detection is private: the recipient decrypts in
`ackDKGRound`, verifies `VerifyFeldmanShare`, and skips ack on failure. A single
malicious contributor can therefore force the DEALT phase to time out by sending
bad shares, and the protocol needs operator intervention or a later retry.

The design doc already documents the corrupted-share DoS as deferred. From an
audit perspective this remains the largest DKG liveness gap.

Remediation: add per-recipient verifiable-encryption correctness proofs to
`MsgContributeDKG`, or implement a canonical on-chain skip-set protocol that
recomputes commitments and `ea_pk` deterministically.

Evidence:
- `x/vote/keeper/msg_server_ceremony.go`: `ContributeDKG` validates point and
  count shape only.
- `app/prepare_proposal_ceremony.go`: `ackDKGRound` performs the first
  plaintext-share Feldman verification and aborts ack on failure.
- `docs/tss-ceremony.md`: Issue 2 documents the deferred corrupted-share DoS.

#### TSS-03b — [P1] Threshold-sized ack survivor set can time out at tally

The corrupted-share vector has a more severe liveness variant: a malicious
contributor can send valid-looking but wrong encrypted shares to selected
victims, causing those victims to skip ack. If the DEALT timeout path activates
with exactly `t` ackers and the attacker remains in that surviving set, the
attacker can later withhold partial decryptions. Honest validators then have
only `t-1` partials, and the round finalizes with `TallyTimedOut=true`.

Initial mitigation: require the bounded-subset DKG contribution quorum and the
DEALT ack timeout quorum to exceed `t` by a margin:

```
f(n) = floor((n - t(n)) / 2)
x(n) = f(n) // tolerated later partial-decryption withholders
y(n) = f(n) // tolerated non-contributors / non-ackers before activation
required(n) = max(t(n) + x(n), n - y(n))
```

The `n - y(n)` side bounds pre-activation missing validators, while
`t(n) + x(n)` preserves tally liveness if validators withhold after activation.
Because integer rounding means the two sides are not always equal, the
implementation takes the stricter side with `max(...)`. For `n=5`, this gives
`t=3`, `required=4`, so one later withholding survivor still leaves three
partial decryptions. This pairs with the contribution-timeout design in
[vote-sdk#323](https://github.com/valargroup/vote-sdk/issues/323).

This is not a cryptographic fix for malformed encrypted shares; verifiable
encryption or a complaint/justification protocol remains the direct fix.

#### TSS-04 — [P1] `ProcessProposal` accepts malformed DKG contributions that FinalizeBlock later rejects

`validateInjectedDKGContribution` only checks round status, ceremony status,
creator membership, duplicate contribution, and proposer identity. It does not
mirror `ContributeDKG`'s checks for commitment count, payload count, payload
coverage, duplicate recipients, self-recipient rejection, Pallas commitment
validity, or ECIES ephemeral point validity.

A malicious proposer can propose an injected DKG contribution that passes
`ProcessProposal` but fails in `FinalizeBlock`. That burns proposer slots until
REGISTERING timeout and jailing, rather than rejecting the block immediately.
This is a soft liveness DoS and a proposal-validation symmetry bug.

Remediation: extract a shared `ValidateDKGContributionShape(round, msg)` helper
and call it from both `ProcessProposal` and `ContributeDKG`.

Evidence:
- `app/process_proposal.go`: `validateInjectedDKGContribution` stops after
  proposer identity validation.
- `x/vote/keeper/msg_server_ceremony.go`: `ContributeDKG` has the missing shape
  and point checks.

#### TSS-05 — [P1] ECIES share encryption lacks AEAD context binding

The ECIES layer derives `k = SHA256(E_compressed || S.x)` and calls
`ChaCha20-Poly1305.Seal(..., aad=nil)` with a zero nonce. Fresh ephemeral
scalars make nonce reuse unlikely in the honest path, but the ciphertext is not
authenticated to `round_id`, sender, recipient, or the contributor commitment
digest. Envelope replay or recipient/context substitution is therefore detected
only indirectly by later Feldman verification, not by the AEAD boundary.

Remediation: version the envelope KDF/AEAD transcript and bind
`round_id || sender_valoper || recipient_valoper || H(feldman_commitments)` as
AEAD associated data. Add a KDF domain tag such as `svote-ecies-v1`.

Evidence:
- `crypto/ecies/ecies.go`: `deriveKey` hashes only `E` and `S.x`.
- `crypto/ecies/ecies.go`: `Seal` and `Open` pass `nil` associated data.
- `app/prepare_proposal_ceremony.go`: callers pass only plaintext share bytes;
  no round/sender/recipient context enters ECIES.

#### TSS-06 — [P2] Partial-decryption DLEQ transcript relies on implicit context binding

`VerifyPartialDecryptDLEQ` proves `log_G(VK_i) == log_C1(D_i)` and checks both
Chaum-Pedersen legs against the same response scalar. The transcript includes
`G`, `VK_i`, `C1`, `D_i`, `R1`, and `R2`, but it does not explicitly include
`round_id`, `validator_index`, `proposal_id`, or `vote_decision`.

This is likely sound for the current implementation because `VK_i` is derived
from round-specific combined Feldman commitments and `C1` is the accumulator's
randomized ElGamal component. Still, explicit context binding would prevent
future refactors from turning implicit binding into replay surface.

Remediation: version the proof transcript and include
`round_id || validator_index || proposal_id || vote_decision` in the
Fiat-Shamir challenge.

Evidence:
- `crypto/elgamal/dleq.go`: `pdDleqChallenge` hashes only group elements plus
  the domain tag.
- `x/vote/keeper/msg_server_tally_decrypt.go`: `SubmitPartialDecryption`
  derives `VK_i` from on-chain commitments and verifies the DLEQ before storing.

#### TSS-07 — [P2] Contribution PoK is absent, leaving the rogue-key/bias argument non-local

`MsgContributeDKG` has Feldman commitments and encrypted shares, but no Schnorr
proof of knowledge for the committed constant term or full polynomial
coefficient vector. Feldman ack verification prevents an attacker from using
unknown-discrete-log commitments while still producing valid shares, so this is
not an immediate key-extraction finding under the current threat model. It does,
however, force reviewers to rely on the Joint-Feldman bias argument and makes
adaptive-key concerns harder to dismiss.

Remediation: add a Schnorr PoK over at least `C_{i,0}` and preferably the full
commitment vector, bound to `round_id`, contributor address, and commitment
bytes.

Evidence:
- `proto/svote/v1/tx.proto`: `MsgContributeDKG` has no proof field.
- `x/vote/keeper/msg_server_ceremony.go`: contribution validation checks shape
  and points, not knowledge of coefficient openings.

### Positive confirmations

- Feldman share verification is called for each inbound contributor share, and
  the summed combined share is re-verified before the ack share is written.
- Partial decryptions are DLEQ-verified on-chain before storage; `SubmitTally`
  recomputes the Lagrange combination from stored partials and checks
  `C2 - combined == totalValue * G`.
- Shamir indices are assigned 1-based at round creation and preserved after
  non-acker stripping; `SubmitPartialDecryption` checks the submitted index
  against the stored `ShamirIndex`.
- Pallas public keys, Feldman commitments, and ECIES ephemeral keys are
  deserialized through non-identity point validation before use.

### Recommended remediation order

1. Decide and enforce the production privacy threshold (`ceil(n/2)` vs.
   strict-majority `floor(n/2)+1`) and gate singleton ceremonies.
2. Add verifiable-encryption proofs or a deterministic skip-set protocol for
   corrupted-share liveness.
3. Mirror DKG contribution shape validation in `ProcessProposal`.
4. Bind ECIES envelopes to round/sender/recipient context and add a KDF domain
   tag.
5. Version and explicitly context-bind partial-decryption DLEQ transcripts.
6. Add Schnorr PoK for contribution commitments.

## 1. Parameters, genesis & threshold

- [ ] **[P0]** `t = ceil(n/2)` is the **privacy** threshold, not just liveness: any `t` validators can decrypt anything under `ea_pk`, including individual ballots they observed pre-aggregation. Confirm `t` was chosen deliberately against a stated ballot-secrecy goal, not from the formula. Document the privacy-vs-liveness tradeoff explicitly.
- [ ] **[P1]** `t >= 1` always, and `t <= n`; reject `t = 0`. Confirm the `n=1 ⇒ t=1` "full key in one share" path is unreachable on mainnet (gated by chain-id / explicit dev flag).
- [ ] **[P1]** Ceremony timeout activation uses a quorum separate from `t`: `required(n) = max(t + x, n - y)` with `x = y = floor((n - t) / 2)`. Confirm DKG contribution timeout and DEALT ack timeout both use this value, not a half-ack quorum / `nAcks >= t`.
- [ ] **[P1]** `min_ceremony_validators` default of `1` cannot silently launch a real round with no threshold security. Enforce a higher floor for non-test chains.
- [ ] **[P2]** Curve parameters (Pallas) are fixed in code/genesis, not negotiable at runtime.
- [ ] **[P2]** Generator reuse: `spend_auth_g` is shared between Orchard spend-auth and this ElGamal/Feldman use. Confirm no cross-protocol interaction (a signature or proof in one context cannot be replayed as a commitment/opening in the other); domain-separate where the same generator spans protocols.

## 2. Field & group discipline (Pallas)

- [ ] **[P1]** All secret/coefficient/share/Lagrange arithmetic is mod the **scalar field** (Pallas group order `Fq`), never the base field `Fp`. Grep for any reduction against the wrong modulus.
- [ ] **[P1]** Every incoming point (commitments `C_{i,j}`, `E`, `pk_i`, `C1`, `C2`, `D_i`) is validated **on-curve and in the prime-order subgroup** before use; reject identity/torsion points where the protocol assumes a generator-order point.
- [ ] **[P1]** Scalar deserialization rejects values `>= q` (non-canonical) and handles the zero scalar explicitly where it would be degenerate.
- [ ] **[P2]** Point/scalar encodings are canonical and fixed-length; compression/decompression round-trips and rejects malformed encodings.

## 3. Polynomial generation & coefficients

- [ ] **[P1]** Polynomial degree is exactly `t-1`; `coeffs` file holds exactly `t` scalars. Reject contributions whose commitment vector length `!= t`.
- [ ] **[P1]** `f_i(0) = s_i` (constant term is the secret contribution); confirm coefficient indexing has no off-by-one (`C_{i,0}` is the term that feeds `ea_pk`).
- [ ] **[P1]** `s_i` and `a_1..a_{t-1}` are sampled from a CSPRNG, uniformly over `Fq`, fresh per round. No seeding from round_id, height, or other attacker-influenceable input.
- [ ] **[P2]** Coefficients and per-recipient shares are zeroized from memory after envelopes are built (the doc's step 7) — confirm zeroization is not elided by the compiler/optimizer and covers all copies.

## 4. Feldman commitments

- [ ] **[P1]** `C_{i,j} = a_j * G` computed against the correct generator `G = spend_auth_g`.
- [ ] **[P1]** `VerifyFeldmanShare`: `share_i * G == Σ_j C_j * i^j` is actually evaluated, and a failure routes to "skip ack" (today's behavior) — confirm it never silently accepts.
- [ ] **[P1]** The exponent `i^j` uses the validator's canonical 1-based `shamirIndex`, identical to the index used everywhere else (§6, §9, §11).
- [ ] **[P2]** `ea_pk` derived as `C_0` (constant-term commitment) matches `sum_i(C_{i,0})` computed independently; confirm the two derivations agree (the doc claims `ea_pk` need not be published separately — verify that claim holds in code).

## 5. ECIES share transport

- [ ] **[P1]** **AEAD associated data binds context.** ChaCha20-Poly1305 currently authenticates no context, so envelopes are replayable across rounds/recipients. Confirm AAD includes `round_id || sender_index || recipient_index` (ideally + contributor commitment digest). If absent → finding.
- [ ] **[P1]** **Ephemeral `e` freshness.** Fixed `nonce=0` is only safe while `e` (hence `k`) never repeats. Confirm `e` is sampled fresh per envelope from a CSPRNG and there is no code path that reuses it to the same recipient. Prefer a random/transcript-derived nonce so AEAD safety doesn't rest solely on RNG freshness.
- [ ] **[P2]** **KDF domain separation.** `k = SHA256(E_compressed || S.x)` lacks a domain tag; add one (you already version `svote-pd-dleq-v1` — apply the same discipline).
- [ ] **[P1]** Recipient validates `E` is a valid prime-order point before computing `S = sk_i * E`; rejects identity/small-order `E` (prevents invalid-curve / small-subgroup leakage of `sk_i`).
- [ ] **[P2]** `S.x`-only KDF: the ±S x-coordinate ambiguity is functionally fine (both parties derive the same `S`) but confirm no logic depends on the full point elsewhere.

## 6. Contribution handler (`ContributeDKG`, on-chain / FinalizeBlock)

- [ ] **[P1]** Validates proposer identity == contributor, and contributor ∈ ceremony validator snapshot.
- [ ] **[P1]** Rejects duplicate contribution per validator per round.
- [ ] **[P1]** Validates commitment count == `t` and ECIES envelope count == `n-1` (one per other validator, none addressed to self, none duplicated, exactly one per recipient).
- [ ] **[P1]** Validates every commitment point and every envelope's `E` point (§2) before append.
- [ ] **[P1]** `CombineCommitments` is a point-wise sum over the **agreed validator set** only; confirm it cannot be triggered before all `n` valid contributions, and that the `n`-th-contribution transition is atomic (no partial DEALT state on a failed sum).
- [ ] **[P1]** On contribution timeout, bounded-subset finalization only combines commitments from contributors when `len(contributors) >= required(n)`; otherwise the round fails. Non-contributors are stripped before DEALT and their entropy is not included.
- [ ] **[P1]** `threshold`, `feldman_commitments`, `ea_pk` are all set in the same atomic DEALT transition and are mutually consistent.
- [ ] **[P2]** A contribution arriving after DEALT (late proposer) is rejected, not appended.

## 7. Verifiable-share gap & corrupted-share DoS (design doc Issue 2)

- [ ] **[P0]** There is **no on-chain proof** that an ECIES envelope decrypts to the value its Feldman commitment claims — detection is private (recipient decrypts, fails `VerifyFeldmanShare`, skips ack). Confirm the team accepts the resulting single-validator stall risk, or scope the fix.
- [ ] **[P1]** Recommended fix: NIZK encryption-correctness proof at contribution time ("ct for `j` encrypts `f_i(j)` consistent with `C_{i,*}`") so the chain rejects bad contributions immediately and attribution is on-chain — this deletes the deferred majority-vote skip-set machinery entirely.
- [ ] **[P1]** If the majority-vote skip-set path is implemented instead: verify all honest validators converge on the **same** canonical skip set (no view-split), recomputed `feldman_commitments`/`ea_pk` are identical across nodes, and the analysis holds for ≥2 colluding selective-targeting validators, not just one.
- [ ] **[P2]** Current mitigation (log offender → chain-upgrade jail) is documented and the offender address is logged identically by all honest validators (deterministic attribution).

## 8. Bias / single-phase argument

- [ ] **[P1]** The "last contributor biases `ea_pk` but it's harmless" argument: confirm `ea_pk` is used **only** for ElGamal vote encryption and nowhere that requires a uniform key (no CRS, no Fiat-Shamir base, no proof-system parameter). If any such second use exists, the Gennaro-2007 carve-out does not apply.
- [ ] **[P2]** Add a Schnorr **proof of possession** of `s_i` (knowledge of `C_{i,0}`'s discrete log) at contribution time — cheap, standard, closes the rogue-key surface explicitly so reviewers needn't re-derive the bias argument.

## 9. Ack handler & combined share

- [ ] **[P1]** Loads own coeffs, computes `own_partial = EvalPolynomial(coeffs, shamirIndex)` with the **same** `shamirIndex` used by others when they evaluated shares *for* this validator.
- [ ] **[P1]** Decrypts each inbound envelope, runs `VerifyFeldmanShare` against **that contributor's individual** commitments (not the combined ones) before summing.
- [ ] **[P1]** `combined_share = own_partial + Σ received_shares` is re-verified against `round.FeldmanCommitments` (combined) before being written — confirm this second check exists and gates the disk write.
- [ ] **[P1]** Ack is only injected after successful combined-share verification; a verification failure skips ack (no partial/garbage share persisted).

## 10. On-disk key-material lifecycle

- [ ] **[P1]** `coeffs.<round_id>` and `share.<round_id>` written mode `0600`; confirm parent dir perms and umask don't widen them.
- [ ] **[P1]** Crash window: if a node dies after writing `coeffs` but before writing `share` (and deleting `coeffs`), confirm recovery recomputes the combined share rather than stalling, and that a half-written file is detected (length/format check) not consumed.
- [ ] **[P1]** `coeffs` is deleted only **after** `share` is durably written; `share` is deleted only after tally finalized. Verify ordering and fsync/durability.
- [ ] **[P2]** At-rest threat model is documented: `0600` does not protect against disk theft, backups, or snapshots. Consider encrypted-at-rest or HSM-backed storage for `coeffs`/`share`.
- [ ] **[P2]** No secret material (coeffs, shares, `s_i`, ephemeral `e`) appears in logs, panics, debug output, or telemetry.

## 11. Index discipline (Shamir / Lagrange)

- [ ] **[P1]** `validator_index` is 1-based everywhere (never 0 — `f(0)` is the secret) and globally unique within a ceremony; reject collisions and zero.
- [ ] **[P1]** The same index value is used consistently across: share evaluation, `EvalPolynomial`, `EvalCommitmentPolynomial`, on-chain `validator_index`, and Lagrange coefficient computation. A mismatch silently yields wrong reconstruction, not an error — add an assertion.
- [ ] **[P1]** `CombinePartials` Lagrange coefficients are computed mod `Fq` over **exactly** the set of present indices, recomputed per accumulator from the actual submitter set (not a fixed assumption).

## 12. Tally — partial decryptions (`MsgSubmitPartialDecryption`)

- [ ] **[P1]** Handler validates round status == TALLYING, `validator_index` 1-based and == creator, and rejects duplicate submissions per validator per round.
- [ ] **[P1]** Each `D_i = share_i * C1` is accompanied by a Chaum-Pedersen DLEQ and the chain **verifies** it (`VerifyPartialDecryptDLEQ`) against `VK_i = EvalCommitmentPolynomial(round.FeldmanCommitments, shamirIndex)` **before** storing. Confirm verification failure rejects the message.
- [ ] **[P1]** `VK_i` is derived from on-chain combined commitments, not from validator-supplied data.
- [ ] **[P2]** Each entry validates 32-byte `partial_decrypt`, valid `proposal_id`, valid `vote_decision`, and a valid accumulator reference.

## 13. DLEQ proofs (`crypto/elgamal/dleq.go`)

- [ ] **[P1]** Chaum-Pedersen proves `log_G(VK_i) == log_{C1}(D_i)`; confirm the verifier checks **both** legs against the **same** response scalar (a proof that only constrains one leg is forgeable).
- [ ] **[P1]** Fiat-Shamir challenge hashes the full statement: domain tag `svote-pd-dleq-v1`, `G`, `C1`, `VK_i`, `D_i`, both commitment points, **and** round/proposal/validator context — so a proof cannot be replayed across accumulators, rounds, or validators (Frozen-Heart class).
- [ ] **[P1]** Challenge is reduced mod `Fq`; response/verification arithmetic is in the scalar field.
- [ ] **[P2]** Transcript encoding is canonical and unambiguous (length-prefixed or fixed-width), and the prover cannot grind the statement after seeing the challenge.

## 14. Tally — combine & finalize (`MsgSubmitTally`)

- [ ] **[P1]** Finalization gate `CountPartialDecryptionValidators >= threshold` counts **distinct** valid submitters (no double-count from resubmission).
- [ ] **[P1]** `skC1 = CombinePartials(partials, threshold)` re-runs Lagrange from **stored** partials; the on-chain handler independently recomputes and checks `C2 - combined == totalValue * G` (does not trust the submitter's claimed total).
- [ ] **[P1]** **BSGS range bound:** confirm a hard cap on per-accumulator total matched to the BSGS table size, with graceful failure (not hang/panic) if `v` exceeds range — otherwise an inflated accumulator is a tally DoS.
- [ ] **[P1]** `TallyResult` is stored and round → FINALIZED atomically; partial decryptions for the round are retained/cleaned per spec (and `share` file deletion is triggered only post-finalize).
- [ ] **[P2]** Behavior when fewer than `threshold` validators ever submit (tally cannot complete): confirm a defined timeout/failure, not an indefinite TALLYING hang.

## 15. KV storage layout

- [ ] **[P1]** Partial-decryption key `0x12 || round_id || validator_index || proposal_id || decision` uses fixed-width big-endian fields (as documented); confirm no encoding ambiguity lets two distinct logical keys collide.
- [ ] **[P1]** Prefix scans (`0x12 || round_id`, `0x12 || round_id || validator_index`) cannot be confused by a `round_id` that contains the prefix byte pattern; `round_id` is fixed 32 bytes so verify length enforcement.
- [ ] **[P2]** Stored protobuf entries are validated on read (not just write) before use in combination.

## 16. CometBFT integration

- [ ] **[P1]** `PrepareProposal` logic is deterministic w.r.t. on-chain state and does not write state (auto-contribute/auto-ack inject messages; state changes happen in `FinalizeBlock`). Confirm no disk write in a consensus-critical path that must be replay-safe.
- [ ] **[P1]** All ceremony state transitions (DEALT, FINALIZED, jailing) occur in `FinalizeBlock`/`EndBlocker`, are deterministic, and produce identical results on every honest node (no wall-clock, map-iteration-order, or floating-point nondeterminism).
- [ ] **[P1]** Re-proposal / re-round: if a proposer's block is not finalized, confirm auto-contribute does not regenerate a *different* polynomial (which would change commitments) — coefficients must be stable across re-proposals for the same round (the doc flags this as the reason vote extensions were avoided; verify the chosen design is actually idempotent).
- [ ] **[P2]** A malicious proposer cannot inject another validator's contribution/ack (identity check in §6/§9 covers this — confirm at the proposal-injection layer too).

## 17. State machine

- [ ] **[P1]** Transitions are one-directional and guarded: REGISTERING → DEALT only on `n` valid contributions; DEALT → ACTIVE/CONFIRMED only on `n` (or skip-set-adjusted) acks; → TALLYING → FINALIZED. No path skips a required phase.
- [ ] **[P1]** Each phase has a timeout (`DefaultContributionTimeout` for REGISTERING, DEALT timeout for acks, and — per §14 — a tally timeout) so no phase hangs forever.
- [ ] **[P2]** Replaying a message from a prior round/`ssid` into a new round is rejected (round_id binding everywhere).

## 18. Liveness, timeout & slashing

- [ ] **[P1]** REGISTERING timeout jails non-contributors and marks the round `SESSION_STATUS_CEREMONY_FAILED`; confirm jailing targets only validators in the snapshot who genuinely failed to contribute (no jailing of validators who contributed but whose block didn't land).
- [ ] **[P1]** Retry path derives a **fresh** `vote_round_id` from creation height and re-snapshots currently-eligible validators (no reuse of a stale snapshot that includes a jailed/offline validator).
- [ ] **[P1]** DEALT timeout strips non-ackers only when `len(ackers) >= required(n)`, preserving enough slack that one surviving withholder cannot force `TallyTimedOut=true` for an otherwise valid round.
- [ ] **[P2]** Slashing economics: confirm the penalty for a corrupted-share griefing validator (§7) actually exceeds the griefing benefit, or that the manual-jail mitigation is operationally realistic.

## 19. Key rotation (`MsgRotatePallasKey`)

- [ ] **[P1]** Rotation is blocked during in-flight ceremonies; confirm there is no race where a rotation lands between snapshot and contribution (which would orphan envelopes encrypted to the old key).
- [ ] **[P2]** Past ECIES ciphertexts in completed `DkgContributions` remain decryptable only to the (now-rotated-out) old key — confirm no replay of old envelopes into a new round.

## 20. Cross-cutting crypto hygiene

- [ ] **[P1]** Every proof/verification return value (`VerifyFeldmanShare`, `VerifyPartialDecryptDLEQ`, point validations) is consumed and gates control flow — grep for computed-and-discarded results.
- [ ] **[P1]** Constant-time handling of secret scalars (shares, `s_i`, coefficients); no secret-dependent branching or table indexing in share/partial-decrypt math.
- [ ] **[P2]** Dependencies (Pallas/Pasta arith, ChaCha20-Poly1305, BSGS) pinned and free of known advisories; check the curve and AEAD libs against current disclosures.

---

## Adversarial test scenarios (must exist in the suite)

These are the tests that all-honest CI will never cover:

1. **Out-of-range / wrong-field scalar** in a contribution → rejected (§2, §3).
2. **Bad share to a subset of validators** (selective targeting) → ack-skip path behaves correctly; with the verifiable-encryption fix, contribution is rejected on-chain (§7).
3. **Commitment/payload count mismatch** (too few/many commitments or envelopes, envelope to self, duplicate recipient) → rejected (§6).
4. **Forged DLEQ** (valid against one leg only, or replayed from another accumulator/round/validator) → rejected (§13).
5. **Duplicate submission** of contribution, ack, or partial decryption → rejected (§6, §9, §12).
6. **Replayed ECIES envelope** from a prior round/recipient → rejected once AAD binding is added (§5).
7. **Inflated accumulator** beyond BSGS range → graceful tally failure, no hang/panic (§14).
8. **Index collision / zero index** across two validators → rejected, not silently mis-reconstructed (§11).
9. **Late contribution after DEALT** / **rotation mid-ceremony** → rejected (§6, §19).
10. **Last-contributor `ea_pk` bias** → ceremony still completes and tally still verifies (confirms the bias argument operationally) (§8).

## Time-boxed triage order

1. §20 first item — trace every verification return to an abort.
2. §1 / §7 / §18 — threshold choice, quorum hardening, and the verifiable-share gap (the design decisions cheapest to change now).
3. §13 — DLEQ soundness and transcript binding.
4. §5 — ECIES context binding and nonce fragility.
5. §11 — index discipline end-to-end (silent-corruption class).

Failures in 1–4 are typically exploitable or liveness-breaking; §11 produces wrong tallies without erroring, which is arguably worse than a crash.