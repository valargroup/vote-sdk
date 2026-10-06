# CIRCUITS spec: proxy delegation via public delegate pools (B2), voting-circuits

**Labels.** [code] means the source does this today (file:line). [doc] means a document claims it. [measured] means I ran it in the throwaway prototype. [inference] means it is my analysis.

**Path shorthands.**
- `vc/` = voting-circuits origin/main v0.12.2 snapshot (`.../scratchpad/src/voting-circuits`).
- `pv:` = `git show origin/roman/private-vote-implementation:<path>` in `/Users/czstudio/Documents/voting-circuits`.
- `vs/` = vote-sdk worktree (`/Users/czstudio/Documents/vote-sdk/.claude/worktrees/vote-delegation-planning-29b451`).
- `zv/` = zcash_voting snapshot (`.../scratchpad/src/zcash_voting/zcash_voting`).
- `proto/` = my prototype (`.../scratchpad/proto-circuits/voting-circuits/src/proxy_delegation`).

No repo was modified.

---

## 0. Verdict

1. **No fatal flaw at the circuit layer.** B2 works with:
   - one new additive circuit, ZKP4;
   - an unchanged ZKP3;
   - unchanged ZKP1 and ZKP2 VKs.

   [measured] A full ZKP4 prototype (14 conditions) fits **K=11 at 2,015/2,048 rows**, the same high-water mark as ZKP2. The proof is **11,008 B** against the 15,360 B cap, and proving takes about the same time as ZKP2. Eleven MockProver tests pass, and each negative case is rejected by the intended constraint.

2. **The baseline needs one circuit addition (HIGH).** As specified, ZKP4 lets a single VAN of weight W mint up to W delegation commitments (DCs), because each DC only needs w >= 1. Each DC creates 16 helper reveals. That is an unbounded DoS amplification of helper and reveal load on a fee-less chain. Today a VAN is capped at 50 casts × 16 shares.
   - **Fix:** a per-VAN-chain **DC slot nullifier** (4-bit slot, so at most 16 DCs per registration per round).
   - [measured] It still fits K=11 with no new advice columns.
   - It requires the chain to reject duplicate slot nullifiers *inside one batch*. `CheckNullifiersUnique` does not do this today (`vs/x/vote/keeper/keeper_voting.go:54-66`).

3. **ZKP2's `proposal_id != 0` gate becomes load-bearing for soundness (MEDIUM).**
   - Code: `vc/src/vote_proof/gadgets/authority_decrement.rs:398-416`, documented there as "defense-in-depth".
   - Without the gate, ZKP2 could clear sentinel bit 0 and emit a `(proposal 0, decision d)` commitment while keeping full weight W for all 50 proposals. That would double count W into `Pool[d]`.

4. **"ZKP3 unchanged" holds only while the v1 scalar primitives exist (MEDIUM).** The private-vote branch deletes all of the following:
   - `DOMAIN_VC` as a live tag;
   - the 5-input share commitment;
   - the two-level shares hash;
   - the in-circuit 16-ciphertext El Gamal gadget.

   Source: `pv:CHANGELOG.md:53-58`, `pv:src/domain_tags.rs:25-31`. The merge must keep them; see §6.

5. **Non-circuit blockers for proposal-0 reveals.** The chain, the helper and zcash_voting all reject `proposal_id = 0` and `decision >= 8` today (§4.3).

6. **Integrator flag (not circuits, MEDIUM).** Decision 4 says per-delegate totals stay hidden. With public routes and published per-option tallies, those totals are statistically inferable. When a delegate skips proposal P, `turnout(P') - turnout(P) ≈ Pool[d]` as long as direct-voter turnout is stable across proposals (§9).

---

## 1. Terminology and naming

- Keep **"delegation"** for ZKP1: notes into a VAN.
- The new feature is **proxy delegation**:
  - Rust module `voting_circuits::proxy_delegation`;
  - proof name **"proxy-delegation proof (ZKP #4)"**;
  - commitment **"delegation commitment (DC)"**;
  - per-delegate accumulator **"pool"**;
  - a delegate's per-proposal choice **"route"**.
- Never shorten `proxy_delegation` to `delegation` in code.
- User-facing:
  - ZKP1 is "Register ZEC to vote".
  - Proxy delegation is "Choose delegates" / "Your delegates", with a per-delegate row such as "12.5 ZEC with @alice".
  - Avoid the bare word "delegation" in UI copy, because the codebase uses it for registration.

---

## 2. ZKP4: proxy-delegation (delegation-commitment) proof

A sibling of ZKP2. It spends a **full-authority** VAN, emits a successor VAN with weight `W - w` (same address, round, rand and MAX mask), and emits one DC that carries `w` in 16 El Gamal shares for private delegate index `d`.

### 2.1 Public inputs (11 field elements)

Offsets 0-6 mirror ZKP2 so chain code can be shared (`vc/src/vote_proof/circuit.rs:157-193`).

| Off | Name | Binding |
|---|---|---|
| 0 | `van_nullifier` | `constrain_instance` (C5) |
| 1 | `r_vpk_x` | C4 |
| 2 | `r_vpk_y` | C4 |
| 3 | `vote_authority_note_new` | C6 |
| 4 | `delegation_commitment` | C13 |
| 5 | `vote_comm_tree_root` | C1 |
| 6 | `vote_comm_tree_anchor_height` | transcript-bound only; caller authenticates, as in ZKP2 `circuit.rs:170-177` |
| 7 | `voting_round_id` | copied to advice; feeds C2, C5, C6, C13, C14 |
| 8 | `ea_pk_x` | C11 (El Gamal gadget pins it) |
| 9 | `ea_pk_y` | C11 |
| 10 | `dc_slot_nullifier` | C14 |

ZKP4 has **no `proposal_id` and no `delegate_index` public input**. The delegate index stays hidden inside the DC, and the reveal publishes it later where it cannot be linked to the DC.

### 2.2 Private witnesses

