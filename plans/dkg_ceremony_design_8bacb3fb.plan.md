---
name: DKG ceremony design
overview: Replace the single-dealer ceremony with Joint-Feldman DKG, where each validator generates their own polynomial and distributes shares to all others. The combined ea_sk is never known to any single party. The change reuses all existing ceremony statuses, crypto primitives, and the tally pipeline unchanged.
todos:
  - id: p1-proto
    content: "Phase 1: Add DKGContribution type, MsgContributeDKG, dkg_contributions field, TagContributeDKG codec, signer, ante/ProcessProposal wiring (additive — existing dealer path untouched)"
    status: completed
  - id: p2-combine
    content: "Phase 2: Add CombineCommitments helper in shamir/feldman.go — point-wise sum of n commitment vectors, input validation, full test coverage (algebraic equivalence, combined share verification, single contributor, edge cases)"
    status: completed
  - id: p3-handler
    content: "Phase 3: ContributeDKG handler — validate contributions, store in DkgContributions, finalize via CombineCommitments on n-th contribution (REGISTERING→DEALT), FindContributionInRound helper, ErrDuplicateContribution, EventTypeContributeDKG, 20 tests covering happy path, partial accumulation, real-crypto integration, and 13 rejection cases"
    status: completed
  - id: p4-injector
    content: "Phase 4: Implement CeremonyDKGContributionPrepareProposalHandler — generate random polynomial, shamir.Split, FeldmanCommit, persist coefficients to disk via writeCoeffs, ECIES encrypt n-1 shares (excludes self), inject MsgContributeDKG. coeffsPathForRound helper. Not wired into ComposedPrepareProposalHandler yet."
    status: completed
  - id: p4-injector-tests
    content: "Phase 4 tests (5): injection happy path (correct tag, t commitments, n-1 payloads, no self-reference), coefficients round-trip (parse back, FeldmanCommit matches published), ECIES envelopes decryptable + Feldman-verified, skips when already contributed, skips when not ceremony validator, skips when no Pallas key"
    status: completed
  - id: p5-ack
    content: "Phase 5: DKG-aware ack path — refactor CeremonyAckPrepareProposalHandler to branch on DKG vs dealer, extract ackDealerRound and ackDKGRound helpers, export EvalPolynomial in shamir, add loadCoeffs/zeroAndDeleteCoeffsFile helpers. DKG path: load coefficients → eval own partial → decrypt + verify per-contributor shares → sum combined → verify against combined commitments → delete coefficients file"
    status: completed
  - id: p5-ack-tests
    content: "Phase 5 tests (5): DKG ack happy path (3 validators, real crypto, combined share = sum of all shares, Feldman-verified, coefficients deleted), bad share rejection (tampered ECIES payload → no ack), single validator (n=1 edge case), plus 2 existing dealer ack tests pass unchanged after refactor"
    status: completed
  - id: p6-wire
    content: "Phase 6: Wire DKG — swap CeremonyDealPrepareProposalHandler for CeremonyDKGContributionPrepareProposalHandler in ComposedPrepareProposalHandler (app.go), clear DkgContributions on ceremony timeout reset in EndBlocker (both reset paths in module.go), filter DkgContributions in StripNonAckersFromRound (keeper_ceremony.go), add NextBlockWithTxs testutil helper, update dealer-path integration tests to invoke deal handler directly (callDealHandler), update PrepareProposal→ProcessProposal pipeline test to expect MsgContributeDKG"
    status: completed
  - id: p6-wire-tests
    content: "Phase 6 tests (4 new + updated): TestDKGContributionThroughPipeline (CallPrepareProposal injects MsgContributeDKG), TestEndBlockerClearsDKGContributionsOnTimeout (DEALT timeout resets DkgContributions to nil), TestPrepareProposalDKGContributionAcceptedByProcessProposal (n=2..7 pipeline round-trip), StripNonAckersFromRound updated with DkgContributions assertions. All existing dealer integration tests updated to bypass pipeline via callDealHandler — behavior unchanged."
    status: completed
  - id: p7-integration
    content: "Phase 7: Multi-validator DKG integration test -- n validators contribute, all ack, tally produces correct result"
    status: pending
  - id: p8-docs
    content: "Phase 8: Rewrite tss-ceremony.md Step 4 with full DKG design, security rationale (bias analysis, why no COMMITTING phase, why no vote extensions), and updated security properties"
    status: pending
  - id: p9-cleanup
    content: "Phase 9: Remove single-dealer remnants — delete DealExecutiveAuthorityKey handler, CeremonyDealPrepareProposalHandler, CLI command, ceremony_payloads/ceremony_dealer proto fields, TagDealExecutiveAuthorityKey, all dealer-specific tests"
    status: pending
