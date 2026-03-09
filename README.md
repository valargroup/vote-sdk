# shielded-vote

Cosmos SDK application chain for private voting using Zcash-derived cryptography.

## Technical Assumptions

1. The chain launches with a single genesis validator. Additional validators join post-genesis via `MsgCreateValidatorWithPallasKey`, which atomically creates the validator and registers their Pallas key for the ceremony. Raw `MsgCreateValidator` is blocked in the ante handler for live transactions. Validator set changes beyond that are handled via major upgrades or a PoA module (future).
2. Client interaction avoids Cosmos SDK protobuf encoding:
   - **Tx submission:** Client sends a plain JSON POST; server handler parses JSON and encodes as needed.
   - **Query:** gRPC gateway supports JSON out-of-the-box.
3. No native `x/gov` module. The vote module implements custom private voting instead of reusing standard Cosmos governance.

## Architecture

### Module: `x/vote`

The vote module has two major subsystems: the **EA Key Ceremony** (automatic per voting round) and **Voting Rounds** (transition from PENDING through ceremony to ACTIVE).

### EA Key Ceremony (Per-Round)

The EA key ceremony runs **automatically per voting round**. Each `MsgCreateVotingSession` creates a round in `PENDING` status and snapshots all eligible validators (bonded + registered Pallas key) into the round's ceremony fields. The ceremony proceeds automatically via `PrepareProposal` — no manual intervention is needed after initial Pallas key registration.

#### Per-Round Ceremony State Machine

Ceremony state is stored on the `VoteRound` itself (fields `ceremony_status`, `ceremony_validators`, etc.). There is no global singleton ceremony state.

```
  PENDING (REGISTERING) ──> PENDING (DEALT) ──> ACTIVE (CONFIRMED)
                                  │                (all acked)
                       timeout    │
                       (< 1/2)   │ timeout (≥ 1/2)
                          │       │
                          v       v
                    REGISTERING   ACTIVE (CONFIRMED)
                    (reset for    + strip non-ackers
                     re-deal)
```

| From        | To                 | Trigger                       | Condition                                       |
| ----------- | ------------------ | ----------------------------- | ----------------------------------------------- |
| REGISTERING | DEALT              | Auto-deal via PrepareProposal | Block proposer is a ceremony validator          |
| DEALT       | CONFIRMED + ACTIVE | MsgAckExecutiveAuthorityKey   | All validators acked (fast path)                |
| DEALT       | CONFIRMED + ACTIVE | EndBlocker timeout            | >= 1/2 acked at timeout; non-ackers stripped    |
| DEALT       | REGISTERING        | EndBlocker timeout            | < 1/2 acked; reset for re-deal by next proposer |

Key behaviors:
- **Fast path vs timeout** — the fast path confirms when ALL validators ack (no stripping needed). The timeout path confirms with >= 1/2 acks (integer arithmetic: `acks * 2 >= validators`) and strips non-ackers.
- **Auto-deal** — the block proposer automatically deals when it detects a PENDING round in REGISTERING state. No manual `ceremony.sh deal` step.
- **Auto-ack** — each block proposer auto-acks via PrepareProposal when it detects a DEALT round.
- **Non-acker stripping** — validators who fail to ack within the timeout are stripped from the round's ceremony (removed from `ceremony_validators` and `ceremony_payloads`). No miss counters or ceremony-based jailing — liveness enforcement is handled by `x/slashing` block-miss detection.
- **Ceremony log** — each state transition appends a timestamped entry to `ceremony_log` on the round, visible in queries and the admin UI.

#### Pallas Key Registration (One-Time)

Validators register their Pallas key once via `MsgRegisterPallasKey` or `MsgCreateValidatorWithPallasKey`. Keys are stored in a global registry (prefix `0x0C`) and persist across rounds.

#### Auto-Deal and Auto-Ack via PrepareProposal

`PrepareProposal` composes two ceremony injectors:
1. **Auto-deal** — if a PENDING round is in REGISTERING state and the proposer is a ceremony validator, generate `ea_sk`, Shamir-split it into `(t, n)` shares, ECIES-encrypt `share_i` to each validator, publish `VK_i = share_i * G` and `threshold = ceil(n/2)`, and inject `MsgDealExecutiveAuthorityKey`.
2. **Auto-ack** — if a PENDING round is in DEALT state and the proposer hasn't acked, decrypt the payload to recover their share, verify `share_i * G == VK_i` (threshold mode) or `ea_sk * G == ea_pk` (legacy), inject `MsgAckExecutiveAuthorityKey`, and write the share/key to disk.