- `vote_comm_tree_path: [Fp; 24]`, `vote_comm_tree_position: u32`.
- `vpk_g_d`, `vpk_pk_d`: `NonIdentityPoint`.
- `total_note_value` = W, the input VAN weight in ballots.
- `remaining_note_value` = W_new = W - w.
- `van_comm_rand`, `vote_authority_note_old`.
- `vsk: Fq`, `rivk_v: Fq`, `alpha_v: Fq`, `vsk_nk: Fp`.
- `shares[16]`; `enc_share_c1_x/c1_y/c2_x/c2_y[16]`; `share_blinds[16]`; `share_randomness[16]`.
- `ea_pk: Affine`.
- `delegate_index: Fp`, a u32 by builder contract.
- `dc_slot: Fp`, 4 bits.

Prototype struct: `proto/circuit.rs:180-212`.

### 2.3 Constants baked into the VK

Each constant is assigned with `assign_advice_from_constant` and copy-constrained to the enabled `constants` fixed column, so it is part of the VK.

| Constant | Value |
|---|---|
| `DOMAIN_VAN` | 0 |
| `MAX_PROPOSAL_AUTHORITY` | 2^51-1 (`vc/src/params.rs:45`) |
| VAN nullifier tag | `"vote authority spend"` (`vc/src/domain_tags.rs:61-63`) |
| `DOMAIN_VC` | 1 |
| `POOL_PROPOSAL_ID` | 0 |
| Slot tag | `"proxy delegation slot"` (new string tag) |

Prototype: `proto/circuit.rs:413-420, 506, 532, 707-716`.

### 2.4 Conditions

Numbering mirrors ZKP2 where a condition is shared. All gadgets are the existing `pub(crate)` ones, so ZKP4 must live inside voting-circuits.

| # | Relation | Gadget / placement |
|---|---|---|
| C1 | `MerklePath(van_old, pos, path) = root` (instance 5) | `synthesize_poseidon_merkle_path::<24>` on the primary Poseidon config, as in ZKP2 `circuit.rs:986-1006` |
| C2 | `van_old = Poseidon2(Poseidon6(DOMAIN_VAN, gd.x, pkd.x, W, round, MAX), rand)` with **MAX as the constant cell** | `van_integrity_poseidon` (`vc/src/gadgets/van_integrity.rs:103-138`). The authority slot is wired to the MAX constant, so only VANs whose mask is exactly MAX can be opened. This is the "nothing voted yet" requirement. |
| C3 | `pkd = [CommitIvk(ExtractP([vsk]G), nk, rivk)] gd` | `prove_address_ownership` (as ZKP2 `circuit.rs:918-946`) |
| C4 | `r_vpk = [vsk]G + [alpha_v]G` -> instance 1, 2 | `prove_spend_authority` (as `circuit.rs:959-967`). The hotkey signs the batch sighash out of circuit. |
| C5 | `van_nullifier = Poseidon4(nk, "vote authority spend", round, van_old)` | **Byte-identical to ZKP2 C5** (`circuit.rs:1040-1070`), so both circuits share one nullifier set |
| C6 | `van_new = Poseidon2(Poseidon6(DOMAIN_VAN, gd.x, pkd.x, W_new, round, MAX), rand)` -> instance 3 | Same address, round and rand cells as C2; same MAX constant cell |
| C7 | `W_new in [0, 2^30)` | `LookupRangeCheck.copy_check(W_new, 3 words, strict)` on El Gamal track B (`elgamal_advices_b[9]`); the 52-row authority chip that used to sit there is gone |
| C8 | `sum(shares_0..15) + W_new = W` | 15 + 1 `AddChip` rows plus `constrain_equal` |
| C9 | each `share_i in [0, 2^30)` | as ZKP2 `circuit.rs:1218-1225` |
| C10 | `shares_hash = Poseidon16(Poseidon5(blind_i, c1x, c2x, c1y, c2y))` | `compute_shares_hash_in_circuit`, 2 on primary and 14 on the hash track (`vc/src/shares_hash.rs:142-187`) |
| C11 | `C1_i = [r_i]G`, `C2_i = [share_i]G + [r_i]ea_pk`, both coordinates, `r_i != 0` | `prove_elgamal_encryptions` on two tracks, split 8/8 (`vc/src/gadgets/elgamal.rs:194-300`) |
| C12 | `w = sum(shares) != 0` | `NonZeroConfig` on track B (`vc/src/gadgets/nonzero.rs:46-71`) |
| C13 | `DC = Poseidon5(DOMAIN_VC, round, shares_hash, 0, delegate_index)` -> instance 4 | `vote_commitment_poseidon` (`vc/src/gadgets/vote_commitment.rs:73-99`) on the primary track. The `0` is the `POOL_PROPOSAL_ID` constant cell. |
| C14 | `dc_slot in [0, 16)`; `dc_slot_nullifier = Poseidon4(nk, TAG_SLOT + dc_slot, round, rand)` -> instance 10 | See the C14 details below |

**C14 details.**
- The range check is `copy_short_check(dc_slot, 4)` on track B (`zakura-halo2-gadgets-2.0.0/src/utilities/lookup_range_check.rs:234-251`).
- One `AddChip` row computes `TAG_SLOT + slot`.
- The Poseidon runs on a **third Pow5 config that reuses `elgamal_advices_b[1..5]`** (no new advice columns) with 6 new fixed columns.
- [measured] Every other placement I tried overflowed K=11.

[measured] `proto/circuit.rs:427-742` implements all of C1-C14.

### 2.5 Why each bound exists (soundness)

- **C7, `W_new < 2^30` (anti-inflation, and the w <= W check).**
  - Without it, a prover picks w > W. Then `W_new = W - w` wraps to `p - (w-W)`, and C8 still holds in F_p.
  - The DC then carries w, inflating the pool by `w-W`. The junk successor is unspendable, but the inflation has already happened.
  - With C7: W <= 2^30 (every VAN; see the invariant below) and w < 2^34 (C9), so `W = w + W_new` holds over the integers and `w <= W`.
  - [measured] Test `zkp4_rejects_overdelegation_w_gt_w` fails only at "3 words range check".
