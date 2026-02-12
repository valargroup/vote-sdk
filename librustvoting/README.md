# librustvoting

Core voting library for Zashi governance. Implements the client-side cryptographic operations and SQLite persistence for the voting round lifecycle.

Used by `zcash-voting-ffi` (iOS via UniFFI) and potentially by other clients.

## Modules

| Module            | Status  | What it does                                                                 |
| ----------------- | ------- | ---------------------------------------------------------------------------- |
| `storage/`        | Real    | SQLite database — round state, proofs, votes. WAL mode, versioned migrations |
| `hotkey`          | Real    | Random Pallas keypair generation for voting hotkeys                          |
| `decompose`       | Real    | Binary weight decomposition into 4 shares (protocol max)                     |
| `elgamal`         | Real    | El Gamal encryption of vote shares under EA public key (Pallas curve)        |
| `action`          | Stubbed | Constructs dummy Orchard action for Keystone delegation signing              |
| `governance`      | Stubbed | Governance commitment (VAN) construction                                     |
| `witness`         | Stubbed | Delegation witness assembly from inclusion/exclusion proofs                  |
| `zkp1`            | Stubbed | ZKP #1 — delegation proof (simulates progress, returns placeholder)          |
| `zkp2`            | Stubbed | ZKP #2 — vote commitment proof (returns placeholder bundle)                  |
| `vote_commitment` | Stubbed | Share payload construction for helper server delegation                      |
| `types`           | Real    | Shared types: `VotingError`, `EncryptedShare`, `VoteCommitmentBundle`, etc.  |

## Storage Schema

Four tables in `storage/migrations/001_init.sql`:

**`rounds`** — One row per voting session. Tracks phase progression (Initialized → HotkeyGenerated → DelegationConstructed → WitnessBuilt → DelegationProved → VoteReady). Stores round parameters (snapshot height, EA public key, nc_root, nullifier IMT root) and optional session JSON.

**`proofs`** — One row per round. Stores the delegation witness blob and proof blob with a success flag. Written by `build_delegation_witness()` and `generate_delegation_proof()`.

**`votes`** — One row per (round, proposal). Records the vote choice, commitment JSON, and a `submitted` flag that tracks whether the vote has landed on-chain. Unique constraint on `(round_id, proposal_id)`.

**`cached_tree_state`** — Caches the vote commitment tree state fetched from lightwalletd, keyed by round.

## Usage

All operations go through `VotingDb`:

```rust
let db = VotingDb::open("voting.sqlite3")?;
db.init_round(&params, None)?;

let hotkey = db.generate_hotkey(round_id)?;
let action = db.construct_delegation_action(round_id, &hotkey, &notes)?;
let witness = db.build_delegation_witness(round_id, &action, &inclusion, &exclusion)?;
let proof = db.generate_delegation_proof(round_id, &progress_reporter)?;

let shares = db.encrypt_shares(round_id, &[64, 32, 2, 1])?;
let bundle = db.build_vote_commitment(round_id, 0, 0, &shares, &van_witness, &reporter)?;
db.mark_vote_submitted(round_id, 0)?;
```

Each method validates the current phase, performs the operation, persists results, and advances the phase. Phase transitions are atomic with the data writes.

## Dependencies

- `pasta_curves` / `ff` / `group` — same curve library as Orchard/Zcash
- `rusqlite` (bundled) — SQLite with no system dependency
- `blake2b_simd` — hashing
- Halo2, Poseidon, orchard crates are listed but not yet wired (pending real ZKP implementation)
