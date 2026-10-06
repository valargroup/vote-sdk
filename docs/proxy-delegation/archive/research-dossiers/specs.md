# Specification read: proxy delegation vs. the ZIP drafts and the shielded-vote-book

"Proxy delegation" here means giving some or all of a wallet's per-round voting weight to another person's hotkey. "Delegation" or "ZKP1" means today's note-to-VAN registration.

**Legend.** [DOC] means a spec or the book says it. [CODE] means I spot-checked code only to flag spec drift; the code-level agents own those details. [INF] means my own inference.

**File keys.** `S=/private/tmp/claude-501/-Users-czstudio-Documents-vote-sdk--claude-worktrees-vote-delegation-planning-29b451/0cff7cc1-5f3f-4de1-be83-322a1fdf7727/scratchpad/src`, `V=/Users/czstudio/Documents/vote-sdk/.claude/worktrees/vote-delegation-planning-29b451`

| Key | File |
|---|---|
| ZIP-A | `$S/zips/draft-valargroup-shielded-voting__adam_voting-protocol-client-delay.md` (PR 1200 header; newer circuit text, MUST-heavy) |
| ZIP-G | `$S/zips/draft-valargroup-shielded-voting__greg_shielded-voting-fixes.md` |
| WAPI | `$S/zips/draft-valargroup-shielded-voting-wallet-api__adam_shielded-voting-wallet-api.md` |
| SUB | `$S/zips/draft-valargroup-submission-server__adam_submission-server-client-delay.md` |
| SETUP | `$S/zips/draft-valargroup-shielded-voting-setup__adam_voting-setup-client-delay.md` |
| KS | `$S/zips/draft-valargroup-keystone-voting__draft-valargroup-keystone-voting.md` |
| CER | `$S/zips/draft-valargroup-ea-key-ceremony__greg_ea-ceremony.md` |
| BOOK/ | `$S/shielded-vote-book/` |

---

## 0. Executive summary

