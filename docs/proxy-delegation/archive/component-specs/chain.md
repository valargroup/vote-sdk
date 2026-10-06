# CHAIN component spec: proxy delegation via public delegate pools (B2)

**Component:** vote-sdk (`x/vote`, `app`, `api`, `circuits` FFI, `internal/helper`, `cmd/svoted` helper wiring). Base: `main` at `6e5f6928`, worktree `/Users/czstudio/Documents/vote-sdk/.claude/worktrees/vote-delegation-planning-29b451`. Paths are relative to that root unless absolute.

**Labels:** **[code]** is verified in the current source, with file:line. **[doc]** is a claim from a doc. **[proto]** is a measurement from my prototype in `/private/tmp/claude-501/-Users-czstudio-Documents-vote-sdk--claude-worktrees-vote-delegation-planning-29b451/0cff7cc1-5f3f-4de1-be83-322a1fdf7727/scratchpad/proto-chain/` (`route_test.go`, `dk_test.go`). **[inference]** is my design reasoning. **[decision]** is a choice this spec makes.

**Terminology.** "Delegation", `MsgDelegateVote` and ZKP1 keep their codebase meaning (notes → VAN). The new feature is **proxy delegation**. A **delegate** is a public person in the registry. A **proxy-delegation commitment (DC)** is the proposal-0 vote-commitment-shaped leaf. A **delegate pool** `Pool[d]` is `TallyKey(round, 0, d)`. A **route** is a delegate's public per-proposal choice. The **delegate key (DK)** is the delegate's long-lived signing key. ZKP4 is called the **proxy-delegation proof**. Every new proto field uses the `proxy_` prefix and never a bare `delegation_` prefix, so it cannot be confused with ZKP1.

---

## 0. Summary of chain decisions

1. **Signature scheme.** The DK is **Ed25519**, verified with CometBFT's ZIP-215 verifier. The chain rejects non-canonical and small-order keys at registration. Verifier attestation keys are also Ed25519. The rationale is in §4.8.
2. **New wire tags.** There are five custom-wire tags: `0x09` mixed action batch, `0x0B` ZKP1 plus mixed action batch, `0x0C` register delegate, `0x0F` delegate route and `0x10` update delegate. Each one is listed explicitly in `IsVoteTag` and in the canonical-encoding set. Two coordinator payloads are added: proxy-delegation params (verifier set and enable flag) and delegate status.
3. **Proposal-0 reveals.** `MsgRevealShare` with `proposal_id = 0` is accepted iff the round has proxy delegation enabled and `1 ≤ vote_decision < NextDelegateIndex`, meaning the index exists. Delegate status is **not** checked at reveal time; status gates routes only. ZKP3 is unchanged.
4. **Delegate-index bound.** ZKP4 exposes a public `delegate_index_bound`. The chain requires `1 ≤ bound < NextDelegateIndex` when the DC is submitted, so a DC to a nonexistent index is rejected up front instead of burning weight at reveal time.
5. **Tally hook.** Routing runs in **EndBlock at the ACTIVE→TALLYING transition**. For each `(p, o)` it computes `agg[p][o] = direct + Σ routed pools + Enc(0; ρ)`, where `ρ` is a hash of the combined ciphertext. This deterministic re-randomization is **mandatory**. Without it an attacker can force an identity `C1` that makes every partial decryption fail its DLEQ check, and the round then finalizes empty (prototype-confirmed, §6.4).
6. **Timing.** DCs are accepted until a stored per-round `cutoff_time = vote_end_time − clamp(duration/10, 60 s, 3600 s)`. Routes and reveals are accepted until `vote_end_time`.
7. **Per-round enable.** The flag is snapshotted from coordinator-set `ProxyDelegationParams.enable_for_new_rounds` when the round is created. It is **not** a new `MsgCreateVotingSession` field, because a new field breaks the dormant-merge policy (§9.3).
8. **Tree capacity.** A tree-capacity guard (`ErrCommitmentTreeFull`) is added in `AppendCommitment`, and `MaxTreePosition` is corrected to 2^24−1.
9. **Activation.** The feature ships dormant behind `types.ProxyDelegationEnabled = false` and is activated by the `v1.7.0` coordinated halt (no-op store migration).

---

## 1. Constants (`x/vote/types/keys.go`, new `x/vote/types/proxy.go`)

| Name | Value | Notes |
|---|---|---|
| `MaxProxyDelegationsPerBatch` | 10 | DC actions per `0x09` or `0x0B` message |
| `MaxVoteActionBatchSize` | 50 (= `MaxCastVoteBatchSize`) | Total actions per message. This keeps worst-case size equal to today's 50-cast composite. Remaining casts go in a follow-up `0x06` batch. |
| `MinDelegateIndex` | 1 | Index 0 is reserved and never valid, so a zeroed witness cannot route weight to the first registrant. |
| `RouteDecisionAbstain` | `0xFFFFFFFF` | Explicit public abstain. It is recorded but never added (open question 3). |
| `MaxDelegateVerifiers` | 16 | |
| `MaxAttestationValiditySeconds` | 604800 (7 days) | |
| `AttestationClockSkewSeconds` | 300 | |
| `DelegateKeyRecoveryDelaySeconds` | 172800 (48 h) | Open question 4 |
| `MinDelegateKeyChangeIntervalSeconds` | 600 | Bounds key churn |
| `MaxDelegateStatusReasonLen` | 256 | Printable ASCII |
| `ProxyCutoffFractionDenominator` | 10 | Cutoff buffer is `duration/10`, clamped to `[60, 3600]` s |
| `CommitmentTreeCapacity` | `1 << 24` | Depth-24 tree [doc] dossier chain §1.7 (vote-commitment-tree `hash.rs:13-25`) |
| `MaxTreePosition` | `CommitmentTreeCapacity − 1` | Today it wrongly says 2^32−1 [code] `x/vote/types/keys.go:76-79` |
| `DefaultDelegateQueryLimit` / `MaxDelegateQueryLimit` | 100 / 500 | Pagination |

---

## 2. Wire format and codec (`api/codec.go`)

[code] Today's tags are 0x02–0x07, plus ceremony tags 0x08, 0x0D and 0x0E (`api/codec.go:27-46`). `IsVoteTag` is a contiguous range check (`:57-59`). Canonical encoding is enforced only for 0x06 and 0x07 (`:143-154`). 0x0A must never be used (`:25-26`).

| Tag | Message | Signed by |
|---|---|---|
| `0x09` | `MsgVoteActionBatch` | Hotkey `r_vpk` per action (RedPallas, as for casts) |
| `0x0B` | `MsgDelegateAndVoteActionBatch` | ZKP1 spend-auth (HW or local) plus hotkey `r_vpk` per action |
| `0x0C` | `MsgRegisterDelegate` | Verifier attestation (Ed25519, threshold) plus DK proof-of-possession |
| `0x0F` | `MsgDelegateRoute` | DK (Ed25519) |
| `0x10` | `MsgUpdateDelegate` | Current DK, or attestation plus new-key PoP |

**Tag rationale.** These tags avoid 0x0A, 0x12 and 0x1A (Cosmos `TxRaw` field tags) and the ceremony tags.

**Codec changes:**
- `IsVoteTag(b)` becomes `b ∈ [0x02,0x07] || (ProxyDelegationEnabled && b ∈ {0x09,0x0B,0x0C,0x0F,0x10})`.
- Add the new tags to `TagForMessage` and `DecodeVoteTx`.
- **All five new tags enforce canonical encoding:** run `rejectUnknownProtoFields` (`api/codec.go:164-197` already recurses through oneofs) and require a byte-identical deterministic re-marshal.
- Recommended in the same upgrade: also make 0x04 canonical, which removes tx-hash malleability of reveals. Only svoted's own helper produces reveals today.

---

## 3. Shared validation primitives (`x/vote/types/proxy.go`)

- `ValidateDelegateKey(dk []byte) error`:
  - `len == 32`.
  - `filippo.io/edwards25519` `Point.SetBytes` succeeds and `Point.Bytes()` equals `dk` (canonical encoding).
  - `MultByCofactor(P) != identity` (not small-order).
  - Small-order keys must be rejected because ZIP-215 verification accepts them and they are universally forgeable. [proto] `dk_test.go` passes for valid, identity and order-2 keys. [code] `filippo.io/edwards25519 v1.1.0` is already in `go.mod:86`.
- `VerifyDelegateSig(dk, digest32, sig64) bool` uses `cmted25519.PubKey(dk).VerifySignature(digest, sig)`. [code] CometBFT v0.38.21 `crypto/ed25519/ed25519.go:28` uses `VerifyOptionsZIP_215`, which is deterministic across validators.
- `ValidateVerifierID(id)` uses the same grammar as `ValidateEndorserID` (`[a-z0-9-]{1,64}`, `keys.go:411-426`).
- `ProxyDelegationCutoff(createdAtTime, voteEndTime uint64) uint64`:
  ```
  if voteEndTime <= createdAtTime: return createdAtTime
  buf := clamp((voteEndTime - createdAtTime)/10, 60, 3600)
  if buf >= voteEndTime - createdAtTime: buf = (voteEndTime - createdAtTime)/2
  return voteEndTime - buf
  ```
- `EffectiveDelegateKey(rec, blockTime) (dk []byte, epoch uint64)` returns `(rec.pending_dk, rec.key_epoch+1)` when `len(pending_dk)==32 && blockTime >= pending_dk_activation_time`, otherwise `(rec.dk, rec.key_epoch)`. Every reader uses this function (ante, handlers, queries). Handlers that write a record first materialize the pending key: set `dk=pending`, `key_epoch++`, `key_changed_at_time=activation`, clear pending.

