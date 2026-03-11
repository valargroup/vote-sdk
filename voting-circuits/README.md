# voting-circuits

Governance ZKP circuits (delegation, vote proof, share reveal) for the Zally voting protocol.

Built with [halo2](https://github.com/zcash/halo2) and a local fork of the [Orchard](https://github.com/zcash/orchard) shielded protocol. The crate is `no_std`-compatible with an optional `std` feature.

## Proof flow

```
Orchard Notes ──► Delegation (ZKP 1) ──► Vote Authority Notes (VANs)
                                              │
                                              ▼
                  Vote Proof  (ZKP 2) ──► Vote Commitments + encrypted shares
                                              │
                                              ▼
                  Share Reveal (ZKP 3) ──► Revealed shares for tally
```

1. [**Delegation**](https://valargroup.gitbook.io/shielded-vote-docs/zkp-specifications/zkp1-delegation-proof) spends Orchard notes and mints VANs that carry delegated voting weight.
2. [**Vote Proof**](https://valargroup.gitbook.io/shielded-vote-docs/zkp-specifications/zkp2-vote-proof) spends a VAN to cast a vote, producing El Gamal-encrypted shares and a vote commitment.
3. [**Share Reveal**](https://valargroup.gitbook.io/shielded-vote-docs/zkp-specifications/zkp3-vote-reveal-proof) opens a single encrypted share and proves it belongs to a registered vote commitment.

## Package layout

```
src/
├── lib.rs                        # Crate root — re-exports the three circuits
├── circuit/                      # Shared gadgets used across circuits
│   ├── address_ownership.rs      #   CommitIvk + diversified-address integrity
│   ├── elgamal.rs                #   El Gamal encryption (vote proof condition 11)
│   ├── poseidon_merkle.rs        #   Poseidon-based Merkle path verification
│   ├── van_integrity.rs          #   VAN commitment hash (two-layer Poseidon)
│   └── vote_commitment.rs        #   Vote commitment hash
├── shares_hash.rs                # Shares-hash gadget (shared by ZKP 2 & 3)
│
├── delegation/                   # ZKP #1 — Delegation circuit (K=14)
│   ├── circuit.rs                #   15-condition halo2 circuit
│   ├── builder.rs                #   Multi-note bundle builder (up to 5 notes)
│   ├── prove.rs                  #   Prove / verify helpers
│   ├── imt.rs                    #   Indexed Merkle Tree (data structure)
│   ├── imt_circuit.rs            #   IMT non-membership proof gadget
│   ├── README.md                 #   Detailed specification
│   └── plans/                    #   Design documents (.plan.md)
│
├── vote_proof/                   # ZKP #2 — Vote Proof circuit (K=14)
│   ├── circuit.rs                #   12-condition halo2 circuit
│   ├── builder.rs                #   Builder producing VoteProofBundle
│   ├── prove.rs                  #   Prove / verify helpers
│   ├── authority_decrement.rs    #   Proposal-authority decrement gadget
│   └── README.md                 #   Detailed specification
│
└── share_reveal/                 # ZKP #3 — Share Reveal circuit (K=11)
    ├── circuit.rs                #   5-condition halo2 circuit
    ├── builder.rs                #   Builder
    └── prove.rs                  #   Prove / verify helpers

benches/
└── delegation.rs                 # Criterion benchmarks for delegation proving
```

### Shared gadgets (`circuit/`)

Reusable halo2 gadgets that appear in more than one circuit:

| Gadget | Used by | Purpose |
|--------|---------|---------|
| `address_ownership` | Delegation, Vote Proof | CommitIvk + diversified-address binding |
| `elgamal` | Vote Proof | El Gamal encryption of vote shares |
| `poseidon_merkle` | All three | Poseidon Merkle-path membership proofs |
| `van_integrity` | Delegation, Vote Proof | Two-layer Poseidon hash for VAN commitments |
| `vote_commitment` | Vote Proof, Share Reveal | Hash of `(domain, round_id, shares_hash, proposal_id, decision)` |

`shares_hash` (at crate root) computes a two-level Poseidon hash over 16 blinded share commitments and is shared by ZKP 2 and ZKP 3.

### Circuit details

| Circuit | K | Rows | Conditions | Spec |
|---------|---|------|------------|------|
| Delegation | 14 | 16 384 | 15 | [ZKP #1](https://valargroup.gitbook.io/shielded-vote-docs/zkp-specifications/zkp1-delegation-proof) |
| Vote Proof | 14 | 16 384 | 12 | [ZKP #2](https://valargroup.gitbook.io/shielded-vote-docs/zkp-specifications/zkp2-vote-proof) |
| Share Reveal | 11 | 2 048 | 5 | [ZKP #3](https://valargroup.gitbook.io/shielded-vote-docs/zkp-specifications/zkp3-vote-reveal-proof) |

## Companion crate

The `orchard/` directory at the workspace root is a local fork of the Zcash Orchard shielded-transaction protocol. It supplies NoteCommit, nullifier derivation, CommitIvk, and SpendAuthG primitives consumed by the circuits here.

## Building

```bash
cargo build
```

## Testing

```bash
cargo test

# Row-budget smoke tests (ignored by default, prints utilization)
cargo test row_budget -- --nocapture --ignored
```

## Benchmarks

```bash
cargo bench   # runs delegation proving benchmarks via Criterion
```

## Features

| Feature | Default | Description |
|---------|---------|-------------|
| `std` | yes | Enables `std` support |

## Key dependencies

| Crate | Role |
|-------|------|
| `halo2_proofs` | Proof system (with batch verification) |
| `halo2_gadgets` | Standard gadgets (Poseidon, Sinsemilla, ECC) |
| `pasta_curves` | Pallas / Vesta curve arithmetic |
| `orchard` (local) | Orchard note commitment, nullifier, CommitIvk |
| `halo2_poseidon` | Poseidon hash for Merkle trees and commitments |
| `incrementalmerkletree` | Incremental Merkle tree data structure |
| `sinsemilla` | Sinsemilla hash (used via Orchard) |
