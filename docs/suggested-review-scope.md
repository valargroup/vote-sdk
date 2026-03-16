# Audit Scope: Shielded Vote SDK

**Cosmos SDK v0.53 appchain — Halo2 ZKPs, RedPallas, ElGamal, Poseidon Merkle Trees**

## Architecture Overview

The system is a Cosmos SDK (v0.53) appchain implementing private ZK voting via Halo2 proofs, RedPallas signatures, ElGamal encryption, and Poseidon Merkle trees. It uses a dual-mode transaction encoding: standard Cosmos `Tx` envelope for administrative messages, and a custom 1-byte-tag wire format for vote-round and auto-injected ceremony messages.

---

## Review Priority

1. `crypto` package.
   1. El-gamal
   2. The rest
2. Wiring of the custom ZKP proof and signature verifiers
3. Nullifier checks for relevant messages
4. Message auth for all messages
   1. User messages
   2. Validator messages
      1. Prepare/process proposal + ante
   3. Admin messages
5. Helper server timing privacy
6. Validator ceremony
   1. Liveness
   2. Correctness
   3. State-machine
7. SDK key malleability
8. Spam vectors

---

## Area Summary

| # | Area | Core Concern | Critical Invariant |
|---|---|---|---|
| 1 | [Cryptographic Primitives](#area-1-cryptographic-primitives) | ElGamal, Shamir, DLEQ, Poseidon FFI | Homomorphic encryption correctness, threshold reconstruction |
| 2 | [Serialization Canonicality](#area-2-serialization-canonicality) | Point and scalar deserialization | Non-canonical encodings rejected; unique byte repr on-chain |
| 3 | [Tally Integrity](#area-3-tally-integrity) | Accumulation, Lagrange, partial decryption | No unbound ciphertexts reach accumulator; combined D_i recovers correct tally |
| 4 | [ZK State Machine Gating](#area-4-zk-state-machine-gating) | Ante pipeline enforces ZKP verification | No state write without valid ZKP; no bypass via governance/sudo |
| 5 | [Fee Strategy](#area-5-fee-strategy) | Fee-free design safety | Spam mitigated via nullifiers, proposer-only injection, validator gating |
| 6 | [Alternate Tx Encoding](#area-6-alternate-tx-encoding) | Custom 1-byte-tag wire format | No tag collision with protobuf; three-layer proposer injection |
| 7 | [Threshold Ceremony](#area-7-threshold-ceremony) | Key distribution, liveness, recovery | Fresh ea_sk per ceremony; half-ack recovery; TALLYING has no timeout |
| 8 | [Message Authorization](#area-8-message-authorization) | Auth matrix, domain separation, spam | Every message type auth-gated; injected msgs unsubmittable via mempool |
| 9 | [Helper Server Privacy](#area-9-helper-server-privacy) | Timing privacy, share lifecycle, proof gen | Poisson-process timing hides submission order; witness data purged |
| 10 | [General Correctness](#area-10-general-correctness) | State machine, memory safety, errors | Unidirectional state transitions; no swallowed errors on state-mutating paths |

---

## Area Descriptions

### Area 1: Cryptographic Primitives

The `crypto/` package contains all foundational building blocks. ElGamal encryption is the key primitive: it provides additively homomorphic encryption for private vote accumulation, threshold decryption, and tally verification. All vote privacy guarantees flow from correctness here. Includes Shamir secret sharing, Feldman VSS, DLEQ proofs, ECIES share transport, and Poseidon Merkle tree FFI.

See [verification details](suggested-review-details.md#area-1-cryptographic-primitives).

### Area 2: Serialization Canonicality

All on-chain point and scalar deserialization must reject non-canonical encodings to prevent consensus divergence and deduplication bypass. The Pallas curve has prime order (no cofactor), but `FromAffineCompressed` must still reject x >= p. Scalars in DLEQ proofs and Shamir shares must be < q. Identity point acceptance rules differ by context (rejected for public keys, accepted for partial decryptions).

See [verification details](suggested-review-details.md#area-2-serialization-canonicality).

### Area 3: Tally Integrity

Covers the full tally pipeline: ElGamal homomorphic accumulation via `AddToTally()`, threshold Lagrange combination of partial decryptions, tally completeness enforcement, and BSGS discrete log recovery. Includes ciphertext malleability concerns (crafted `enc_share` manipulating accumulators) and partial decryption substitution (malicious `D_i` poisoning Lagrange combination). A single malicious validator can prevent tally finalization in Step 1 (no per-submission DLEQ).

See [verification details](suggested-review-details.md#area-3-tally-integrity).

### Area 4: ZK State Machine Gating

All state-mutating handlers for ZKP-authenticated messages must be gated by the ante pipeline. Verify validation ordering (cheapest checks first), absence of admin/governance/sudo bypasses, deterministic `EndBlocker` transitions, and that all keeper write methods are reachable only from properly-gated handlers.

See [verification details](suggested-review-details.md#area-4-zk-state-machine-gating).

### Area 5: Fee Strategy

The chain is intentionally fee-free. Spam resistance relies on nullifier uniqueness, one-pending-round limit, proposer-only injection, validator gating, and post-genesis `MsgCreateValidator` blocking. Verify the infinite gas meter behavior and accepted tradeoffs.

See [verification details](suggested-review-details.md#area-5-fee-strategy).

### Area 6: Alternate Tx Encoding

The custom 1-byte-tag wire format coexists with standard Cosmos `Tx` protobuf. Verify tag collision prevention (0x0A exclusion), decoder routing, and the three-layer proposer-injection security: mempool barrier, ProcessProposal validation, FinalizeBlock re-check. ProcessProposal intentionally skips DLEQ verification (safety preserved via FinalizeBlock).

See [verification details](suggested-review-details.md#area-6-alternate-tx-encoding).

### Area 7: Threshold Ceremony

Covers key generation, Shamir splitting, ECIES-encrypted share distribution, share verification on ack, EndBlocker timeout with half-ack evaluation, and disk persistence. Liveness is a first-class concern: the DEALT phase has a 30-min timeout with half-ack recovery, but the TALLYING phase has no timeout and no recovery path for offline validators. Dealer trust model: dealer sees full `ea_sk` in Step 1 but deletes it after distribution.

See [verification details](suggested-review-details.md#area-7-threshold-ceremony).

### Area 8: Message Authorization

Covers the complete auth matrix for all 11 message types, injected-message proposer-identity enforcement, sighash domain separation (ZIP-244, Blake2b-256, SHA-256 ack hash, DLEQ domain tag), and validator spam vectors from staked funds in a fee-free design. Four proposer-injected messages must be unsubmittable through normal transaction paths.

See [verification details](suggested-review-details.md#area-8-message-authorization).

### Area 9: Helper Server Privacy

The helper server is a privacy-critical sidecar: it queues encrypted vote shares with Poisson-process timing delays, generates ZKP #3 via Halo2 FFI, and submits `MsgRevealShare`. Covers temporal unlinkability (exponential delay distribution, 60-second cutoff side channel), SQLite share lifecycle (idempotent enqueue, witness data purging), and proof generation error handling.

See [verification details](suggested-review-details.md#area-9-helper-server-privacy).

### Area 10: General Correctness

Broad review of state machine transitions (PENDING -> ACTIVE -> TALLYING -> FINALIZED), integer safety, error propagation, and defensive programming across the non-crypto application logic.

See [verification details](suggested-review-details.md#area-10-general-correctness).

---

## File Cross-Reference

| File | Areas |
|---|---|
| `crypto/elgamal/elgamal.go` | 1, 3 |
| `crypto/elgamal/dleq.go` | 1, 2, 3 |
| `crypto/elgamal/serialize.go` | 1, 2 |
| `crypto/shamir/shamir.go` | 1, 3 |
| `crypto/shamir/feldman.go` | 1 |
| `crypto/shamir/partial_decrypt.go` | 1, 2, 3 |
| `crypto/ecies/` | 1, 7 |
| `crypto/votetree/` | 1 |
| `x/vote/ante/validate.go` | 3, 4, 5, 8 |
| `x/vote/keeper/msg_server.go` | 4, 8 |
| `x/vote/keeper/msg_server_ceremony.go` | 2, 7, 8 |
| `x/vote/keeper/msg_server_tally_decrypt.go` | 2, 3, 4 |
| `x/vote/keeper/keeper_tally.go` | 3, 7 |
| `x/vote/keeper/keeper_ceremony.go` | 4, 7, 8 |
| `x/vote/keeper/keeper_vote_manager.go` | 8 |
| `x/vote/types/sighash.go` | 8 |
| `x/vote/types/codec.go` | 8 |
| `x/vote/module.go` | 7, 10 |
| `app/ante.go` | 4, 5, 6, 8 |
| `app/ante_ceremony.go` | 5, 8 |
| `app/decode.go` | 6 |
| `app/process_proposal.go` | 6, 8 |
| `app/prepare_proposal.go` | 7, 8 |
| `app/prepare_proposal_ceremony.go` | 7, 8 |
| `app/prepare_proposal_partial_decrypt.go` | 7, 8 |
| `app/app.go` | 8 |
| `api/codec.go` | 6 |
| `internal/helper/processor.go` | 9 |
| `internal/helper/store.go` | 9 |
| `internal/helper/submit.go` | 9 |
