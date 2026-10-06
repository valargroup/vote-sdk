# voting-circuits deep read: dossier for proxy delegation

**Scope and method.** I read `voting-circuits` origin/main v0.12.2 (snapshot at `/private/tmp/claude-501/-Users-czstudio-Documents-vote-sdk--claude-worktrees-vote-delegation-planning-29b451/0cff7cc1-5f3f-4de1-be83-322a1fdf7727/scratchpad/src/voting-circuits`, cited below as `vc/`). I diffed it against the chain-pinned 0.12.0 at `~/.cargo/registry/src/index.crates.io-1949cf8c6b5b557f/voting-circuits-0.12.0`. I cross-checked consumers in vote-sdk (`vs/` = `/Users/czstudio/Documents/vote-sdk/.claude/worktrees/vote-delegation-planning-29b451`) and zcash_voting (`zv/` = `.../scratchpad/src/zcash_voting`).

I measured row budgets, VK fingerprints and proving times myself. The builds went into `scratchpad/vc-target` with a throwaway harness at `scratchpad/vptime`, and no repo was modified. Every timing is from an **Apple M3 Ultra with 28 threads, release build, Zakura backend**. Mobile will be several times slower.

Labels used below:
- **[code]** means the source does this today.
- **[docs]** means a document claims it.
- **[measured]** means I ran it.
- **[inference]** means it is my analysis.

"Proxy delegation" means the new feature, giving voting power to another person. "ZKP1/delegation" keeps its existing meaning: notes go to a VAN.

---

## 0. Version check: 0.12.0 (chain) vs 0.12.2 (clients)

- vote-sdk pins `voting-circuits = "=0.12.0"` (`vs/circuits/Cargo.toml:28`). zcash_voting pins `=0.12.2` (`zv/Cargo.toml:20`), and so does Vizor (`vizor-wallet/rust/Cargo.lock:7393-7395`).
- `diff -r` of `src/` changes only `delegation/{mod,prove}.rs`, `vote_proof/{mod,prove}.rs`, `share_reveal/{mod,prove}.rs` and `prove_error.rs`. Every hunk is the `prepare_*_proving` fixed-base-table API (CHANGELOG `vc/CHANGELOG.md:14-26`) or the crypto-family bump (`:5-12`). There are no circuit changes.
- [measured] The pinned VK fingerprint arrays are byte-identical in 0.12.0 and 0.12.2 (`vc/src/delegation/prove.rs:280`, `vc/src/vote_proof/prove.rs:291`, `vc/src/share_reveal/prove.rs:335`). The three `vk_fingerprint_unchanged` tests pass on 0.12.2. **The chain and client VKs are identical.**

## 1. The VAN today

### 1.1 Structure [code]

`vc/src/gadgets/van_integrity.rs:9-12, 61-79, 103-138`:
```
van_comm_core = Poseidon<6>(DOMAIN_VAN=0, g_d_x, pk_d_x, value, voting_round_id, proposal_authority)
VAN           = Poseidon<2>(van_comm_core, van_comm_rand)
```
- Poseidon is P128Pow5T3, width 3, rate 2 (`vc/src/protocol_hash.rs:17-20`).
- `value` is **ballots**, where 1 ballot = 12,500,000 zat (`vc/src/params.rs:14`). ZKP1 constrains `num_ballots ∈ [1, 2^30]` through the `nb_minus_one` 30-bit check (`vc/src/delegation/circuit.rs:1340-1375, 1444-1447`).
- There is a documented one-ballot *under*-claim window and no over-claim (`vc/src/delegation/README.md:357-377`).
- The address goes in as **x-coordinates only**. The module warns that the hash "must not be treated as a standalone commitment to unique full points" (`van_integrity.rs:14-22`; `vc/src/vote_proof/README.md:107`). Soundness comes from ZKP2's full-point ownership check plus the nullifier.

### 1.2 Domain tags [code]

From `vc/src/domain_tags.rs:22-73`:
- `DOMAIN_VAN=0` and `DOMAIN_VC=1` are numeric tags in the shared vote tree.
- String tags: `"vote authority spend"` (VAN nullifier), `"governance authorization"` (ZKP1 gov-null domain) and `"share spend"` (ZKP3).
- PRF personalization `"ZcashVote_Expand"`, with PRF domains 0x00 to 0x04.
- A distinctness test is at `:123-161`. The README asks that new tags be registered here first (`vc/README.md:54`).