---

## 4. Messages

### 4.1 `MsgVoteActionBatch` (tag `0x09`): mixed DCs plus casts against a real anchor

```proto
// ZKP4 public data for one proxy delegation. The delegate index is private in the DC.
message ProxyDelegationAction {
  bytes  van_nullifier                = 1; // 32: same derivation and nullifier set as ZKP2
  bytes  r_vpk                        = 2; // 32: compressed Pallas, non-identity
  bytes  vote_authority_note_new      = 3; // 32: successor VAN (weight W-w, authority MAX)
  bytes  proxy_delegation_commitment  = 4; // 32: DC = Poseidon(DOMAIN_VC, round, shares_hash, 0, d)
  bytes  proof                        = 5; // ZKP4, 1..MaxProofSize
  bytes  vote_auth_sig                = 6; // 64: RedPallas by r_vpk over the batch digest
  uint32 delegate_index_bound         = 7; // public ZKP4 input: proves 1 <= d <= bound
}
message VoteAction {
  oneof action {
    ProxyDelegationAction proxy_delegation = 1;
    MsgCastVote           cast             = 2;
    // 3..9 reserved for future cast variants (e.g. private-choice v2 casts)
  }
}
message MsgVoteActionBatch {
  bytes  vote_round_id                = 1;
  uint64 vote_comm_tree_anchor_height = 2;
  repeated VoteAction actions         = 3;
}
message MsgVoteActionBatchResponse { bytes batch_digest = 1; }
```

**ValidateBasic** (stateless):
- `vote_round_id` is 32 bytes and `vote_comm_tree_anchor_height ≠ 0`.
- `1 ≤ len(actions) ≤ 50`. Every action is non-nil with exactly one oneof arm set.
- **Ordering:** all `proxy_delegation` actions precede all `cast` actions, and `1 ≤ #DC ≤ 10`. ZKP4 requires authority == MAX, so a DC after a cast could never verify; the rule gives early, cheap rejection. A pure-cast list must use 0x06, which keeps one canonical form.
- DC field checks: 32-byte fields; `r_vpk` not all zeros; proof `1..MaxProofSize`; signature 64 bytes; `delegate_index_bound ≥ 1`.
- Casts: `cast.validateBasic(false)` (`msgs.go:128-160`), and `cast.vote_round_id` and `cast.vote_comm_tree_anchor_height` must equal the batch-level values.
- **Intra-message dedupe**, because `CheckNullifiersUnique` does not detect duplicates within a list (`keeper_voting.go:54-66`):
  - `van_nullifier` unique across **all** actions.
  - Cast `proposal_id` unique.
  - The set {every `proxy_delegation_commitment`, every `vote_commitment`, the final `vote_authority_note_new`} unique.

**Ante** (`x/vote/ante/validate.go`; steps marked R also run on RecheckTx):
1. ValidateBasic.
2. R: `ValidateRoundForVoting` (ACTIVE and `blockTime < vote_end_time`, `keeper_voting.go:313-341`), then `ValidateRoundForProxyDelegation(round)`, which requires `round.proxy_delegation.enabled` and `blockTime < cutoff_time`. It copies the CheckTx unset-block-time passthrough (`keeper_voting.go:324-333`).
3. R: `ValidateProposalId` for every cast (pattern at `validate.go:93-117`). For every DC, `delegate_index_bound < NextDelegateIndex`, else `ErrInvalidDelegateIndexBound`. Also `EnsureCommitmentCapacity(round, 1+len(actions))`.
4. R: `CheckNullifiersUnique(VAN, all action nullifiers)`.
5. Signatures: `digest = ComputeVoteActionBatchSighash(msg)` (§4.9). Every action's `r_vpk` must decode as non-identity (`elgamal.UnmarshalPublicKey`), and `SigVerifier.Verify(r_vpk, digest, vote_auth_sig)` must pass. All signatures are checked before any proof.
6. Proofs:
   - `root = GetCommitmentRootAtHeight(anchor)`, which must be non-nil. `proofRoot = root`.
   - For action i: a DC is verified with `ZKPVerifier.VerifyProxyDelegation(proof, ProxyDelegationInputs{van_nf, r_vpk, van_new, dc, proofRoot, anchor_height, delegate_index_bound, round_id, ea_pk})`. A cast is verified with the existing `verifyVoteCommitmentProof(cast, proofRoot, ea_pk)`.
   - Between actions, `proofRoot = votetree.SingleLeafRoot(action_i.vote_authority_note_new)`. This is the same chaining as `validate.go:429-445`. Verification stops at the first failure.

**Handler** `VoteActionBatch`. Every fallible check runs before any write, following the `msg_server.go:298-321` pattern:
1. Re-run ValidateBasic. Re-check the round (enabled, before cutoff), the anchor root, cast proposals, bounds against `NextDelegateIndex`, `CheckNullifiersUnique` and capacity.
2. `SetNullifier(VAN, nf)` for each action, in order.
3. `finalVAN := AppendCommitment(actions[last].vote_authority_note_new)`. Then for each action i in order, `AppendCommitment(commitment_i)`. That is the DC for a DC action and the `vote_commitment` for a cast. Intermediate VANs are never appended (same as `msg_server.go:252-269`).
4. Emit event `vote_action_batch` with:
   - `vote_round_id`, `batch_digest`, `batch_size`
   - `final_van_leaf_index`
   - `action_kinds` (comma list of `proxy_delegation|cast`)
   - `commitment_leaf_indices` (action order, always `final+1+i`)
   - `proposal_ids` (0 for DC actions)
   - `van_nullifiers`
   - `proxy_delegation_count`
5. Return `batch_digest`.

### 4.2 `MsgDelegateAndVoteActionBatch` (tag `0x0B`): ZKP1 plus mixed actions

```proto
message MsgDelegateAndVoteActionBatch {
  MsgDelegateVote     delegation = 1;
  repeated VoteAction actions    = 2;
}
message MsgDelegateAndVoteActionBatchResponse { bytes batch_digest = 1; }
```

- **ValidateBasic:** `delegation.ValidateBasic()` (gov-nullifier dedupe, tx1 effects, `msgs.go:66-117`). Then the same action rules as §4.1, with three differences: there is no top-level anchor, every nested cast has anchor 0 and the delegation's round, and the round is `delegation.vote_round_id`. Requires ≥1 DC; DC-only ("delegate everything") is allowed.
- **Ante:**
  - Round checks as §4.1, steps 2–4, plus `CheckNullifiersUnique(Gov, delegation.gov_nullifiers)`.
  - Then all action signatures over `ComputeDelegateAndVoteActionBatchSighash`.
  - Then `verifyDelegation` (`validate.go:248-318`).
  - Then actions are chained from `proofRoot = SingleLeafRoot(delegation.van_cmx)` with public `anchor_height = 0` (pattern `validate.go:185-243`).
- **Handler:** as the existing composite (`msg_server.go:290-354`), plus DC actions. Gov and VAN nullifiers are set, the final VAN and every action commitment are appended, and `van_cmx` is never appended. Event `delegate_and_vote_action_batch` carries the §4.1 attributes plus `nullifier_count`.
- **Size:** 1 ZKP1 + ≤50 action proofs is the same envelope as today's 50-cast composite. That composite is about 577 KB raw, about 78% of the effective REST cap [doc] (dossier chain §2). Wallets split anything beyond that into a follow-up 0x06 batch anchored to the new final VAN.

### 4.3 `MsgRevealShare` (tag `0x04`, unchanged proto): proposal 0

[code] Today `ValidateBasic` requires `proposal_id ∈ 1..50` and `decision < 8` (`msgs.go:245-250`). The handler validates proposal and option (`msg_server_tally_decrypt.go:33-41`), then calls `AddToTally` and `IncrementShareCount` (`:49-56`). ZKP3 takes `proposal_id` and `vote_decision` as unconstrained public field elements (`circuits/src/ffi.rs:970-990`; voting-circuits `src/share_reveal/circuit.rs:1-35`).

**New rule, active only when `ProxyDelegationEnabled`:**
- **ValidateBasic:** if `proposal_id == 0`, require `vote_decision ≥ MinDelegateIndex` and skip the `< MaxVoteOptions` check. All other fields are unchanged. Otherwise the existing rule applies.
- **Ante**, new step 3 (R), runs before the ZKP3 proof is paid for: if `proposal_id == 0`, require `round.proxy_delegation.enabled` (else `ErrProxyDelegationDisabled`) and `vote_decision < NextDelegateIndex` (else `ErrDelegateNotFound`).
- **Handler:**
  - `ValidateRoundForVoting`.
  - If `p == 0`, apply the same two checks; otherwise `ValidateProposalId` and `ValidateVoteDecision` as today.
  - `CheckAndSetNullifier(Share)`.
  - `AddToTally(round, 0, d, enc)` validates identity points as today (`keeper_tally.go:35-81`).
  - `IncrementShareCount(round, 0, d)`.
  - The existing `reveal_share` event carries `proposal_id=0` and `vote_decision=d`.
- **Meaning of "valid delegate for this round":** the index **exists at reveal time**; status is ignored. Rationale:
  - The chain cannot see `d` when the DC is submitted (it is private in ZKP4), so a status check at reveal would silently strand honest delegators of a delegate suspended after their DC.
  - An inactive delegate's pool simply abstains, which is the outcome product decision 2 specifies for a delegate that does not vote.
  - The registry is append-only, so "exists" is monotone and deterministic.
  - Registration before round creation is **not** required, because mid-round registrations are allowed.