- **C9, `share_i < 2^30`.** This is the same reason as ZKP2 cond 9 (`circuit.rs:1184-1214`): the base-field sum must equal the scalar-field plaintexts, and the result must stay BSGS-bounded. It also gives `w < 2^34 < p`, which makes C12 meaningful over the integers.
- **C12, `w != 0` (so w >= 1).** It is needed for three reasons:
  - (a) It makes the successor differ from the input VAN (weight strictly decreases), so every VAN in a chain is distinct and VAN nullifiers never repeat inside a batch. That matters because the chain misses intra-list duplicates.
  - (b) It forbids free zero-weight DCs. Each DC costs 16 helper ZKP3s and 16 reveal txs.
  - (c) It avoids silently burning the remaining weight into an already-nullified duplicate VAN.
  - [measured] `zkp4_rejects_zero_weight` fails at gate `value * inv = 1`.
- **C2 = MAX (constant), and C6 = MAX.** The pool is routed to *every* proposal, so the delegated weight must hold authority on every proposal. MAX guarantees nothing has been cast from this VAN, which rules out double counting on an already-voted proposal. Keeping MAX on the successor lets the delegator keep adding DCs or start casting.
  - [measured] `zkp4_rejects_non_max_authority` uses mask `MAX - 2^1` and is rejected at VAN integrity/Merkle.
- **C14, slot cap.** All VANs of one registration chain share `(nk, round, rand)`, because ZKP2 and ZKP4 both reuse rand (`vc/src/vote_proof/circuit.rs:1112-1123`). So there are at most 16 distinct slot nullifiers per chain, which means at most 16 DCs per ZKP1 registration per round.
  - Keyed by nk: unlinkable to anyone else.
  - Bound to round: no cross-round collision even if keys are reused.
  - `TAG_SLOT + s` (s < 16) only changes byte 0 of the 21-byte ASCII tag. It never equals `"vote authority spend"`, so slot and VAN nullifiers are disjoint Poseidon4 families.
- **No range check on W.** Every VAN satisfies W <= 2^30:
  - ZKP1 range: `vc/src/delegation/circuit.rs:1340-1375`;
  - ZKP4 C7;
  - ZKP2 preserves W.
  
  Soundness here only needs C7 and C9.
- **No range check on `delegate_index`.** An index that is not a u32, or not registered, makes the DC unrevealable. The chain rejects the reveal and the delegator only loses its own weight; there is no inflation path. The builder takes a `u32`.

### 2.6 Preimages (normative)

```text
van           = Poseidon2(Poseidon6(0, gd.x, pkd.x, W, round, MAX), rand)          // van_integrity.rs:61-79
van_nullifier = Poseidon4(nk, tag("vote authority spend"), round, van_old)           // vote_proof/circuit.rs:218-229
share_comm_i  = Poseidon5(blind_i, c1_i.x, c2_i.x, c1_i.y, c2_i.y)                  // shares_hash.rs:46-63
shares_hash   = Poseidon16(share_comm_0..15)                                         // shares_hash.rs:65-90
DC            = Poseidon5(1 /*DOMAIN_VC*/, round, shares_hash, 0 /*pool*/, delegate_index)
dc_slot_nf    = Poseidon4(nk, tag("proxy delegation slot") + slot, round, rand),  slot in [0,16)
share_nf      = Poseidon4(tag("share spend"), DC, share_index, blind_i)              // share_reveal/circuit.rs:185-196 (unchanged)
```

All hashes use `P128Pow5T3`, width 3, rate 2, `ConstantLength<L>`.

### 2.7 Namespace separation

**ZKP2 cannot produce a proposal-0 commitment** [code]:
- `proposal_id` is copied from the instance into a single cell (`vote_proof/circuit.rs:1083-1094`).
- That *same cell* feeds the authority chip (`:1096-1102`) and the VC preimage (`:1423, 1434-1443`).
- Chip row 0 enforces `q_cond_6·(1 - proposal_id·pid_inv) = 0` (`authority_decrement.rs:407-416`), which no witness satisfies when proposal_id = 0.
- The lookup alone *admits* `(0,1)`, because it is the load-bearing default row (`:149-160`), and bit 0 of every mask is set.
- So the gate is now the only in-circuit barrier against `VC(0, d)` with full retained weight.
- Second and third barriers:
  - the chain rejects MsgCastVote proposal 0 (`circuit.rs:140-141` [doc]);
  - the builder rejects proposal 0 (`vote_proof/builder.rs:399-404`).
- The MockProver test `proposal_id_zero_fails` exists (`circuit.rs:2741`) and must be relabelled as a proxy-delegation soundness test.
- The private-vote branch keeps the gate (`pv:src/vote_proof/gadgets/authority_decrement.rs:37-38, 405`), and so does `adam/lookup-free-authority-prototype`.

**ZKP4 cannot produce `p != 0`.** The proposal slot is a fixed-column constant cell and DOMAIN_VC is a constant, so no witness or instance can change them. [measured] `zkp4_rejects_nonzero_pool_proposal` fails.

**Leaf types.**
- DC and VC leaves are both `Poseidon5(DOMAIN_VC, …)`. They differ in the proposal slot: 0 vs 1..50.
- VAN leaves are `Poseidon2(...)`. The `ConstantLength` capacity encodes the length, so a VAN leaf can never open as a DC or VC, and vice versa.
- ZKP3 binds `proposal_id` publicly, so a DC share can only be revealed into `(round, 0, d)` and a VC share only into `(round, p, decision)`.

**Domain tag decision.** Reuse `DOMAIN_VC` with proposal 0 rather than add a new `DOMAIN_DC`. A new tag would change ZKP3's VK (`share_reveal/circuit.rs:794-798` bakes `DOMAIN_VC`).