isProject: false
---

# Joint-Feldman DKG: Minimal Wiring Design

## Core Idea

Each validator independently generates a random polynomial, publishes Feldman commitments and ECIES-encrypted shares for every other validator. The combined EA key is `ea_pk = sum(C_{i,0})` -- no single party ever knows `ea_sk = sum(s_i)`.

## Why Single-Phase (No Separate COMMITTING)

Standard Pedersen DKG separates commitment from share distribution to prevent the last participant from biasing `ea_pk`. On this chain, the last proposer to contribute *can* still see previous commitments and adapt their `C_{i,0}`.

However, **biasing `ea_pk` does not help the attacker** in our model -- `ea_pk` is used only for ElGamal vote encryption, and the attacker still cannot learn `ea_sk` (the sum of all secret terms). The security goal is that no single party knows `ea_sk`, which holds as long as at least one validator is honest, regardless of `ea_pk` distribution.

A separate COMMITTING phase would add an extra state, an extra message type, and ~n extra blocks of latency for no practical security gain. If desired later, it can be added as an intermediate phase.

## State Machine (Unchanged Statuses)

```
REGISTERING ──[n x MsgContributeDKG]──> DEALT ──[n x MsgAck]──> CONFIRMED ──> ACTIVE
```

Compare to current:

```
REGISTERING ──[1 x MsgDeal]──────────> DEALT ──[n x MsgAck]──> CONFIRMED ──> ACTIVE
```

The only structural change: REGISTERING -> DEALT requires `n` messages instead of 1. The ack phase and all transitions after are identical.

## Protobuf Changes

### New type in [types.proto](vote-sdk/proto/svote/v1/types.proto)

```protobuf
message DKGContribution {
  string         validator_address   = 1;  // contributor
  repeated bytes feldman_commitments = 2;  // C_{i,j} for j=0..t-1 (t points)
  repeated DealerPayload payloads    = 3;  // ECIES envelopes, one per OTHER validator (n-1 entries)
}
```

### New field on `VoteRound`

```protobuf
repeated DKGContribution dkg_contributions = 28;
```

Existing fields **removed** (no backward compat needed):

- `ceremony_payloads` (field 17) -- delete; payloads live inside `dkg_contributions`
- `ceremony_dealer` (field 19) -- delete; no single dealer

Existing fields **repurposed** (set at REGISTERING -> DEALT transition):

- `feldman_commitments` (field 24) -- stores **combined** commitments `C_j = sum_i(C_{i,j})`
- `ea_pk` (field 10) -- stores `sum_i(C_{i,0}).Compress()`

Also remove the `dealer` field (6) from `CeremonyState` and `payloads` field (4) from `CeremonyState`.

### New message in [tx.proto](vote-sdk/proto/svote/v1/tx.proto)

```protobuf
rpc ContributeDKG(MsgContributeDKG) returns (MsgContributeDKGResponse);

message MsgContributeDKG {
  string                 creator             = 1;
  bytes                  vote_round_id       = 2;
  repeated bytes         feldman_commitments = 3;  // t compressed Pallas points
  repeated DealerPayload payloads            = 4;  // n-1 ECIES envelopes
}

message MsgContributeDKGResponse {}
```

`MsgDealExecutiveAuthorityKey` is deleted from proto entirely (message + RPC). `MsgAckExecutiveAuthorityKey` is reused unchanged.