#### Timeout (EndBlocker)

Only the DEALT phase has a timeout (default: 30 minutes). On timeout:
- **>= 1/2 acked:** Confirm ceremony, strip non-ackers, activate round.
- **< 1/2 acked:** Reset to REGISTERING for re-deal by the next proposer.

#### ECIES Encryption Scheme

Each validator's `ea_sk` share is encrypted using ECIES over the Pallas curve with **SpendAuthG** as the generator:

1. `E = e * SpendAuthG` (ephemeral public key)
2. `S = e * pk_i` (ECDH shared secret)
3. `k = SHA256(E_compressed || S.x)` (symmetric key)
4. `ct = ChaCha20-Poly1305(k, nonce=0, ea_sk)` (authenticated encryption)

### VoteManager Role

The VoteManager is a singleton on-chain address that gates who can create voting sessions. Before any `MsgCreateVotingSession` is accepted, a VoteManager must be set.

**`MsgSetVoteManager`** -- Reassigns the VoteManager role.
- Only the current VoteManager can call this — validators cannot reassign it
- Transfers the caller's full `usvote` balance to the new manager atomically
- The VoteManager must be set in genesis (no bootstrap path)
- Uses custom wire format tag `0x0C` and REST endpoint `POST /shielded-vote/v1/set-vote-manager`
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

#### Rationale

The standard Cosmos SDK `Tx` envelope requires a signer address, fee fields, and a conventional signature (secp256k1 or ed25519). Vote-round messages (`MsgDelegateVote`, `MsgCastVote`, `MsgRevealShare`) cannot use this envelope because they are authenticated via **ZKP + RedPallas spend-auth signatures** — there is no conventional Cosmos account involved. Similarly, `MsgDealExecutiveAuthorityKey`, `MsgAckExecutiveAuthorityKey`, and `MsgSubmitPartialDecryption` are **auto-injected by the block proposer** via `PrepareProposal` and are never client-signed at all.

The custom wire format is the minimal encoding that satisfies both cases: a single-byte type tag lets the `TxDecoder` unambiguously identify the message type without parsing a full `TxBody`, and the tag byte acts as the sole discriminator between the custom path and the standard Cosmos SDK path. Messages that do have a conventional signer (ceremony setup messages, `MsgCreateVotingSession`) still use the standard `Tx` envelope and flow through normal signature verification.

#### Wire Format

Each custom transaction is a 1-byte tag followed by a protobuf-encoded message body:

```
[tag (1 byte)] [proto-encoded message body]
```

| Tag    | Message                           | Category                | Auth mechanism              |
| ------ | --------------------------------- | ----------------------- | --------------------------- |
| `0x01` | `MsgCreateVotingSession`          | Voting round            | Standard Cosmos Tx (signed) |
| `0x02` | `MsgDelegateVote`                 | Voting round            | ZKP #1 + RedPallas          |
| `0x03` | `MsgCastVote`                     | Voting round            | ZKP #2 + RedPallas          |
| `0x04` | `MsgRevealShare`                  | Voting round            | ZKP #3                      |
| `0x05` | `MsgSubmitTally`                  | Voting round (injected) | Proposer identity check     |
| `0x06` | `MsgRegisterPallasKey`            | Ceremony                | Standard Cosmos Tx (signed) |
| `0x07` | `MsgDealExecutiveAuthorityKey`    | Ceremony (injected)     | Proposer identity check     |
| `0x08` | `MsgAckExecutiveAuthorityKey`     | Ceremony (injected)     | Proposer identity check     |
| `0x09` | `MsgCreateValidatorWithPallasKey` | Ceremony                | Standard Cosmos Tx (signed) |
| `0x0C` | `MsgSetVoteManager`               | Management              | Standard Cosmos Tx (signed) |
| `0x0D` | `MsgSubmitPartialDecryption`      | Tallying (injected)     | Proposer identity check     |

Any transaction whose first byte does not match a known tag is decoded as a standard Cosmos SDK `Tx`. Tag `0x0A` is deliberately skipped because it collides with the standard Cosmos Tx protobuf encoding (field 1, wire type 2) — this collision is what makes the two decoders unambiguously distinguishable by a single byte peek. Note that raw `MsgCreateValidator` is blocked by the ante handler for live transactions -- post-genesis validators must use `MsgCreateValidatorWithPallasKey` (tag `0x09`) instead.

### Message Authentication Invariants