**Register in `domain_tags.rs`:**
- `POOL_PROPOSAL_ID = 0`, documented as the DOMAIN_VC sub-namespace;
- `proxy_delegation_slot()` = `"proxy delegation slot"`;
- PRF domains `VOTE_PRF_DOMAIN_DC_ELGAMAL=0x10`, `_DC_BLIND=0x11`, `_DC_SHUFFLE=0x12`, `_DC_REMAINDER=0x13`, `_DC_ELGAMAL_SINGLE_SHARE=0x14`. These avoid 0x05/0x06, which the private-vote branch claims (`pv:src/domain_tags.rs:66-69`).

Add all of them to the pinned-value and distinctness tests (`vc/src/domain_tags.rs:90-161`). The distinctness test must include `slot_tag + s` for s in 0..16.

---

## 3. Separate ZKP4 vs a mode inside ZKP2: decision and measurements

**Decision: a separate ZKP4 circuit.**

| Circuit (K) | rows / 2^K | adv / fixed / lookups / perm | PIs | proof B | prove (8 thr) | verify |
|---|---|---|---|---|---|---|
| ZKP2 today (11) | 2,015 / 2,048 | 34 / 76 / 5 / 41 | 11 | 11,008 | 98.6 ms (build+prove, same process) | ~1.9 ms [dossier] |
| **ZKP4 recommended, with C14 (11)** | **2,015 / 2,048** | 34 / 78 / 4 / 44 | 11 | **11,008** | **95.6 ms** | 1.65 ms |
| ZKP4 without C14 (11) | 2,015 / 2,048 | 34 / 73 / 4 / 41 | 10 | 10,560 | 91 ms | 1.6 ms |
| B1, 10 public slots (11) | 1,662 / 2,048 | 34 / 72 / 4 / 41 | 60 | 10,528 | 91 ms | n/a |
| ZKP3 (10), unchanged | 976 / 1,024 | 13 / 19 / 0 / 20 | 9 | 4,992 | ~26 ms [dossier] | n/a |

[measured] Notes on the measurements:
- Hardware: Apple M3 Ultra, release, Zakura, `RAYON_NUM_THREADS=8`.
- The host was shared (load average 12-17), so treat times as relative. The dossier's unloaded ZKP2 figure is ~71 ms, which suggests ~70 ms for ZKP4 [inference].
- ZKP4 keygen: 89 ms; keygen peak RSS 140 MiB (ZKP2 136 MiB in the same harness).
- Witness-independence holds: `Circuit::default()` max_rows equals the filled max_rows.
- ZKP4 primary track headroom is still 33 rows (27 usable). Any future condition must go on another track, or the circuit moves to K=12 (about 2x prove time).

**Why not a mode inside ZKP2:**
1. **VK impact.** It would change ZKP2's VK for every voter, forcing Zodl and old Vizor to upgrade in lockstep and violating "Zodl must not break". ZKP4 is additive: ZKP1/2/3 VKs stay byte-identical, which the fingerprint tests at `vc/src/vote_proof/prove.rs:277-300` etc. already verify.
2. **Rows.** A mode needs muxes on:
   - authority: decrement vs MAX-equality;
   - weight: W vs W-w;
   - proposal slot: pid vs 0, with the load-bearing proposal-0 gate disabled when mode = 1, which is a soundness hazard;
   
   plus C7, C12 and C14. That is on top of a 98.4%-full circuit.
3. **No privacy gain.** ZKP2's `proposal_id` is public, so a DC would still be distinguishable unless the ZKP2 public interface changed too.
4. **Cost.** Each ZKP4 costs about one ZKP2, so 10 DCs cost about 10 ZKP2-equivalents. Mobile: the dossier reports ~2 CPU-s per ZKP2 including overhead on an M4 Max (`zv/../docs/bundle_pipeline_benchmark.md:39-55`), so expect several seconds per DC on phones [inference].

---

## 4. ZKP3 reuse for DC reveals (`proposal_id = 0`, `vote_decision = delegate_index`)

### 4.1 Circuit [code]: no range checks, no lookups, no `decision < 8` assumption

- `proposal_id` and `vote_decision` are only `assign_advice_from_instance`'d (`vc/src/share_reveal/circuit.rs:528-553`) and hashed into the VC preimage (`:793-815`).
- No gate or lookup touches them. CircuitCost reports `lookups: 0`, and the only custom gate is the share mux (`:389-482`).
- The share nullifier binds only `(vote_commitment, share_index, blind)` (`:867-901`).
- The builder (`vc/src/share_reveal/builder.rs:60-122`) and the verifier (`prove.rs:211`) are generic.
- The chain FFI verify only checks canonical Fp encoding (`vs/circuits/src/ffi.rs:935-1003`).
- Go writes `uint64(VoteDecision)` into slot 6 (`vs/ffi/zkp/halo2/verify.go:431-435`).
- The helper's FFI prover takes `vote_decision: u32` with no range check (`ffi.rs:1062-1063, 1208-1222`).

### 4.2 [measured] `zkp3_reveals_dc_share_with_pool_proposal_and_large_index`

The test builds a real ZKP4 witness and reveals shares 0 and 7 with the unchanged `share_reveal::build_share_reveal(..., proposal_id=0, vote_decision=1234, ...)`. The reveal passes against `SingleLeafRoot(DC)`. Each of the following fails:
- decision 1235;
- proposal 1.

`delegate_index = u32::MAX` also passes.

### 4.3 Non-circuit blockers that must change (no VK change)

| Location | Current behaviour | Required change |
|---|---|---|
| Chain `MsgRevealShare.ValidateBasic` (`vs/x/vote/types/msgs.go:245-250`; `vote_validation.go:51-57`) | Requires proposal 1..50 and decision < 8 | Allow `proposal_id == 0` when the round has the proxy capability; the decision is then a delegate index (u32) checked against the registry in the keeper |
| Helper `validatePayload` (`vs/internal/helper/api.go:771-778`) | Same restriction; choice validator at `:742` | Same relaxation; the choice validator for proposal 0 becomes a registry lookup. `vcHash` against the tree leaf (`:663`) works unchanged. |
| zcash_voting `build_share_payloads` (`zv/src/vote_commitment.rs:32-33`) and `VoteShareWire::validate` (`zv/src/wire_codec.rs:303-313`) | Reject proposal 0 and decision >= 8 | Need a DC payload path that skips them |
| `vc/src/share_reveal/prove.rs:169-181` | Caller contract says proposal_id "must come from the active session's published proposal list" | Doc must add the pool namespace |