### Codec tag in [api/codec.go](vote-sdk/api/codec.go)

```go
TagContributeDKG byte = 0x0E
```

Add to `IsCeremonyTag`, `DecodeCeremonyTx`, ante handler routing, and `ProcessProposal` validation.

## On-Chain Handler: `ContributeDKG`

New handler in [msg_server_ceremony.go](vote-sdk/x/vote/keeper/msg_server_ceremony.go):

1. `ValidateProposerIsCreator` (proposer-only, no mempool)
2. `GetPendingRoundWithCeremony(REGISTERING)`
3. Validate creator is in `ceremony_validators`
4. Reject duplicate contribution (scan `dkg_contributions` for creator)
5. Validate `len(feldman_commitments) == threshold`
6. Validate `len(payloads) == n - 1` (all validators except self)
7. Validate each payload references a distinct ceremony validator (not self)
8. Validate all Feldman commitments and ephemeral PKs are valid Pallas points
9. Append to `round.DkgContributions`
10. **If `len(DkgContributions) == n`:** compute combined values and transition:

```go
// Compute combined Feldman commitments and ea_pk
for j := 0; j < t; j++ {
    combined := contributions[0].commitments[j]
    for i := 1; i < n; i++ {
        combined = combined.Add(contributions[i].commitments[j])
    }
    round.FeldmanCommitments[j] = combined.Compress()
}
round.EaPk = round.FeldmanCommitments[0]  // C_0 = ea_pk
round.CeremonyStatus = DEALT
round.CeremonyPhaseStart = blockTime
round.CeremonyPhaseTimeout = DefaultDealTimeout
```

## PrepareProposal Changes

### DKG contribution injector (replaces deal injector)

File: [prepare_proposal_ceremony.go](vote-sdk/app/prepare_proposal_ceremony.go)

`CeremonyDKGPrepareProposalHandler` replaces `CeremonyDealPrepareProposalHandler`:

1. Load Pallas SK (confirm node is configured)
2. Find first PENDING round with REGISTERING status
3. Check proposer is in `ceremony_validators`
4. **Check proposer hasn't already contributed** (scan `dkg_contributions`)
5. Generate random secret `s_i` and polynomial `f_i(x)` of degree `t-1` via `shamir.Split(s_i, t, n)`
6. Compute Feldman commitments via `shamir.FeldmanCommit(G, coeffs)`
7. **Save coefficients to disk**: `<ea_sk_dir>/dkg_coeffs.<hex(round_id)>` (t x 32 bytes, mode 0600)
8. ECIES-encrypt `f_i(j)` to each other validator `j`'s Pallas PK (n-1 envelopes)
9. Zero coefficients and shares from memory
10. Inject `MsgContributeDKG`

### Modified ack injector

The ack injector changes from single-dealer verification to multi-contributor verification:

1. Find first PENDING round with DEALT status (unchanged)
2. Check proposer hasn't acked (unchanged)
3. **Load own coefficients** from `<ea_sk_dir>/dkg_coeffs.<hex(round_id)>`
4. Compute `own_partial = evalPolynomial(coeffs, own_shamir_index)`
5. **For each other validator's contribution** in `round.DkgContributions`:
  - Find the ECIES envelope addressed to self
  - Decrypt with own Pallas SK
  - Parse as Pallas scalar
  - Verify against that contributor's Feldman commitments via `VerifyFeldmanShare`
  - If any fails: log error, skip ack (ceremony will timeout)
6. `combined_share = own_partial + sum(received_shares)`
7. Verify `combined_share` against combined commitments on `round.FeldmanCommitments`
8. Write `combined_share` to disk as `share.<hex(round_id)>`
9. Delete coefficients file
10. Compute ack_signature (same as current: `SHA256("ack" || ea_pk || validator_address)`)
11. Inject `MsgAckExecutiveAuthorityKey` (unchanged message)

## EndBlocker Adaptation

The DEALT timeout path in [module.go](vote-sdk/x/vote/module.go) works mostly as-is. On timeout reset to REGISTERING, also clear `dkg_contributions`:

