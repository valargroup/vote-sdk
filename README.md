# zally

Cosmos SDK application chain for private voting using Zcash-derived cryptography.

## Technical Assumptions

1. The chain launches with a single genesis validator. Additional validators join post-genesis via `MsgCreateValidatorWithPallasKey`, which atomically creates the validator and registers their Pallas key for the ceremony. Raw `MsgCreateValidator` is blocked in the ante handler for live transactions. Validator set changes beyond that are handled via major upgrades or a PoA module (future).
2. Client interaction avoids Cosmos SDK protobuf encoding:
   - **Tx submission:** Client sends a plain JSON POST; server handler parses JSON and encodes as needed.
   - **Query:** gRPC gateway supports JSON out-of-the-box.
3. No native `x/gov` module. The vote module implements custom private voting instead of reusing standard Cosmos governance.

## Architecture

### Module: `x/vote`

The vote module has two major subsystems: the **EA Key Ceremony** (one-time chain setup) and **Voting Rounds** (created after the ceremony completes).

### EA Key Ceremony (Per-Round)

The EA key ceremony runs **automatically per voting round**. Each `MsgCreateVotingSession` creates a round in `PENDING` status and snapshots all eligible validators (bonded + registered Pallas key) into the round's ceremony fields. The ceremony proceeds automatically via `PrepareProposal` — no manual intervention is needed after initial Pallas key registration.

#### Per-Round Ceremony State Machine

Ceremony state is stored on the `VoteRound` itself (fields `ceremony_status`, `ceremony_validators`, etc.). There is no global singleton ceremony state.

```
  PENDING (REGISTERING) ──> PENDING (DEALT) ──> ACTIVE (CONFIRMED)
                                  │                (all acked)
                       timeout    │
                       (< 1/3)   │ timeout (≥ 1/3)
                          │       │
                          v       v
                    REGISTERING   ACTIVE (CONFIRMED)
                    (reset for    + strip non-ackers
                     re-deal)
```

| From | To | Trigger | Condition |
|---|---|---|---|
| REGISTERING | DEALT | Auto-deal via PrepareProposal | Block proposer is a ceremony validator |
| DEALT | CONFIRMED + ACTIVE | MsgAckExecutiveAuthorityKey | All validators acked (fast path) |
| DEALT | CONFIRMED + ACTIVE | EndBlocker timeout | >= 1/3 acked at timeout; non-ackers stripped |
| DEALT | REGISTERING | EndBlocker timeout | < 1/3 acked; reset for re-deal by next proposer |

Key behaviors:
- **Fast path vs timeout** — the fast path confirms when ALL validators ack (no stripping needed). The timeout path confirms with >= 1/3 acks (integer arithmetic: `acks * 3 >= validators`) and strips non-ackers.
- **Auto-deal** — the block proposer automatically deals when it detects a PENDING round in REGISTERING state. No manual `ceremony.sh deal` step.
- **Auto-ack** — each block proposer auto-acks via PrepareProposal when it detects a DEALT round.
- **Miss tracking** — validators snapshotted into a ceremony who fail to ack have a consecutive miss counter incremented. After 3 consecutive misses, the validator is jailed.
- **Ceremony log** — each state transition appends a timestamped entry to `ceremony_log` on the round, visible in queries and the admin UI.

#### Pallas Key Registration (One-Time)

Validators register their Pallas key once via `MsgRegisterPallasKey` or `MsgCreateValidatorWithPallasKey`. Keys are stored in a global registry (prefix `0x0C`) and persist across rounds.

#### Auto-Deal and Auto-Ack via PrepareProposal

`PrepareProposal` composes two ceremony injectors:
1. **Auto-deal** — if a PENDING round is in REGISTERING state and the proposer is a ceremony validator, generate `ea_sk`, Shamir-split it into `(t, n)` shares, ECIES-encrypt `share_i` to each validator, publish `VK_i = share_i * G` and `threshold = ceil(n/3) + 1`, and inject `MsgDealExecutiveAuthorityKey`.
2. **Auto-ack** — if a PENDING round is in DEALT state and the proposer hasn't acked, decrypt the payload to recover their share, verify `share_i * G == VK_i` (threshold mode) or `ea_sk * G == ea_pk` (legacy), inject `MsgAckExecutiveAuthorityKey`, and write the share/key to disk.

#### Timeout (EndBlocker)

Only the DEALT phase has a timeout (default: 30 minutes). On timeout:
- **>= 1/3 acked:** Confirm ceremony, strip non-ackers, activate round. Increment miss counter for each non-acker; jail if >= 3 consecutive misses.
- **< 1/3 acked:** Reset to REGISTERING for re-deal by the next proposer. Increment miss counters.