### 1.3 Proposal authority [code]

`vc/src/params.rs:31-45`:
- `MAX_PROPOSALS = 50` and `PROPOSAL_AUTHORITY_BITS = 51`.
- `MAX_PROPOSAL_AUTHORITY = 2^51 - 1`, which has every bit set, including bit 0.

**Sentinel bit 0** (`vc/src/vote_proof/circuit.rs:129-150`):
- Proposal IDs are 1-indexed. Bit 0 is "always set and never decremented".
- ZKP2 rejects `proposal_id = 0` with an inverse gate.
- The `(0,1)` entry of the authority lookup table is load-bearing, because it is the default lookup row on every unselected row (`vc/src/vote_proof/gadgets/authority_decrement.rs:149-160`).
- ZKP1 bakes the full mask in as a circuit constant (`vc/src/delegation/circuit.rs:1518-1525`). Every freshly minted VAN therefore has the full mask, and changing the mask would change the delegation VK (`params.rs:40-45`).

### 1.4 VAN nullifier [code]

`van_nullifier = Poseidon<4>(vsk_nk, "vote authority spend", voting_round_id, VAN)` (`vc/src/vote_proof/circuit.rs:206-230, 1040-1070`). It is the first `constrain_instance`, at offset 0. Because it is round-scoped, the same hotkey address can be reused across rounds without collisions (tests at `circuit.rs:2147-2208`).

### 1.5 Hotkey key hierarchy

- [code, zcash_voting] The hotkey is an ordinary Orchard `SpendingKey` from `UnifiedSpendingKey::from_seed(network, 64-byte stored secret, account 0)`. The VAN target is `fvk.address_at(0, External)` (`zv/zcash_voting/src/hotkey.rs:13-19, 70-104`).
- [docs] The hotkey doc says wallets "generate this secret once per local voting identity and round" (`hotkey.rs:22-30`).
- [code, voting-circuits builder] The vote proof builder derives the keys as follows (`vc/src/vote_proof/builder.rs:386-397, 711-723`):
  - `vsk` is the sign-normalized `ask`.
  - `vsk_nk = fvk.nk()`.
  - `rivk_v = fvk.rivk(External)`.
  - `address = fvk.address_at(address_index, External)`.
- The builder asserts `[vsk]G == ak` and `[CommitIvk(ak,nk,rivk_v)]g_d == pk_d` before proving (`builder.rs:731-775`).
- The *circuit* does not enforce scope or index. `rivk_v` is a free witness, so any address of the key works if the matching `rivk` is supplied. The builder hard-codes External scope.

### 1.6 ZKP2 conditions that bind a VAN to the hotkey [code]

From `vc/src/vote_proof/circuit.rs`:
- **Cond 2** (`:894-911`): the VAN preimage uses the x-coordinates of the witnessed `vpk_g_d` and `vpk_pk_d` `NonIdentityPoint`s (`:799-813`). `DOMAIN_VAN` is a constant (`:842-848`).
- **Cond 3** (`:918-946`): `ak = ExtractP([vsk]SpendAuthG)`, then `ivk = CommitIvk_{rivk_v}(ak, vsk_nk)`, then `vpk_pk_d == [ivk]vpk_g_d` (full-point equality, via the shared `address_ownership` gadget).
- **Cond 4** (`:959-967`): `r_vpk = [alpha_v]G + [vsk]G` goes to public offsets 1 and 2. The vote signature is checked out of circuit.
- **Cond 5**: the nullifier uses the *same* `vsk_nk` cell.

Only the holder of the hotkey's `(vsk, nk, rivk)` can spend a VAN. Knowing the VAN opening (`num_ballots`, `van_comm_rand`) does **not** let anyone else spend it, so a delegator cannot claw back a delegated VAN.

### 1.7 ZKP1 already mints a VAN for any address [code]

- `build_delegation_bundle(..., output_recipient: orchard::Address, ..., van_comm_rand, ...)` takes the recipient as an argument (`vc/src/delegation/builder.rs:716-728`).
- The circuit comment says "The output address (g_d_new, pk_d_new) is NOT checked against ivk … bound transitively through van_comm … hashed into rho_signed" (`vc/src/delegation/circuit.rs:1225-1241`; README `:311`).
- The successor VAN in ZKP2 **reuses the same `van_comm_rand`**, the same address, the same weight and the same round. Only the mask changes (`vote_proof/circuit.rs:884, 1112-1131`; builder `:423-461`).