```go
round.DkgContributions = nil  // add alongside existing ceremony_payloads = nil
```

REGISTERING remains without timeout (same as current -- waits indefinitely for contributions).

## On-Disk Files


| File                         | When written                       | When deleted                        | Contents                              |
| ---------------------------- | ---------------------------------- | ----------------------------------- | ------------------------------------- |
| `dkg_coeffs.<hex(round_id)>` | DKG contribution (PrepareProposal) | Ack (after combined share computed) | t x 32 bytes: polynomial coefficients |
| `share.<hex(round_id)>`      | Ack (PrepareProposal)              | After tally finalized               | 32 bytes: combined Shamir share       |


## What Is Completely Unchanged

- **All crypto primitives**: `shamir.Split`, `FeldmanCommit`, `VerifyFeldmanShare`, `EvalCommitmentPolynomial`, `ecies.Encrypt/Decrypt`, DLEQ proofs
- **Tally pipeline**: partial decryptions, Lagrange interpolation, BSGS -- reads `round.FeldmanCommitments` (now combined) and `round.Threshold` (same formula)
- **VK_i derivation**: `EvalCommitmentPolynomial(combined_commitments, shamir_index)` -- identical call, combined commitments are drop-in
- **CeremonyStatus enum**: reuses REGISTERING, DEALT, CONFIRMED (no new enum values)
- **MsgAckExecutiveAuthorityKey**: message and handler unchanged
- **Threshold formula**: `ThresholdForN(n)` unchanged
- **Pallas key registration**: `MsgRegisterPallasKey` unchanged
- **Round creation**: `CreateVotingSession` snapshot logic unchanged (ShamirIndex assignment)

## Security Properties


| Property                 | Dealer (current)              | DKG (proposed)                        |
| ------------------------ | ----------------------------- | ------------------------------------- |
| Who knows `ea_sk`        | Dealer (in memory, one block) | **Nobody**                            |
| Single party can decrypt | Dealer can                    | **No**                                |
| Bad share detection      | Feldman at ack time           | Feldman at ack time (per contributor) |
| Bad partial decryption   | DLEQ proof                    | DLEQ proof (unchanged)                |
| Liveness (all honest)    | n+1 blocks                    | 2n blocks                             |
| Offline validator        | Ceremony hangs at REGISTERING | Ceremony hangs at REGISTERING         |


## Blocks Required

- DKG contributions: n blocks (one per proposer turn)
- Acks: n blocks (one per proposer turn, possibly overlapping if last contributor and first acker are in same block)
- Total: ~2n blocks worst case (vs n+1 for dealer)

## Development Phases

Each phase is a commit. The existing dealer path works throughout phases 1-5. Phase 6 wires the DKG path into the live pipeline. Phase 9 removes the old dealer code after the DKG path is proven end-to-end.

### Phase 1: Proto + codec boilerplate (additive only)

Purely additive — all existing messages, fields, handlers, and tests remain unchanged and functional.

- [types.proto](vote-sdk/proto/svote/v1/types.proto):
  - Add `DKGContribution` message (validator_address, feldman_commitments, payloads)
  - Add `dkg_contributions` field (28) to `VoteRound`
- [tx.proto](vote-sdk/proto/svote/v1/tx.proto):
  - Add `MsgContributeDKG` + `MsgContributeDKGResponse` message and `ContributeDKG` RPC
- Regenerate Go types
- Add `TagContributeDKG = 0x0E` to [codec.go](vote-sdk/api/codec.go), update `IsCeremonyTag`, add encode/decode case in `DecodeCeremonyTx` (keep `TagDealExecutiveAuthorityKey`)
- Register `DKGContribution` and `MsgContributeDKG` in [types/codec.go](vote-sdk/x/vote/types/codec.go)
- Add `ProvideContributeDKGSigner` (noopSignerFn) in [module.go](vote-sdk/x/vote/module.go) alongside existing `ProvideDealExecutiveAuthorityKeySigner`
- Add `MsgContributeDKG` cases in ante handler, `ceremonyValidatorRequired`, `isVoteModuleMsg`
- Add `CeremonyDKGContributionPrepareProposalHandler` stub in [prepare_proposal_ceremony.go](vote-sdk/app/prepare_proposal_ceremony.go) (no-op, not wired into `app.go`)
- Add `validateInjectedDKGContribution` in [process_proposal.go](vote-sdk/app/process_proposal.go) alongside existing `validateInjectedDeal`
- **Tests**: codec round-trip for `MsgContributeDKG`, `IsCeremonyTag(TagContributeDKG)`, ProcessProposal accepts/rejects DKG contribution, signer completeness