---

## 5. Batch chaining

**Action order inside one atomic batch** (one VAN chain):

```text
[ZKP1?]  ->  ZKP4_1 … ZKP4_k  ->  ZKP2_1 … ZKP2_m      (k <= 10 policy, k <= 16 hard per chain; m <= 50)
```

**Anchors:**
- Action 0 uses the real root at `vote_comm_tree_anchor_height`, or `SingleLeafRoot(van_cmx)` with synthetic height 0 when ZKP1 is present.
- Action i+1 uses `SingleLeafRoot(action_i.vote_authority_note_new)`.
- This is the existing mechanism (`vs/x/vote/ante/validate.go:181-243, 380-449`). It generalises to a heterogeneous ordered action list with **no circuit change**: ZKP4 consumes the root exactly like ZKP2.

**Ordering is cryptographically enforced.** After any ZKP2 the mask is no longer MAX, so a later ZKP4 cannot open the successor (C2).
- [measured] The relation is covered by `zkp4_rejects_non_max_authority`.
- The chain should still reject `cast-before-DC` statelessly, to fail fast.
- DCs can also be added in later txs, anchored at the real tree, as long as the chain has not cast yet. Successor VANs keep MAX.

**Sighash and signatures.** One batch-wide digest covers every action in order (ZKP1 + DCs + casts). Each ZKP4 `r_vpk` signs it, as for casts.

**State effects (chain contract).**
- Every `van_nullifier` goes into the VAN nullifier set (type 0x01), the **same set** as ZKP2.
- Every `dc_slot_nullifier` goes into a new round-scoped set (type 0x03).
- **Both sets need intra-batch duplicate rejection.**

**Tree appends.**
- The final successor VAN is appended once.
- Then DC_1..DC_k, then VC_1..VC_m, in action order. Intermediate VANs are not appended, as today.
- DC leaf indices must be exposed in events/responses, because the client needs them for helper payloads and ZKP3 paths.

**Interaction with `MsgDelegateAndCastVoteBatch`.** A ZKP1 VAN always has mask MAX, because ZKP1 bakes the constant (`vc/src/delegation/circuit.rs:1518-1525`). ZKP4_1 anchors at `SingleLeafRoot(van_cmx)`, so registration + proxy delegation + casts fit in one tx.

**Size.** 1 + 10 + 50 actions is about 61 × 11 KB ≈ 670 KB. That is under the 5 MiB block limit (`vs/app/consensus_limits.go:13`).

**Proposed wire shape** (the chain component owns the final version):

```proto
message MsgProxyDelegate {               // one ZKP4 action
  bytes  van_nullifier = 1;  bytes vote_authority_note_new = 2;
  bytes  delegation_commitment = 3;  bytes dc_slot_nullifier = 4;
  bytes  proof = 5;  bytes vote_round_id = 6;  uint64 vote_comm_tree_anchor_height = 7;
  bytes  vote_auth_sig = 8;  bytes r_vpk = 9;
}
message VoteAction { oneof action { MsgProxyDelegate proxy_delegate = 1; MsgCastVote cast = 2; } }
message MsgVoteActionBatch { repeated VoteAction actions = 1; }            // DCs first
message MsgDelegateAndVoteActionBatch { MsgDelegateVote delegation = 1; MsgVoteActionBatch batch = 2; }
```

**FFI public-input buffer:** 11 × 32 B in the order of §2.1, with `r_vpk` decompressed, as for ZKP2.

---

## 6. Compatibility with private vote choice (`origin/roman/private-vote-implementation`)

**What the branch does [code]:**
- Replaces ZKP2 with a compact cast circuit (no El Gamal; 1,662 rows) and ZKP3 with a vector reveal: 37 PIs, 8 buckets (`pv:src/share_reveal/circuit.rs:928`; its header comment "69 public inputs" at `:62` is stale).
- Uses `VC_v2 = Poseidon5(DOMAIN_VC_V2=2, round, shares_hash, proposal, D)` (`vc/docs/design.md:296-306`).
- **Removes** v1 `DOMAIN_VC`, the 5-input share commitment, the two-level shares hash and `prove_elgamal_encryptions`. Its `elgamal.rs` keeps only `base_to_scalar` and `elgamal_encrypt` (`pv:CHANGELOG.md:53-58`, `pv:src/gadgets/elgamal.rs:42,92`).
- ZKP1.5 proofs are 57.7 KiB (`design.md:470-475`), above the 15 KiB consensus cap, so that release needs a chain change anyway.

**Recommendation: DCs stay scalar forever.** A pool contribution has no hidden choice to protect; the "choice" is the delegate, revealed publicly but unlinkably at reveal time.
- ZKP4 depends only on v1 primitives: scalar El Gamal, the v1 share commitment and shares hash, DOMAIN_VC with proposal 0.
- Keep the v1 scalar ZKP3 **byte-identical** as the **pool-reveal** circuit, with its VK fingerprint pinned to today's value (`vc/src/share_reveal/prove.rs:327-345`).
- After private vote, the chain routes reveals by `proposal_id`:
  - `0` -> scalar pool reveal (MsgRevealShare, 9 PIs);
  - `1..50` -> vector ZKP3.
- The chain must refuse scalar reveals for `proposal_id != 0` in v2 rounds. ZKP2-v2 never produces `DOMAIN_VC` leaves, so this is defense in depth.

**DOMAIN_VC vs DOMAIN_VC_V2.** In the branch, `DOMAIN_VC` is `#[allow(dead_code)]` "retired" (`pv:src/domain_tags.rs:25-31`). It must instead be documented as **"live: proxy-delegation pool commitments (proposal 0) only"**. v1 VCs and DCs can never collide with v2 VCs, because the domains differ.