- **Per-block cap:** these reveals count toward `MaxVoteShareSubmissionsPerBlock = 256` (`app/vote_share_submission_proposal.go:13`). That counting keys on tag 0x04 and needs no change.

### 4.4 `MsgRegisterDelegate` (tag `0x0C`)

```proto
message VerifierSignature { string verifier_id = 1; bytes signature = 2; } // Ed25519, 64 bytes
message MsgRegisterDelegate {
  bytes  dk                     = 1; // 32-byte Ed25519 public key
  bytes  identity_commitment    = 2; // 32 bytes, opaque to the chain (identity contract §13)
  uint64 attested_at            = 3; // unix seconds
  uint64 attestation_expires_at = 4;
  repeated VerifierSignature attestation = 5;
  bytes  dk_pop_sig             = 6; // Ed25519 by dk over D4
}
message MsgRegisterDelegateResponse { uint32 delegate_index = 1; }
```

- **ValidateBasic:**
  - `ValidateDelegateKey(dk)`.
  - `identity_commitment` is 32 bytes and not all zeros.
  - `1 ≤ len(attestation) ≤ 16`, with valid and unique `verifier_id`s and 64-byte signatures.
  - `dk_pop_sig` is 64 bytes.
  - `0 < attested_at < attestation_expires_at` and `expires_at − attested_at ≤ 7 days`.
- **Ante:**
  - R: params exist with ≥1 verifier, else `ErrNoDelegateVerifiers`.
  - R: when block time is set, `attested_at ≤ blockTime + 300` and `blockTime < expires_at`, else `ErrInvalidAttestation`.
  - R: `dk` is absent from `DelegateKeyIndex`, which includes historical keys; else `ErrDelegateKeyInUse`.
  - Verify: compute `D3(action=1, index=0, epoch=0, dk, identity_commitment, attested_at, expires_at)`. **Every** entry's `verifier_id` must be in the current set and its signature must verify, and the count of distinct verifier IDs must be ≥ `verifier_threshold`. Dedupe is by verifier ID and pubkey (set validation guarantees unique pubkeys), never by signature bytes. This avoids the ICNS bug [doc] identity dossier.
  - Then `VerifyDelegateSig(dk, D4, dk_pop_sig)`.
- **Handler:**
  - Re-check the stateful conditions.
  - `index = NextDelegateIndex` (default 1 when absent). If `index == MaxUint32`, fail. Set `NextDelegateIndex = index+1`.
  - Write `DelegateRecord{index, key_type=ED25519, dk, key_epoch=0, identity_commitment, status=ACTIVE, registered_at_height/time, attesting_verifier_ids (sorted), key_changed_at_time=blockTime}`.
  - Write `DelegateKeyIndex[dk] = index`.
  - Event `register_delegate{delegate_index, dk, identity_commitment, verifier_ids}`.

**Spam gate.** Without a valid threshold attestation the tx fails CheckTx and is never gossiped. A replayed valid tx fails on DK uniqueness. PoP binds the attestation to the key holder, so the tx cannot be front-run onto another key.

### 4.5 `MsgUpdateDelegate` (tag `0x10`)

```proto
message RotateDelegateKey  { bytes new_dk = 1; bytes new_dk_pop_sig = 2; bytes current_dk_sig = 3; }
message RecoverDelegateKey { bytes new_dk = 1; bytes new_dk_pop_sig = 2; uint64 attested_at = 3;
                             uint64 attestation_expires_at = 4; repeated VerifierSignature attestation = 5; }
message RetireDelegate     { bytes current_dk_sig = 1; }
message MsgUpdateDelegate {
  uint32 delegate_index = 1;
  uint64 key_epoch      = 2; // must equal the effective epoch (replay guard)
  oneof op { RotateDelegateKey rotate = 3; RecoverDelegateKey recover = 4; RetireDelegate retire = 5; }
}
message MsgUpdateDelegateResponse {}
```

- **ValidateBasic:** `delegate_index ≥ 1`; exactly one op; key and signature sizes; for `recover`, attestation shape as in §4.4.
- **Ante** (R):
  - The record exists (`ErrDelegateNotFound`) and `status ≠ RETIRED` (`ErrDelegateRetired`).
  - `key_epoch` equals the effective epoch (`ErrDelegateKeyEpochMismatch`).
  - For rotate and recover: `blockTime ≥ key_changed_at_time + 600` (`ErrDelegateKeyChangeTooSoon`), and `new_dk` is not in `DelegateKeyIndex`.
- **Verify:**
  - rotate: the effective DK signs `D5(op=1,new_dk)` and `new_dk` signs `D6`.
  - recover: a threshold attestation over `D3(action=2, index, epoch, new_dk, record.identity_commitment, …)` plus `new_dk` PoP over `D6`. The attestation must be for the stored identity commitment.
  - retire: the effective DK signs `D5(op=3, zero32)`.
- **Handler** (materialize any activated pending key first):
  - **Rotate:** set `dk=new_dk`, `key_epoch++`, clear pending, `key_changed_at_time=blockTime`, and `DelegateKeyIndex[new_dk]=index`. This takes effect immediately, so the old key can cancel a hostile recovery.
  - **Recover:** set `pending_dk=new_dk`, `pending_dk_activation_time=blockTime+48h` and reserve `DelegateKeyIndex[new_dk]`. Lazy activation at that time increments the epoch. This path is for a lost key or X-driven rebinding, with a cooling-off window.
  - **Retire:** `status=RETIRED`, which is terminal.
  - Event `update_delegate{delegate_index, op, key_epoch, new_dk, activation_time}`.
- Old keys stay in `DelegateKeyIndex` forever and can never be re-registered.

### 4.6 `MsgDelegateRoute` (tag `0x0F`): the delegate's public vote

```proto
message DelegateRouteChoice { uint32 proposal_id = 1; uint32 vote_decision = 2; } // or RouteDecisionAbstain
message MsgDelegateRoute {
  bytes  vote_round_id  = 1;
  uint32 delegate_index = 2;
  uint64 key_epoch      = 3;
  repeated DelegateRouteChoice routes = 4; // 1..50, strictly ascending proposal_id
  bytes  dk_sig         = 5;               // Ed25519 by the effective DK over D7
}
message MsgDelegateRouteResponse {}
```

- **ValidateBasic:**
  - Round is 32 bytes and index ≥ 1.
  - `1 ≤ len(routes) ≤ 50`, strictly ascending `proposal_id` in `1..50` (canonical order and uniqueness in one rule).
  - Each `vote_decision < MaxVoteOptions` or `== RouteDecisionAbstain`.
  - Signature is 64 bytes.
- **Ante:**
  - R: `ValidateRoundForVoting`, so the route deadline is `vote_end_time`, and `round.proxy_delegation.enabled`.
  - R: the record exists and its effective status is `ACTIVE` (`ErrDelegateInactive`), and `key_epoch` matches.
  - R: every proposal exists, and every non-abstain decision is `< len(options)` (`ValidateVoteChoice`, `vote_validation.go:37-47`).
  - R: no chosen proposal is already in the stored `DelegateRouteSet(round, d)` (`ErrDelegateRouteExists`).
  - Verify the signature with the effective DK.
- **Handler:**
  - Re-check everything.
  - Merge the new entries `{proposal_id, vote_decision, height, key_epoch}` into the set, keeping it sorted by `proposal_id`. All-or-nothing.
  - Event `delegate_route{vote_round_id, delegate_index, proposal_ids, vote_decisions, key_epoch}`.
- **One-shot** per `(round, d, p)`. Routes are final even if the delegate is later suspended or retired. Those status changes block **future** routes only (open question 2).

### 4.7 Coordinator payloads (standard Cosmos tx via `MsgProposeCoordinatorAction`)

[code] The precedent is a closed payload switch (`msg_server_coordinator_actions.go:17-32,226-333`).

```proto
message DelegateVerifier      { string verifier_id = 1; bytes ed25519_pubkey = 2; }
message ProxyDelegationParams { bool enable_for_new_rounds = 1; uint32 verifier_threshold = 2;
                                repeated DelegateVerifier verifiers = 3; }
message MsgSetProxyDelegationParams { string creator = 1; ProxyDelegationParams params = 2; }
message MsgSetDelegateStatus { string creator = 1; uint32 delegate_index = 2;
                               DelegateStatus status = 3; string reason = 4; }
```

- **`MsgSetProxyDelegationParams`:**
  - ValidateBasic: creator is bech32. `len(verifiers) ≤ 16`, with valid unique IDs and unique 32-byte non-zero canonical Ed25519 keys (`ValidateDelegateKey`). If there are no verifiers, `threshold == 0`; otherwise `1 ≤ threshold ≤ len`.
  - Execute: replace the params atomically (like `MsgUpdateVoteManagers`). Event `set_proxy_delegation_params{enable_for_new_rounds, verifier_threshold, verifier_ids}`.
  - With an empty verifier set, registration and recovery are disabled. Existing delegates keep working.
- **`MsgSetDelegateStatus`:**
  - `status ∈ {ACTIVE, SUSPENDED}` and `reason` is ≤256 printable ASCII.
  - Execute: the record exists and is not RETIRED. Set status, `status_changed_at_height` and `status_reason`. Event `set_delegate_status{delegate_index, old_status, new_status, reason}`.
  - This is the impersonation takedown lever. It cannot un-retire a delegate or void routes already made.
