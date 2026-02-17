# zally

Cosmos SDK application chain for private voting using Zcash-derived cryptography.

## Technical Assumptions

1. The chain launches with a single genesis validator. Additional validators join post-genesis via `MsgCreateValidator`. Validator set changes beyond that are handled via major upgrades or a PoA module (future).
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

The ceremony is a looping state machine. Timeout in any active phase resets to `INITIALIZING`, allowing the ceremony to restart cleanly.

```
        ┌──────────────────────────────────────┐
        │                                      │
        v                                      │
  INITIALIZING ──> REGISTERING ──> DEALT ──> CONFIRMED
                       │              │
                       │   timeout    │  timeout
                       └──────────────┘
                              │
                              v
                        INITIALIZING
```

| From | To | Trigger | Condition |
|------|-----|---------|-----------|
| INITIALIZING / nil | REGISTERING | First `MsgRegisterPallasKey` | Auto-created on first registration |
| REGISTERING | DEALT | `MsgDealExecutiveAuthorityKey` | >= 1 validator registered, valid ea_pk, 1:1 payload-to-validator mapping |
| REGISTERING | INITIALIZING | EndBlocker timeout | `block_time >= phase_start + phase_timeout` (full reset) |
| DEALT | CONFIRMED | `MsgAckExecutiveAuthorityKey` | **All** registered validators have acked |
| DEALT | INITIALIZING | EndBlocker timeout | `block_time >= phase_start + phase_timeout` (full reset, regardless of partial acks) |

Key behaviors:
- **CONFIRMED** is only reached when every registered validator explicitly acks. Timeout never produces CONFIRMED.
- Timeout in either REGISTERING or DEALT performs a **full reset** to INITIALIZING (all fields cleared).
- After CONFIRMED, no reset mechanism exists.

#### Messages

**`MsgRegisterPallasKey`** -- A validator registers their Pallas public key for the ceremony.
- Creates the ceremony (REGISTERING) on first call, or from INITIALIZING after a reset
- Sets `phase_start` and `phase_timeout` (120s default) when transitioning to REGISTERING
- Validates the key is a valid, non-identity, on-curve Pallas point (32 bytes compressed)
- Rejects duplicate registrations from the same validator address
- Only accepted while ceremony is REGISTERING or INITIALIZING

**`MsgDealExecutiveAuthorityKey`** -- The bootstrap dealer distributes encrypted `ea_sk` shares.
- Validates `ea_pk` is a valid Pallas point
- Requires exactly one ECIES-encrypted payload per registered validator (1:1 mapping)
- Validates each payload's `ephemeral_pk` is a valid Pallas point
- Stores `ea_pk`, payloads, dealer address, updates `phase_start` and `phase_timeout` (30s default)
- Transitions ceremony to DEALT

**`MsgAckExecutiveAuthorityKey`** -- A registered validator acknowledges receipt of their `ea_sk` share.
- Only accepted while ceremony is DEALT
- Rejects acks from non-registered validators
- Rejects duplicate acks from the same validator
- Records the ack with block height
- When all validators have acked, transitions to CONFIRMED

#### Timeout (EndBlocker)

Both REGISTERING and DEALT phases are subject to timeout (`block_time >= phase_start + phase_timeout`):
- On timeout in either phase, the ceremony is **fully reset** to INITIALIZING (all fields cleared)
- Registration timeout: 120 seconds (validators to register)
- Deal/ack timeout: 30 seconds (validators to acknowledge)

#### ECIES Encryption Scheme

Each validator's `ea_sk` share is encrypted using ECIES over the Pallas curve:
1. Ephemeral scalar `e` is generated randomly
2. `E = e * G` (ephemeral public key, stored on-chain)
3. `S = e * pk_i` (ECDH shared secret)
4. `k = SHA256(E_compressed || S.x)` (symmetric key derivation)
5. `ct = ChaCha20-Poly1305(k, nonce=0, ea_sk)` (authenticated encryption)

Validators decrypt by computing `S = sk_i * E` and deriving the same symmetric key.

### Voting Rounds

After the ceremony reaches CONFIRMED, voting sessions can be created.

```
ACTIVE ──> TALLYING ──> FINALIZED
  ^
  │ (gated: requires CONFIRMED ceremony)
```

**`MsgCreateVotingSession`** reads `ea_pk` from the confirmed ceremony state (not from the message). The round stores its own copy of `ea_pk` for future key rotation support.

### On-Chain State (KV Store Keys)

| Key | Type | Description |
|-----|------|-------------|
| `0x09` | `CeremonyState` (singleton) | EA key ceremony lifecycle |
| `0x01` | `VoteRound` (per round) | Voting session state |
| `0x02-0x08` | Various | Nullifiers, tallies, commitment tree, etc. |

### CeremonyState Fields

```protobuf
enum CeremonyStatus {
  CEREMONY_STATUS_UNSPECIFIED   = 0;
  CEREMONY_STATUS_INITIALIZING  = 1; // Waiting for first validator registration
  CEREMONY_STATUS_REGISTERING   = 2; // Accepting validator pk_i registrations
  CEREMONY_STATUS_DEALT         = 3; // DealerTx landed, awaiting acks
  CEREMONY_STATUS_CONFIRMED     = 4; // All validators acked, ea_pk ready
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
