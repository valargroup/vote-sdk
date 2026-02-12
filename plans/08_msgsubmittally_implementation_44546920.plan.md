---
name: MsgSubmitTally implementation
overview: Add MsgSubmitTally message to transition a voting session from TALLYING to FINALIZED, with creator authorization, full ante pipeline validation, wire format encoding, REST endpoint, and comprehensive test coverage across keeper, ante, codec, and ABCI integration layers.
todos:
  - id: proto
    content: Add MsgSubmitTally + MsgSubmitTallyResponse to tx.proto Msg service, regenerate pb.go with buf generate
    status: done
  - id: types-msgs
    content: Add ValidateBasic (vote_round_id non-empty, creator non-empty) and VoteMessage interface implementations for MsgSubmitTally
    status: done
  - id: types-codec
    content: Register MsgSubmitTally in RegisterInterfaces for MsgServiceRouter
    status: done
  - id: types-events
    content: Add EventTypeSubmitTally constant to events.go
    status: done
  - id: keeper-validate
    content: Add ValidateRoundForTally(ctx, roundID, creator) keeper helper — checks TALLYING status + creator match
    status: done
  - id: keeper-handler
    content: Add SubmitTally msg_server handler — transitions round to FINALIZED, emits event
    status: done
  - id: ante-validate
    content: Handle MsgSubmitTally in ante validation pipeline — routes to ValidateRoundForTally instead of ValidateRoundForShares
    status: done
  - id: api-codec
    content: Add TagSubmitTally (0x05) wire tag, update IsVoteTag/TagForMessage/EncodeVoteTx/DecodeVoteTx
    status: done
  - id: api-handler
    content: Add POST /zally/v1/submit-tally REST endpoint in api/handler.go
    status: done
  - id: module
    content: Add ProvideSubmitTallySigner noop signer and register in module init()
    status: done
  - id: fixtures
    content: Add ValidSubmitTally(roundID, creator) test fixture
    status: done
  - id: tests
    content: "Add tests: keeper SubmitTally (happy path, wrong status, creator mismatch, event emission, finalized-rejects-shares), ante SubmitTally (tallying/active/finalized/creator-mismatch/recheck), codec (encode/decode/tag), ABCI integration (full lifecycle, creator auth, active-round rejection)"
    status: done
isProject: false
---

# MsgSubmitTally Implementation

## Motivation

The voting session lifecycle has three phases: ACTIVE → TALLYING → FINALIZED. The transition from ACTIVE to TALLYING is automatic via the EndBlocker when `vote_end_time` expires. However, the transition from TALLYING to FINALIZED was missing — there was no way for the election authority to signal that tallying is complete and lock the session from further `MsgRevealShare` submissions.

`MsgSubmitTally` fills this gap: the session creator submits it to finalize the round, preventing any further state changes.

## Design Decisions

- **Creator authorization**: Only the session creator (the `creator` field stored in `VoteRound`) can submit `MsgSubmitTally`. This provides simple ownership-based authorization without requiring new key types.
- **No cryptographic proof**: Unlike the three ZKP messages, `MsgSubmitTally` requires no ZKP or RedPallas signature. Authorization is via creator identity match only.
- **Strict TALLYING requirement**: The round MUST be in `SESSION_STATUS_TALLYING` — not ACTIVE, not FINALIZED. This prevents premature finalization before the EndBlocker has transitioned the round.
- **Wire tag 0x05**: Extends the existing tag range (0x01–0x04) to 0x01–0x05, maintaining the simple sequential scheme.
- **No nullifiers**: `MsgSubmitTally` has no nullifiers — `GetNullifiers()` returns nil, matching `MsgCreateVotingSession`.

## Proto Changes

### [sdk/proto/zvote/v1/tx.proto](sdk/proto/zvote/v1/tx.proto)

Added `SubmitTally` RPC to the `Msg` service and two new message types:

```protobuf
message MsgSubmitTally {
  bytes  vote_round_id = 1;
  string creator       = 2;  // Must match the session creator
}

message MsgSubmitTallyResponse {}
```

## Validation Pipeline

### ValidateBasic (stateless)
- `vote_round_id` must be non-empty
- `creator` must be non-empty

### Ante Handler (stateful)
- Round must exist (KV read)
- Round must be in `SESSION_STATUS_TALLYING` (not ACTIVE or FINALIZED)
- `msg.Creator` must match `round.Creator`
- No nullifier checks (no nullifiers)
- No cryptographic proof checks (no ZKP/signature)

The ante pipeline routes `MsgSubmitTally` to `ValidateRoundForTally()` instead of the generic `ValidateRoundForShares()` used by `MsgRevealShare`, because submit-tally requires strictly TALLYING status and creator verification.

## Keeper Handler

`SubmitTally` in `msg_server.go`:
1. Fetches the round, validates status is TALLYING and creator matches
2. Updates status to `SESSION_STATUS_FINALIZED`
3. Writes updated round back to KV store
4. Emits `submit_tally` event with round ID, creator, old/new status

## Wire Format

Tag `0x05` maps to `MsgSubmitTally`. The `IsVoteTag()` range was updated from `0x01–0x04` to `0x01–0x05`.

## REST API

```
POST /zally/v1/submit-tally → MsgSubmitTally
```

Accepts JSON body with `vote_round_id` (base64) and `creator` fields.

## Test Coverage

### Keeper tests (`msg_server_test.go`)
- Happy path: TALLYING → FINALIZED
- Rejected: round is ACTIVE
- Rejected: round is already FINALIZED
- Rejected: creator mismatch
- Rejected: round not found
- Event emission verification
- FINALIZED round status preserved after submit

### Ante tests (`validate_test.go`)
- Happy path: tallying round + correct creator
- ValidateBasic failures: empty round ID, empty creator
- Round state: not found, ACTIVE rejected, FINALIZED rejected
- Creator mismatch rejected
- RecheckTx passes with tallying round

### Codec tests (`codec_test.go`)
- Encode/decode round-trip
- Tag mapping
- Updated invalid-tag boundary (0x06 instead of 0x05)

### ABCI integration tests (`abci_test.go`)
- Full lifecycle: ACTIVE → delegate → cast → TALLYING → reveal → submit-tally → FINALIZED → reveal-rejected
- Creator authorization: wrong creator rejected, correct creator succeeds
- Active round rejection: cannot finalize before TALLYING

## File Change Summary

- **Proto** (1 file): `sdk/proto/zvote/v1/tx.proto`
- **Generated** (2 files): `tx.pb.go`, `tx_grpc.pb.go`
- **Types** (3 files): `msgs.go`, `codec.go`, `events.go`
- **Keeper** (2 files): `keeper.go` (ValidateRoundForTally), `msg_server.go` (SubmitTally handler)
- **Ante** (1 file): `validate.go` (MsgSubmitTally routing)
- **API** (2 files): `codec.go` (tag 0x05), `handler.go` (REST endpoint)
- **Module** (1 file): `module.go` (signer provider)
- **Test fixtures** (1 file): `fixtures.go` (ValidSubmitTally)
- **Tests** (5 files): `msg_server_test.go`, `validate_test.go`, `validate_test.go` (types), `codec_test.go`, `abci_test.go`
