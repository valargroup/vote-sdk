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

### EA Key Ceremony

The Election Authority (EA) key ceremony is a **one-time chain-level setup**, not per voting round. Once the ceremony completes and `ea_pk` is confirmed in global state, any number of voting sessions can reference that key. The ceremony must complete before any `MsgCreateVotingSession` is accepted.

The ceremony lifecycle is tracked by a singleton `CeremonyState` in the KV store, separate from `VoteRound`.

#### State Machine

The ceremony is a looping state machine. `REGISTERING` persists indefinitely until a deal is submitted or the ceremony is re-initialized. Only the `DEALT` phase has a timeout. On DEALT timeout, the ceremony either confirms (>= 2/3 acked, non-ackers jailed) or resets (< 2/3 acked).

```
                                                   ┌─────────────┐
                                                   │             │
        v                                          │             │
  [*] ──> REGISTERING ──> DEALT ──> CONFIRMED      │             │
                             │  │                  │             │
                  timeout    │  │ all acked        │             │
                  (< 2/3)    │  │ (fast path)      │             │
                     │       │  │                  │             │
                     v       │  v                  │             │
               REGISTERING   │ CONFIRMED           │             │
                             │ + strip & jail      │             │
                             │ non-ackers (≥ 2/3)  │             │
                             └─────────────────────┘             │
                                                                 │
        MsgReInitializeElectionAuthority ────────────────────────┘
```

| From                          | To          | Trigger                                                           | Condition                                                                                            |
| ----------------------------- | ----------- | ----------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------- |
| nil / REGISTERING (empty)     | REGISTERING | First `MsgRegisterPallasKey` or `MsgCreateValidatorWithPallasKey` | Auto-created on first registration                                                                   |
| REGISTERING                   | DEALT       | `MsgDealExecutiveAuthorityKey`                                    | >= 1 validator registered, valid ea_pk, 1:1 payload-to-validator mapping                             |
| DEALT                         | CONFIRMED   | `MsgAckExecutiveAuthorityKey`                                     | **All** registered validators have acked (fast path, immediate)                                      |
| DEALT                         | CONFIRMED   | EndBlocker timeout                                                | **>= 2/3** validators acked at timeout; non-ackers stripped from state and jailed via staking module |
| DEALT                         | REGISTERING | EndBlocker timeout                                                | **< 2/3** validators acked at timeout (full reset, ceremony failed)                                  |
| CONFIRMED / REGISTERING / nil | REGISTERING | `MsgReInitializeElectionAuthority`                                | Not during DEALT; no active/tallying voting sessions                                                 |

Key behaviors:
- **REGISTERING** has **no timeout** — it persists until a deal is submitted or the ceremony is re-initialized via `MsgReInitializeElectionAuthority`.
- **CONFIRMED** is reached via two paths: (1) all validators ack before timeout (immediate transition), or (2) >= 2/3 validators acked when the DEALT timeout fires (timeout transition with jailing).
- On DEALT timeout with >= 2/3 acks: non-acking validators are **stripped** from `CeremonyState.validators` and `CeremonyState.payloads`, and **jailed** via the staking module (removed from the active validator set).
- On DEALT timeout with < 2/3 acks: full reset to REGISTERING (ceremony failed, no jailing).
- From CONFIRMED, REGISTERING, or nil, a validator can submit `MsgReInitializeElectionAuthority` to reset the ceremony for a fresh key ceremony. Only rejected during DEALT.

#### Messages

**`MsgRegisterPallasKey`** -- A validator registers their Pallas public key for the ceremony.
- Creates the ceremony (REGISTERING) on first call, or appends to existing REGISTERING state
- Validates the key is a valid, non-identity, on-curve Pallas point (32 bytes compressed)
- Rejects duplicate registrations from the same validator address
- Only accepted while ceremony is REGISTERING or nil

**`MsgCreateValidatorWithPallasKey`** -- Atomically creates a validator and registers their Pallas key.
- Wraps a standard `MsgCreateValidator` (encoded as bytes) plus a `pallas_pk` field
- Decodes the embedded staking message and calls through to the staking module's `MsgServer.CreateValidator`
- Registers the Pallas key in the ceremony state (same logic as `MsgRegisterPallasKey`)
- Required for all post-genesis validators — raw `MsgCreateValidator` is blocked by the ante handler
- Uses custom wire format tag `0x09` and a dedicated REST endpoint
- The bootstrap validator (created via `gentx` at genesis) registers their Pallas key separately via `MsgRegisterPallasKey`

**`MsgDealExecutiveAuthorityKey`** -- The bootstrap dealer distributes encrypted `ea_sk` shares.
- Validates `ea_pk` is a valid Pallas point
- Requires exactly one ECIES-encrypted payload per registered validator (1:1 mapping)
- Validates each payload's `ephemeral_pk` is a valid Pallas point
- Stores `ea_pk`, payloads, dealer address, updates `phase_start` and `phase_timeout` (30s default)
- Transitions ceremony to DEALT