- **Registration:**
  - Both payloads are registered via `RegisterImplementations`, and nested `ProxyDelegationParams` and `DelegateVerifier` via `gogoproto.RegisterType` (`x/vote/types/codec.go:17-25`).
  - Both are added to `coordinatorActionPayloadTypes`, validate and execute.
  - **All of this registration is gated by the dormant flag** (§9.3).

### 4.8 Signature scheme for the delegate key [decision]

**Chosen: Ed25519** (RFC 8032 keys, ZIP-215 verification via CometBFT, small-order and non-canonical keys rejected).

The rationale, tied to the other components:
1. **Identity component.** The X proof post carries `zvote-delegate:v1:<bech32m(dk)>`. The identity research recommends Ed25519 identity keys so that registration statements are verifiable by anyone in any language (Go stdlib, JS, Python) [doc] identity dossier §3.5. Under B2 the identity key **is** the chain DK: there is no per-round receive address, so one key gives one binding.
2. **Client component.** zcash_voting already depends on `ed25519-dalek 2` (`zcash_voting/Cargo.toml:51,94`) for config verification, so Vizor's Rust core needs no new crypto dependency. The DK can be derived from a 64-byte ZIP-32 registered secret (seed[0..32]) or generated randomly with a backup.
3. **Custody.** A DK controls a whole pool, so it is a high-value key. Ed25519 has HSM, KMS and YubiKey options for institutional delegates; RedPallas has none.
4. **Safety separation.** A RedPallas DK lives in the same group and code path as Orchard spend-auth and hotkeys. A client that derived the DK from an Orchard account would publish an `ak`, and digest-domain mistakes would be one step from cross-protocol confusion. Ed25519 cannot be confused with any Zcash spend authority.
5. **Chain.** Ed25519 is pure Go, deterministic and real in every test build. The RedPallas FFI is a mock in untagged builds (`ffi/redpallas/verifier_default.go`), which could hide registry signature bugs.
6. **What was traded away:** in-circuit use of the DK (for example encrypting pool totals to the delegate). If ever needed, add a separate optional Pallas `delegate_enc_pk` to the record. `DelegateRecord.key_type` exists for agility.

**Rejected alternatives:**
- secp256k1: delegates have no Cosmos accounts, account creation is coordinator-only (`accounts.go:14-22`), and Vizor has no secp256k1 key management.
- RedPallas: see points 3–5 above.

**Verifier attestation keys** are Ed25519, matching the PIR attestation precedent (`internal/pirupdate/attestation.go:108-120`) and zcash_voting `trusted_keys`.

### 4.9 Digest encodings (all BLAKE2b-256, unkeyed, domain string prefix, the same idiom as `x/vote/types/sighash.go`)

`write32`, `writeU32As32` and `writeU64As32` are the existing helpers (`sighash.go:134-154`). `lp8(s)` is `u8(len(s)) || s` for the chain ID, which comes from `ctx.ChainID()`. Ed25519 signs the 32-byte digest.

- **D1, `SVOTE_VOTE_ACTION_BATCH_SIGHASH_V1`.** Every action's `vote_auth_sig` signs this one digest (anti-truncation, anti-reorder, anti-graft).
  ```
  domain || write32(round_id) || writeU64As32(anchor_height) || writeU32As32(n) ||
  for i: writeU32As32(i) || writeU32As32(kind: 1=proxy_delegation, 2=cast) ||
         write32(r_vpk) || write32(van_nullifier) || write32(van_new) || write32(dc | vc) ||
         writeU32As32(delegate_index_bound | proposal_id)
  ```
- **D2, `SVOTE_DELEGATE_AND_VOTE_ACTION_BATCH_SIGHASH_V1`.** Same as D1, with `write32(delegation.van_cmx)` in place of the anchor field.
- **D3, `SVOTE_DELEGATE_ATTESTATION_V1`.** Verifiers sign this.
  ```
  domain || lp8(chain_id) || u8(action: 1 register, 2 recover) || writeU32As32(delegate_index or 0) ||
  writeU64As32(key_epoch or 0) || write32(dk_or_new_dk) || write32(identity_commitment) ||
  writeU64As32(attested_at) || writeU64As32(expires_at)
  ```
- **D4, `SVOTE_DELEGATE_REGISTER_POP_V1`.** `domain || lp8(chain_id) || write32(dk) || write32(D3)`.
- **D5, `SVOTE_DELEGATE_UPDATE_V1`.** `domain || lp8(chain_id) || writeU32As32(index) || writeU64As32(key_epoch) || writeU32As32(op: 1 rotate, 3 retire) || write32(new_dk or zero32)`.
- **D6, `SVOTE_DELEGATE_KEY_POP_V1`.** `domain || lp8(chain_id) || writeU32As32(index) || writeU64As32(key_epoch+1) || write32(new_dk)`.
- **D7, `SVOTE_DELEGATE_ROUTE_V1`.** `domain || lp8(chain_id) || write32(round_id) || writeU32As32(index) || writeU64As32(key_epoch) || writeU32As32(n) || for each (ascending): writeU32As32(proposal_id) || writeU32As32(vote_decision)`.

e2e-tests `src/sighash.rs` and the zcash_voting builders must mirror D1–D7 byte for byte.

### 4.10 New errors (`x/vote/types/errors.go`; codes 2–14 and 21–49 are taken, `errors.go:10-58`)

| Code | Name | Message |
|---|---|---|
| 11 (existing) | `ErrCommitmentTreeFull` | commitment tree is full (now actually used) |
| 50 | `ErrProxyDelegationDisabled` | proxy delegation is not enabled for this round |
| 51 | `ErrProxyDelegationClosed` | proxy delegation cutoff has passed |
| 52 | `ErrDelegateNotFound` | delegate not found |
| 53 | `ErrDelegateInactive` | delegate is not active |
| 54 | `ErrDelegateKeyInUse` | delegate key already registered |
| 55 | `ErrInvalidDelegateKey` | invalid delegate key |
| 56 | `ErrInvalidAttestation` | invalid or insufficient verifier attestation |
| 57 | `ErrDelegateRouteExists` | delegate already routed this proposal |
| 58 | `ErrInvalidDelegateIndexBound` | delegate index bound out of range |
| 59 | `ErrDelegateKeyEpochMismatch` | delegate key epoch mismatch |
| 60 | `ErrDelegateKeyChangeTooSoon` | delegate key changed too recently |
| 61 | `ErrDelegateRetired` | delegate is retired |
| 62 | `ErrNoDelegateVerifiers` | no delegate verifiers configured |

### 4.11 Plumbing checklist (modelled on PR 452 [doc] dossier chain §3.1)

- **Proto, service and registration:**
  - Add `rpc` entries in `proto/svote/v1/tx.proto:14-32` and regenerate.
  - No-op `CustomGetSigner` providers for the five custom-wire messages (`x/vote/module.go` init list).
  - Extend `isVoteModuleMsg` (`app/ante.go:240-248`) so they cannot ride in standard txs.
  - Bounded metrics labels in `x/vote/ante/metrics.go:72-76,133-147`.
  - `VoteMessage` interface implementations. `AcceptsTallyingRound=false` for all of them. `GetVoteRoundId` returns nil for register and update (no round step) and the round ID otherwise. `GetNullifiers` returns nil for the composites, which handle nullifiers explicitly.
- **REST tx routes** (`api/handler.go:157-169`), all with strict canonical JSON (`decodeAndValidateCanonicalJSON`) and covered by `TestIngressTimeoutNeverBroadcasts`:
  - `POST /shielded-vote/v1/vote-action-batch`
  - `POST /shielded-vote/v1/delegate-and-vote-action-batch`
  - `POST /shielded-vote/v1/register-delegate`
  - `POST /shielded-vote/v1/update-delegate`
  - `POST /shielded-vote/v1/delegate-route`

---

## 5. State layout, genesis, queries and light clients

### 5.1 KV prefixes

[code] The prefixes in use are 0x01–0x08, 0x0A–0x0C and 0x0E–0x19 (`x/vote/types/keys.go:111-208`). 0x09 and 0x0D are free but were used historically (`CeremonyStateKey`, git `f0f85189`; `CeremonyMissPrefix`, git `cea3d192`), so **avoid them**. All new prefixes come from 0x1A onward.

| Prefix / key | Layout | Value |
|---|---|---|
| `0x1A` DelegateRecord | `0x1A ‖ u32BE(index)` | `DelegateRecord` |
| `0x1B` DelegateKeyIndex | `0x1B ‖ dk(32)` | `u32BE(index)` (all current, historical and pending keys, permanent) |
| `0x1C` DelegateRouteSet | `0x1C ‖ round_id(32) ‖ u32BE(index)` | `DelegateRouteSet` |
| `0x1D` ProxyDelegationParams | single key | `ProxyDelegationParams` |
| `0x1E` NextDelegateIndex | single key | `u32BE`. Absent means 1. |
| `0x1F` RoutingBase | `0x1F ‖ round_id ‖ u32BE(p) ‖ u32BE(o)` | 64-byte pre-routing direct accumulator, written only for buckets that had one and received pools (auditability, §6.5) |
| existing `0x05` TallyKey | `(round, 0, d)` | `Pool[d]` ciphertext |
| existing `0x0B` ShareCountKey | `(round, 0, d)` | Revealed share count of `Pool[d]` |
| existing `0x01` NullifierKey | `0x01 ‖ 0x02 ‖ round ‖ share_nf` | DC share-reveal status |