## 2. ZKP2 batch chaining ("predecessor-derived single-leaf roots")

**Mechanism [code].** The ZKP2 circuit is unchanged; the trick lives entirely in the verifier. `MsgCastVoteBatch` is an ordered list of `MsgCastVote` messages from one VAN chain.

1. The chain verifies every signature against one batch-wide sighash.
2. Proof 0 is verified against the real tree root at the anchor height.
3. Each later proof `i+1` is verified against `votetree.SingleLeafRoot(votes[i].VoteAuthorityNoteNew)`. That is the root of a depth-24 tree whose only leaf, at position 0, is the predecessor's successor VAN (`vs/x/vote/ante/validate.go:380-449`).
4. `MsgDelegateAndCastVoteBatch` anchors the first cast at `SingleLeafRoot(delegation.VanCmx)` with synthetic anchor height 0 (`validate.go:181-243`; proto `vs/proto/svote/v1/tx.proto:92-113`).

The prover passes position 0 and empty-subtree siblings. Unit tests build the same path with `build_single_leaf_merkle_path` (`vc/src/vote_proof/circuit.rs:1740-1750`).

**What lands in the tree [docs].**
- Delegation adds `[VAN]`.
- A single vote adds `[successor VAN, VC]`.
- A batch of N adds `[final VAN, VC_0..VC_{N-1}]`. "Intermediate batch VANs are not tree outputs" (`zv/docs/chain_submission_invariants.md:277-285`; `vs/ffi/votetree/README.md:7`).

**Why [code/docs + inference].**
- **Atomicity and latency.** A successor VAN created in the same transaction is not in any historical root, so the next proof could not otherwise anchor to it without waiting a block.
- **Integrity.** One sighash prevents truncation, reordering and grafting (proto comment `tx.proto:92-96`).
- **Smaller tree.** Intermediate VANs are never appended.
- **No VK change.**
- **Soundness.** A single-leaf root can only open that exact VAN. Its nullifier still goes into the round's VAN nullifier set.

**Reuse for proxy delegation [inference].** The same pattern gives atomic "delegate-and-split" (ZKP1, then a split proof anchored at `SingleLeafRoot(van_cmx)`) and "split-then-vote-with-retained-output", with no new anchoring machinery. The chain must name *which* split output the chained proof consumes and must not append that output globally.

**Linkability side effect [code].** Every intermediate `vote_authority_note_new` is still published in the batch message bytes (`tx.proto:76-88`).

## 3. Per-circuit sizing

Rows, columns and bytes below are [measured] with `row_budget` and `CircuitCost`. Times come from my harness for ZKP2 and ZKP3 (build plus prove, 6 and 4 samples) and from the criterion bench for ZKP1.

| | ZKP1 delegation | ZKP2 vote proof | ZKP3 share reveal |
|---|---|---|---|
| K / rows available | 12 / 4,096 (`delegation/circuit.rs:111`) | 11 / 2,048 (`vote_proof/circuit.rs:120`) | 10 / 1,024 (`share_reveal/circuit.rs:102`) |
| Rows used (high-water) | **4,039 (98.6%)**, 57 left | **2,015 (98.4%)**, 33 left (27 usable after 6 blinding rows) | 976 (95.3%) |
| Smallest K that fits | 12 (K=11 rejected) | 11 | 10 |
| Advice / fixed / instance cols | 30 / 71 / 1 | 34 / 76 / 1 | 13 / 19 / 1 |
| Lookups / permutation cols / max degree | 7 / 47 / 9 | 5 / 41 / 9 | 0 / 20 / 6 |
| Public inputs | 14 | 11 | 9 |
| Proof bytes | 11,328 | 11,008 | 4,992 |
| Prove time | **116 ms** | **~71 ms** | **~26 ms** |
| Verify time | 2.34 ms | ~1.9 ms | — |
| Keygen | — | 124 ms | 28 ms |

- **ZKP1 key sizes:** pk 156 MB and vk 562 KiB in memory. The prepared tables add 26.4 MiB.
- **Consensus proof cap:** `MaxProofSize = 15 KiB = 15,360 B` (`vs/x/vote/types/keys.go:65-74`). The ZKP3 test mirrors it (`vc/src/share_reveal/prove.rs:293`). ZKP1 and ZKP2 are within about 4 KB of the cap.