**Vector ZKP3 with D=1 is rejected.**
- A v2-format DC needs 16 × 8 = 128 encryptions (ZKP1.5-sized, ~57.7 KiB, ~531 ms [doc]).
- The v2 VC preimage has no slot for `delegate_index`: it is `(…, proposal, D)`.
- D=1 is only structurally permitted (`design.md:158-161`).

**Sequencing without double VK bumps:**
- **S1 (recommended).** Ship proxy delegation first as voting-circuits 0.13.0: add ZKP4 only, with ZKP1/2/3 fingerprints unchanged. Private vote ships later: it changes ZKP2/ZKP3-vote and adds ZKP1.5, while ZKP4 and pool-reveal stay untouched (both pinned).
- **S2.** Private vote first: the proxy release must restore the deleted v1 gadgets and the old ZKP3 module as `pool_reveal`. Its VK equals the pre-private-vote ZKP3 fingerprint. More resurrection work, but still one bump per circuit.
- **S3.** Bundle both releases.

In every option, each VK changes at most once. The hard rule: **never define ZKP4 or pool-reveal in terms of `DOMAIN_VC_V2`, the bridge or the vector share commitment.**

**Privacy delta in a v2 world.** Helpers stop learning vote choices but still learn each DC's delegate index (§9).

---

## 7. B1 (in-tx public pool slots, no helpers), circuit side

[measured] The prototype is `proto/circuit_b1.rs`:
- the same spend front half (C1-C6, C8, C12);
- 10 slots `(delegate_index_j public & transcript-bound, Enc(w_j) constrained to instance)`;
- no shares hash and no DC.

Results: K=11, **1,662 rows**, 10,528 B, **60 public inputs**, prove ≈ ZKP4. **One proof covers all 10 delegations.** In-circuit cost is not a problem (10 encryptions fit the two 8-share El Gamal tracks), and B1 avoids 16k helper ZKP3s and reveal txs entirely.

**Privacy versus the election authority (EA) and observers [inference]:**
- **B1:**
  - Each slot is a single ciphertext of the exact `w_j`, posted with a public `d_j` in one anonymous tx.
  - Any party holding the full EA key (a colluding threshold) decrypts the delegator's complete allocation profile: delegate set + exact amounts + total.
  - Every observer sees the delegate *set* linked to one tx. That set is a fingerprint and leaks the count. Padding with zero-weight dummy slots hides the count from observers but not from the EA.
- **B2:**
  - The DC hides both `d` and `w`.
  - Shares are 16-way denomination splits revealed unlinkably over time, so the EA learns only per-pool multisets of share values.
  - The helper that processes a DC learns `d` and the DC position (hence the tx), but not `w`.
  - Only a helper + EA collusion approaches B1 for that DC.
  - The single-share last-moment layout gives the EA the exact `w` for that DC.

**Verdict.** Use B2, consistent with decision 4. B1 is a credible fast path only if the owner accepts EA-visible amounts.

---

## 8. Builder and prover API (ballot-denominated), helpers, tests, VK pinning

### 8.1 `voting_circuits::proxy_delegation` (new public module)

```rust
pub const K: u32 = 11;
pub const POOL_PROPOSAL_ID: u32 = 0;
pub const MAX_DC_SLOTS: u8 = 16;
pub struct Circuit { /* §2.2, pub(super) */ }            // Default = keygen circuit
pub struct Instance { pub van_nullifier, r_vpk_x, r_vpk_y, vote_authority_note_new,
    delegation_commitment, vote_comm_tree_root, vote_comm_tree_anchor_height,
    voting_round_id, ea_pk_x, ea_pk_y, dc_slot_nullifier: pallas::Base }
impl Instance { pub const NUM_PUBLIC_INPUTS: usize = 11; pub fn from_parts(..)->Self;
                pub fn to_halo2_instance(&self)->Vec<vesta::Scalar>; }

pub struct ProxyDelegationTransition { pub vote_authority_note_old: pallas::Base,
    pub vote_authority_note_new: pallas::Base, pub total_ballots: u64, pub remaining_ballots: u64 }
/// Plans the VAN transition without proving (for batch planning and recovery).
pub fn derive_proxy_delegation_transition(sk: &SpendingKey, address_index: u32,
    total_ballots: u64, delegated_ballots: u64, van_comm_rand: pallas::Base,
    voting_round_id: pallas::Base) -> Result<ProxyDelegationTransition, ProxyDelegationBuildError>;

pub struct DelegationCommitmentBundle { pub proof: Vec<u8>, pub instance: Instance,
    pub r_vpk_bytes: [u8; 32], pub delegate_index: u32, pub delegated_ballots: u64,
    pub remaining_ballots: u64, pub dc_slot: u8, pub shares_hash: pallas::Base,
    pub share_comms: [pallas::Base; 16], pub share_blinds: [pallas::Base; 16],
    pub encrypted_shares: [vote_proof::EncryptedShareOutput; 16], pub single_share: bool }
/// Builds and proves ZKP4. Checks 1 <= delegated <= total <= 2^30, dc_slot < 16.
pub fn build_delegation_commitment_proof(sk: &SpendingKey, address_index: u32,
    total_ballots: u64, delegated_ballots: u64, delegate_index: u32, dc_slot: u8,
    van_comm_rand: pallas::Base, voting_round_id: pallas::Base,
    vote_comm_tree_path: [pallas::Base; VOTE_COMM_TREE_DEPTH], vote_comm_tree_position: u32,
    anchor_height: u32, ea_pk: pallas::Affine, alpha_v: pallas::Scalar, single_share: bool,
) -> Result<DelegationCommitmentBundle, ProxyDelegationBuildError>;
pub enum ProxyDelegationBuildError { ZeroDelegation, DelegationExceedsWeight{delegated:u64,total:u64},
    WeightOutOfRange(u64), SlotOutOfRange(u8), InvalidElectionPublicKey,
    InvalidRandomizedVotingPublicKey, InvalidShares(String), Prove(ProveError) }

/// DC = Poseidon5(DOMAIN_VC, round, shares_hash, 0, delegate_index).
pub fn delegation_commitment_hash(voting_round_id: pallas::Base, shares_hash: pallas::Base,
    delegate_index: u32) -> pallas::Base;
/// Slot nullifier, for client slot allocation and recovery (query the chain for s in 0..16).
pub fn dc_slot_nullifier(sk: &SpendingKey, voting_round_id: pallas::Base,
    van_comm_rand: pallas::Base, slot: u8) -> pallas::Base;
/// The 16 share nullifiers a delegator checks on chain to confirm its pool contribution was revealed.
pub fn delegation_share_nullifiers(delegation_commitment: pallas::Base,
    share_blinds: &[pallas::Base; 16]) -> [pallas::Base; 16];
/// Deterministically re-derives DC share secrets (crash recovery and verification) without proving.
pub fn rederive_delegation_shares(sk: &SpendingKey, voting_round_id: pallas::Base,
    vote_authority_note_old: pallas::Base, delegate_index: u32, delegated_ballots: u64,
    ea_pk: pallas::Affine, single_share: bool) -> Result<DelegationShareSecrets, ProxyDelegationBuildError>;
pub fn create_delegation_commitment_proof(c: Circuit, i: &Instance) -> Result<Vec<u8>, ProveError>;
pub fn verify_delegation_commitment_proof(proof: &[u8], i: &Instance) -> Result<(), String>;
pub fn delegation_commitment_params() / _proving_key(&Params) / _cached_keys() / warm_… / prepare_…;
```