#### ECIES Encryption Scheme

Each validator's `ea_sk` share is encrypted using ECIES over the Pallas curve with **SpendAuthG** as the generator:

1. `E = e * SpendAuthG` (ephemeral public key)
2. `S = e * pk_i` (ECDH shared secret)
3. `k = SHA256(E_compressed || S.x)` (symmetric key)
4. `ct = ChaCha20-Poly1305(k, nonce=0, ea_sk)` (authenticated encryption)

### VoteManager Role

The VoteManager is a singleton on-chain address that gates who can create voting sessions. Before any `MsgCreateVotingSession` is accepted, a VoteManager must be set.

**`MsgSetVoteManager`** -- Sets or changes the VoteManager address.
- **Bootstrap:** When no VoteManager exists, any bonded validator can set the first one
- **Update:** Once set, the current VoteManager **or any bonded validator** can change it
- Non-validators who are not the current VoteManager are rejected
- Uses custom wire format tag `0x0C` and REST endpoint `POST /zally/v1/set-vote-manager`
- Stored as a singleton `VoteManagerState` in the KV store (key `0x0A`)

### Voting Rounds

After the ceremony reaches CONFIRMED and a VoteManager is set, voting sessions can be created.

```
ACTIVE ──> TALLYING ──> FINALIZED
  ^
  │ (gated: requires CONFIRMED ceremony + VoteManager)
```

**`MsgCreateVotingSession`** reads `ea_pk` from the confirmed ceremony state (not from the message). The round stores its own copy of `ea_pk` for future key rotation support. Only the VoteManager can create voting sessions. An optional `description` field provides human-readable context for the round.

**`MsgSubmitPartialDecryption`** is auto-injected via `PrepareProposal` when a round is in TALLYING state and threshold mode is active. Each validator submits `D_i = share_i * C1` per accumulator. Cannot be submitted through the mempool.

**`MsgSubmitTally`** is auto-injected via `PrepareProposal` once `t` partial decryptions exist on-chain. The proposer Lagrange-combines them to recover `ea_sk * C1`, runs BSGS, and submits plaintext totals. Cannot be submitted through the mempool.

### PrepareProposal / ProcessProposal Pipeline

`PrepareProposal` composes four injectors that run sequentially on each proposed block:
1. **Ceremony deal injection** — if a PENDING round is in REGISTERING and the proposer is a ceremony validator, auto-deal via `MsgDealExecutiveAuthorityKey`
2. **Ceremony ack injection** — if a PENDING round is in DEALT and the proposer hasn't acked, auto-ack via `MsgAckExecutiveAuthorityKey`
3. **Partial decryption injection** (threshold mode) — if a TALLYING round has `threshold > 0` and the proposer hasn't yet submitted, compute `D_i = share_i * C1` per accumulator and inject `MsgSubmitPartialDecryption`
4. **Tally injection** — when `t` partials are on-chain (threshold mode) or `ea_sk` is on disk (legacy), Lagrange-combine and BSGS-solve, then inject `MsgSubmitTally`

`ProcessProposal` validates all injected txs on non-proposer validators before accepting a block. `MsgAckExecutiveAuthorityKey`, `MsgSubmitPartialDecryption`, and `MsgSubmitTally` are all blocked from the mempool (CheckTx rejects them).

### Custom Wire Format

Vote and ceremony transactions bypass the standard Cosmos SDK `Tx` envelope. Each transaction is a single-byte tag followed by a protobuf-encoded message body:

```
[tag (1 byte)] [proto-encoded message body]
```

| Tag    | Message                            | Category                |
| ------ | ---------------------------------- | ----------------------- |
| `0x01` | `MsgCreateVotingSession`           | Voting round            |
| `0x02` | `MsgDelegateVote`                  | Voting round            |
| `0x03` | `MsgCastVote`                      | Voting round            |
| `0x04` | `MsgRevealShare`                   | Voting round            |
| `0x05` | `MsgSubmitTally`                   | Voting round (injected) |
| `0x06` | `MsgRegisterPallasKey`             | Ceremony                |
| `0x07` | `MsgDealExecutiveAuthorityKey`     | Ceremony                |
| `0x08` | `MsgAckExecutiveAuthorityKey`      | Ceremony (injected)     |
| `0x09` | `MsgCreateValidatorWithPallasKey`  | Ceremony                |
| `0x0B` | `MsgReInitializeElectionAuthority` | Ceremony                |
| `0x0C` | `MsgSetVoteManager`                | Management              |

