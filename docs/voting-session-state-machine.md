# Voting Session State Machine

> Extracted from [cosmos-sdk-messages-spec.md](./cosmos-sdk-messages-spec.md)

---

## Session States

```protobuf
enum SessionStatus {
  SESSION_STATUS_UNSPECIFIED = 0;
  SESSION_STATUS_ACTIVE      = 1;  // Accepting votes
  SESSION_STATUS_TALLYING    = 2;  // Vote window closed, awaiting tally
  SESSION_STATUS_FINALIZED   = 3;  // Tally submitted and verified
}
```

| Status | Description | Allowed Messages |
|---|---|---|
| `ACTIVE` | Session is accepting delegations, votes, and share reveals | `MsgDelegateVote`, `MsgCastVote`, `MsgRevealShare` |
| `TALLYING` | Vote window closed, awaiting tally submission | `MsgRevealShare` (grace period only), `MsgSubmitTally` |
| `FINALIZED` | Tally submitted and verified; session is read-only | None (queries only) |

---

## State Transition Diagram

```
                    MsgCreateVotingSession
                           │
                           ▼
                  ┌─────────────────┐
                  │  ACTIVE session  │
                  └────────┬────────┘
                           │
              ┌────────────┼────────────┐
              │            │            │
              ▼            ▼            ▼
       MsgDelegateVote  MsgCastVote  MsgRevealShare
              │            │            │
              ▼            ▼            ▼
       ┌──────────┐  ┌──────────┐  ┌────────────────┐
       │ Gov null  │  │ VAN null │  │ Share null set  │
       │   set     │  │   set    │  │ Tally accum.    │
       │ VCT: +VAN │  │ VCT: +VAN│  └────────────────┘
       └──────────┘  │      +VC  │
                     └──────────┘
                           │
                    vote_end_time reached
                           │
                           ▼
                  ┌─────────────────┐
                  │ TALLYING session │◄── MsgRevealShare still accepted
                  └────────┬────────┘    (grace period)
                           │
                    MsgSubmitTally
                           │
                           ▼
                  ┌─────────────────┐
                  │ FINALIZED session│
                  └─────────────────┘
```

---

## Per-Message State Changes

| Message | Gov Null Set | VAN Null Set | Share Null Set | Vote Commitment Tree | Tally Accum |
|---|---|---|---|---|---|
| `MsgDelegateVote` | +4 entries | — | — | +1 leaf (VAN) | — |
| `MsgCastVote` | — | +1 entry | — | +2 leaves (new VAN + VC) | — |
| `MsgRevealShare` | — | — | +1 entry | — | +(C1,C2) accumulation |
| `MsgSubmitTally` | — | — | — | — | Results stored |

---

## Transition Details

### ACTIVE → ACTIVE (within voting window)

**MsgDelegateVote** (Phase 2):
1. Record 4 gov nullifiers into the gov nullifier set
2. Append VAN (`gov_comm`) to the vote commitment tree
3. Update tree root

**MsgCastVote** (Phase 3):
1. Record VAN nullifier into the VAN nullifier set
2. Append new VAN (`vote_authority_note_new`) to the vote commitment tree
3. Append vote commitment to the vote commitment tree
4. Update tree root

**MsgRevealShare** (Phase 5):
1. Record share nullifier into the share nullifier set
2. Accumulate encrypted share into tally: point-wise addition of `(C1, C2)`
3. Increment share count

### ACTIVE → TALLYING

Triggered automatically when `block_time >= vote_end_time`.

- `MsgDelegateVote` and `MsgCastVote` are **rejected**
- `MsgRevealShare` is still **accepted** during the grace period (`vote_end_time + SHARE_GRACE_PERIOD`)

### TALLYING → FINALIZED

**MsgSubmitTally**:
1. Store `TallyResult` for each `(proposal_id, vote_decision)` pair
2. Set `session.status = SESSION_STATUS_FINALIZED`

---

## Nullifier Sets

Three independent nullifier sets per voting round prevent double-spending at each protocol phase:

| Set | Key Pattern | Populated By | Prevents |
|---|---|---|---|
| **Gov nullifier set** | `gov_null/{round_id}/{nullifier}` | `MsgDelegateVote` | Double-delegation of the same note |
| **VAN nullifier set** | `van_null/{round_id}/{nullifier}` | `MsgCastVote` | Double-vote with the same VAN |
| **Share nullifier set** | `share_null/{round_id}/{nullifier}` | `MsgRevealShare` | Double-count of the same share |

---

## Vote Commitment Tree

An append-only Poseidon Merkle tree (depth 32) shared by Vote Authority Notes (VANs) and Vote Commitments (VCs), with domain separation:

- `DOMAIN_VAN = 0` — prepended to VAN leaf preimage
- `DOMAIN_VC = 1` — prepended to VC leaf preimage

Leaves are inserted in transaction order:

| Inserted By | Leaf Contents |
|---|---|
| `MsgDelegateVote` | `vote_authority_note` (the VAN commitment) |
| `MsgCastVote` | `vote_authority_note_new` AND `vote_commitment` (two leaves per tx) |

The chain maintains a rolling window of recent tree roots to support concurrent proof generation.
