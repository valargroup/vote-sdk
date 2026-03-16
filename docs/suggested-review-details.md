# Audit Verification Details

Detailed verification checklists for the [Audit Scope](suggested-review-scope.md). Each point is tagged:

- `[CRITICAL]` — violation breaks vote privacy or consensus safety
- `[HIGH]` — violation enables denial-of-service or liveness failure
- `[MEDIUM]` — defense-in-depth concern, exploitable only with other bugs

---

## Area 1: Cryptographic Primitives

[Back to audit map](suggested-review-scope.md#area-1-cryptographic-primitives)

**Files:** All files in `crypto/elgamal/`, `crypto/shamir/`, `crypto/ecies/`, `crypto/votetree/`

### Modules

| Module | Purpose | Key Operations |
|---|---|---|
| `crypto/elgamal/` | ElGamal encryption on Pallas curve | `Encrypt`, `Decrypt`, `HomomorphicAdd`, BSGS discrete log recovery |
| `crypto/elgamal/dleq.go` | DLEQ proof construction and verification | `GenerateDLEQProof`, `VerifyDLEQProof` — proves decryption correctness |
| `crypto/elgamal/serialize.go` | Point/ciphertext marshalling | `DecompressPallasPoint`, `UnmarshalPublicKey`, `UnmarshalCiphertext`, identity sentinels |
| `crypto/shamir/` | Shamir secret sharing and reconstruction | `Split`, `Combine`, `LagrangeCoefficient` — threshold key distribution |
| `crypto/shamir/feldman.go` | Feldman VSS commitments | `GenerateCommitments`, `VerifyShare` — verifiable share distribution |
| `crypto/shamir/partial_decrypt.go` | Threshold partial decryption | `PartialDecrypt`, `CombinePartials` — Lagrange interpolation in the exponent |
| `crypto/ecies/` | ECIES hybrid encryption | ChaCha20-Poly1305 encrypted share transport to validators |
| `crypto/votetree/` | Poseidon Merkle tree via FFI | CGo/FFI bridge to Rust Poseidon implementation |

### ElGamal Correctness

- `[CRITICAL]` `Encrypt(pk, v)` produces `(r*G, v*G + r*pk)` with `r` sampled uniformly — verify no bias in scalar sampling and that the generator `G` is the canonical Pallas generator
- `[CRITICAL]` `HomomorphicAdd` correctly computes component-wise point addition — verify associativity and that identity ciphertexts are handled correctly
- `[CRITICAL]` `Decrypt(sk, ct)` recovers `v*G` via `C2 - sk*C1` — verify this matches the encryption formula
- `[HIGH]` BSGS discrete log recovery: verify the baby-step giant-step table is correctly constructed and that the search bound is sufficient for the vote value domain
- `[CRITICAL]` DLEQ proof: verify Fiat-Shamir challenge is binding to all public inputs (`G`, `pk`, `C1`, `C2 - v*G`) and that the domain tag `"svote-dleq-v1"` is unambiguous

### Shamir and Threshold Operations

- `[CRITICAL]` `Split(secret, t, n)` produces shares on a degree-(t-1) polynomial with `secret` as the constant term — verify polynomial evaluation is correct and that share indices start at 1 (not 0, which would leak the secret)
- `[CRITICAL]` `LagrangeCoefficient` computes the correct basis polynomial evaluation — verify no division-by-zero on duplicate indices and that the scalar field arithmetic is correct
- `[CRITICAL]` `CombinePartials` performs Lagrange interpolation in the exponent — verify the combination produces `sk * C1` when all shares are honest
- `[HIGH]` Feldman commitment verification: `share_i * G == Σ(commitment_j * i^j)` — verify the polynomial evaluation matches

### Serialization Edge Cases

- `[CRITICAL]` All point decompression rejects non-canonical encodings (x >= p)
- `[CRITICAL]` Identity point handling: rejected for public keys, accepted for partial decryptions — verify both paths
- `[HIGH]` No consensus-critical panics reachable from attacker-controlled input (check `intToScalar`, generator decompression, `scalarFromUint64`, `IdentityCiphertextBytes`)
- `[MEDIUM]` CGo/FFI memory safety in `crypto/votetree/kv_callbacks.go`: `cgo.Handle` recovery, C.malloc/C.free ownership, iterator lifecycle

---

## Area 2: Serialization Canonicality

[Back to audit map](suggested-review-scope.md#area-2-serialization-canonicality)

**Files:** `crypto/elgamal/serialize.go`, `crypto/elgamal/dleq.go`, `crypto/shamir/partial_decrypt.go`, `x/vote/keeper/msg_server_ceremony.go`, `x/vote/keeper/msg_server_tally_decrypt.go`

### Point Deserialization

- `[CRITICAL]` `curvey.PointPallas.FromAffineCompressed()` rejects x-coordinates >= the Pallas base field prime p — a non-canonical encoding that maps to the same point would let an attacker submit a "different" `ea_pk`, `pallas_pk`, or `enc_share` that is logically identical but has different bytes, potentially bypassing deduplication or creating consensus divergence if validators deserialize differently
- `[CRITICAL]` `DecompressPallasPoint()` in `crypto/elgamal/serialize.go` treats all-zeros as the identity point — verify this sentinel does not collide with any valid compressed point encoding
- `[CRITICAL]` `UnmarshalPublicKey()` correctly rejects the identity point for public keys stored on-chain (ea_pk, pallas_pk, ephemeral_pk) — an identity public key in ElGamal causes `r * pk = O`, making C2 = v*G (plaintext leaks)
- `[HIGH]` `UnmarshalPoint()` intentionally accepts the identity for partial decryption points `D_i = share_i * C1` stored on-chain — verify that an attacker cannot submit an identity `D_i` to poison the Lagrange combination in `SubmitTally`

### Scalar Deserialization

- `[CRITICAL]` `curvey.ScalarPallas.SetBytes()` rejects values >= q (the Pallas scalar field order) — used in `VerifyDLEQProof` to deserialize the challenge `e` and response `z` during `SubmitTally` (legacy mode)
- `[CRITICAL]` Shamir share scalars used in `CombinePartials()` during tally Lagrange interpolation — a zero index or duplicate index could cause division-by-zero in `LagrangeCoefficient()` or produce incorrect reconstruction. Verify the on-chain `ValidatorIndex == ShamirIndex` check prevents this
- `[CRITICAL]` Verify there is no on-chain path where a zero `ea_sk` can be stored (via `MsgDealExecutiveAuthorityKey`), which would make all ciphertexts trivially decryptable — `UnmarshalPublicKey(ea_pk)` rejects identity, but verify that `ea_pk = 0*G = O` is the only manifestation of a zero secret key

---

## Area 3: Tally Integrity

[Back to audit map](suggested-review-scope.md#area-3-tally-integrity)

**Files:** `x/vote/keeper/keeper_tally.go`, `x/vote/keeper/msg_server_tally_decrypt.go`, `crypto/elgamal/elgamal.go`, `crypto/elgamal/dleq.go`, `crypto/shamir/partial_decrypt.go`, `crypto/shamir/shamir.go`, `x/vote/ante/validate.go`

### Homomorphic Accumulation

- `[CRITICAL]` `HomomorphicAdd()` correctly accumulates encrypted vote shares
- `[CRITICAL]` `AddToTally()` deserializes `enc_share` bytes via `UnmarshalCiphertext()` before accumulation — verify deserialized points are on the curve (enforced by `FromAffineCompressed`) and no code path allows raw bytes to reach the accumulator without point validation
- `[CRITICAL]` Verify the full on-chain validation chain for `MsgRevealShare.EncShare`: `ValidateBasic()` field-length check -> ante `verifyRevealShare()` ZKP check -> keeper `AddToTally()` point decompression. No gap should exist where malformed or unbound bytes reach the accumulator
- `[HIGH]` The accumulator is initialized to the identity ciphertext `(O, O)` and grows via `HomomorphicAdd` — verify that an attacker cannot submit `enc_share = -accumulator` to reset the tally to zero (should be prevented by ZKP binding the ciphertext to the vote, but verify the circuit enforces this)

### Threshold Lagrange Combination

- `[CRITICAL]` Lagrange interpolation in the exponent correctly recovers `sk * C1` (threshold mode)
- `[CRITICAL]` `ValidateTallyCompleteness()` prevents omitted entries
- `[HIGH]` `SubmitPartialDecryption` validates each `entry.PartialDecrypt` via `elgamal.UnmarshalPoint()` (accepts identity) — verify whether a validator submitting `D_i = O` (identity) for all accumulators would cause incorrect tally results or if `SubmitTally` verification catches this
- `[HIGH]` Step 1 stores partial decryptions on-chain without DLEQ verification against VK_i — a malicious proposer-validator can inject arbitrary points as their own partial decryption. Verify that `SubmitTally` Lagrange + value-point comparison is sufficient to detect this (final check `C2 - combined != totalValue * G` will fail, but a single malicious validator can prevent finalization entirely)

### Edge Cases

- `[HIGH]` BSGS bound implications (liveness vs safety) — verify the search bound is sufficient for the vote value domain
- `[HIGH]` Without per-submission DLEQ verification (deferred to Step 2), a malicious validator can force the tally to fail repeatedly by submitting garbage `D_i` values, with no on-chain mechanism to identify and exclude the bad actor
- `[MEDIUM]` Zero-vote accumulators and sequential processing guarantees

---

## Area 4: ZK State Machine Gating

[Back to audit map](suggested-review-scope.md#area-4-zk-state-machine-gating)

**Files:** `x/vote/ante/validate.go`, `x/vote/keeper/msg_server.go`, `x/vote/keeper/msg_server_tally_decrypt.go`, `x/vote/keeper/keeper_ceremony.go`

### Invariants

- `[CRITICAL]` Validation ordering: cheapest checks first, nullifier always checked
- `[CRITICAL]` No admin/governance/sudo bypasses for ZK state
- `[CRITICAL]` All keeper write methods are only reachable from properly-gated handlers
- `[HIGH]` `EndBlocker` performs only deterministic transitions, no unverified state writes

---

## Area 5: Fee Strategy

[Back to audit map](suggested-review-scope.md#area-5-fee-strategy)

**Files:** `app/ante.go`, `app/ante_ceremony.go`, `x/vote/ante/validate.go`

### Invariants

- `[MEDIUM]` No fee mechanism exists to bypass (intentional design)
- `[HIGH]` Spam resistance via nullifier uniqueness, one-pending-round limit, proposer-only injection, validator gating, and post-Genesis `MsgCreateValidator` blocking
- `[HIGH]` Infinite gas meter behaviour for both vote and standard Cosmos transactions
- `[MEDIUM]` Accepted tradeoffs are documented (no per-tx size limit, no gas-based resource exhaustion protection)

---

## Area 6: Alternate Tx Encoding

[Back to audit map](suggested-review-scope.md#area-6-alternate-tx-encoding)

**Files:** `api/codec.go`, `app/decode.go`, `app/process_proposal.go`, `app/ante.go`

### Invariants

- `[CRITICAL]` Tag collision prevention (0x0A exclusion for Cosmos Tx protobuf compatibility)
- `[HIGH]` Custom decoder routing logic
- `[CRITICAL]` Three-layer proposer-injection: mempool barrier, ProcessProposal validation, FinalizeBlock re-check
- `[MEDIUM]` ProcessProposal intentionally skips DLEQ verification (safety preserved via FinalizeBlock)

---

## Area 7: Threshold Ceremony

[Back to audit map](suggested-review-scope.md#area-7-threshold-ceremony)

**Files:** `app/prepare_proposal_ceremony.go`, `x/vote/keeper/msg_server_ceremony.go`, `x/vote/keeper/keeper_ceremony.go`, `x/vote/module.go` (EndBlocker), `crypto/shamir/shamir.go`, `crypto/ecies/`, `app/prepare_proposal_partial_decrypt.go`, `app/prepare_proposal.go`, `x/vote/keeper/msg_server_tally_decrypt.go`, `x/vote/keeper/keeper_tally.go`

### Key Distribution

- `[CRITICAL]` Fresh `ea_sk` generation and zeroing after use
- `[CRITICAL]` Shamir `(t, n)` splitting with `t = max(2, ceil(n/2))`
- `[HIGH]` ECIES encryption (ChaCha20-Poly1305) of shares to validator Pallas keys
- `[CRITICAL]` Share verification on ack (`share_i * G == VK_i` for threshold, `ea_sk * G == ea_pk` for legacy)
- `[HIGH]` Ack signature construction and verification
- `[MEDIUM]` Disk persistence at `0600` permissions
- `[MEDIUM]` Dealer trust model (Step 1 limitation: dealer sees full `ea_sk`)

### Liveness and Recovery

| Phase | Timeout | Offline Validator Impact | Recovery Path |
|---|---|---|---|
| **REGISTERING** | None | No deal injected until ceremony validator becomes proposer | Proposer rotation — eventually a ceremony validator proposes |
| **DEALT** (ack window) | 30 min (`DefaultDealTimeout = 1800s`) | Validators who never propose never inject `MsgAckExecutiveAuthorityKey` | EndBlocker timeout triggers half-ack evaluation |
| **TALLYING** | **None** | Validators who never propose never submit `MsgSubmitPartialDecryption` | **No recovery path** — round can remain in TALLYING indefinitely |

- `[HIGH]` `EndBlocker` timeout handling: half-ack logic, safety check (`nAcks >= threshold`), `StripNonAckersFromRound()` preserving `ShamirIndex`
- `[HIGH]` Half-ack rule correctness
- `[HIGH]` Validator exclusion via jailing
- `[HIGH]` Risk of ceremony getting stuck
- `[CRITICAL]` Risk of tally becoming non-decryptable / unrecoverable
- `[HIGH]` Threshold tolerance: how it relates to BFT and tally TSS thresholds
- `[MEDIUM]` Dealer trust: current code deletes private key after distributing shares (not enforced by protocol)
- `[HIGH]` Offline validator scenarios and recovery paths

---

## Area 8: Message Authorization

[Back to audit map](suggested-review-scope.md#area-8-message-authorization)

**Files:** `app/ante.go`, `app/ante_ceremony.go`, `x/vote/ante/validate.go`, `x/vote/keeper/msg_server.go`, `x/vote/keeper/keeper_vote_manager.go`, `x/vote/types/codec.go`, `x/vote/types/sighash.go`, `x/vote/keeper/msg_server_ceremony.go`, `app/prepare_proposal.go`, `app/prepare_proposal_ceremony.go`, `app/prepare_proposal_partial_decrypt.go`, `app/process_proposal.go`, `x/vote/keeper/keeper_ceremony.go`, `app/app.go`, `x/vote/module.go`

### Complete Auth Matrix

| Message | Wire Format | Required Auth | Gate |
|---|---|---|---|
| `MsgCreateValidatorWithPallasKey` | Standard Tx | Cosmos secp256k1 signing | Standard ante handler (exempt from `CeremonyValidatorDecorator`) |
| `MsgRegisterPallasKey` | Standard Tx | Cosmos secp256k1 signing + bonded validator | `CeremonyValidatorDecorator` |
| `MsgDelegateVote` | Custom (0x02) | ZKP #1 + RedPallas spend-auth sig | `verifyDelegation()` + `SigVerifier.Verify` |
| `MsgCastVote` | Custom (0x03) | ZKP #2 + RedPallas vote-auth sig | `verifyCastVote()` + `SigVerifier.Verify` |
| `MsgRevealShare` | Custom (0x04) | ZKP #3 | `verifyRevealShare()` |
| `MsgCreateVotingSession` | Standard Tx | VoteManager module account only | `ValidateVoteManagerOnly` |
| `MsgSetVoteManager` | Standard Tx | VoteManager module account only | `ValidateVoteManagerOnly` |
| `MsgDealExecutiveAuthorityKey` | Custom (0x07) | Proposer-only (injected) | `ValidateProposerIsCreator` |
| `MsgAckExecutiveAuthorityKey` | Custom (0x08) | Proposer-only (injected) | `ValidateProposerIsCreator` |
| `MsgSubmitPartialDecryption` | Custom (0x0D) | Proposer-only (injected) | `ValidateProposerIsCreator` |
| `MsgSubmitTally` | Custom (0x05) | Proposer-only (injected) | `ValidateProposerIsCreator` + DLEQ/Lagrange |

### Injected Message Security

- `[CRITICAL]` `ValidateProposerIsCreator` rejects during `CheckTx`/`ReCheckTx` (mempool barrier), making injected messages unsubmittable via standard ante handler paths
- `[CRITICAL]` Creator field is always resolved from `ctx.BlockHeader().ProposerAddress` — validator A cannot set Creator to validator B's operator address and have it pass
- `[CRITICAL]` ProcessProposal re-validates proposer identity for each injected message, preventing a byzantine proposer from injecting messages attributed to other validators
- `[HIGH]` FinalizeBlock re-executes the full ante pipeline, so even if ProcessProposal is skipped by 2/3 vote, state writes still require valid auth
- `[CRITICAL]` No code path allows the four injected messages to bypass the proposer-identity check (e.g., via governance, sudo, or direct keeper call)

### Auth Matrix Verification

- `[HIGH]` Validator-focused messages (`MsgCreateValidatorWithPallasKey`, `MsgRegisterPallasKey`) require standard Cosmos SDK signature verification — no custom bypass
- `[CRITICAL]` User-facing ceremony messages (`MsgDelegateVote`, `MsgCastVote`) are authenticated via RedPallas signatures over correct sighash domains (ZIP-244 for delegation, Blake2b-256 for cast vote)
- `[HIGH]` VoteManager-only messages (`MsgCreateVotingSession`, `MsgSetVoteManager`) cannot be executed by regular validators or users — `ValidateVoteManagerOnly` checks `creator == mgr.Address` with no fallback
- `[CRITICAL]` No message type is missing from the auth matrix (i.e., no unprotected handler entry points)
- `[HIGH]` `CeremonyValidatorDecorator` correctly distinguishes which standard-Tx messages require bonded-validator status and which are exempt

### Domain Separation

- `[CRITICAL]` `MsgDelegateVote` uses ZIP-244 sighash construction; `MsgCastVote` uses `Blake2b-256(CastVoteSighashDomain || fields)` — verify these are structurally distinct (different hash functions, different domain tags) so no cross-type signature reuse is possible
- `[HIGH]` `write32()` in `sighash.go` zero-pads short fields and truncates long fields to 32 bytes — verify this cannot create collisions (e.g., a 31-byte `vote_round_id` with trailing zero produces the same hash input as a 32-byte ID ending in zero)
- `[MEDIUM]` The ceremony ack hash `SHA256("ack" || ea_pk || validator_address)` uses a 3-byte domain tag — verify it cannot collide with any other on-chain hash domain (`dleqDomainTag = "svote-dleq-v1"`, `CastVoteSighashDomain = "SVOTE_CAST_VOTE_SIGHASH_V0"`)
- `[MEDIUM]` The ack hash is not a cryptographic signature — any block proposer can compute it for any validator. Verify this is intentional and that `ValidateProposerIsCreator` is the actual auth gate
- `[HIGH]` Verify that ceremony resets always generate a fresh `ea_sk`/`ea_pk` pair — if `ea_pk` is reused across resets, the ack hash would be identical, potentially allowing a replayed ack from a previous failed ceremony to pass

### Validator Spam Vectors

- `[HIGH]` Validators who received stake from the VoteManager cannot use those funds to submit arbitrary Cosmos SDK messages (e.g., `MsgSend`, `MsgSubmitProposal`, param-change proposals) at zero cost given the fee-free design
- `[HIGH]` The infinite gas meter and absence of fees do not allow a funded validator to flood the mempool with standard Cosmos transactions unrelated to the vote protocol
- `[HIGH]` If governance or bank modules are enabled, confirm that validators cannot exploit them (or confirm they are disabled/restricted)
- `[MEDIUM]` Evaluate whether the post-genesis `MsgCreateValidator` blocking and one-pending-round limit are sufficient to prevent a malicious validator from creating operational disruption
- `[MEDIUM]` Assess whether additional rate-limiting or message-type allowlisting is needed for standard Cosmos transactions beyond the existing spam mitigations

---

## Area 9: Helper Server Privacy

[Back to audit map](suggested-review-scope.md#area-9-helper-server-privacy)

**Files:** `internal/helper/processor.go`, `internal/helper/store.go`, `internal/helper/submit.go`

### Temporal Unlinkability

- `[CRITICAL]` `exponentialDelay()` in `processor.go` uses `crypto/rand` to sample from `Exp(1/mean)` — verify the distribution is correctly implemented and that the conversion from uniform bytes to exponential samples does not introduce bias
- `[HIGH]` The 60-second cutoff before `vote_end_time` bypasses all jitter and submits immediately — this creates a timing side channel where an observer knows that shares submitted in the final minute were ready at least 60 seconds before deadline. Assess whether this is an acceptable tradeoff
- `[MEDIUM]` Intra-batch jitter (`intraShareDelay()`) uses half the mean of the inter-cycle delay — verify that an observer watching submission times within a batch cannot correlate them back to the original share arrival order
- `[MEDIUM]` On crash recovery (`store.go:recover()`), shares get fresh random delays — verify that no timing information from the previous session survives in the SQLite database or WAL

### SQLite Persistence and Share Lifecycle

- `[HIGH]` Idempotent `Enqueue()` distinguishes insert/duplicate/conflict — verify that a malicious client cannot overwrite a queued share with different payload data (e.g. by resubmitting with the same `round_id:share_index` but different `enc_share` values)
- `[MEDIUM]` `PurgeExpiredRounds()` deletes shares after the vote window — verify this is timely and complete (SQLite deleted pages may retain data forensically; assess whether `VACUUM` or `PRAGMA secure_delete` is needed)
- `[HIGH]` Private witness data (`primary_blind`, `share_comms`) is stored in SQLite for proof generation — verify these are purged after submission and not retained for retries of already-submitted shares
- `[MEDIUM]` WAL mode (`PRAGMA journal_mode=WAL`) allows concurrent reads during writes — verify the mutex in `ShareStore` is sufficient and that concurrent `Enqueue` + `TakeReady` + `PurgeExpiredRounds` cannot produce inconsistent state

### Proof Generation and Chain Submission

- `[CRITICAL]` The prover generates ZKP #3 proofs from private witness data (Merkle path, primary_blind, share_comms) — verify that witness data is not logged, and that proof generation errors do not leak witness values in error messages
- `[HIGH]` `ChainSubmitter` submits to the chain's REST API with a 180-second timeout — verify that a slow or unresponsive chain cannot cause the helper to accumulate goroutines (`maxConcurrent` bounds should be respected)
- `[HIGH]` Duplicate nullifier handling (`IsDuplicateNullifier`) treats chain rejection code `ErrDuplicateNullifier` as benign (another helper submitted first) — verify that no other rejection codes are silently swallowed
- `[MEDIUM]` Failed shares are marked via `MarkFailed()` — verify these cannot be re-enqueued or retried in a way that generates multiple proofs for the same witness

---

## Area 10: General Correctness

[Back to audit map](suggested-review-scope.md#area-10-general-correctness)

**Files:** `x/vote/keeper/`, `app/`, `x/vote/module.go`

### Invariants

- `[CRITICAL]` State machine transitions are unidirectional and irreversible (`PENDING -> ACTIVE -> TALLYING -> FINALIZED`)
- `[HIGH]` Integer safety (no overflows in `ShamirIndex`, share counts, tally values)
- `[HIGH]` Error propagation (no silently-swallowed errors in state-mutating paths)
- `[MEDIUM]` Defensive programming: point validation before FFI, nullifier scoping, round existence checks, triple-layer proposer identity enforcement
