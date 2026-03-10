# Shielded Vote Book — AI Index

This is a structural index of the `shielded_vote_book` Obsidian vault (symlinked at the repo root). Use this to identify which files to read for a given topic without ingesting the entire book.

**To set up the symlink:** `ln -s /path/to/your/shielded_vote_book shielded_vote_book` (see `docs/ai_setup.md`)

---

## Overview

| File                              | Summary                                                                                                                                                                                                                                                                                             |
| --------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `overview/design-principles.md`   | Keystone compatibility, single-session voting, best-effort balance privacy (homomorphic encryption, vote splitting), unlinkable voting keys, auditable tallying, lightweight PIR-based nullifier proofs.                                                                                            |
| `overview/overall-flow.md`        | The five phases: Phase 0 (setup with snapshot, proposals, EA key, VKs), Phase 1 (delegation via ZKP #1, VAN creation), Phase 2 (voting via ZKP #2 with VC and 16 encrypted shares), Phase 3 (share submission to servers), Phase 4 (share reveal via ZKP #3, homomorphic accumulation), then tally. |
| `overview/privacy-guarantees.md`  | What is hidden (identity, total balance, individual amounts, which VC, real nullifiers) vs. revealed (aggregate totals, that you voted, share count). Trust model for EA and share submission servers.                                                                                              |
| `overview/comparison-to-today.md` | Compares to current Zcash governance: Keystone-compatible signing, no registration period (PIR), background ZKPs, vote amount privacy (splitting + unlinkable shares), auditable tally (Chaum–Pedersen proof).                                                                                      |

## User Flow

| File                               | Summary                                                                                                                                                                                                                             |
| ---------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `userflow/wallet-setup.md`         | User opens governance tab, wallet finds Orchard notes at snapshot height, generates voting hotkey, authorizes delegation (Keystone or in-app), submits ZKP #1. PIR nullifier exclusion, Merkle inclusion proofs, 0.125 ZEC minimum. |
| `userflow/casting-a-vote.md`       | User selects proposals/choices, confirms once, wallet splits weight into 16 shares, encrypts with El Gamal, builds VC, runs ZKP #2, submits to chain, sends shares to server for ZKP #3 at staggered times.                         |
| `userflow/delegating-your-vote.md` | Delegation: VAN is spent and split into two VANs (delegate + delegator), same `allowed_proposals` bitmask. Supports partial delegation. Amounts stay private.                                                                       |

## Data Types

| File            | Summary                                                                                                                                                                                                                                                                                                                                                                        |
| --------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `data-types.md` | Core data structures: **VAN** (Vote Authority Note — fields, two-layer Poseidon commitment, nullifier with explicit 4-input Poseidon, lifecycle), **VC** (Vote Commitment — shares_hash, proposal_id, decision), **Vote Share** (encryption, nullifier using blind, constraints in ballots), **VCT** (Vote Commitment Tree — global across rounds, depth 24, insertion order). |

## Chain

| File                                    | Summary                                                                                                                                                                                                     |
| --------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `chain/role-of-the-vote-chain.md`       | Vote chain as source of truth: VCT, three nullifier sets (governance, VAN, share), encrypted share accumulators. Verifies ZKP #1/#2/#3.                                                                     |
| `chain/roles-and-admin.md`              | Admin roles, permissions, and governance controls on the vote chain.                                                                                                                                        |
| `chain/ea-key-setup-ceremony.md`        | Per-round EA key ceremony: dealer chosen from block proposers, generates `ea_sk`/`ea_pk`, ECIES encryption for validators, ACK via PrepareProposal, fast/timeout confirmation, future TSS/DKG.              |
| `chain/chain-api.md`                    | Chain API: voting session structure (`voting_round_id` from Poseidon), proposals (1–15 per session, 2–8 options), `vote_decision` encoding (chain-validated, not circuit-enforced), tally results via gRPC. |
| `chain/building-from-source.md`         | Build instructions for the vote chain binary.                                                                                                                                                               |
| `chain/setting-up-a-validator.md`       | Validator setup, key registration, joining the network.                                                                                                                                                     |
| `chain/setting-up-a-monitoring-node.md` | Monitoring node setup (non-validating).                                                                                                                                                                     |

## Circuit Components

| File                                        | Summary                                                                                                                                                                                   |
| ------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `circuits/overview.md`                      | Component map: which gadgets are reused from Orchard vs. new, which ZKPs use each, 10-bit lookup range check pattern.                                                                     |
| `circuits/note-commitment-integrity.md`     | Standard Orchard NoteCommit (ZKP #1).                                                                                                                                                     |
| `circuits/nullifier-derivation.md`          | Standard Orchard DeriveNullifier (ZKP #1).                                                                                                                                                |
| `circuits/merkle-tree-membership.md`        | Poseidon Merkle path verification (ZKP #1/#2/#3). Three trees: NCT (depth 32, Sinsemilla), IMT (depth 29, Poseidon), VCT (depth 24, Poseidon, global). Conditional skip for padded notes. |
| `circuits/imt-non-membership.md`            | Indexed Merkle Tree exclusion proof for governance nullifiers (ZKP #1).                                                                                                                   |
| `circuits/diversified-address-integrity.md` | Standard Orchard ivk check (ZKP #1).                                                                                                                                                      |
| `circuits/spend-authority.md`               | SpendAuthSig rerandomization (ZKP #1/#2).                                                                                                                                                 |
| `circuits/rho-binding.md`                   | Binds signed note to delegated notes and round (ZKP #1).                                                                                                                                  |
| `circuits/governance-nullifier.md`          | Domain-separated nullifier for double-delegation prevention (ZKP #1). Explicit 4-input Poseidon: `Poseidon(nk, domain_tag, voting_round_id, real_nf)`.                                    |
| `circuits/van-nullifier.md`                 | Nullifier for Vote Authority Notes (ZKP #2). Explicit 4-input Poseidon: `Poseidon(vsk.nk, domain_tag, voting_round_id, van)`. Domain separation from governance nullifiers.               |
| `circuits/share-nullifier.md`               | Nullifier for individual vote shares (ZKP #3). Uses `blind` (not `enc_share`). 16 shares (0..15).                                                                                         |
| `circuits/proposal-authority-decrement.md`  | Bitmask decrement for proposal permissions (ZKP #2). Detailed bit-manipulation logic.                                                                                                     |
| `circuits/ballot-scaling.md`                | Floor-division of zatoshi balance into ballot count (ZKP #1).                                                                                                                             |
| `circuits/shares-range-check.md`            | Range proof for each share value `[0, 2^30)` (ZKP #2).                                                                                                                                    |
| `circuits/el-gamal-encryption-integrity.md` | Proves valid El Gamal ciphertext construction (ZKP #2).                                                                                                                                   |
| `circuits/shares-hash-integrity.md`         | Flat hash over 16 encrypted shares (ZKP #2/#3).                                                                                                                                           |
| `circuits/vote-commitment-integrity.md`     | Hash of domain tag + shares hash + proposal + decision (ZKP #2/#3).                                                                                                                       |
| `circuits/domain-tag-separation.md`         | Prevents VAN/VC collision in shared tree (ZKP #1/#2/#3). VAN uses two-layer Poseidon.                                                                                                     |
| `circuits/poseidon-hash.md`                 | Poseidon P128Pow5T3 usage throughout.                                                                                                                                                     |
| `circuits/share-membership.md`              | Share membership proof for ZKP #3.                                                                                                                                                        |

## ZKP Specifications

| File                             | Summary                                                                                                                                                                                                                                                                                    |
| -------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| `zkps/zkp1-delegation-proof.md`  | **ZKP #1 — Delegation:** Proves ownership of up to 5 unspent Orchard notes at snapshot height and correct delegation to a governance hotkey. Public inputs: nullifiers, roots, VAN, `vote_round_id`. Conditions: signed note, output note, VAN integrity, ballot scaling, per-note checks. |
| `zkps/zkp2-vote-proof.md`        | **ZKP #2 — Vote Proof:** 11 public inputs including `ea_pk`. Proves valid vote by consuming a VAN and producing a new VAN (decremented proposal authority) and a VC with 16 encrypted shares. VAN ownership, nullifier, shares sum, range checks, El Gamal integrity, VC integrity.        |
| `zkps/zkp3-vote-reveal-proof.md` | **ZKP #3 — Vote Reveal:** 7 public inputs (x-coordinates of ciphertext, no anchor height). Proves a single encrypted share comes from a valid VC without revealing which one. Submission server runs this. VC membership, share opening (blinded commitments), share nullifier derivation. |

## Delegation

| File                                    | Summary                                                                                                                                                               |
| --------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `delegation/delegation-setup.md`        | Delegation mechanics: delegator's VAN is spent, two new VANs created (delegate + delegator). ZKP ensures amount sum and `allowed_proposals` preservation.             |
| `delegation/partial-delegation.md`      | Partial delegation: VAN split so delegate gets `delegated_amount`, delegator keeps the rest. Supports multiple delegates via successive delegations.                  |
| `delegation/server-delegated-shares.md` | Why share submission is delegated to servers: reliability, temporal unlinkability (staggered submissions), parallelism. Payload format, server behavior, trust model. |

## Appendices

| File                                                | Summary                                                                                                                                                                                                    |
| --------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `appendices/el-gamal.md`                            | El Gamal on Pallas: encryption `(r*G, v*G + r*R)`, additive homomorphism for tally, decryption via bounded discrete log. Ballot scaling reduces bit-width.                                                 |
| `appendices/tally.md`                               | Tally flow: (1) public ciphertext aggregation per `(proposal_id, decision)`, (2) EA decryption with `ea_sk` + bounded discrete log, (3) Chaum–Pedersen proof of correct decryption.                        |
| `appendices/pir-efficient-merkle-path-retrieval.md` | PIR spec for private Merkle path retrieval over ~50M leaves using YPIR (SimplePIR). Three-tier layout (11 plaintext + 8 PIR + 7 PIR layers), sentinel nullifiers, row layouts, storage/bandwidth analysis. |

---

## Quick lookup by topic

| Topic                                | Read these files                                                                  |
| ------------------------------------ | --------------------------------------------------------------------------------- |
| Protocol overview / how voting works | `overview/overall-flow.md`, `README.md`                                           |
| Privacy model and threat model       | `overview/privacy-guarantees.md`, `overview/design-principles.md`                 |
| Data structures (VAN, VC, VCT)       | `data-types.md`                                                                   |
| ZKP #1 (delegation proof)            | `zkps/zkp1-delegation-proof.md`, then `circuits/` files for individual conditions |
| ZKP #2 (vote proof)                  | `zkps/zkp2-vote-proof.md`, then `circuits/` files for individual conditions       |
| ZKP #3 (share reveal proof)          | `zkps/zkp3-vote-reveal-proof.md`, then `circuits/share-membership.md`             |
| Circuit gadget details               | `circuits/overview.md` (map), then specific `circuits/*.md`                       |
| El Gamal encryption / tally          | `appendices/el-gamal.md`, `appendices/tally.md`                                   |
| Chain setup / validator ops          | `chain/building-from-source.md`, `chain/setting-up-a-validator.md`                |
| EA key ceremony                      | `chain/ea-key-setup-ceremony.md`                                                  |
| Chain API / session structure        | `chain/chain-api.md`                                                              |
| Delegation mechanics                 | `delegation/delegation-setup.md`, `delegation/partial-delegation.md`              |
| Server share submission              | `delegation/server-delegated-shares.md`                                           |
| Nullifier PIR                        | `appendices/pir-efficient-merkle-path-retrieval.md`                               |
| Wallet UX flow                       | `userflow/wallet-setup.md`, `userflow/casting-a-vote.md`                          |