**`MsgAckExecutiveAuthorityKey`** -- A registered validator acknowledges receipt of their `ea_sk` share.
- **Auto-injected via PrepareProposal** — validators do not submit this manually
- Cannot be submitted through the mempool (blocked by `ValidateAckSubmitter` during CheckTx)
- Only accepted while ceremony is DEALT
- Rejects acks from non-registered validators
- Rejects duplicate acks from the same validator
- Records the ack with block height and signature `SHA256("ack" || ea_pk || validator_address)`
- **Fast path:** When all validators have acked, transitions to CONFIRMED immediately
- **Timeout path:** If the DEALT timeout fires with >= 2/3 acks, EndBlocker transitions to CONFIRMED, strips non-ackers from ceremony state, and jails them via the staking module. If < 2/3 acked, the ceremony resets.
- With round-robin proposer selection and `n` validators, all acks complete within ~`n` blocks after the DealerTx lands

**`MsgReInitializeElectionAuthority`** -- Resets the ceremony back to REGISTERING so a new key ceremony can begin.
- Rejected during DEALT (awaiting acks)
- Also rejected if any voting session is ACTIVE or TALLYING — resetting the ceremony would orphan in-flight sessions that depend on the current `ea_pk`
- Allowed when ceremony state is nil, REGISTERING, or CONFIRMED (and no active/tallying voting sessions exist)
- Clears all ceremony fields (validators, payloads, acks, `ea_pk`, dealer, timers)
- Uses custom wire format tag `0x0B` and REST endpoint `POST /zally/v1/reinitialize-ea`
- Enables key rotation: after a CONFIRMED ceremony and all voting sessions are finalized, validators can start a fresh one
- Provides an escape hatch for stuck REGISTERING phases (e.g., wrong keys registered, validators offline)

#### Auto-Ack via PrepareProposal

When a block proposer detects the ceremony is in DEALT state and they haven't acked yet, `PrepareProposal` automatically:
1. Loads the validator's Pallas secret key from `vote.pallas_sk_path`
2. ECIES-decrypts their `ea_sk` share using `pallas_sk`
3. Verifies `ea_sk * G == ea_pk` (rejects garbage from a malicious dealer)
4. Injects `MsgAckExecutiveAuthorityKey` into the proposed block
5. Writes `ea_sk` to `vote.ea_sk_path` on disk, priming the auto-tally system

`ProcessProposal` validates injected ack txs on non-proposer validators: checks that the creator is a registered validator, the ceremony is DEALT, and no duplicate ack exists.

**Configuration** (`app.toml`):
```toml
[vote]
ea_sk_path = "$HOME/.zallyd/ea.sk"
pallas_sk_path = "$HOME/.zallyd/pallas.sk"
```

**CLI**: `zallyd pallas-keygen --home $HOME/.zallyd` generates the Pallas keypair (mirrors `ea-keygen`). Called automatically by `init.sh`.

#### Timeout (EndBlocker)

Only the DEALT phase is subject to timeout (`block_time >= phase_start + phase_timeout`). REGISTERING has no timeout — it persists until a deal is submitted or the ceremony is re-initialized.

- **DEALT timeout with >= 2/3 acks:** Transition to CONFIRMED. Non-acking validators are stripped from `CeremonyState` (removed from `validators` and `payloads`) and jailed via the staking module's `Jail()` method, which removes them from the active validator set. A `ceremony_validator_jailed` event is emitted for each jailed validator. Default: 30 seconds.
- **DEALT timeout with < 2/3 acks:** Full reset to REGISTERING (ceremony failed, no jailing). This ensures a quorum is required for the ceremony to succeed.

#### ECIES Encryption Scheme

Each validator's `ea_sk` share is encrypted using ECIES over the Pallas curve with **SpendAuthG** as the generator (the same generator used for ElGamal encryption in the ZKP circuit and for Pallas keypair generation):

1. Ephemeral scalar `e` is generated randomly
2. `E = e * SpendAuthG` (ephemeral public key, stored on-chain)
3. `S = e * pk_i` (ECDH shared secret, where `pk_i = sk_i * SpendAuthG`)
4. `k = SHA256(E_compressed || S.x)` (symmetric key derivation; `S.x` is the x-coordinate with sign bit cleared)
5. `ct = ChaCha20-Poly1305(k, nonce=0, ea_sk)` (authenticated encryption)

Validators decrypt by computing `S = sk_i * E` and deriving the same symmetric key. The zero nonce is safe because each ephemeral key `e` is fresh, making the derived key `k` unique per encryption.

**Why SpendAuthG?** The Pallas keypair (`pallas-keygen`) is generated using `elgamal.KeyGen`, which uses SpendAuthG as the generator. The ECIES ephemeral key must use the same generator so that the ECDH shared secrets match: `e * (sk * SpendAuthG) == sk * (e * SpendAuthG)`. Using the standard Pallas generator would break this equality.

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

**`MsgSubmitTally`** is auto-injected via `PrepareProposal` when a round enters TALLYING (same pattern as auto-ack). Cannot be submitted through the mempool.

### PrepareProposal / ProcessProposal Pipeline

`PrepareProposal` composes two injectors that run sequentially on each proposed block:
1. **Ceremony ack injection** — if ceremony is DEALT and the proposer hasn't acked, inject `MsgAckExecutiveAuthorityKey`
2. **Tally injection** — if a round is TALLYING, decrypt accumulators and inject `MsgSubmitTally`

`ProcessProposal` validates all injected txs on non-proposer validators before accepting a block. Both `MsgAckExecutiveAuthorityKey` and `MsgSubmitTally` are blocked from the mempool (CheckTx rejects them).

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