### Phase 2: CombineCommitments helper

Isolated crypto utility, no chain integration.

- Add `CombineCommitments(contributions [][]curvey.Point) ([]curvey.Point, error)` to [feldman.go](vote-sdk/crypto/shamir/feldman.go)
- Takes n commitment vectors (each length t), returns combined vector via point addition
- **Tests**: sum of 3 individual Feldman commitment sets matches the commitment set of the summed polynomial; edge cases (n=1, mismatched lengths)

### Phase 3: On-chain ContributeDKG handler

New handler alongside existing DealExecutiveAuthorityKey. Both coexist.

- Implement `ContributeDKG` in [msg_server_ceremony.go](vote-sdk/x/vote/keeper/msg_server_ceremony.go)
- Add `FindContributionInRound(round, valAddr)` helper to [keeper_ceremony.go](vote-sdk/x/vote/keeper/keeper_ceremony.go)
- Validation: proposer-only, ceremony validator, no duplicate, commitment count = t, payload count = n-1, all valid Pallas points
- On final contribution (n-th): deserialize all commitment vectors, call `CombineCommitments`, set `ea_pk` + `feldman_commitments`, transition to DEALT
- **Tests**: handler rejects non-proposer, non-validator, duplicate, wrong counts; partial accumulation stays REGISTERING; final contribution computes correct combined commitments and transitions to DEALT

### Phase 4: DKG contribution injector ✓

Implemented `CeremonyDKGContributionPrepareProposalHandler` in [prepare_proposal_ceremony.go](vote-sdk/app/prepare_proposal_ceremony.go). Not wired into `ComposedPrepareProposalHandler` yet (Phase 6).

- `coeffsPathForRound(dir, roundID)` → `<dir>/coeffs.<hex(round_id)>` for polynomial coefficient persistence
- `writeCoeffs(path, coeffs)` serializes `t` concatenated 32-byte Pallas scalars to disk (mode 0600)
- Handler flow: load Pallas SK → resolve proposer → find REGISTERING round → check validator membership → check no prior contribution → generate random `s_i` → `shamir.Split(s_i, t, n)` → `FeldmanCommit(G, coeffs)` → persist coefficients → ECIES-encrypt `n-1` shares (excludes self) → inject `MsgContributeDKG`
- Coefficients are zeroed in memory after write; persisted file is read by Phase 5 ack handler to compute `f_i(shamirIndex)`
- **Tests** (5 in [ceremony_deal_test.go](vote-sdk/app/ceremony_deal_test.go)):
  - `TestDKGContributionInjection`: happy path — correct tag, `t=2` commitments, `n-1=2` payloads (no self), ECIES decryptable by intended recipients, Feldman-verified shares, coefficients file on disk
  - `TestDKGContributionCoeffsRoundTrip`: parse coefficients back from disk, re-derive `FeldmanCommit` matches published commitments
  - `TestDKGContributionSkipsWhenAlreadyContributed`: pre-populate contribution → no injection
  - `TestDKGContributionSkipsWhenNotCeremonyValidator`: proposer absent from validators → no injection
  - `TestDKGContributionSkipsWhenNoPallasKey`: empty `pallasSkPath` → no injection

### Phase 5: DKG-aware ack path ✓

Refactored `CeremonyAckPrepareProposalHandler` in [prepare_proposal_ceremony.go](vote-sdk/app/prepare_proposal_ceremony.go) to branch on DKG vs dealer. Both paths work.