Every message has a specific set of auth checks enforced across the ABCI pipeline. The table below lists the complete check set for each message. "PP" = PrepareProposal, "ProcessProp" = ProcessProposal.

#### Standard Cosmos SDK messages

These flow through the standard ante chain: signature verification (`SigVerificationDecorator`), then `CeremonyValidatorDecorator` for validator-gated types.

| Message                           | Who can submit               | Ante checks                                                            | MsgServer checks                                                                                                         |
| --------------------------------- | ---------------------------- | ---------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------ |
| `MsgRegisterPallasKey`            | Any bonded validator         | secp256k1 sig + `CeremonyValidatorDecorator` (bonded validator gate)   | Valid Pallas point; no duplicate registration                                                                            |
| `MsgCreateValidatorWithPallasKey` | Anyone (becomes a validator) | secp256k1 sig; exempt from `CeremonyValidatorDecorator`                | Delegates to `x/staking` `CreateValidator`; registers Pallas key; rejects duplicates                                     |
| `MsgSetVoteManager`               | Current VoteManager only     | secp256k1 sig; exempt from `CeremonyValidatorDecorator` (has own auth) | `ValidateVoteManagerOnly`: only the current VoteManager can reassign; transfers full `usvote` balance to the new manager |
| `MsgCreateVotingSession`          | VoteManager only             | secp256k1 sig (standard Cosmos Tx)                                     | `ValidateVoteManagerOnly`: creator must be the on-chain VoteManager address                                              |
| `MsgCreateValidator`              | **Blocked** post-genesis     | Ante handler rejects at `BlockHeight > 0`                              | N/A — never reaches MsgServer                                                                                            |

#### Vote-round messages (custom wire format, ZKP/RedPallas auth)

These use the custom wire format and bypass the Cosmos Tx envelope. Auth is handled by the `ValidateVoteTx` pipeline in `x/vote/ante`.

| Message           | Who can submit                    | Ante checks                                                                                                                                                                      | MsgServer checks                                                               |
| ----------------- | --------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------ |
| `MsgDelegateVote` | Any Zcash note holder (anonymous) | `ValidateBasic` (field sizes); round ACTIVE; gov nullifier uniqueness; RedPallas sig over sighash; ZKP #1 (delegation proof: note ownership, VAN encoding, nc_root, nf_imt_root) | Record gov nullifiers; append van_cmx to commitment tree                       |
| `MsgCastVote`     | Any delegation holder (anonymous) | `ValidateBasic`; round ACTIVE; VAN nullifier uniqueness; RedPallas sig over canonical sighash; ZKP #2 (vote commitment: VAN ownership, ea_pk binding, commitment tree anchor)    | Record VAN nullifier; append vote_authority_note_new + vote_commitment to tree |
| `MsgRevealShare`  | Any vote holder (anonymous)       | `ValidateBasic`; round ACTIVE or TALLYING; share nullifier uniqueness; ZKP #3 (vote share: share ownership, commitment tree anchor)                                              | Record share nullifier; HomomorphicAdd enc_share into tally accumulator        |

#### Proposer-injected messages (custom wire format, proposer identity auth)

These are auto-injected by `PrepareProposal` and **cannot be submitted through the mempool** (CheckTx/ReCheckTx reject them). Auth is enforced at three layers: ante handler (per-tag dispatch), ProcessProposal (non-proposer validators verify before accepting a block), and MsgServer (FinalizeBlock execution).

| Message                        | Ante check                                                                           | ProcessProposal check                                                                                                           | MsgServer check                                                                                                                                                           |
| ------------------------------ | ------------------------------------------------------------------------------------ | ------------------------------------------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `MsgDealExecutiveAuthorityKey` | `ValidateProposerIsCreator` (mempool block + creator == proposer)                    | Round PENDING + REGISTERING; payload count matches; creator is ceremony validator; creator == proposer                          | `ValidateProposerIsCreator`; round PENDING + REGISTERING; creator in ceremony validators; ea_pk valid; payloads 1:1 with validators; threshold + VK validation            |
| `MsgAckExecutiveAuthorityKey`  | `ValidateProposerIsCreator` (mempool block + creator == proposer)                    | Round PENDING + DEALT; creator is ceremony validator; no duplicate ack; creator == proposer                                     | `ValidateProposerIsCreator`; round PENDING + DEALT; creator in ceremony validators; no duplicate ack                                                                      |
| `MsgSubmitPartialDecryption`   | `ValidateProposerIsCreator` (mempool block + creator == proposer)                    | Round TALLYING + threshold > 0; creator is ceremony validator; ValidatorIndex == ShamirIndex; no duplicate; creator == proposer | `ValidateProposerIsCreator`; round TALLYING + threshold > 0; creator in ceremony validators; ValidatorIndex == ShamirIndex; no duplicate; entries are valid Pallas points |
| `MsgSubmitTally`               | `ValidateVoteTx` → `ValidateProposerIsCreator` (mempool block + creator == proposer) | Round TALLYING; creator == proposer                                                                                             | Round TALLYING; verify each entry against on-chain accumulators (DLEQ proof in legacy mode, Lagrange re-derivation in threshold mode); transition to FINALIZED            |