- **No normative text exists for proxy delegation in any ZIP.**
  - Both protocol drafts only say the send-based VAN model keeps a future "VAN-to-VAN delegation proof that consumes one VAN and produces two" possible (ZIP-A:342-345, 1522-1533, 1613-1618; ZIP-G:398-403, 1460-1471, 1598-1603).
  - The book describes a non-normative VAN split (BOOK/delegation/*.md, BOOK/userflow/delegating-your-vote.md). It points to "§6.0 in Gov Steps V1", an external baseline document that is not in any repository provided.
- **Specs lag the code in ways that matter for this feature.**
  - Proposal authority: every ZIP and the book say it is 16 bits wide with 15 proposals. The code uses 51 bits and 50 proposals ([CODE] `$S/voting-circuits/src/params.rs:30-44`, `$V/x/vote/types/keys.go:48`).
  - Delegation sighash: the ZIPs say the chain trusts a client-provided sighash (ZIP-A:740-749, WAPI:541-547). The chain actually recomputes it from on-chain `tx1_effects` ([CODE] `$V/x/vote/ante/validate.go:259-272`).
  - Signed-note value: the ZIPs say 0 in the circuit (ZIP-A:631,664-665; KS:38-39,199-203). The circuit requires exactly 1 zatoshi ([CODE] `$S/voting-circuits/src/delegation/README.md:18-19,177-178`).
  - Batch messages: `MsgCastVoteBatch` and `MsgDelegateAndCastVoteBatch` exist in code but in no ZIP ([CODE] `$V/proto/svote/v1/tx.proto:97,110`). WAPI:191-193 still says proposals cannot be voted in parallel.
- **The delegate must know the delegated `num_ballots`.** It is a private witness in ZKP2 Condition 8 (ZIP-A:900-902). The delegate therefore always learns the exact delegated amount. This contradicts BOOK/delegation/partial-delegation.md:34 ("Nobody can observe the delegation amounts") but matches BOOK/overview/design-principles.md:20 ("No one but the delegation recipient can see the delegation amount").
- **On "can I find out whether my delegate voted?":**
  - Nothing in the specs provides a way for a third party to check this.
  - From the formulas [INF]: ZKP2 MUST reuse the old VAN's address and `gov_comm_rand` (ZIP-A:895-896). So anyone who knows the delegated VAN's full opening can recognise its successor VANs among public tree leaves or cast-vote messages. That shows *that*, *when* and *on which proposals* the delegate voted, but not *how*.
  - The VAN nullifier is keyed by the delegate's `vsk.nk` (ZIP-A:332-336), so the delegator cannot compute it.
  - The decision is only verifiable if the delegate discloses the VC opening.
  - This contradicts the rationale that reused VAN fields are "never externally observable" (ZIP-A:1470-1480).
- **Keystone.** Today the device signs one single-action PCZT per bundle. It displays a 1-zatoshi spend, a 0 fee, the raw "To" hotkey address and an unverified memo (KS:283-307).
  - [CODE] The "To" address is cryptographically bound to the VAN hotkey. The circuit builds the VAN `vpk` from the output note's address (`$S/voting-circuits/src/delegation/README.md:311-323`). The chain binds `cmx_new` to the signed TX1 action (`$V/ffi/tx1/effects.go:38-62`).
  - A "30% to @alice" split cannot be expressed or verified on Keystone without circuit, TX1-profile and display changes.
  - The spec direction (KS:332-335) supports doing proxy delegation afterwards with the hotkey, with no Keystone involvement.

---

## 1. Every mention of third-party, partial, split, re-delegation, "optional extension" and future work

### 1.1 Protocol ZIP: both drafts

| # | Normative or descriptive text | ZIP-A | ZIP-G | Notes |
|---|---|---|---|---|
| 1 | Terminology. "VAN nullifier: … published when the VAN is consumed (to cast a vote or delegate)" | 86-90 | 86 | Presumes a VAN-consuming delegation that is never specified. |
| 2 | "**VAN nullifier.** When a VAN is consumed (to vote or delegate), its nullifier is: …" | 329-330 | 389 | Same as row 1. |
| 3 | "A VAN MUST be created during delegation (Phase 1) and consumed during voting (Phase 2), which MUST produce a replacement VAN with updated proposal_authority." | 338-340 (normative MUSTs) | 398-400 ("**Lifecycle.**", descriptive, no MUSTs) | Proxy delegation needs a third creation and consumption path. |
| 4 | "The VAN model is designed to support future extensions such as partial delegation (splitting num_ballots across multiple delegates), but this ZIP specifies only the delegation and voting operations." | 342-345 | 400-403 | Same text in both. |
| 5 | Governance hotkey definition: "An Orchard key hierarchy, distinct from the holder's spending key … generated on a general-purpose device" | 74-78 | absent | ZIP-G dropped the terminology entry. |
| 6 | "**Unlinkable delegation.** A holder delegates voting power to a locally-generated hotkey" | 135-137 | 132-134 | "Locally-generated" assumes the holder owns the hotkey. |
| 7 | Requirement: "The protocol supports delegation of voting authority to a third-party hotkey." | **absent** | **213-214** | The only explicit third-party requirement. ZIP-A dropped it. |
| 8 | "The Delegation Proof does not constrain the hotkey address to match the holder's key; the output address is bound … through the VAN commitment and the rho binding" | 495-499 | 547-551 | The basis for today's ZKP1-to-another-party's-hotkey (custody handoff). |
| 9 | Hotkey generation: "MUST be derived deterministically from a seed … fresh BIP 39 mnemonic … MUST NOT be exported or backed up; it is needed only for the duration of the voting round" | 501-518 (MUST) | 553-557 ("MAY … or sampled randomly … an application concern") | Direct contradiction. [DOC] the book guide says the SDK uses `generate_random_voting_hotkey` with a 32-byte seed (BOOK/wallet-integration/guide.md:48,220-226). |
| 10 | "Why a Send-Based VAN Model": "preserves the ability to add partial delegation … a future VAN-to-VAN delegation proof could consume one VAN and produce two with subdivided ballot counts … Under the keysharing alternative, the delegate holds the full key and therefore the full voting weight" | 1506-1543 | 1444-1481 | The key design rationale. It says "two" outputs, not k. |
| 11 | Open issue: "Partial delegation (a VAN-to-VAN delegation proof that consumes one VAN and produces two …) is enabled … but not specified … would allow a holder to distribute voting weight across multiple delegates" | 1613-1618 | 1598-1603 | Same text. |
| 12 | Open issue: simplified non-send VAN model "forecloses partial delegation" | 1619-1627 | 1604-1612 | Same text. |
| 13 | Voters "MUST distribute shares across multiple independent servers" | 1193-1196 | 1152-1155 ("MAY") | Contradiction. |
| 14 | Hardware firmware future work ("governance network byte … display the delegation context") | absent | 1313-1323, 1554-1565, 1590-1597 | Relevant to Keystone display. |
| 15 | Bitmask is 16 bits with proposals 1-15 (`MAX_PROPOSAL_AUTHORITY = 2^16-1`) | 321-325, 359, 703-707, 794 | 381-385, 417, 705, 767 | Both stale vs [CODE] 51 bits / 50 proposals. |
| 16 | VCT capacity rationale: "10,000 voters each voting on 50 proposals produce roughly 1 million leaves" | 1557-1572 | 1523-1538 | Inconsistent with its own 15-proposal limit. Must be recomputed for proxy delegation. |
| 17 | Stray fragment "construction." under Deployment | 1590 | n/a | Editorial bug. |

**Which draft is canonical?** ZIP-A matches the book and code on y-coordinate share commitments (ZIP-A:400-404 vs ZIP-G:448-451, BOOK/zkps/zkp3-vote-reveal-proof.md:17). It also has the vote sighash, delegation sighash and per-vote secret derivation. ZIP-G uniquely carries the third-party requirement, the Voting Round Identifier section (ZIP-G:312-354), the EA key section and the firmware-migration text. Neither draft alone is a sound base; the plan must pick one.

### 1.2 Book (non-normative)

- **BOOK/README.md:14** says "**Delegation (TODO)** lets you assign voting rights (fully or partially) to third parties".
- **BOOK/delegation/delegation-setup.md:3-34.**
  - Delegation is "transfer their voting authority to another party. The delegate receives a VAN and can vote **or further delegate**" (line 3).
  - Mechanism: consume the delegator's VAN and produce two new VANs, one for the delegate with "their hotkey and the delegated amount" and one for the delegator with the remainder (lines 9-13). "The ZKP proves the amounts sum correctly and the `allowed_proposals` bitmask is preserved" (line 15).
  - The delegate's VAN carries the same bitmask (lines 19-24).
  - Privacy (lines 28-30): "The delegation amount is hidden inside the VCT", "The delegator's identity is hidden", "**The delegate's identity is hidden (their hotkey is committed inside the VAN, not published)**".
  - Status: "**Delegation is specified as an optional extension (§6.0 in Gov Steps V1). The core protocol prioritizes direct voting first.**" (line 34).
- **BOOK/delegation/partial-delegation.md:1-38.**
  - Example: 100 ZEC split into 30 for the delegate and 70 kept (lines 13-21).
  - "**Multiple delegates**: … by performing successive delegations … Each delegation is a separate transaction that consumes your current VAN" (lines 23-30).
  - "Nobody can observe the delegation amounts" (line 34).
  - "Partial delegation follows naturally from the VAN split mechanism but is specified as part of the optional delegation extension (§6.0)" (line 38).
- **BOOK/userflow/delegating-your-vote.md:1-28.** It repeats the split, says the bitmask is inherited, and says the delegate can "Further delegate to other parties" (line 24).
- **BOOK/data-types.md:38-42.** VAN lifecycle: "Consumed in Phase 2 (voting) or during delegation … When delegating: amount is split, two new VANs produced".
- **BOOK/overview/design-principles.md:20.** "**Private delegation** — You can partially delegate your votes to third parties. No one but the delegation recipient can see the delegation amount."
- **BOOK/overview/design-principles.md:25.** "Servers who receive delegations can fully delay the votes themselves" (ambiguous; probably share servers).
- **"Gov Steps V1".** [DOC] It is cited by the book (delegation-setup.md:34, partial-delegation.md:38) and by `$S/voting-circuits/src/vote_proof/README.md:241` ("Baseline spec (Gov Steps V1 §3.5 Step 2 …)"). It is the original baseline spec. Its §6.0 is the only place the delegation extension is said to be "specified". I could not find it in any provided checkout.

### 1.3 Other ZIPs

- SETUP:141-142 lists "VAN nullifiers (from delegation consumption)". This is wrong for today's protocol, where VAN nullifiers come from votes; it is accidentally right for proxy delegation.
- SETUP:122-125 puts "delegation" among out-of-scope voter-facing interactions.
- KS:132-134 (non-requirement) and KS:447-461 (open issues) list voting-aware firmware and memo standardization as future work.
- WAPI:29-33 defines "Delegation" only as ZKP1. WAPI:603 "Share Delegation" uses "delegation" for sending shares to helpers.
- The book does the same in BOOK/delegation/server-delegated-shares.md and BOOK/data-types.md:104 ("Delegated in Phase 3"). That is a third meaning of "delegation", and these should be renamed.

### 1.4 Contradictions to resolve (book vs. ZIPs vs. code)

| Topic | Book | ZIP-A | ZIP-G | Code |
|---|---|---|---|---|
| Field names | `total_note_value`, `allowed_proposals` (BOOK/zkps/zkp2-vote-proof.md:26,40,48,52; BOOK/delegation/*.md; BOOK/data-types.md:98) | `num_ballots`, `proposal_authority` | same as ZIP-A | n/a |
| `vsk.nk` vs `nk` | "the **same field element** … not distinct key material" (BOOK/data-types.md:36; BOOK/circuits/van-nullifier.md:16,23-25; BOOK/circuits/governance-nullifier.md:28-30) | distinct when the hotkey is separate (1355-1366) | same as ZIP-A (841-857) | separate hotkeys exist (custody handoff), so the book is stale |
| Governance nullifier | `Poseidon(nk, dom, real_nf)` with ConstantLength<3> (BOOK/circuits/governance-nullifier.md:12-17) | `Poseidon(nk, tag_gov, round, nf)` with ConstantLength<4> (655, 1249) | same as ZIP-A | not checked |
| Signed note value | 1 (BOOK/zkps/zkp1-delegation-proof.md:25) | 0 (631, 664-665) | 0 (675, 1297) | exactly 1 zatoshi ([CODE] `$S/voting-circuits/src/delegation/README.md:18-19,177-178`) |
| Bitmask width | 16 bits, bit 0 sentinel (BOOK/data-types.md:20; BOOK/circuits/proposal-authority-decrement.md:99-101) | 16 | 16 | 51 bits, 50 proposals ([CODE] params.rs:30-44; vote_proof/README.md:241-250; `$V/x/vote/types/keys.go:48`) |
| Delegation sighash | out-of-circuit check of a signature over `sighash` (BOOK/zkps/zkp1-delegation-proof.md:60) | client-provided, not recomputed (740-749) | "application-defined sighash" | recomputed from `tx1_effects` ([CODE] `$V/x/vote/ante/validate.go:259-272`) |
| Share decomposition | base-10 denominations, PRF-weighted remainder, shuffle (BOOK/appendices/share-splits.md) | deferred to SUB | n/a | n/a; SUB:534-541 says "roughly evenly … floor division" (stale) |
| `submit_at = 0` | n/a | "immediate (last-moment mode)" (1183) | removed | n/a; SUB:264-265 says immediate; **WAPI:635 says "submit at the last possible moment before vote_end_time"** (contradiction) |
| Tally proof | Chaum-Pedersen proof by the EA (BOOK/appendices/tally.md:27-33; BOOK/overview/design-principles.md:33) | threshold, deferred to CER | "Why No Per-Validator DLEQ Proofs" (1415-1443) | n/a |

---

## 2. Privacy and security claims vs. proxy delegation

**What the specs claim.** There are no claims of coercion resistance, receipt-freeness or vote-buying resistance anywhere; a grep across all ZIPs and the book found none. The properties actually claimed are listed below.

| Claim | Source | Effect of proxy delegation |
|---|---|---|
| Voters "cast stake-weighted votes … without revealing their identity, individual balances, or vote allocations" | ZIP-A:100-102 | **Weakened toward the delegate.** The delegate must witness `num_ballots` (ZKP2 Condition 8, ZIP-A:900-902), so it learns the exact delegated ballots. If the delegator gives 100% of a bundle, that equals the bundle's balance to within 0.125 ZEC. If the delivery channel identifies the delegator (an X DM, an authenticated off-chain package as in the custody flow), the delegate learns identity plus balance. |
| "Nobody can observe the delegation amounts" | BOOK/delegation/partial-delegation.md:34; BOOK/userflow/delegating-your-vote.md:18 | **False for the delegate; true for third parties.** It contradicts BOOK/overview/design-principles.md:20. |
| "The delegate's identity is hidden (their hotkey is committed inside the VAN, not published)" | BOOK/delegation/delegation-setup.md:30 | **Made false by the influencer directory.** The commitment still hides the address on-chain, but the delegate's address is public by design. |
| "The governance hotkey address is never published, so a quantum adversary cannot learn the delegated amount" | BOOK/overview/privacy-guarantees.md:26 | The premise becomes false for delegates. [INF] The amount is still hidden by `gov_comm_rand` blinding (Poseidon), so the conclusion survives; the reasoning text must change. |
| Keystone: "The output address (governance hotkey) … is freshly generated per voting round and is not linked to the holder's on-chain Orchard addresses" | KS:107-109 | **False if ZKP1 targets a published delegate address.** |
| Keystone memo shows "{amount} ZEC" and "does not enter the ZKP circuit" | KS:110-112, 235-241 | [INF, plus a CODE layout check] The memo sits in the Orchard `enc_ciphertext` encrypted to the output (hotkey) address. `tx1_effects` (821 bytes = 1 version byte + one 820-byte action, `$V/ffi/tx1/effects.go:11-17`; `$V/proto/svote/v1/tx.proto:68`) is posted on the vote chain. In a direct-to-delegate ZKP1 the delegate can therefore decrypt the memo and learn the holder's **total eligible ZEC**, not just the delegated bundle. |
| "Why Reusing VAN Address and Randomness … safe because … the shared fields are never externally observable" | ZIP-A:1470-1480; ZIP-G:1365-1376 | **False against any party that knows the VAN opening.** A delegator who chose the delegate VAN's `gov_comm_rand` (as the custody controller does today, `$S/zcash_voting/docs/exporting-to-external-software.md:80-85`) can recompute the successor VAN for each single-bit clear and match it against public leaves or `vote_authority_note_new`. That reveals which proposals were voted, in what order and when. |
| Unlinkability to on-chain identity (governance nullifiers unlinkable without `nk`) | ZIP-A:153-160, 202-203 | **Preserved.** The delegator's ZKP1 is unchanged. [INF] A split submitted right after ZKP1 by the same client is timing-linkable to that ZKP1 transaction (it links the bundle to its delegate set, not to an identity). Splitting several bundles at once links those bundles together. |
| Balance hiding via vote splitting, and "No single submission server learns enough information to reconstruct a voter's total ballot count" | ZIP-A:162-167; SUB:204-205 | Preserved per VAN, since splits only shrink VANs. Weakened in last-moment mode (§5). Weakened if a VAN-merge operation is added: large merged VANs fall into high denomination bands (BOOK/appendices/share-splits.md:113-124). |
| Individual amounts never revealed; only the aggregate per (proposal, decision) | ZIP-A:169-171, 208-209; CER:454-455 | Preserved. The sole-voter-on-an-option leak (SUB:549-555, "Balance amendment") now reveals a delegate's **aggregate delegated weight** when they are the only voter on an option. |
| Vote commitment unlinkability (ZKP3 hides which VC) | ZIP-A:173-177 | Preserved. If a delegate voluntarily reveals `shares_hash` plus blinds as a receipt, those shares become linkable for that VC. |
| Voter identity isolation: "All vote commitments and share reveals reference only this hotkey … the EA never sees anything that connects the two" | SUB:162-172 | Holds for delegators. For public delegates the hotkey is linked to a public identity, which is intended. |
| "Double-voting … detectable via deterministic VAN nullifiers" | ZIP-G:202-203; ZIP-A:206-207 | **Must be preserved by design.** The split must publish the same VAN nullifier (same `vsk.nk`, same `"vote authority spend"` tag, same set) as a vote. Otherwise one VAN could be both voted and split. |
| "One user session to vote" | BOOK/overview/design-principles.md:9-13 | Holds for delegators. **Breaks for delegates**, who must come back or run an always-on agent as delegations arrive. |
| Receipt-freeness (not claimed) | n/a | [INF] Already absent: per-vote secrets are deterministic from the hotkey seed (ZIP-A:520-558), so any voter can reproduce and reveal a VC opening. Proxy delegation makes vote rental trivial: the buyer becomes the delegate, and the delegated weight is verifiable by the buyer. Nothing claimed becomes false, but the threat model should say this explicitly. |

---

## 3. VAN definition and lifecycle text that must change

1. **Terminology** (ZIP-A:32-36, 86-90; ZIP-G:same). Define proxy delegation. Rename the ZKP1 operation (for example "registration") or qualify it. Make "consumed (to cast a vote or delegate)" precise.
2. **Lifecycle MUSTs** (ZIP-A:338-340).
   - Add: a VAN MAY be created by a proxy-delegation (split) transaction, and MAY be consumed by a split.
   - A delegated VAN may never be consumed, because the delegate abstains.
3. **Future-extension paragraph** (ZIP-A:342-345; ZIP-G:400-403). Replace it with the actual specification.
4. **VAN fields** (ZIP-A:310-327). Widen `proposal_authority` to 51 bits and 50 proposals, matching code. Decide whether a delegate-VAN needs any marker; [INF] it should not, for indistinguishability.
5. **VCT leaf insertion order** (ZIP-A:444-445, "a delegation transaction inserts one VAN; a vote transaction inserts both a new VAN and a VC"). Add the split transaction: k+1 VANs, in order. Specify ordering inside atomic batches; the code has synthetic-root chaining (`$V/x/vote/ante/validate.go:226-240`).
6. **Nullifier sets** (ZIP-A:447-466). State that a split publishes into the VAN nullifier set using the identical derivation (ZIP-A:332). Do not add a new tag.
7. **Governance hotkey** (ZIP-A:468-518).
   - Today the hotkey MUST be fresh BIP-39, not exported, not backed up, and round-only.
   - Add a delegate-hotkey profile: stable, publishable, backed up, possibly reused across rounds, or attested per round by a long-lived identity key.
   - Reconcile with ZIP-G:553-557 (MAY) and with the SDK's random 32-byte seed (BOOK/wallet-integration/guide.md:220-226).
8. **Per-vote secret derivation** (ZIP-A:520-558).
   - Add deterministic derivation for split secrets: output `gov_comm_rand_i`, and the randomizer for the split signature.
   - This keeps crash recovery working (rationale ZIP-A:1334-1351).
   - `vote_root` already includes `van_old`, so a delegate with many VANs gets distinct secrets.
9. **ZKP2 Condition 7** (ZIP-A:887-896, "MUST reuse the old VAN's diversified address and commitment randomness"). Keep it if delegator observability is a feature. Change it to re-randomize on first spend if it is a leak; that is a ZKP2 circuit change.
10. **Rationale "Why Reusing VAN Address and Randomness"** (ZIP-A:1470-1480). Correct the "never externally observable" claim.
11. **Rationale "Why a Send-Based VAN Model"** (ZIP-A:1506-1543). Update "consume one VAN and produce two". Choose iterated 1→2 (book) or 1→(k+1), k ≤ 10.
12. **Rationale "Why Delegation to a Hotkey"** (ZIP-A:1311-1332). Today it is motivated only by hardware wallets. Add a separate rationale for proxy delegation.
13. **Motivation bullet** "Unlinkable delegation … locally-generated hotkey" (ZIP-A:135-137).
14. **Rationale "Why VCT Depth 24"** (ZIP-A:1557-1572). Recompute capacity.
    - [INF] Each delegated VAN is voted separately at 2 leaves per proposal, so 10k delegators × 11 VANs × 50 proposals × 2 is about 11M leaves, against 2^24 ≈ 16.7M.
15. **Ballot scaling** (ZIP-A:561-582). Splits must be whole ballots (1 ballot = 0.125 ZEC). Percentages need a rounding rule. Each delegate output must be at least 1 ballot, or 0 must be explicitly allowed.
16. **Book.** Rewrite BOOK/data-types.md:36-42, BOOK/circuits/van-nullifier.md, BOOK/circuits/governance-nullifier.md:28-30 and BOOK/delegation/*.md. Use the field names `num_ballots` and `proposal_authority`. Fix the `vsk.nk == nk` claim. Fix the ZEC-denominated example (partial-delegation.md:15-21).

**Circuit conditions a split proof needs** [INF, derived from ZKP2 Conditions 1-7]:
- VCT membership of `van_in`.
- Old-VAN integrity.
- Diversified-address and spend-authority checks for `vsk`.
- VAN nullifier, identical to Condition 5.
- `Σ num_ballots_out = num_ballots_in`, with **each output range-checked** to below 2^30 (and at least 1 if required) to prevent field wraparound. The book only says "amounts sum correctly".
- **Bitmask copied by equality** to every output, never reset to `MAX_PROPOSAL_AUTHORITY`, which ZKP1 uses as a constant (ZIP-A:703-707).
- Output VAN integrity with recipient `vpk_i`.
- A per-proposal variant (disjoint bitmasks, full weight) is also expressible by the data model, but the spec does not mention it.

---

## 4. Wallet API ZIP: sections to extend

| Section | Lines | Extension needed |
|---|---|---|
| Terminology "Delegation" and "VAN" | 29-39 | Add proxy delegation, delegator, delegate and delegated VAN. Today a VAN is "consumed … when the holder casts a vote". |
| Requirements | 80-91 | Delegator: can split and deliver, can (optionally) observe use. Delegate: can discover incoming VANs and vote with many VANs. |
| High-level steps 5-13 | 144-193 | Step 8 says "Identify the wallet's VAN by its commitment `van_cmx` computed during step 6" (161-164). This assumes the wallet created the VAN itself, so a delegate needs an incoming-VAN discovery step. Add a split step after step 8. Steps 9-13 "repeated sequentially … cannot be voted on in parallel" (191-193) is already stale given `MsgCastVoteBatch`. |
| Vote Configuration Format, validation and distribution | 209-293 | The single-round `config_version:1` schema is stale vs [CODE] the signed static/dynamic config with per-round `RoundEntry{auth_version, ea_pk, signatures}` (`$S/zcash_voting/zcash_voting/src/config/mod.rs:855-892`). Proxy delegation flags or a delegate-directory pointer need a home here (§7). |
| Data query endpoints | 295-509 | Possibly add: (a) per-block encrypted VAN-opening ciphertexts for trial decryption, modelled on Commitment Tree Leaves (434-462); (b) a VAN-nullifier spent query (none is specified today); (c) a delegate directory, if it is chain-hosted. |
| Delegation Transaction | 511-567 | Stale: lists a client `sighash` field (538, 541-547), while code uses `tx1_effects`. Add a new **Proxy Delegation (VAN split) Transaction** endpoint. Its body would be modelled on Vote Commitment (586-597): `van_nullifier`, `r_vpk`, the `vote_authority_note_new[]` outputs, proof, anchor height, signature, round id, plus optional encrypted openings. |
| Vote Commitment Transaction | 569-601 | Unchanged for a delegate voting one VAN. Document multi-VAN and batch behaviour. |
| Share Delegation / Submit Share / Share Status | 603-693 | Rename "Share Delegation" to avoid a third meaning. Share Status by nullifier is the hook for optional delegate receipts. |
| Vote Commitment Tree | 695-703 | Document successor-VAN matching if observability is intended. |
| Version Handling | 712-741 | The split circuit bumps `vote_protocol`. WAPI:719-722 makes wallets MUST-reject unknown `vote_protocol`, so a bump forces every wallet to update [INF]. Consider capability-style optionality. |
| Encoding | 765-802 | New fields and types for the split transaction and encrypted openings. |
| Open issues | 833 | Empty; add proxy-delegation open items. |

The book guide §12 (BOOK/wallet-integration/guide.md:594-603) documents today's custody handoff. That flow is full-bundle ZKP1 to a round-bound `VotingHotkeyTargetV1`:
- The target contains chain id, network, round, address index 0 and the 43-byte raw Orchard address (`$S/zcash_voting/docs/exporting-to-external-software.md:41-49`).
- The VAN opening, including `van_comm_rand`, is delivered off-chain (`...:80-85`).
- The same doc says no public-chain recovery exists (`...:175-195`).

This is the closest existing delegator/delegate API, and it is round-bound. An influencer would need a new target per round, or a long-lived identity that attests per-round targets.

---

## 5. Submission server ZIP interplay

- **Mechanics** [DOC].
  - Each share goes to exactly ⌈s/2⌉ servers chosen at random (SUB:332-342).
  - `submit_at` is drawn from Uniform(now, vote_end_time − buffer) (SUB:270-285).
  - The buffer is min(⌊0.1 × (vote_end − ceremony_start)⌋, 3600 s) (SUB:294-301).
  - In the last-moment buffer: single share, index 0, `submit_at = 0` (SUB:303-312).
  - Each share costs one ZKP3 proof, and proofs are expensive (SUB:183-193, 316-324).
- **Delegate load** [INF].
  - No VAN merge exists, so a delegate holding V delegated VANs needs V×P ZKP2 proofs and up to V×P×16 share payloads and ZKP3 proofs.
  - Per-helper quotas are crate-enforced (BOOK/wallet-integration/guide.md:668).
  - Heavy delegates near the deadline face exactly the lost-vote risk that last-moment mode exists for.
- **Late delegations** [INF].
  - If delegations arrive in the last-moment buffer, the delegate's votes become single-share.
  - That exposes each delegated VAN's full amount to any decryptor (BOOK/appendices/share-splits.md:134-140).
  - It also produces V simultaneous immediate reveals with the same decision, a timing fingerprint of the delegate's aggregate.
  - This argues for a delegation cutoff before `vote_end_time − buffer`.
- **Decisions are public** (SUB:145-160). All of a delegate's shares carry the same decision. That is expected for public influencers, but it concentrates censorship incentive. The mitigation is still ⌈s/2⌉ redundancy (SUB:362-374).
- **Sole voter on an option** (SUB:549-555). The tally reveals the delegate's aggregate delegated weight.
- **Share sizes and bounds.** Shares are below 2^30 and `num_ballots` is at most 2^30 (ZIP-A:574-582, 904-912). Total supply is about 1.68×10^8 ballots, so even a fully merged VAN fits [INF]. Splits conserve weight, so tally discrete-log bounds are unchanged.
- **Payload privacy** (SUB:129-135, 344-360). Unchanged per VAN.
- **Split transaction path** [INF]. The split is a hotkey-signed chain transaction, not a helper payload. It inherits no helper timing protection, so it is timing-correlated with the delegator's ZKP1 unless it is atomically batched (code precedent: `MsgDelegateAndCastVoteBatch`, `$V/proto/svote/v1/tx.proto:110`) or delayed.
- **Spec drift.** SUB:534-541 ("roughly evenly … floor division") contradicts the book's base-10 strategy. SUB:264-265 contradicts WAPI:635 on `submit_at = 0`.

---

## 6. Keystone voting ZIP: what is displayed and signed, and whether proxy delegation fits

**What the device signs** [DOC].
- The ZIP 244 sighash of a governance PCZT with **one** Orchard action (KS:29-34, 217-256, 408-419):
  - spend side: the dummy signed note, with rho bound to `Poseidon(cmx_1..5, van, round)` (KS:185-197);
  - output side: 1 zatoshi to the governance hotkey, carrying a memo (KS:232-241);
  - ZIP-32 path `[32', 133', account']` (KS:243-250).
- Keystone is needed once per bundle of up to 5 notes and never again in the round (KS:158-163, 332-335).

**What the device displays** (KS:283-302): `Amount: 0.00000001 ZEC`, `Fee: 0 ZEC`, `From #1 0.00000001 ZEC Mine <shielded>`, `To #1 0.00000001 ZEC {governance hotkey address}`, and `Memo: I am authorizing this hotkey managed by my wallet to vote on {round_name} with {amount}.{frac} ZEC`.

**What is cryptographically bound.**
- [DOC] The memo is informational and outside the circuit (KS:110-112).
- [DOC, stale] The chain does not recompute the sighash (ZIP-A:740-749).
- [CODE] The chain recomputes the sighash from `tx1_effects` and requires the action's `rk`, nullifier and `cmx_new` to equal the proof's public inputs (`$V/x/vote/ante/validate.go:259-272`; `$V/ffi/tx1/effects.go:38-62`).
- [CODE] The circuit builds the VAN `vpk` from the output note's `g_d_new` and `pk_d_new` (`$S/voting-circuits/src/delegation/README.md:311-323`).
- Therefore the displayed "To" address **is** the VAN's hotkey (up to the x-only negation caveat noted there). `num_ballots` is not displayed except in the unverified memo text.

**Can "Delegating 30% to @alice" be shown or authorized on Keystone?**
- **@alice.** The device can only show a raw Orchard address, which is bound. A handle can only appear in the memo, which nobody verifies. Users would have to compare a raw address.
- **30% split at ZKP1 time.** This needs:
  - a multi-VAN ZKP1, with a changed rho-binding arity (one `van` input today);
  - a multi-action PCZT (`tx1.ActionCount = 1`, `$V/ffi/tx1/effects.go:13`);
  - a new TX1 profile on the chain;
  - a new ZKP1 verification key.
- The device would then show k outputs of 1 zatoshi each; the weights could only live in the memo. Encoding weights as output values would make the screen look like real fund movement, breaking the 1-zatoshi / 0-fee reassurance (KS:304-307, 357-369). `v_new` is unconstrained (ZIP-A:635).
- **Whole-bundle routing with no circuit change.** This exists today: ZKP1 to a target hotkey per bundle (KS:158-163 "MAY choose to delegate fewer batches"; custody handoff). Granularity is note bundles, decided by SDK bundling policy, not percentages.
- **Future firmware.** A governance network byte could display "delegation context" natively (KS:447-457; ZIP-G:1313-1323).
- **Recommendation implied by the specs** [INF]:
  - Keep Keystone registration unchanged, targeting the user's own hotkey.
  - Do proxy delegation afterwards as a hotkey-signed VAN split with no Keystone involvement.
  - This satisfies "minimize user-facing signatures" (ZIP-A:204-205) and "no firmware changes" (ZIP-A:212-214; KS:122-123).
  - The hotkey can already vote the full weight, so letting it split grants no new authority. The display then lives in Vizor, not Keystone.

**Privacy caveats when Keystone targets a delegate directly** [INF]:
- KS:107-109 "freshly generated per round" becomes false.
- The `{amount}` memo is decryptable by the delegate from on-chain `tx1_effects`.

---

## 7. Setup ZIP: round configuration and service discovery

- **Round parameters** (SETUP:313-326): `snapshot_height`, `snapshot_blockhash`, `proposals` (1-15, stale; code allows 50), `vote_end_time`, `nullifier_imt_root`, `nc_root`, `verification_keys`. There is no delegation flag, delegate limit or cutoff.
  - The round ID is a Poseidon hash of fixed fields: SETUP:328-331; ZIP-G:312-354 (8 field inputs); BOOK/chain/chain-api.md:11.
  - [INF] Binding a new per-round parameter into `voting_round_id` changes the derivation for the chain and all clients. Storing it as a non-hashed `VoteRound` field avoids that but is not covered by `proposals_hash` or the round-id commitment.
  - A new split circuit verification key must be in `verification_keys` (SETUP:325-326), so the feature is per-round opt-in from round creation.
- **Lifecycle** (SETUP:341-349). In ACTIVE, "Voters may delegate, vote, and submit shares". There is no delegation-cutoff phase. Timing parameters list only `vote_end_time` (SETUP:350-356).
- **Service discovery** (SETUP:271-292). A centralized `/api/voting-config` bootstrap directory, where validators self-register and an admin approves them through an admin UI.
  - [INF] This is the closest spec precedent for a curated delegate directory (self-register, admin-approve, publish).
  - [CODE] Wallets actually consume the signed static/dynamic config (`$S/zcash_voting/zcash_voting/src/config/mod.rs:855-892`; BOOK/wallet-integration/guide.md:95-157).
  - A delegate directory or flags could be (a) a new signed per-round field, which needs a new `auth_version`, or (b) a separate signed document referenced from the dynamic config.
  - The local `/Users/czstudio/Documents/token-holder-voting-config/README.md` still documents the WAPI v1 single-round schema.
- **Roles** (SETUP:167-214) have a single vote manager. [CODE] The chain has `MsgUpdateVoteManagers` and endorser messages (`$V/proto/svote/v1/tx.proto:230,282-315`). [INF] If directory curation needs on-chain authority, the endorser concept may be reusable.
- SETUP:141-142 mislabels VAN nullifiers as coming "from delegation consumption".

---

## 8. What voters can verify today, and the extension to "did my delegate vote"

**What voters can verify** [DOC]:
- Wallets "confirm their on-chain inclusion" (WAPI:87-88).
- Transaction polling (WAPI:752-757).
- Share status by nullifier (WAPI:669-693; step 13 at WAPI:187-189).
- The SDK requires two helpers to agree on a confirmation (BOOK/wallet-integration/guide.md:551).
- SUB open issue: "Voters currently have no privacy-preserving way to confirm that their shares were submitted"; PIR-based confirmation is future work (SUB:542-548).
- Tally verification covers aggregates only (ZIP-A:210-211; SETUP:358-376; CER:450-455).
- BOOK/overview/privacy-guarantees.md:16 says "That you voted — a governance nullifier is published".
- No spec text covers verifying a third party's use of delegated weight.

**What the formulas allow** [INF]:
1. **That, when and which proposals.** A delegator who knows the delegated VAN's opening `(vpk, num_ballots, round, authority, gov_comm_rand)` can compute the successor commitments for each possible single-bit clear (at most 50) and match them. The match can be against public leaves (WAPI:434-462) or against `vote_authority_note_new` in cast-vote messages (WAPI:586-597; [CODE] batch messages carry each intermediate one, `$V/x/vote/ante/validate.go:226-240`). This needs no delegate cooperation and works only because of ZIP-A:895-896.
2. **The VAN nullifier route is unavailable**, because it is keyed by the delegate's `vsk.nk` (ZIP-A:332-336).
3. **How the delegate voted** needs the delegate's cooperation. The vote transaction inserts `van_new` and `vc` together (ZIP-A:444-445), so a tracked successor VAN identifies the VC.
   - The delegate can reveal `(shares_hash, vote_decision)`. The delegator then checks `vc = Poseidon(DOMAIN_VC, round, shares_hash, proposal_id, decision)` (ZIP-A:351).
   - Revealing the blinds as well lets the delegator compute share nullifiers (ZIP-A:418) and query share status to confirm the vote was counted.
   - This is a verifiable receipt, and also a vote-buying receipt.
4. **To hide usage from delegators,** the delegate must re-randomize `gov_comm_rand` (or the address) on first spend. That is a ZKP2 change.

---

## 9. Spec drift the plan must reconcile before extending the specs

- 16-bit / 15 proposals in all ZIPs and the book vs 51-bit / 50 in code.
- Client-provided sighash vs on-chain `tx1_effects` recomputation.
- Signed-note value 0 vs 1 zatoshi.
- Batch and delegate-and-cast messages absent from the ZIPs.
- The WAPI config schema vs the signed static/dynamic config.
- Equal-split decomposition (SUB) vs base-10 decomposition (book).
- `submit_at = 0` semantics.
- MUST vs MAY share distribution.
- The book's `vsk.nk == nk` claim, governance-nullifier arity and field names.
- The book's single-EA Chaum-Pedersen tally vs threshold decryption.
- "Gov Steps V1 §6.0" is unavailable.