### 8.2 PRF for DC secrets

```text
dc_share_prf = BLAKE2b-512(
    personal "ZcashVote_Expand",
    sk || domain(0x10..0x14) || round || 0u64_le || van_old || share_index_u8
       || delegate_index_u32_le || delegated_ballots_u64_le)
```

This mirrors `vote_share_prf` (`vc/src/vote_proof/builder.rs:508-526`) and reuses `denomination_split` and `deterministic_shuffle` over `delegated_ballots`. Binding `(d, w)` means a re-planned DC from the same VAN never reuses an El Gamal nonce for a different plaintext.

### 8.3 Additions to existing modules (no VK change)

- `vote_proof::build_vote_proof_from_ballots(.., num_ballots: u64, ..)` and `derive_vote_authority_transition_from_ballots(..)`. Today's builder takes zatoshi and floor-divides (`builder.rs:782`), which is a trap after a ZKP4 leaves `W - w` ballots. The zatoshi versions become wrappers.
- `share_reveal::build_pool_share_reveal(path, pos, share_comms, blind, c1x, c2x, c1y, c2y, share_index, delegate_index: u32, round)` = `build_share_reveal(.., proposal_id = 0, vote_decision = delegate_index, ..)`.
- `share_reveal::share_nullifier_hash` is already public (`share_reveal/mod.rs:20`).
- The chain FFI (vote-sdk `circuits/`) gains `sv_verify_delegation_commitment_proof(proof_ptr, len, pi_ptr, 352)` and `sv_warm_verifier_caches` coverage. The ZKP3 FFI is unchanged.

### 8.4 Frozen test vectors

[measured] Little-endian `to_repr` hex:
- `delegation_commitment_hash(42, 100, 7)` = `vote_commitment_hash(42, 100, 0, 7)` = `f779fa343235da28e635da7793dc62ff4cd023d023098815b832503c10a09305`
- `share_nullifier_hash(that DC, 3, 1001)` = `d06effb5e792fc712ef603d497c5efba35ab7314d259a18ea7b99d71ff778d02`
- `dc_slot_nullifier_hash(nk=1, round=42, rand=5, slot=3)` = `c962cce9c2fdc68f9496d3fe67142f28d50139396457af7820ff477874003510` (prototype tag; regenerate if the tag text changes)
- Add an end-to-end vector with:
  - `sk = [0x42;32]`, round `0xCAFE`, rand `0xDEAD`;
  - W = 1000, w = 107, d = 7, slot 0;
  - fixed `alpha_v`.

  Pin all 11 instance values (the proof bytes are randomized).

### 8.5 MockProver tests to write

Tests marked (P) are already in the prototype `proto/circuit/tests.rs`.

1. (P) valid partial; full delegation `W_new = 0`; `W = 2^30`; slot 15.
2. (P) w > W -> C7.
3. (P) w = 0 -> C12.
4. (P) `W_new = W` with w > 0 -> C8.
5. `W_new = p - k` field wrap -> C7.
6. (P) input mask `MAX - 2^p` for p in {1, 50}, and mask 0 -> C2/C1.
7. Successor instance built with a non-MAX mask, or a different rand, address or round -> C6.
8. (P) DC instance with proposal 1 -> C13. (P) wrong delegate index -> C13. DC built with `DOMAIN_VC_V2` -> C13.
9. (P) single share >= 2^30 with a compensating sum -> C9. Tampered share vs sum -> C8.
10. Wrong `ea_pk` instance; zero El Gamal randomness; wrong c1/c2 x/y on either track -> C11.
11. Wrong nk or wrong nullifier instance -> C5. **ZKP4/ZKP2 nullifier parity** for the same VAN (native, and both MockProvers).
12. Wrong `r_vpk` -> C4. Wrong vsk/rivk -> C3. Wrong root or position -> C1.
13. (P) slot 16 -> C14. Wrong slot nullifier instance. Same slot gives the same nullifier across two chained ZKP4s (documents the chain dedup dependency).
14. Default circuit with a valid instance fails. Row budget and witness independence at K=11. `NUM_PUBLIC_INPUTS == 11` and offset test.
15. Cross-circuit:
    - (P) ZKP3 reveals a DC share with (0, d); fails with d±1 and with proposal 1;
    - a ZKP2 VC cannot be opened as (0, d);
    - ZKP4 -> ZKP4 -> ZKP2 chained via single-leaf roots passes;
    - ZKP2 -> ZKP4 fails (ordering);
    - from a W_new = 0 successor, a further ZKP4 fails.