- Detect DKG round via `len(round.DkgContributions) > 0`
- Extracted `ackDealerRound` (existing dealer logic) and `ackDKGRound` (new DKG logic) into separate functions; common tail (disk write, ack message, injection) shared
- Exported `shamir.EvalPolynomial` in [shamir.go](vote-sdk/crypto/shamir/shamir.go) for the DKG ack handler to recompute `f_i(shamirIndex)` from persisted coefficients
- Added `loadCoeffs(path, expectedT)` — reads and parses coefficient file, zeroes raw bytes after parsing
- Added `zeroAndDeleteCoeffsFile(dir, roundID, logger)` — overwrites with zeros and removes
- DKG ack path: load coefficients → `EvalPolynomial(coeffs, shamirIndex)` → iterate contributions, decrypt ECIES, verify against each contributor's individual Feldman commitments → sum into `combined_share` → verify against combined commitments → write share → delete coefficients file
- Dealer path: unchanged (extracted into `ackDealerRound`)
- **Tests** (3 new + 2 existing pass unchanged, in [ceremony_deal_test.go](vote-sdk/app/ceremony_deal_test.go)):
  - `TestDKGAckHappyPath`: 3 validators, real crypto — combined share = Σ shares at proposer's index, Feldman-verified, share file written, coefficients file deleted
  - `TestDKGAckRejectsBadShare`: tampered ECIES payload → no ack injected
  - `TestDKGAckSingleValidator`: n=1 edge case — combined share = single contributor's own share
  - `TestCeremonyAckThresholdMode` / `TestCeremonyAckThresholdRejectsBadShare`: existing dealer ack tests pass unchanged after refactor

### Phase 6: Wire and swap (completed)

Swap the deal injector for the DKG injector in the pipeline. Dealer code stays for backward-compat (removed in Phase 9).