```proto
enum DelegateStatus  { DELEGATE_STATUS_UNSPECIFIED = 0; DELEGATE_STATUS_ACTIVE = 1;
                       DELEGATE_STATUS_SUSPENDED = 2; DELEGATE_STATUS_RETIRED = 3; }
enum DelegateKeyType { DELEGATE_KEY_TYPE_UNSPECIFIED = 0; DELEGATE_KEY_TYPE_ED25519 = 1; }
message DelegateRecord {
  uint32 delegate_index = 1;  DelegateKeyType key_type = 2;  bytes dk = 3;  uint64 key_epoch = 4;
  bytes identity_commitment = 5;  DelegateStatus status = 6;
  uint64 registered_at_height = 7;  uint64 registered_at_time = 8;
  repeated string attesting_verifier_ids = 9;
  bytes pending_dk = 10;  uint64 pending_dk_activation_time = 11;
  uint64 key_changed_at_time = 12;  uint64 status_changed_at_height = 13;  string status_reason = 14;
}
message DelegateRoute    { uint32 proposal_id = 1; uint32 vote_decision = 2; uint64 height = 3; uint64 key_epoch = 4; }
message DelegateRouteSet { bytes vote_round_id = 1; uint32 delegate_index = 2; repeated DelegateRoute routes = 3; } // sorted by proposal_id
message ProxyDelegationRoundState {
  bool enabled = 1;  uint64 cutoff_time = 2;  bool pools_routed = 3;
  uint32 routed_pool_count = 4;  uint32 applied_route_count = 5;  uint32 next_delegate_index_at_creation = 6;
}
// VoteRound gains: ProxyDelegationRoundState proxy_delegation = 31;   (types.proto:36-79, last field 30)
```

**Round creation.** `executeCreateVotingSession` (`msg_server.go:36-139`) sets `round.proxy_delegation` only when `ProxyDelegationEnabled && params.enable_for_new_rounds`. It then sets `{enabled: true, cutoff_time: ProxyDelegationCutoff(created_at_time, vote_end_time), next_delegate_index_at_creation}`; otherwise the field stays nil. The snapshot is taken when the coordinator action **executes**, and the field is immutable afterwards.

### 5.2 Genesis

```proto
// GenesisState gains (last field today: 18, types.proto:116-132):
ProxyDelegationParams proxy_delegation_params = 19;
uint32 next_delegate_index = 20;
repeated DelegateRecord delegates = 21;
repeated DelegateKeyIndexEntry delegate_key_index = 22;   // { bytes dk = 1; uint32 delegate_index = 2; }
repeated DelegateRouteSet delegate_routes = 23;
repeated GenesisRoutingBase routing_bases = 24;          // { bytes round_id; uint32 proposal_id; uint32 vote_decision; bytes ciphertext; }
```

- **Export:** iterate 0x1A–0x1F in key order. Pools and pool share counts already round-trip through `tally_accumulators` and `share_counts`, which accept any `proposal_id` (`types/genesis.go:134-147`; `keeper/genesis.go:128-147,477-590`).
- **ValidateGenesis:**
  - Params: threshold rules and unique verifier IDs and keys.
  - `next_delegate_index ≥ 1`. Records have unique indices in `[1, next)`, valid `dk` and `key_type`, and a pending key is 32 bytes iff the activation time is non-zero.
  - Each record's current `dk` (and its pending key, if any) appears in `delegate_key_index` pointing to that record. Key-index entries are unique and every index is `< next`.
  - Route sets reference existing rounds and delegates, with strictly ascending `proposal_id` in `1..50`.
  - Routing bases reference existing rounds and hold 64-byte ciphertexts.
  - Rounds with `proxy_delegation.pools_routed` must not be ACTIVE.

### 5.3 Queries (gRPC in `query.proto`, REST in `api/query_handler.go`)

| gRPC | REST | Notes |
|---|---|---|
| `ProxyDelegationParams` | `GET /shielded-vote/v1/proxy-delegation/params` | params plus `next_delegate_index` |
| `Delegate{delegate_index}` | `GET /shielded-vote/v1/delegates/{index}` | record plus `effective_dk`, `effective_key_epoch`, `effective_status` |
| `DelegateByKey{dk}` | `GET /shielded-vote/v1/delegates/by-key/{dk_hex}` | Register this route before `{index}` (gorilla/mux, as for `latest`) |
| `Delegates{start_index, limit, status}` | `GET /shielded-vote/v1/delegates?start_index=&limit=&status=` | Ascending index, `limit ≤ 500`, returns `next_start_index` |
| `DelegateRoutes{round, index}` | `GET /shielded-vote/v1/delegate-routes/{round_id}/{index}` | |
| `RoundDelegateRoutes{round, start_index, limit}` | `GET /shielded-vote/v1/delegate-routes/{round_id}?start_index=&limit=` | Lets wallets download all routes without revealing interest |
| `DelegatePools{round, start_index, limit}` | `GET /shielded-vote/v1/delegate-pools/{round_id}?…` | `{delegate_index, share_count, ciphertext}` |
| `NullifierStatus{round, type, nullifier}` | `GET /shielded-vote/v1/nullifier/{round_id}/{type}/{nf_hex}` | Fills the "no nullifier query" gap [doc] dossier chain §1.9 |
| `ProtocolCapabilities` (extended) | existing | §9.1 |

`ProposalTally` must reject `proposal_id == 0`. Today it would return an unpaginated map of every pool (`query_server.go:118-135`). Pools are served by `DelegatePools` instead.

### 5.4 Light-client verifiability (Vizor IAVL proofs)

[doc] Vizor proves `vote`-store keys with `/abci_query` against a signed header (vizor `docs/voting-participation.md:17-31`). Every proxy fact is a point lookup:

- **Delegate identity and status:** key `0x1A‖u32BE(d)`. Wallets must check the snapshot DK against this proven record before building a DC. Otherwise a lying REST server could steer delegations to another index.
- **Routes:** key `0x1C‖round‖u32BE(d)`. One proof gives every route of `d`. Non-membership proves the delegate made no route.
- **DC share inclusion:** key `0x01‖0x02‖round‖share_nf`. Membership proves the share was added to `Pool[d]`, because the nullifier `Poseidon(tag, VC, idx, blind)` binds the DC, and the DC binds `(0, d)` (voting-circuits `share_reveal/circuit.rs:24-31`).
- **Round enable flag and cutoff:** key `0x04‖round` (`VoteRound`).
- **Pool share count:** `ShareCountKey(round, 0, d)`.

---

## 6. Tally hook

### 6.1 Placement [decision]

Routing runs in **EndBlock, step 2**, in the same iteration that sets ACTIVE→TALLYING (`x/vote/module.go:491-512`). It runs before `SetVoteRound`, and after step 1 has computed final roots (`:437-489`).

It is **not** lazy in PrepareProposal for four reasons:
- PrepareProposal cannot write state.
- ProcessProposal, `SubmitPartialDecryption` and `SubmitTally` would each have to recompute combined ciphertexts identically.
- In the transition block no reveal or route can land, because DeliverTx enforces `blockTime < vote_end_time`. So the pools are final when EndBlock runs.
- The partial-decrypt injector reads `GetProposalTally` for TALLYING rounds (`app/prepare_proposal_partial_decrypt.go:153-167`), so it must see routed accumulators from the very first TALLYING block.

### 6.2 Algorithm (`keeper.RouteDelegatePools(ctx, kv, round) (summary, error)`)

```
if round.proxy_delegation == nil || !round.proxy_delegation.enabled || round.proxy_delegation.pools_routed: return
sums := ordered map (p, o) -> ciphertext                  // never iterate a Go map unsorted
for each DelegateRouteSet s under prefix 0x1C||round, in key order (delegate_index ascending):
    poolBytes := GetTally(round, 0, s.delegate_index)
    if poolBytes == nil: continue                          // no revealed shares: nothing to add
    pool := UnmarshalCiphertext(poolBytes)                 // written by AddToTally; a failure is state corruption -> return err (halt)
    pools_used++
    for r in s.routes (proposal_id ascending):
        if r.vote_decision == RouteDecisionAbstain: continue
        if ValidateEntryBounds(round, r.proposal_id, r.vote_decision) != nil: log + continue   // unreachable: validated at route time
        sums[(p, o)] = sums[(p, o)] ⊕ pool ; routes_applied++
for (p, o) in sums sorted by (p, o):
    base := GetTally(round, p, o)                          // may be nil (no direct votes)
    combined := (base == nil) ? sums[(p,o)] : base ⊕ sums[(p,o)]
    if base != nil: Set(0x1F||round||p||o, base)           // routing base for audit
    rho := HashToScalarPallas("svote-route-rerand-v1" || round_id || u32BE(p) || u32BE(o) || Marshal(combined))
    final := combined ⊕ (rho·G, rho·ea_pk)                 // Enc(0; rho), plaintext unchanged
    for i in 1..8 while final.C1 or final.C2 is identity:  // probability about 2^-253
        rho = HashToScalarPallas(rho bytes); final = combined ⊕ Enc(0; rho)
    Set(TallyKey(round, p, o), Marshal(final))
round.proxy_delegation.{pools_routed=true, routed_pool_count=pools_used, applied_route_count=routes_applied}
emit proxy_pools_routed{vote_round_id, routed_pool_count, applied_route_count, buckets_touched}
```