16. ZKP2 `proposal_id_zero_fails` retained and relabelled load-bearing.
17. Real-proof test: `proof.len() <= 15*1024` (pattern at `share_reveal/prove.rs:291-318`).

### 8.6 VK fingerprint pinning

- Add `proxy_delegation::prove::tests::vk_fingerprint_unchanged`, blake2b-256 of `{:?}` of `vk.pinned()` (the pattern at `vote_proof/prove.rs:277-300`).
- The prototype fingerprint is `975055e5a882947d619442064fb0071acd85dc8a6d8423f35f2f30554aab90cd`. It is illustrative only; pin it after final layout review.
- Release gate: the ZKP1/2/3 fingerprint tests must pass **unchanged**.
- Publish the ZKP4 fingerprint in `ProtocolCapabilities`. Clients refuse to prove if it differs from their local key.

---

## 9. Successor-VAN traceability and privacy deltas

**Successor VAN.** It reuses `rand`, the address and MAX, with weight `W - w`. Only the holder of the VAN opening can trace it, and that is the delegator's own hotkey. In B2, **nobody else ever receives a VAN opening**, unlike option A where a VAN is handed to the delegate.
- The delegate learns nothing about individual delegators; contributions arrive as anonymous reveals. With option A the delegate would learn each delegator's exact `num_ballots` (dossier §7).

**Public observers** see:
- `van_nullifier`, the successor commitment, the DC and the slot nullifier, all hiding or pseudorandom;
- the action types and the count k of DCs in a batch;
- intermediate successor VANs in the tx body, which is already the case for vote batches.

They do not see `d`, `w`, `W - w` or the slot index. Slot nullifiers from different txs of one chain are unlinkable (nk-keyed).

**Helpers** learn, per DC, `d` and the DC leaf position (hence the tx). They do not learn the amount. This matches today's helper knowledge of `(proposal, decision)` for votes, but is weaker than the private-vote design for votes.

**Pool totals (integrator flag).** Routes are public and per-option tallies are published, so `sum_o T[p][o] = D_p + sum_{d routed on p} Pool[d]`.
- When delegates skip some proposals, turnout differences between proposals approximate `Pool[d]`. The approximation is good whenever direct turnout D_p is stable across proposals.
- Mitigation: delegates route every proposal (an explicit abstain option counted like a vote). Decrypting an "unrouted" bucket would leak the sum *exactly*.

**Delegator verification needs no new circuit.** It is: tx inclusion; `delegation_share_nullifiers(DC, blinds)` all present in the share-nullifier set (the chain accepts a reveal only if its public `(0, d)` matches the DC); public routes.

---

## 10. Contracts

### Provided by circuits

- **P1:** The ZKP4 VK, public-input layout and preimages (§2).
- **P2:** ZKP3 unchanged, with pool reveals using `(proposal 0, decision = delegate_index u32)`.
- **P3:** The ballot-denominated builders and helpers (§8).
- **P4:** ZKP1/2/3 VKs byte-identical.
- **P5:** Deterministic DC secrets from `(sk, round, van_old, d, w, layout)`.

### Required from the chain

- **R1:** Verify ZKP4 with roots per §5:
  - the real root at height for the first action;
  - `SingleLeafRoot(prev.vote_authority_note_new)` after that;
  - `SingleLeafRoot(van_cmx)` after ZKP1.

  Take `ea_pk` and round from round state.
- **R2:** VAN nullifiers go into the same set as ZKP2 (0x01). Slot nullifiers go into a new round-scoped set (0x03). Fix `CheckNullifiersUnique` to reject intra-list duplicates.
- **R3:** One batch-wide sighash over the ordered actions.
- **R4:** Stateless checks: DC actions precede casts; at most 10 DCs per batch.
- **R5:** Append the final VAN plus DC and VC leaves; expose DC leaf indices; add the tree-capacity check.
- **R6:** Accept `MsgRevealShare` with proposal 0 only in proxy-enabled rounds, with decision = a registered `delegate_index`.
  - The validity rule must be monotone (index < registry length). Never reject because of a later revocation.
  - Accumulate into `TallyKey(round, 0, d)`.
- **R7:** Keep rejecting `MsgCastVote` proposal 0.
- **R8:** After private vote, use scalar ZKP3 only for proposal 0.
- **R9:** Exclude pools from decryption and completeness checks; add `Pool[d]` into `agg[p][route(d,p)]` at ACTIVE->TALLYING.
- **R10:** Registry indices are u32, append-only and never reassigned.
- **R11:** Advertise the capability with the ZKP4 VK fingerprint and the slot cap.

### Required from the helper

- **H1:** Accept proposal-0 payloads with a u32 decision in proxy-enabled rounds. `vcHash`/leaf check unchanged; choice validator = registry; same scheduling and single-share support.

### Required from the client (zcash_voting / Vizor)

- **C1:** Plan all DCs for a VAN before any cast. Persist `(d, w, slot, layout)` intents. Allocate slots sequentially and recover them via `dc_slot_nullifier` queries.
- **C2:** Add a DC payload path past the proposal and decision validators.
- **C3:** Delegate only to registered indices.
- **C4:** Check the ZKP4 VK fingerprint against capabilities before proving.
- **C5:** Evict the ZKP4 proving key after use on mobile (~140 MiB peak).
- **C6:** Delegator verification per §9.

---

## 11. Prototype and reproduction

**Prototype files** (throwaway; repos untouched):
- `proto/mod.rs`
- `proto/circuit.rs`: the recommended ZKP4 (C1-C14)
- `proto/circuit/tests.rs`: 13 MockProver tests plus ignored harnesses
- `proto/circuit_b1.rs`, `proto/circuit_b1/tests_b1.rs`: the B1 variant

**Commands:**
- Tests: `CARGO_TARGET_DIR=…/proto-circuits/target cargo test --release proxy_delegation`
- Diagnostics: append `-- --ignored --nocapture` with `row_budget`, `prove_verify_timing`, `compare_zkp2_zkp4_timing`, `explain_negative_failures`, `b1_mock_rows_and_timing`.