**Public-input layouts [code]:**
- **ZKP1** (`vc/src/delegation/circuit.rs:160-191`): `nf_signed, rk_x, rk_y, cmx_new, van_comm, vote_round_id, nc_root, nf_imt_root, gov_null_1..5, dom`.
- **ZKP2** (`vc/src/vote_proof/circuit.rs:157-193`): `van_nullifier, r_vpk_x, r_vpk_y, vote_authority_note_new, vote_commitment, vote_comm_tree_root, anchor_height (unconstrained, transcript-bound), proposal_id, voting_round_id, ea_pk_x, ea_pk_y`.
- **ZKP3** (`vc/src/share_reveal/circuit.rs:118-159`): `share_nullifier, c1_x, c1_y, c2_x, c2_y, proposal_id, vote_decision, vote_comm_tree_root, voting_round_id`.

**Column layouts:**
- ZKP2: 10 primary advice, two 10-column El Gamal tracks, a 4-column hash Poseidon track, and the authority-decrement chip on El Gamal track B (`vote_proof/circuit.rs:567-509 configure`; README `:460-481`).
- ZKP1: 10 shared advice plus 4 lanes of 5 for the Sinsemilla Merkle paths and IMT Poseidon paths (`delegation/circuit.rs:522-760`).

**Unit costs [measured, ZKP1 `cost_breakdown`]:**

| Operation | Rows |
|---|---|
| One Poseidon permutation (Pow5) | 37 |
| `Poseidon<2>` | 1 permutation, about 38 |
| `Poseidon<4>` | 2 permutations |
| `Poseidon<6>` | 3 permutations |
| VAN integrity | 4 permutations, about 150 |
| Variable-base scalar mul | 137 |
| Full-width fixed-base mul | 85 |
| CommitIvk `hash_to_point` | 52 |
| NoteCommit | about 110 + 85 + decompositions, roughly 300 |
| Sinsemilla generator table | 1,024 fixed rows |
| Merkle swap row | 1 per level |

The depth-24 vote-tree path costs about 24 × 38 ≈ 912 rows. The `vote_proof/README.md:136` figure of "~1,560 rows" assumes 65 rows per permutation and is stale.

**Other benchmarks [docs].** `docs/design.md` describes an **unimplemented** "ZKP 1.5 private vote choice" redesign. Its numbers on an M4 Max: ZKP 1.5 (K=11) proves in 531 ms, the compact ZKP2 in 81 ms, and ZKP3 in 38 ms (`docs/design.md:463-480`). It would change the ZKP2 and ZKP3 VKs and introduce `DOMAIN_VC_V2` (`:296-306, 459-461`). Any proxy-delegation VK work should be coordinated with it.

## 4. Reusable gadgets

All gadgets are **`pub(crate)`** (`vc/src/gadgets/mod.rs:6-12`). New circuits therefore have to live inside the voting-circuits crate.

| Gadget | Where | Cost |
|---|---|---|
| `van_integrity_poseidon` / `van_integrity_hash` | `gadgets/van_integrity.rs:61-138` | 4 permutations, about 150 rows on one Poseidon config |
| `synthesize_poseidon_merkle_path{_with_configs,_with_config_schedule}` + `MerkleSwapGate` | `gadgets/poseidon_merkle.rs:40-230` | about 38 rows per level; levels can rotate across several Poseidon configs (ZKP3 uses an 8/16 split, `share_reveal/circuit.rs:107-111`) |
| `prove_address_ownership` + `spend_auth_g_mul` | `gadgets/address_ownership.rs:42-128` | `[vsk]G` 85, CommitIvk about 52 + 85 + canonicity of about 30, `[ivk]g_d` 137: roughly 300-400 ECC rows. Needs the Sinsemilla chip and table. |
| `prove_spend_authority` (1:1 Orchard copy) | `gadgets/spend_authority.rs:81-112` | about 87 rows (`[alpha]G` plus add); constrains `rk` x and y to the instance |
| `NonZeroConfig` | `gadgets/nonzero.rs:22-71` | 1 row |
| `prove_elgamal_encryptions` + `SpendAuthGFixedBase30Config` | `gadgets/elgamal.rs:194+`, `elgamal/fixed_base_30.rs` | per share: full fixed-base 85 + variable-base 137 + custom 30-bit fixed base. Needs a 10-column track. |
| `vote_commitment_poseidon` | `gadgets/vote_commitment.rs:73-99` | `Poseidon<5>`, 3 permutations |
| `compute_shares_hash_in_circuit` | `shares_hash.rs:142+` | 16 × `Poseidon<5>` + `Poseidon<16>` |
| `AuthorityDecrementChip` | `vote_proof/gadgets/authority_decrement.rs:1-120, 371+` | 52 rows × 8 columns plus a 51-entry lookup. A template for any per-bit mask logic. |
| `AddChip` (Orchard) / `MulChip` | `delegation/gadgets/mul_chip.rs` | 1 row each |
| `LookupRangeCheckConfig` | 10-bit words | about 1 row per word; a 30-bit strict check is 3 words |
| IMT non-membership | `delegation/gadgets/imt_circuit.rs` | depth 29 + interval checks; not relevant to proxy delegation |