**Definitions and behavior:**
- **ρ.** `HashToScalarPallas` is `curvey.ScalarPallas.Hash`, the same primitive the DLEQ code uses (`crypto/elgamal/dleq.go:55-56`). `G` is `elgamal.PallasGenerator()` (SpendAuthG).
- **Missing accumulators.** A missing pool means skip. A missing `agg[p][o]` means the bucket becomes the pool sum. Unrouted `(d, p)` means the weight abstains, and the pool stays untouched in state forever.
- **Errors.** The hook returns an error only on KV read/write failure or on a stored ciphertext that will not decode. Either is state corruption, and EndBlock errors halt by design (`module.go:441-460`). Adversarial inputs can never make it error.
- **Pools are never zeroed or decrypted.** `CollectNonEmptyAccumulators` (`keeper_tally.go:307-319`), `ValidateTallyCompleteness` (`:324-341`), `ValidatePartialDecryptionCompleteness` (`:348-365`), the partial-decrypt injector (`prepare_proposal_partial_decrypt.go:153`) and `ValidateEntryBounds` (`keeper_voting.go:291-304`) all iterate `round.Proposals` or require `p ≥ 1`. Proposal 0 is therefore excluded with **no change**. Tests pin this, and a comment guard is added.
- **Newly non-empty buckets.** A bucket that was empty and received a pool becomes non-empty, so the existing completeness checks automatically require its partial decryption and tally entry.
- **Tally timeout.** Unchanged (`module.go:641-662`). Routing completes synchronously before TALLYING starts, so it does not consume timeout budget.
- **Ballot counts.** `VoteSummary.ballot_count` keeps counting direct reveals only (`keeper_tally.go:197-209`). Document this; do not add pool counts.

### 6.3 No double counting and no bound overflow (proof sketch)

**Transitions.** Each VAN `v` has weight `W_v` and authority `A_v`.
- **ZKP1** creates `v` with `A = MAX = 2^51−1`.
- **ZKP2(p)** requires bit `p` set in `A_v`. It consumes `v` and outputs `VC(p, c)` with shares summing to `W_v`, and `v'` with `(W_v, A_v \ {p})`.
- **ZKP4** requires `A_v = MAX`. It consumes `v` and outputs a DC with shares summing to `w ∈ [1, W_v]`, and `v'` with `(W_v − w, MAX)`.

**Facts:**
- **F1.** A VAN is consumed at most once, because ZKP2 and ZKP4 use the same nullifier function (circuits contract C1) and the chain stores both in `NullifierTypeVoteAuthorityNote`.
- **F2.** ZKP2 never emits `p = 0`; it has a non-zero gate (voting-circuits `src/vote_proof/circuit.rs:133`, tests `:2738-2782`). ZKP4 only emits `p = 0`.
- **F3.** Each share is added at most once (share-nullifier set; `msg_server_tally_decrypt.go:44`).
- **F4.** `Pool[d]` is added to buckets only by the hook. The hook runs once per round (status transition plus the `pools_routed` guard) and at most once per `(d, p)`, because route sets hold unique `p`.

**Lemma.** For a root VAN with weight `W0` and any `p ≥ 1`, the lineage contributes at most `W0` to `∪_o agg[p][o]`.

*Proof.* By F1 the lineage is a path. Phase 1 is a run of ZKP4 steps producing `w_1..w_k` and a remainder `W_k = W0 − Σw_i`; range checks prevent field wrap (contract C3/C4). After the first ZKP2, `A ≠ MAX` forever, so no further DCs are possible. Phase 2 has at most one VC on `p`, carrying `W_k`. Each `w_i` reaches at most one bucket of `p` (F4). Partial reveals only reduce contributions. So the total is at most `W_k + Σw_i = W0`. ∎

**Theorem.** For every `p`, `Σ_o value(agg[p][o]) ≤ |ballots| ≤ 21·10^14 zat / 12,500,000 = 1.68·10^8 < 2^28 = TallyBSGSBound` (`keys.go:33-35`).

So every routed bucket stays decodable and passes `SubmitTally`'s `total_value < TallyBSGSBound` check (`msg_server_tally_decrypt.go:106-109`). There is no subtraction anywhere, so no modular wrap is possible. The ρ term adds plaintext 0.

### 6.4 Identity attack (why re-randomization is mandatory)

[code] `AddToTally` rejects identity `C1`/`C2` only incrementally, inside DeliverTx (`keeper_tally.go:45-80`). `VerifyPartialDecryptDLEQ` rejects an identity `C1` (`crypto/elgamal/dleq.go:95-96`). `SubmitPartialDecryption` requires a valid DLEQ for **every** non-empty accumulator (`msg_server_tally_decrypt.go:249-279`).

**Attack:**
1. An attacker controls all randomness in two pools, for example as the sole delegator to two attacker-registered delegates, or in one pool plus their own direct votes on an otherwise unused option.
2. They choose `r` values so that the summed `C1 = O`. Each pool individually passes `AddToTally`.
3. Naive routing then stores an identity-`C1` bucket.
4. Every validator's partial decryption fails DLEQ, so no partials are accepted.
5. After 6 h the whole round finalizes with `tally_timed_out` and empty results.

[proto] `route_test.go::TestIdentityAttackAndRerandomization` builds exactly this. Naive routing gives identity `C1`, and verify returns `C1 must be a valid non-identity point`. The ρ-rerandomized bucket is non-identity and still decrypts to the plaintext sum (15).

With ρ derived from the combined ciphertext, the attacker would need `Σr ≡ −H(…(Σr)G…)`, which is a hash fixed point and infeasible.

### 6.5 Determinism, cost and auditability

- **Determinism.** Inputs are KV state only, iterated in sorted order. Point addition is exact and the serialization is canonical, so the result is independent of order. [proto] `TestRoutingConservation` shows decrypted totals equal the plaintext model (with abstentions) and byte-identical output under reversed route order.
- **Cost.** [proto] M3 Ultra: 1,000 delegates × 50 routes take about 0.50 s, and 10,000 × 50 take about 1.39 s, once per round. That is about 2 µs per add plus about 1 ms per re-randomized bucket (≤400 buckets). This is acceptable for one EndBlock. Add a benchmark to CI and an alert threshold.
- **Auditability.** Validators prune history (`PruningOptionEverything` [doc] dossier chain §7). Storing `RoutingBase` therefore lets anyone recompute, from current state, `final = base ⊕ Σ pools(routed) ⊕ Enc(0; ρ(base ⊕ Σ))` using IAVL-provable inputs. Storing the base leaks nothing: partials are only ever produced for final accumulators.

---

## 7. Timing

| Window | Rule | Authentication |
|---|---|---|
| ZKP1, casts, 0x06/0x07 | Unchanged: ACTIVE and `blockTime < vote_end_time` | `vote_end_time` is bound in round_id |
| DCs (0x09/0x0B) | ACTIVE and `blockTime < round.proxy_delegation.cutoff_time`, where `cutoff = vote_end_time − clamp(duration/10, 60 s, 1 h)` and `duration = vote_end_time − created_at_time` | Stored consensus state, IAVL-provable. Not bound in round_id or `RoundAuthPayloadV2` (dossier chain §4). Misreporting fails safe (§9.2). |
| Reveals, including `p = 0` | Unchanged: `blockTime < vote_end_time` (`msgs.go:554-557`) | round_id |
| Routes | ACTIVE and `blockTime < vote_end_time`. No earlier deadline: there is no override (product decision 2), so delegators need no reaction window, and routes need no helper. | round_id |
| Routing | First block with `blockTime ≥ vote_end_time` | Consensus |

**Why the cutoff takes this shape.** It is derived only from `created_at_time` and `vote_end_time`, both fixed at creation, and is stored. That keeps per-round semantics stable even if a later binary changes the formula. It aligns with the submission-server ZIP's last-moment buffer `min(10%, 3600 s)` [doc] specs dossier (SUB:294-312). Because the helper retry cutoff is 5 minutes (`internal/helper/retry_schedule.go:10-16,68-82`), an immediately submitted DC share still has at least 60 s and normally up to 1 h to land. Users arriving later can still vote directly.

**When weight can be lost, and whether it is visible:**

1. **DC rejected after cutoff, by capacity, or by an invalid bound.** The tx fails and no VAN is spent; the wallet falls back to voting directly. Visible.
2. **DC included but some of its 16 shares not revealed before `vote_end_time`.** The pool gets a partial `w`, which is the same as late votes today. Visible to the delegator: share-nullifier non-membership after TALLYING (§5.4), plus helper `share-status`.
3. **No route for p, route landing at or after `vote_end_time`, or delegate SUSPENDED/RETIRED before routing.** The pool abstains on p. Visible: route-set proof plus record status. The delegate wallet sees its rejected tx.
4. **Recovery pending at `vote_end_time` while the old key is lost.** The pool abstains. Visible as `pending_dk_activation_time`.
5. **DC to an index that does not exist.** Prevented at submission by `delegate_index_bound`. If circuits drop the bound (fallback), the reveal is rejected with the permanent error `ErrDelegateNotFound` and the weight is lost. This is a client bug, and the helper surfaces it as a permanent failure.
6. **Tally timeout.** Everything is empty, the same as today.

Nothing above is silent if the client checks the listed proofs.

---

## 8. Spam and DoS