Any transaction whose first byte does not match a known tag is decoded as a standard Cosmos SDK `Tx`. Tag `0x0A` is deliberately skipped because it collides with the standard Cosmos Tx protobuf encoding (field 1, wire type 2). Note that raw `MsgCreateValidator` is blocked by the ante handler for live transactions -- post-genesis validators must use `MsgCreateValidatorWithPallasKey` (tag `0x09`) instead.

### REST API

The chain exposes a JSON REST API alongside CometBFT RPC. Clients POST JSON bodies for transaction submission and GET for queries — no protobuf encoding required on the client side.

#### Transaction Endpoints

| Method | Path                                     | Description                                             |
| ------ | ---------------------------------------- | ------------------------------------------------------- |
| POST   | `/zally/v1/register-pallas-key`          | Register validator Pallas PK for ceremony               |
| POST   | `/zally/v1/create-validator-with-pallas` | Create validator + register Pallas key (post-genesis)   |
| POST   | `/zally/v1/reinitialize-ea`              | Reset ceremony to REGISTERING (rejected during DEALT)   |
| POST   | `/zally/v1/deal-ea-key`                  | Deal ECIES-encrypted `ea_sk` shares to validators       |
| POST   | `/zally/v1/create-voting-session`        | Create a new voting round (requires CONFIRMED ceremony) |
| POST   | `/zally/v1/delegate-vote`                | Submit a delegation proof (ZKP #1)                      |
| POST   | `/zally/v1/cast-vote`                    | Cast an encrypted vote (ZKP #2)                         |
| POST   | `/zally/v1/reveal-share`                 | Reveal an encrypted share (ZKP #3)                      |
| POST   | `/zally/v1/submit-tally`                 | Submit tally results (normally auto-injected)           |
| POST   | `/zally/v1/set-vote-manager`             | Set or change the VoteManager address                   |

All POST endpoints accept JSON, encode the message with the custom wire format, and broadcast via CometBFT's `broadcast_tx_sync`.

#### Query Endpoints

| Method | Path                                       | Description                                |
| ------ | ------------------------------------------ | ------------------------------------------ |
| GET    | `/zally/v1/ceremony`                       | Current ceremony state and status          |
| GET    | `/zally/v1/rounds/active`                  | Currently active voting round              |
| GET    | `/zally/v1/round/{round_id}`               | Voting round by hex round ID               |
| GET    | `/zally/v1/tally/{round_id}/{proposal_id}` | Tally for a specific proposal              |
| GET    | `/zally/v1/tally-results/{round_id}`       | All tally results for a round              |
| GET    | `/zally/v1/commitment-tree/{height}`       | Vote commitment tree at block height       |
| GET    | `/zally/v1/commitment-tree/latest`         | Latest vote commitment tree                |
| GET    | `/zally/v1/commitment-tree/leaves`         | Tree leaves (`?from_height=X&to_height=Y`) |
| GET    | `/zally/v1/vote-manager`                   | Current VoteManager address                |

### On-Chain State (KV Store Keys)

| Key         | Type                           | Description                                |
| ----------- | ------------------------------ | ------------------------------------------ |
| `0x09`      | `CeremonyState` (singleton)    | EA key ceremony lifecycle                  |
| `0x0A`      | `VoteManagerState` (singleton) | VoteManager address                        |
| `0x01`      | `VoteRound` (per round)        | Voting session state                       |
| `0x02-0x08` | Various                        | Nullifiers, tallies, commitment tree, etc. |

### CeremonyState Fields

```protobuf
enum CeremonyStatus {
  CEREMONY_STATUS_UNSPECIFIED   = 0;
  CEREMONY_STATUS_REGISTERING   = 1; // Accepting validator pk_i registrations (no timeout)
  CEREMONY_STATUS_DEALT         = 2; // DealerTx landed, awaiting acks
  CEREMONY_STATUS_CONFIRMED     = 3; // >=2/3 validators acked, ea_pk ready
}

message CeremonyState {
  CeremonyStatus              status        = 1;
  bytes                       ea_pk         = 2;  // Set when DealerTx lands
  repeated ValidatorPallasKey validators    = 3;  // All registered pk_i
  repeated DealerPayload      payloads      = 4;  // ECIES envelopes from DealerTx
  repeated AckEntry           acks          = 5;  // Per-validator ack status
  string                      dealer        = 6;  // Validator address of the dealer
  uint64                      phase_start   = 7;  // Unix seconds when current phase started
  uint64                      phase_timeout = 8;  // Timeout in seconds for current phase
}
```