## 5. Candidate circuits [inference]

Feasibility, cost estimates and implications for each option follow. Row counts use the measured unit costs above.

### (a) New "VAN split" circuit: 1 VAN in, N VANs out, N from 2 to 11

**Reuse** the front half of ZKP2: conditions 1 to 5 (membership, old-VAN integrity, ownership, spend authority, nullifier).

**Add per output:**
- `van_integrity_poseidon` with the recipient's x-coordinates, the output weight `w_i`, the parent's round and mask, and a fresh `rand_i`.
- A 30-bit range check on `w_i`.
- A sum: `Σw_i = w` (N-1 AddChip rows plus an equality).
- Optionally witness recipient points as `NonIdentityPoint`, which costs about 1 row each and rejects off-curve or burned outputs.

**Rows for N=11:**

| Part | Rows |
|---|---|
| Merkle path | about 912 |
| Old VAN | about 150 |
| Nullifier | about 75 |
| 11 output VANs | about 1,650 |
| **Poseidon subtotal** | **about 2,800** |
| ECC (in parallel) | about 400-500 |
| Weights | about 60 |

**K:**
- 3-4 Poseidon lanes (4 advice + 6 fixed columns each) make **K=11 plausible**, with roughly 22-26 advice columns, an estimated 8-11 KB proof and about 60-80 ms desktop prove time.
- K=12 is the safe fallback, at about 115-150 ms.
- N=2 (the book's 1→2 split) fits K=11 comfortably.

**Public inputs:** `van_nullifier, r_vpk_x, r_vpk_y, vote_comm_tree_root, anchor_height, voting_round_id, van_new_1..N`, so 6 + N (17 for N=11).

**Masks:** outputs inherit the parent's mask, which costs nothing. Total-weight conservation then implies per-proposal conservation automatically.

**Arity choice:**
- Fixed arity with zero-weight dummy outputs hides the delegate count but appends N leaves per split.
- Repeated 1→2 splits (batch-chained with single-leaf roots) use a smaller circuit but leak the count.

**Hard requirements:**
- Use the **identical nullifier derivation and the same nullifier set as ZKP2**.
- Range-check every output weight.
- Keep `DOMAIN_VAN` and the round.
- Make `rand_i` unique per output.

**Compatibility:** purely additive. Old VANs, ZKP1, ZKP2 and ZKP3 are unchanged.

### (b) New "VAN merge/consolidate" circuit: M VANs owned by one key, out 1 VAN

**Cost per input:** membership about 912 + integrity about 150 + nullifier about 75, roughly 1,140 Poseidon rows.
- Prove ownership once if all inputs share one address.
- Otherwise add one `[ivk]g_d` (137 rows) per extra address and reuse `ivk`.
- Output: one VAN with `Σw_i` (range-check below 2^30; total ZEC supply is about 2^27.3 ballots, so this is never binding).

**Sizing:**
- M=4: about 4.7k rows, so K=12 with 2-3 lanes.
- M=8: about 9.3k rows, so K=12 with 3+ lanes or K=13 (about 250-300 ms desktop).

**Public inputs:** M nullifiers + root + anchor + round + `r_vpk` (2) + `van_new`, so M + 6.

**Masks:**
- **OR is unsound**: it would let weight vote twice on a proposal one input already used.
- **Require-equal** is the cheapest: one `constrain_equal` each.
- **AND** is sound but discards authority. It needs 51-bit decompositions per input, about 51 rows per input with an AuthorityDecrement-style chip.
- Recommended v1: require equal masks, which in practice means merging fresh (all-1) VANs before voting.

**Side effect:** merge produces a fresh `rand`, so it **breaks delegator tracking** (see §7).

### (c) Modify ZKP1 to mint several VANs directly at registration

- **Rows:** ZKP1 has 57 rows of headroom. Each extra VAN needs about 150 rows, plus a range check and add. The rho binding grows from `Poseidon<7>(cmx_1..5, van, round)` to `Poseidon<6+N>` (about 9 permutations for N=11) or `Poseidon(cmx_1..5, H(van_1..N), round)`.
- **K:** almost certainly **K=13** unless it is carefully re-laid onto spare IMT-lane rows. That means about 2× prove time and a pk of about 300 MB.
- **Public inputs:** 13 + N.
- **Masks:** per-VAN masks would need `MAX_PROPOSAL_AUTHORITY` turned from a constant into witnesses.
- **The VK changes for every user**, not only those who use proxy delegation.

**Keystone and TX1 problems [code]:**
- The hardware wallet signs one synthetic Ironwood action. TX1 effects are fixed at `ACTION_COUNT = 1`, 820 bytes per action (`vs/circuits/src/tx1.rs:26-35`).
- There is exactly one output note, `cmx_new`, to one address.
- There is one 580-byte `enc_ciphertext`, whose memo currently holds human-readable display text (`zv/zcash_voting/src/action.rs:566-609`, `delegate.rs:2422-2445`).
- The device can therefore show at most one recipient. Other delegates' addresses and weights would be bound cryptographically through rho and `nf_signed` but invisible on the device.
- Delivering N per-recipient ciphertexts would need multi-action TX1, which changes the chain sighash framing (`vs/ffi/tx1/effects.go:40-65`).

**Not recommended.**

### (d) Modify ZKP2 to accept multiple input VANs

- ZKP2 sits at 2,015 of 2,048 rows. Each extra input adds about 1,140 rows, so M=2 already needs K=12 (about 2× prove time).
- A shared successor VAN needs a merged weight and a fresh rand. That is a merge in disguise, and it breaks tracking.
- It changes the VK for every voter. If needed at all, ship it as a separate additive "multi-cast" circuit.

**Not recommended over (b).**

### (e) Per-output proposal-authority subsets with per-proposal weight conservation

**Statement, for each proposal p in 1..50:** `Σ_i w_i · b_{i,p} = w · b_{parent,p}`.
- All `w_i < 2^30` and N ≤ 11, so the left side is below p and the equation holds over the integers.
- If `b_parent,p = 0`, every output with positive weight must have `b_{i,p} = 0`, so subsets are implied.

**Bit 0 must be excluded** and forced to 1 on every output. If bit 0 were included, the rule would collapse back to total-weight conservation and forbid "A gets everything except proposal 3, I keep proposal 3", which needs nominal `Σw_i = 2w`. Also bound each `w_i ≤ w`, so a zero-authority output cannot carry arbitrary weight.

**Constraint count for 51 bits × 11 outputs:**

| Part | Count |
|---|---|
| Output bit booleans | 561 |
| Output mask running sums | 561 |
| Parent bits plus sums (or reuse) | 51 + 51 |
| Conservation (50 rows) | 50 degree-2 constraints of 12 product terms, about 600 multiplications |
| Weight bounds | 11 range checks |
| **Total** | **about 1.9-2.3k constraint terms** |

**Layout options:**
- A wide 51-row gate of about 37 advice columns (11 bits + 11 accumulators + 11 weight copies held with a `w(cur) = w(prev)` gate + parent bit, accumulator, `w` and `2^p`).
- About 600 rows on about 10 columns.

The wide layout risks the **15 KiB proof cap**, because each advice column adds roughly 64-100+ bytes. Feasible at K=12.

**Workaround without (e):** vote on the excluded proposal first (ZKP2 clears bit 3), then split the successor VAN. This does not support "I'll vote on 3 later".

## 6. Recipient discovery

**[code] There is no in-circuit note encryption.** The only encryption in-circuit is El Gamal of vote shares under `ea_pk` (ZKP2 cond 11).

**There is already an out-of-circuit channel for ZKP1-minted VANs:**
- `MsgDelegateVote.tx1_effects` (proto `:57-69`) carries the full Ironwood action: `cv_net, nf, rk, cmx, epk, enc_ciphertext(580), out_ciphertext(80)`, 820 bytes.
- The chain checks only that `rk`, `nf_signed` and `cmx_new` match (`vs/ffi/tx1/effects.go:40-65`) and computes the sighash over it (`vs/x/vote/ante/validate.go:262`).
- The `enc_ciphertext` encrypts the zero-value output note (to the VAN's address) and a 512-byte memo.

A delegate's hotkey IVK can trial-decrypt that ciphertext. Putting `(num_ballots, van_comm_rand)` in the memo, or deriving rand from the note's `rseed`, gives in-band discovery **with no circuit change**. Two points:
- The memo is currently used for HW display text, so the product must decide the encoding.
- I have not verified that the chain serves `tx1_effects` to scanners.

**A garbage ciphertext is detectable, not preventable.**
- The recipient recomputes `van_integrity_hash(own g_d_x, pk_d_x, num_ballots, round, MAX, rand)` and compares it with the public `van_cmx` (or a split output commitment).
- On a mismatch, the recipient treats it as "not received".
- The harm falls on the sender: their weight is burned. It cannot inflate weight.
- Spam costs a real ZKP1, which needs at least 1 ballot of real notes and per-note gov-nullifiers. Zero-weight split outputs would make spam cheaper, so cap dust (§risks).

**In-circuit verifiable encryption (Orchard-style):**
- Per output: `epk = [esk]g_d` and `ss = [esk]pk_d` (2 × 137 rows of variable-base mul) + a KDF Poseidon + `ct = w + Poseidon(ss, tag)`. That is about 300-360 ECC rows per output, about 4k for 11 outputs.
- That needs about 2 more 10-column ECC lanes or K=13, and threatens the 15 KiB cap.
- Not justified, since a mismatch only hurts the sender.

**Deterministic opening:**
- `rand` can be derived as `PRF(KDF([esk]pk_d), output_index)`, with `epk` published. The recipient computes `[ivk]epk`.
- **Weight cannot be derived.** Brute-forcing about 1.7×10^8 possible ballot values per output (about 30-60 minutes) is not viable unless weights are restricted to a public bucket list.
- The minimum is `epk` (32 B) + AEAD(weight ‖ optional mask) (8-16 B + 16 B tag), about 64 B per output, versus about 660 B for a full Orchard note ciphertext.
- Include the output index and a fresh `esk`. Otherwise identical VANs get identical nullifiers and one becomes stuck.

**Current zcash_voting rand derivation [code]:** `van_comm_rand` is derived from the *delegator's own hotkey secret* plus round, bundle and notes (`zv/zcash_voting/src/van_blinding.rs:13-91`). Proxy delegation needs a recipient-recoverable derivation instead.

## 7. Linkability: can a delegator learn whether the delegate voted?

**Yes, today, with no circuit change [code + inference].** ZKP2's successor VAN reuses `van_comm_rand`, the address, the weight and the round (`vote_proof/circuit.rs:884, 1112-1131`; builder `:423-461`). `vote_authority_note_new` is public in every `MsgCastVote`, including every intermediate vote in a batch (`tx.proto:76-88`).

A delegator who minted the VAN knows the address x-coordinates, `num_ballots`, the round and `rand`. They can therefore:
1. Compute the 50 candidates `VAN(mask = MAX − 2^p)` and match them against on-chain `vote_authority_note_new` values.
2. Continue the chain from each matched mask, one bit at a time, at about 50 Poseidon hashes per step.

**They learn** exactly which proposals their weight was used on, in what order, and in which transactions. **They do not learn** the decision: shares are El Gamal-encrypted with PRF randomness keyed by the delegate's hotkey `sk` (`builder.rs:508-600`), and ZKP3 reveals are unlinkable without the delegate's blinds. **The public learns nothing new.**

**What breaks tracking:**
- The VAN nullifier uses the delegate's `nk`, so the delegator cannot see it.
- Any operation that re-randomizes `rand` (merge, re-split by the delegate, multi-input ZKP2) breaks tracking. A delegate who wants to evade can do so whenever such a circuit exists.

**Helper API gap [code]:** the only public transition helper, `vote_proof::derive_vote_authority_transition`, needs the hotkey `sk` (`builder.rs:467-505`). `delegation::van_commitment_hash` fixes the mask to MAX (`delegation/circuit.rs:235-249`). `van_integrity_hash` is `pub(crate)`. A delegator-side tracker needs an exported address-based helper. That is an API change with **no VK change**.

**Minimal changes if merge or re-split exist:**
- **Consumption tag per input:** `tag_i = Poseidon(DOMAIN_CONSUMED, rand_i, round)` as a public input, about 38-75 rows. The delegator learns "consumed by merge or split". It is pseudorandom to the public.
- **Provenance pass-through:** publish `c_i = rand_new + Poseidon(D1, rand_i)` and `d_i = w_new + Poseidon(D2, rand_i)` per input, about 2 permutations + 2 adds each. Delegator i recovers the merged VAN's `(rand, weight)` and keeps tracking through ordinary ZKP2. The cost is that every contributing delegator learns the influencer's total merged weight and full voting activity.
- **No-circuit alternative:** the delegate voluntarily discloses `(shares_hash, decision)` for a `MsgCastVote`. That proves the decision against the public `vote_commitment = Poseidon(DOMAIN_VC, round, shares_hash, proposal_id, decision)`.

**Inherent leak:** the delegate necessarily learns the delegator's exact `num_ballots`, and with full delegation of a note bundle that reveals the delegator's balance rounded down to 0.125 ZEC. This contradicts the book's statement that "Nobody can observe the delegation amounts" (`shielded-vote-book/delegation/partial-delegation.md:34`).

## 8. Existing TODOs, stubs and tests about delegation extensions

There are **no stubs, TODOs or tests for VAN split, merge, re-delegation or proxy delegation in voting-circuits.**
- The tests named `round_scoped_van_redelegation_*` (`vc/src/vote_proof/circuit.rs:2147-2208`) cover re-delegation **across rounds**: same address, distinct VAN and nullifier.
- "Merged circuit" in `delegation/builder.rs:4,111` means the five-note delegation circuit, not VAN merge.
- The only TODOs are `TODO(sean)` VK-fingerprint tripwires (`delegation/prove.rs:266-290`, `vote_proof/prove.rs:277-300`, `share_reveal/prove.rs:321-345`).
- The book's unimplemented VAN split is a **1→2** split (delegate plus change) with **inherited mask**, with multiple delegates done by successive splits (`shielded-vote-book/delegation/partial-delegation.md:1-38`).

## 9. Versioning and compatibility

**How VKs work [code]:**
- There is no on-chain VK registry and no circuit-version field in `MsgDelegateVote` or `MsgCastVote`.
- VKs are regenerated deterministically at process start with `keygen_vk(Params::new(K), Circuit::default())`. IPA needs no trusted setup (`vc/src/delegation/prove.rs:33-70`).
- The chain FFI uses one cached key set per circuit (`vs/circuits/src/ffi.rs:609, 819, 996`). Circuit identity is the exact crate pin (`vs/circuits/Cargo.toml:28`).
- Only TX1 effects carry a version byte (`EFFECTS_VERSION = 1`, `vs/circuits/src/tx1.rs:26-27`).
- `protocol_hash.rs` is **not** a version registry. It holds the Poseidon wrappers and frozen vectors (`vc/src/protocol_hash.rs:1-51`).
- Shape changes are caught by the VK fingerprint tests plus frozen hash vectors (`van_integrity.rs:149-167`, `domain_tags.rs:90-121`).

**Precedent:** changing `MAX_PROPOSALS` broke the ZKP1 and ZKP2 VKs (`vc/CHANGELOG.md:44-51`). The K reductions changed all three VKs and the proof sizes (`:126-149`).

**What each option implies [inference]:**
- **New circuit** (split or merge): additive. Existing VKs, VAN format and old VANs are untouched. It needs a new message type, FFI verify path, nullifier-set integration, sighash, client support, and a chain upgrade *before* first use.
- **Changing ZKP1 or ZKP2:** the VK changes for every user, so all validators must upgrade in a coordinated way and every client must upgrade too.
- **Changing the VAN preimage** (a delegatable flag, a provenance field) moves the ZKP1 and ZKP2 VKs plus every VAN-handling circuit. ZKP3 is unaffected.
- **Round scope helps:** VANs and nullifiers are round-scoped, so old VANs die with their round and **no cross-round migration** is needed. Upgrades must land **between rounds**, because the chain cannot verify two VK versions in the same round today. If mid-round coexistence is ever required, it needs a round-level circuit-version parameter and dual VK caches.
- The 15 KiB proof cap and the matching FFI output buffer are consensus constants (`keys.go:65-74`).
- Coordinate with the ZKP 1.5 / `DOMAIN_VC_V2` redesign, which also bumps the ZKP2 and ZKP3 VKs.