#### Key design invariant

`ValidateProposerIsCreator` is the unified proposer identity check shared by all four injected message types. It enforces two properties:

1. **Mempool exclusion**: `IsCheckTx() || IsReCheckTx()` returns an error, preventing external submission.
2. **Proposer binding**: During FinalizeBlock, `msg.Creator` must equal the operator address of the validator whose consensus key matches `BlockHeader.ProposerAddress`. This prevents a malicious proposer from injecting messages on behalf of other validators.

### REST API

The chain exposes a JSON REST API alongside CometBFT RPC. Clients POST JSON bodies for transaction submission and GET for queries — no protobuf encoding required on the client side.

#### Transaction Endpoints (Custom Wire Format)

Vote-round messages use the custom wire format and are submitted as JSON POST requests:

| Method | Path                              | Description                        |
| ------ | --------------------------------- | ---------------------------------- |
| POST   | `/shielded-vote/v1/delegate-vote` | Submit a delegation proof (ZKP #1) |
| POST   | `/shielded-vote/v1/cast-vote`     | Cast an encrypted vote (ZKP #2)    |
| POST   | `/shielded-vote/v1/reveal-share`  | Reveal an encrypted share (ZKP #3) |

These endpoints accept JSON, encode the message with the custom wire format, and broadcast via CometBFT's `broadcast_tx_sync`. `MsgSubmitTally`, `MsgDealExecutiveAuthorityKey`, `MsgAckExecutiveAuthorityKey`, and `MsgSubmitPartialDecryption` have no REST endpoints — they are proposer-only and auto-injected via PrepareProposal.

Ceremony and management messages (`MsgRegisterPallasKey`, `MsgCreateValidatorWithPallasKey`, `MsgSetVoteManager`, `MsgCreateVotingSession`) are standard Cosmos SDK transactions routed through the MsgServiceRouter. They can be submitted via the Cosmos SDK CLI or gRPC gateway.

#### Query Endpoints

| Method | Path                                               | Description                                |
| ------ | -------------------------------------------------- | ------------------------------------------ |
| GET    | `/shielded-vote/v1/ceremony`                       | Current ceremony state and status          |
| GET    | `/shielded-vote/v1/rounds`                         | List all stored vote rounds                |
| GET    | `/shielded-vote/v1/rounds/active`                  | Currently active voting round              |
| GET    | `/shielded-vote/v1/round/{round_id}`               | Voting round by hex round ID               |
| GET    | `/shielded-vote/v1/vote-summary/{round_id}`        | Denormalized round summary with proposals  |
| GET    | `/shielded-vote/v1/tally/{round_id}/{proposal_id}` | Tally for a specific proposal              |
| GET    | `/shielded-vote/v1/tally-results/{round_id}`       | All tally results for a round              |
| GET    | `/shielded-vote/v1/commitment-tree/{height}`       | Vote commitment tree at block height       |
| GET    | `/shielded-vote/v1/commitment-tree/latest`         | Latest vote commitment tree                |
| GET    | `/shielded-vote/v1/commitment-tree/leaves`         | Tree leaves (`?from_height=X&to_height=Y`) |
| GET    | `/shielded-vote/v1/pallas-keys`                    | All registered Pallas keys                 |
| GET    | `/shielded-vote/v1/vote-manager`                   | Current VoteManager address                |
| GET    | `/shielded-vote/v1/genesis`                        | Chain genesis JSON                         |
| GET    | `/shielded-vote/v1/snapshot-data/{height}`         | Nullifier snapshot data at block height    |
| GET    | `/shielded-vote/v1/tx/{hash}`                      | Transaction status by hash                 |

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
  CEREMONY_STATUS_CONFIRMED     = 3; // All acked (fast path) or >=1/2 acked at timeout, ea_pk ready
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