- **Registry.** Fee-less, but a valid threshold attestation is checked in CheckTx before gossip (Ed25519 only, at most 17 verifications). DK uniqueness covers all historical keys, so replays fail. Rotation and recovery are rate-limited to one per 10 min per delegate, and each requires the current DK or an attestation. State growth is bounded by `attested registrations + key changes/600 s`.
- **Routes.** Each costs one Ed25519 verification in CheckTx. At most 50 per `(round, delegate)`, one-shot. A bad signature is rejected before gossip. Recheck re-applies the one-shot check.
- **DCs.**
  - Each DC needs a ZKP4 and consumes at least 1 ballot (`w ≥ 1`). DCs per VAN ≤ `W`, so total DC leaves ≤ total ballots.
  - Per 0.125 ZEC that is at most 1 DC (16 reveals). An existing 1-ballot VAN can already cast 50 VCs (800 reveals), so proxy delegation **does not worsen** the leaf or reveal amplification bound [inference].
  - Batches are capped at 10 DCs and 50 actions.
  - Optional circuit-level dust minimum: open question 5.
- **Tree capacity.**
  - `AppendCommitment` returns `ErrCommitmentTreeFull` when `index ≥ 1<<24`. DeliverTx failure rolls back the whole tx, so all handlers (old and new) become safe, and EndBlock `ComputeTreeRoot` can never see an overfull tree (today a full tree halts the chain [doc] dossier chain §6).
  - New `EnsureCommitmentCapacity(round, n)` gives an early ante rejection.
  - Fix `MaxTreePosition`.
- **Per-block caps.** DC reveals share `MaxVoteShareSubmissionsPerBlock = 256` with vote reveals, which keeps fairness by round only (`docs/helper_submission_invariants.md`). No new cap is needed for routes or registry txs. A general proof-verification budget per block remains a pre-existing gap, not introduced here.
- **Canonical encoding.** All new tags enforce it (§2). Strict JSON at REST.
- **Mempool cache.** Canonical encoding means one payload has one tx hash.

---

## 9. Capabilities, activation and compatibility

### 9.1 `ProtocolCapabilities` (`query.proto:71-77`, `query_server.go:30-36`)

```proto
bool   proxy_delegation = 4;
uint32 vote_action_batch_wire_tag = 5;              // 0x09
uint32 delegate_and_vote_action_batch_wire_tag = 6; // 0x0B
uint32 register_delegate_wire_tag = 7;              // 0x0C
uint32 delegate_route_wire_tag = 8;                 // 0x0F
uint32 update_delegate_wire_tag = 9;                // 0x10
uint32 max_proxy_delegations_per_batch = 10;        // 10
uint32 max_vote_action_batch_size = 11;             // 50
bytes  proxy_delegation_vk_fingerprint = 12;        // ZKP4 VK hash from FFI sv_proxy_delegation_vk_fingerprint
uint32 proxy_delegation_protocol_version = 13;      // 1
```

While dormant, every field is false or zero.

### 9.2 Per-round enable and how wallets authenticate it

- The flag is `VoteRound.proxy_delegation.enabled`, snapshotted from coordinator params at creation (§5.1).
- It is **not** bound into round_id. Adding it would change the Poseidon round-ID preimage and break every existing client's round-ID handling.
- It is **not** covered by the Ed25519-signed `RoundAuthPayloadV2(round_id, ea_pk, pir_layout)` [doc] client dossier.
- Wallets authenticate it with an IAVL proof of `0x04‖round_id`, the same verifier Vizor uses for gov nullifiers.
- If unverified, misreporting fails safe:
  - A false "enabled" leads to a tx rejected with `ErrProxyDelegationDisabled`, and no VAN is spent.
  - A false "disabled" only hides the feature.
  - A fake "accepted" response is detectable via tx query or a VAN-nullifier proof, the same as for all votes today.
- The client or config component may add a signed hint, but chain safety does not depend on it.

### 9.3 Dormant merge (the `7cd8b521` / `771fbdef` pattern)

`types.ProxyDelegationEnabled = false` gates all of the following:
- The new tags in `IsVoteTag` and `DecodeVoteTx`.
- `RegisterImplementations` and `gogoproto.RegisterType` for every new message and nested type.
- The new coordinator payload cases.
- The `p = 0` branch of `MsgRevealShare` (ValidateBasic stays byte-for-byte as today).
- The params snapshot at round creation.
- `ErrCommitmentTreeFull` behavior. Including it is harmless, since the tree has never been full, but gate it for policy purity.
- `ProtocolCapabilities`.

[code] CometBFT hashes `Code`, `Data` and `GasWanted`/`GasUsed` into `LastResultsHash` (cometbft v0.38.21 `types/results.go:45-54`). So a dormant binary must reject new inputs **with the same code as an old binary**. In practice that means failing at decode: an unknown tag falls through to the Cosmos decoder, and an unregistered Any type URL cannot be resolved.

**This is why the per-round flag cannot be a new `MsgCreateVotingSession` field.** Old binaries reject an unknown critical field at decode inside the coordinator Any, whereas a dormant new binary would decode it and fail later with a different code. That would diverge `LastResultsHash`.

`VoteRound` field 31 is never set while dormant, so stored bytes are identical. The new KV prefixes are untouched.

### 9.4 Upgrade and runbook

- Register `v1.7.0` as a no-op upgrade (`app/upgrades.go`, new `app/v1_7_0_upgrade.go` via `registerNoopUpgrade`). Absent keys mean defaults (`NextDelegateIndex=1`, params nil means disabled).
- Add tests to `app/upgrade_test.go`, a `docs/runbooks/software-upgrades.md` "v1.7.0 coordinated cutover" section, a CHANGELOG entry, and updates to `docs/session-status-lifecycle.md`.
- Also fix the session-status-lifecycle doc drift: "TALLYING: Only RevealShare accepted" is wrong (`docs/session-status-lifecycle.md:17-18` vs `msgs.go:554-557`).

**Runbook:**
1. Confirm every round is FINALIZED or CEREMONY_FAILED and every helper queue is empty (existing checks, `software-upgrades.md:476-495`).
2. Schedule `v1.7.0` via the coordinator.
3. At the halt, swap the binary (a rolling install is forbidden).
4. Post-upgrade: check that `ProtocolCapabilities.proxy_delegation == true` and the VK fingerprint matches the circuits release.
5. Coordinator action `MsgSetProxyDelegationParams{verifiers, threshold, enable_for_new_rounds=false}`.
6. Verifier service live; registrations open; the off-chain profile snapshot is published.
7. Set `enable_for_new_rounds=true` and create a `[TEST]` round. Run the e2e checklist, including a routed tally.
8. Enable for production rounds, and gate Vizor via dynamic config.

The v1.6.x maintenance line keeps the feature dormant.

### 9.5 Mixed-version safety and old wallets

- **Validators:** only a coordinated halt is allowed. A dormant `v1.6.x` binary is state-compatible with v1.6.0 (§9.3).
- **Zodl and old Vizor in a proxy-enabled round are unaffected:**
  - They use tags 0x02, 0x03, 0x06 and 0x07, and helper shares with `p ≥ 1`, all unchanged.
  - DC leaves are ordinary tree leaves, so tree sync and witnesses (`CommitmentLeaves`) work unchanged.
  - Their own tx events keep their shapes, and new event types are ignored.
  - Tally results now include delegated weight, which is the intended semantics, under the same query shape.
  - `VoteSummary` excludes pools.
  - Gov-nullifier participation checks are unchanged.
  - The only new surface they see is an extra `proxy_delegation` object in round JSON. zcash_voting does not use `deny_unknown_fields` [doc] client dossier. Zodl's round parser must be confirmed lenient (client contract).
- **ZKP1, ZKP2 and ZKP3 VKs are unchanged.** Only the new ZKP4 VK is added.

---

## 10. Helper (`internal/helper`, `cmd/svoted/cmd/helper.go`) and the zcash_voting helper client

[code] Today the helper **rejects** proposal-0 shares:
- `validatePayload` requires `vote_decision < 8` and `proposal_id ∈ 1..50` (`internal/helper/api.go:771-778`).
- `ValidateShareChoice` requires `p ≥ 1` (`cmd/svoted/cmd/helper.go:310-341`).

**Changes:**
- **`validatePayload`:** if `ProposalID == 0`, require `VoteDecision ≥ 1` and skip the `< MaxVoteOptions` check. Otherwise unchanged. `TreePosition ≤ MaxTreePosition`, using the new 2^24−1 value.
- **`ValidateShareChoice` (`keeperTreeReader`):** for `p == 0`, the round exists, `round.proxy_delegation.enabled`, and `1 ≤ d < NextDelegateIndex`. Failures map to `ErrInvalidRoundChoice` (a permanent 4xx; wallets must not retry).
- **Unchanged and already correct for DCs:**
  - Commitment check `vcHash(round, shares_hash, 0, d) == leaf[tree_position]` (`api.go:663-679`; the FFI accepts any `u32`, `ffi/votecommitment/votecommitment.go:26-32`).
  - The store primary key `(round_id, share_index, proposal_id, tree_position)` (`store.go:188`).
  - The pre-proof share-nullifier dedupe.
  - The ZKP3 prover.
  - Scheduling: wallet `submit_at`, last-moment window (`store.go:1606-1619`), round-robin by round.
  - Retries.
  - `queue-summary`, which omits proposal IDs (`types.go:167`).