- [app.go](vote-sdk/app/app.go): replaced `CeremonyDealPrepareProposalHandler` with `CeremonyDKGContributionPrepareProposalHandler` in `ComposedPrepareProposalHandler`
- [module.go](vote-sdk/x/vote/module.go): both DEALT timeout reset paths now clear `DkgContributions = nil` alongside existing payload/ack clearing
- [keeper_ceremony.go](vote-sdk/x/vote/keeper/keeper_ceremony.go): `StripNonAckersFromRound` now filters `DkgContributions` (keeps only acked validators' contributions)
- [testutil/app.go](vote-sdk/testutil/app.go): added `NextBlockWithTxs` helper for delivering pre-built txs without going through PrepareProposal
- Dealer-path integration tests in `abci_test.go` updated to invoke `callDealHandler` directly + `NextBlockWithTxs` (behaviour unchanged)
- `TestPrepareProposalDealAcceptedByProcessProposal` → renamed to `TestPrepareProposalDKGContributionAcceptedByProcessProposal`, validates `MsgContributeDKG` pipeline round-trip for n=2..7
- **New tests**: `TestDKGContributionThroughPipeline` (pipeline injects `MsgContributeDKG`), `TestEndBlockerClearsDKGContributionsOnTimeout` (DEALT timeout resets DkgContributions), `StripNonAckersFromRound` DKG assertions

### Phase 7: Integration test

Full ceremony end-to-end with DKG.

- Multi-validator test (n=3, t=2): all three contribute via DKG, all three ack, round goes ACTIVE, cast votes, tally produces correct decrypted result
- Verifies the entire pipeline: DKG contribution -> combined commitments -> ack with combined shares -> partial decryptions with DLEQ -> tally

### Phase 8: Documentation

- Rewrite Step 4 section of [tss-ceremony.md](vote-sdk/docs/tss-ceremony.md) with full design, security rationale, and alternatives analysis

### Phase 9: Remove single-dealer remnants

Delete all code that was kept alive during Phases 1-7 for backward compatibility. After Phase 7 the DKG path is proven end-to-end; nothing references the old dealer path.

- **Proto**: delete `ceremony_payloads` (field 17) and `ceremony_dealer` (field 19) from `VoteRound`; delete `payloads` (field 4) and `dealer` (field 6) from `CeremonyState`; delete `MsgDealExecutiveAuthorityKey`, `MsgDealExecutiveAuthorityKeyResponse`, and `DealExecutiveAuthorityKey` RPC
- **Codec**: remove `TagDealExecutiveAuthorityKey` (0x07) from `IsCeremonyTag`, `DecodeCeremonyTx`, tag constants; update error messages listing valid tags
- **Handler**: delete `DealExecutiveAuthorityKey` in [msg_server_ceremony.go](vote-sdk/x/vote/keeper/msg_server_ceremony.go)
- **Injector**: delete `CeremonyDealPrepareProposalHandler` in [prepare_proposal_ceremony.go](vote-sdk/app/prepare_proposal_ceremony.go) (the DKG injector fully replaces it)
- **Module**: remove `ProvideDealExecutiveAuthorityKeySigner` from init() and signer function
- **Ante**: remove `TagDealExecutiveAuthorityKey` case and `MsgDealExecutiveAuthorityKey` from `isVoteModuleMsg` / `ceremonyValidatorRequired`
- **ProcessProposal**: remove `validateInjectedDeal` and its `TagDealExecutiveAuthorityKey` check
- **CLI**: delete `CmdDealExecutiveAuthorityKey` in [tx.go](vote-sdk/x/vote/client/cli/tx.go)
- **Query server**: remove `Payloads` and `Dealer` mapping from `CeremonyState` response
- **Keeper**: remove `CeremonyPayloads` filtering from `StripNonAckersFromRound`
- **EndBlocker**: remove `CeremonyPayloads = nil` and `CeremonyDealer = ""` from timeout reset (already replaced by `DkgContributions = nil` in Phase 6)
- **Tests**: delete all dealer-specific unit tests (`TestCeremonyDealThresholdMode`, `TestDealExecutiveAuthorityKey_*`, `TestThresholdDowngrade_*`, etc.); update lifecycle tests that still reference `CeremonyPayloads`/`CeremonyDealer`
- Regenerate Go types, verify `go vet ./...` and `go test ./...` clean

## Documentation Update: [tss-ceremony.md](vote-sdk/docs/tss-ceremony.md)

Rewrite the Step 4 section (currently a stub at lines 195-204) into a full design document covering:

**Protocol description:**

- State machine diagram (REGISTERING -> DEALT -> CONFIRMED, same statuses)
- MsgContributeDKG contents and flow
- Ack phase changes (multi-contributor verification, combined share computation)
- On-disk file lifecycle (dkg_coeffs -> share)

**Design rationale -- why single-phase Joint-Feldman DKG:**

- The only known attack is public key bias: the last contributor can see prior commitments and choose their polynomial to influence `ea_pk`
- This does NOT let the attacker learn `ea_sk`: they know their own `s_A` but not `R = sum(s_j, j != A)`, which is protected by discrete log
- `ea_pk` is used solely for ElGamal vote encryption; IND-CPA security holds for any valid key regardless of how it was chosen
- Gennaro et al. (2007) proved Joint-Feldman DKG is secure for threshold decryption despite the bias
- The bias matters only in protocols that require a provably uniform public key (e.g., CRS generation for ZK proofs); our system has no such requirement

**Alternatives considered and rejected:**

- Separate COMMITTING phase: adds a state, a message type, and ~n blocks of latency; does not actually prevent bias on a sequential blockchain (last committer still sees prior commitments); would need hash-commit-reveal (3 phases) for full prevention
- Vote extensions (CometBFT ExtendVote): collapses contributions to 1 block and provides natural simultaneity, eliminating bias entirely; rejected because it introduces a new ABCI surface (ExtendVote, VerifyVoteExtension), in-memory polynomial caching across consensus re-rounds, deferred disk writes, and the bias is harmless anyway; for n <= 9, 2n blocks is negligible latency

**Updated security properties table** replacing the current one (line 154-159), reflecting that no party knows `ea_sk` under DKG