- **Quotas:** none new. Admission requires an on-chain DC leaf, so volume is bounded by chain DCs (§8). Capacity planning: each DC costs the same as one vote (16 proofs at about 0.58 shares/s per worker [doc] dossier chain §4). Delegating 1 DC instead of voting P proposals **reduces** helper load by a factor of P.
- **Privacy note:** a helper learns `(tree_position → d)` for a DC, just as it learns `(VC → proposal, choice)` today. It can link the DC tx to its delegate. The trust is unchanged.
- **zcash_voting helper client (contract):**
  - Send `SharePayload{proposal_id: 0, vote_decision: d, tree_position: DC leaf, shares_hash, share_comms, primary_blind, submit_at}`.
  - The SDK currently enforces `proposal_id ≥ 1` in `validate_proposal_id` (`zcash_voting/src/types.rs:1281-1292`, also `zkp2.rs:133,209`), so it needs a DC-specific path.

---

## 11. ZKP4 FFI and private-vote (v2) compatibility

- **Go:**
  - `ffi/zkp/verify.go` adds `ProxyDelegationInputs{VanNullifier, RVpk, VoteAuthorityNoteNew, ProxyDelegationCommitment, VoteCommTreeRoot, AnchorHeight uint64, DelegateIndexBound uint32, VoteRoundId, EaPk}`.
  - Add `Verifier.VerifyProxyDelegation(proof, inputs) error` and a `MockVerifier` method.
  - `ffi/zkp/halo2/{verify.go,verifier.go,verifier_default.go}` pack 9×32 bytes: `van_nf | r_vpk | van_new | dc | root | anchor u64 LE | bound u32 LE | round_id | ea_pk`. This mirrors ZKP2 packing (`ffi/zkp/halo2/verify.go:280-330`), with `proposal_id` replaced by the bound.
- **Rust** (`circuits/src/ffi.rs`):
  - `sv_verify_proxy_delegation_proof(proof, len, inputs, 288)` decompresses `r_vpk` and `ea_pk` (rejecting identity) and builds the instance through voting-circuits' own `proxy_delegation::Instance` constructor. The FFI must not hand-order instance fields.
  - Add a thread in `sv_warm_verifier_caches`.
  - Add `sv_proxy_delegation_vk_fingerprint`.
  - Update `circuits/include/shielded_vote_circuits.h` and bump `circuits/Cargo.toml` to the voting-circuits release containing ZKP4.
- **Expected public instance order** (circuits contract): `[van_nullifier, r_vpk_x, r_vpk_y, van_new, dc, vote_comm_tree_root, anchor_height, delegate_index_bound, voting_round_id, ea_pk_x, ea_pk_y]`. The proof must be ≤ `MaxProofSize` (15 KiB, `keys.go:74`).
- **Private-vote v2** (`origin/roman/private-vote-implementation`, design `docs/design.md`), whichever lands first:
  - DCs keep `DOMAIN_VC` (v1) with a scalar El Gamal share layout, and are revealed by the **unchanged v1 ZKP3** with a public `vote_decision = d`.
  - The chain must keep the v1 ZKP3 verifier and accept 0x04 **only with `p = 0`** in v2 rounds. v2 vector reveals use their own tag with `DOMAIN_VC_V2`, which cannot open a DC.
  - Routing is unchanged, because v2 still accumulates one ciphertext per `(round, proposal, bucket)`.
  - `VoteAction` reserves oneof fields 3..9 for v2 cast variants.
  - ZKP4 must remain a sibling of the v1 cast relation, not of compact v2 ZKP2.

---

## 12. Test plan

**Types and codec** (`x/vote/types`, `api`):
- Table tests for each ValidateBasic. Cover ordering (DC after cast), DC count 0 and 11, total 51, duplicate VAN nullifiers across DC and cast, duplicate commitments, anchor rules (0x09 non-zero, 0x0B zero), route ordering and abstain sentinel, `p = 0` reveal with `d = 0`, and update oneof arity.
- Golden vectors for D1–D7, shared with e2e `sighash.rs`.
- Canonical-encoding rejection: unknown field, non-deterministic order, unknown nested field inside the oneof.
- `IsVoteTag`, `TagForMessage` and `DecodeVoteTx` round trips.
- **Dormant tests** (resurrect the pattern removed in `771fbdef`): a dormant binary rejects tags 0x09, 0x0B, 0x0C, 0x0F and 0x10 and the new Any payloads with the same ABCI code as v1.6.0, `MsgRevealShare{p=0}` is rejected identically, and `VoteRound` bytes are unchanged.

**Keeper:**
- **Registry:**
  - Index assignment starts at 1 and is monotone.
  - Expired, future, insufficient, unknown-verifier, duplicate-verifier and wrong-chain-ID attestations are rejected.
  - Bad PoP, small-order DK and non-canonical DK are rejected.
  - Reuse of a historical key is rejected.
  - Rotation: epoch binding, replay, the 600 s interval.
  - Recovery: pending, then lazy activation exactly at the boundary, cancelled by a rotate, recovery attestation bound to the stored identity commitment.
  - Retire is terminal. Coordinator suspend and reinstate, and un-retire is rejected.
- **Routes:**
  - One-shot, all-or-nothing batches, decision bounds, abstain.
  - Round disabled, `blockTime == vote_end_time` rejected, delegate suspended or retired, pending key active at the boundary.
- **Reveal `p = 0`:**
  - Accepted for an existing `d` in any status. Rejected for `d = 0`, `d ≥ next`, and when the round is disabled.
  - Updates the pool and pool share count, and shares the nullifier set with votes.
- **Action batches:**
  - Leaf order is `final+1+i`, event attributes are correct, nullifiers are set, and gov nullifiers too for 0x0B.
  - Cutoff and bound checks.
  - Atomicity: an injected failure before writes leaves no state.
  - `ErrCommitmentTreeFull` at capacity, for old handlers as well.
- **Routing hook:**
  - Decrypt-equality against a plaintext model, using a test EA key with randomized lineages, abstentions, missing pools and empty base buckets.
  - Order independence (permuted insertion gives byte-identical KV).
  - The identity-attack vector produces non-identity results.
  - Pools at `p = 0` are excluded from `CollectNonEmptyAccumulators`, partial decryptions and tally entries.
  - Rounds without proxy delegation produce byte-identical state to before the change.
  - `pools_routed` guard; RoutingBase written.
  - Property test: the bucket sum never exceeds total ballots and stays `< TallyBSGSBound`.
- **Genesis:** round-trip export, import and validate with a registry, pending keys, routes, pools and routing bases, plus negative validation cases.

**Ante** (`x/vote/ante`):
- Step ordering: cheap stateful rejections happen before signatures and proofs.
- RecheckTx keeps the one-shot route, DK uniqueness, nullifier, cutoff and capacity checks, and skips signatures and proofs.
- CheckTx with an unset block time.
- ZKP4 chaining roots: the first action uses the real root, later actions use `SingleLeafRoot`, and 0x0B starts from `van_cmx`.
- Under the `halo2,redpallas` tags, a real ZKP4 fixture from voting-circuits.

**App** (`app`):
- `PrepareProposal`/`ProcessProposal` count `p = 0` reveals within the 256 cap.
- `v1.7.0` upgrade test.
- `ProtocolCapabilities`.
- An EndBlock benchmark (1k and 10k delegates).
- A two-node determinism test (same blocks give the same app hash and LastResultsHash) across the transition block.

**Helper:** `validatePayload` and `ValidateShareChoice` with `p = 0`; an end-to-end in-process share submit with `p = 0`.

**e2e** (`e2e-tests`, new `tests/proxy_delegation.rs`; extend `src/api.rs`, `src/payloads.rs`, `src/sighash.rs`):
1. Set the verifier set via a coordinator action, register 3 delegates, query and IAVL-prove the records.
2. Create a round with the flag on.
3. Delegator A: 0x0B with ZKP1, 2 DCs and casts. Delegator B: 0x09 with 1 DC and casts against a real anchor.
4. Helpers reveal DC and VC shares.
5. Delegates route (one abstains on P2, one never routes P3).
6. At TALLYING, check `pools_routed`, finalize, and check that per-option totals equal the expected values.
7. Negative cases: route at or after end, a duplicate route, a DC after cutoff, a bad bound, a reveal with `d ≥ next`.
8. A Zodl-style flow (0x07 plus `p ≥ 1` shares) in the same round still works.
9. Tree-capacity tests are unit-level only.

---

## 13. Cross-component contracts (summary; the full list is in `cross_component_contracts`)

**Circuits (ZKP4) must guarantee:**
- **C1:** the VAN nullifier is exactly ZKP2's derivation.
- **C2:** input authority == MAX.
- **C3:** the successor keeps address, rand and round, has weight `W−w` range-checked to `[0, 2^30)`, and has authority MAX.
- **C4:** `1 ≤ w ≤ W`, with 16 shares each in `[0, 2^30)` summing to `w`.
- **C5:** `DC = Poseidon(DOMAIN_VC, round, shares_hash, 0, d)` with proposal 0 constant and `d` private.
- **C6:** `1 ≤ d ≤ delegate_index_bound` (public).
- **C7:** `r_vpk` spend authority as in ZKP2.
- **C8:** the instance order in §11, K ≤ 12, proof ≤ 15 KiB.
- **C9:** ZKP2's non-zero gate and ZKP3 stay unchanged.

**Identity / verifier service:**
- Signs D3 exactly, with one active registration per provider account.
- `identity_commitment = BLAKE2b-256("svote-delegate-identity-v1" ‖ lp8(provider) ‖ lp8(subject_id) ‖ salt32)`.
- Recovery attestations bind `key_epoch`.
- Requests suspensions through the coordinator.

**Client:** mirrors D1/D2/D7, uses the new event shapes, respects the cutoff and bound, and verifies records and routes by IAVL.

**Vizor:** IAVL proofs for the §5.4 keys, Ed25519 DK custody, and route signing.
