## Glossary

#### Vote Authority Note

This/Users/greg/.claude/plans/dreamy-leaping-abelson.md is a spendable ZCash-like note. It represents (Owner, amount, authorized_proposals).

A vote consumes this note (via nullifier), and produces a new Vote Authority Note with the same owner, amount, but the authorized proposals decremented. It also produces a vote commitment.

#### Vote

A vote is a public reveal of (enc_share, proposal_id, decision) where `enc_share` is an El Gamal ciphertext of the vote amount. This is posted along with a ZKP to prove the ciphertext is from a valid vote commitment, and a nullifier to show that this share from the vote commitment has not been used before. The plaintext amount is never revealed — ciphertexts are accumulated homomorphically and only the aggregate is decrypted at tally time.

#### Vote Commitment

We don't want people to learn your total balance from looking at your vote. So we split up votes into many shares.

A vote commitment is a commitment to a distribution of your votes. We split the total vote weight into 4 shares, each encrypted as an El Gamal ciphertext under the election authority's public key. We hash all 4 encrypted shares into a single `shares_hash`.

The vote commitment is then a commitment to `(DOMAIN_VC, shares_hash, proposal_id, decision)`

### Vote Commitment Tree

An append-only Merkle tree of BOTH Vote Commitments and Vote Authority Notes. The structure mirrors ZCash's note commitment tree (an append-only Merkle tree of fixed depth), but uses **Poseidon** as the hash function instead of Sinsemilla, for faster in-circuit proving.

Leaves are inserted in order as transactions land on the vote chain. Each leaf is either a VAN commitment (from Phase 2) or a vote commitment (from Phase 3). The tree root at a given vote-chain height serves as the anchor for Merkle inclusion proofs in ZKP #2 and ZKP #3.

**Domain separation:** Because VANs and VCs share the same tree, their Poseidon preimages must be structurally non-overlapping — a valid VAN plaintext must never hash to the same leaf as a valid VC plaintext (and vice versa). We prepend a constant domain tag as the first Poseidon input: `DOMAIN_VAN = 0` for Vote Authority Notes and `DOMAIN_VC = 1` for Vote Commitments. This makes collision structurally impossible regardless of Poseidon mode or future arity changes.

### Voting session

We define a voting session as a series of votes with the same snapshot height. A voting session commits to a series of proposals.

### Proposal IDs

The number of proposals for a polling session must be <= 16. Proposals can have a globally unique ordering as a UI property, but for the purpose of all ZKP circuits, proposals are 0-indexed. We design circuits as having 16 proposal authorities. If there are less votes in a poll, less will be used.

### Proposal Authorities

In the vote authorization note we store `authorized_proposals` as an integer. "Full authority" is the default, which is the constant `2^16 - 1 = 65535`. If you represent this in binary, a 1 bit means that proposal has authority to vote on that proposal, with proposal ID being bit index counting from LSB. (`has_authority_for_proposal_i = vote_authority && (1 << i)`)

## Phase 0 (Setup)

**Step 0.1: Pick governance parameters**

We must first choose 4 parameters:

- Snapshot height for balances (`snapshot_height`)
- The snapshot blockhash of ZCash mainnet (`snapshot_blockhash`)
- The proposals we vote on (`proposals`)
- When voting ends (`vote_end_time`)

Snapshot height **must** align with an anchor height that ZCash maintains. We include the snapshot block hash to protect against uncles / stale blocks mined at the snapshot height. How we encode the proposals isn't really relevant for this document, we just need to be able to hash it somehow. Vote end can be a timestamp, its just when we stop accepting votes to declare the final resutl.

**Step 0.2: Create pre-processing parameters**

- Construct the nullifier IMT
  - We gather all Orchard nullifiers from ZCash mainnet, until the snapshot height.
  - We create an IMT over them, using Poseidon.
  - We publish `nullifier_imt_root`
  - This enables voters to create non-membership proofs (proving their notes' real nullifiers are NOT in the set, i.e., notes were unspent at snapshot)
- Publish the note commitment tree root `nc_root` at the snapshot height. (Anchor height)
  - This enables the vote chain to verify that Merkle inclusion proofs in ZKP #1 are against the real main-chain tree (not a fabricated one)

**Step 0.3: Build vote_round_id**

We make `vote_round_id = Poseidon(snapshot_height, snapshot_blockhash_lo, snapshot_blockhash_hi, proposals_hash_lo, proposals_hash_hi, vote_end_time, nullifier_imt_root, nc_root)`, yielding a canonical 32-byte Pallas Fp value.

This will be used to make signatures and votes un-replayable across distinct polling sessions.

**Step 0.4: Publish Verification Keys**

- Publish the verification keys for ZKP #1 (delegation proof) and ZKP #2 (vote proof)
- These are generated once from the circuit definitions

**Step 0.5: Election Authority Keypair**

The election authority generates a keypair `(ea_sk, ea_pk)` where `ea_pk = ea_sk * G`. The public key `ea_pk` is published as a parameter of the voting round; `ea_sk` is kept secret by the election authority and is used only at tally time to decrypt the aggregate vote totals.

This key exists because each voting share is encrypted under `ea_pk` using an additively homomorphic El Gamal scheme (§3.4.1). Individual encrypted shares can be summed publicly, but only the election authority can decrypt the final aggregate — individual vote amounts are never revealed.

## Phase 1 (Keystone Signing)

This phase details how you build the keystone signature to authorize a voting hotkey to vote on your behalf. We have one tx that can authorize up to 4 notes on your behalf for voting. Lets call that one bundle. If you have more than 4 notes, then we need one keystone signature for every chunk of up to 4. If your number of notes is not a multiple of 4, we pad with "padded notes". See section 1.3.5 for how we make padded notes.

We write section 1.3 onwards and phase 2 in the context of a user has 1 bundle, because Zashi aims to maintain 4 notes on your behalf. However, if you have multiple bundles, you would run those steps for each bundle.

We require each bundle to have at least .125 ZEC .

### 1.1 Identify notes

Identify notes to yourself. There are two starting points:

- You have built a new wallet
- You are integrated into an existing wallet.

For a new wallet, you do standard shielded sync from a birthday height until snapshot height. I will not detail anything more.

Now for an existing wallet, how do you do it? We split this problem into two parts.

- Identify which notes you own at snapshot_height
- Create the inclusion proofs for these notes

### 1.1.1 Identify notes you own at snapshot_height

A wallet will have a set of notes maintained up to a height.

If your wallet contains tx history, as Zashi does, you can simply iterate through tx history to construct the set of notes you had at the snapshot height. (TODO: Verify Zashi stores spent note details)

## 1.2 Sample voting key (concurrent)

Randomly sample a voting hotkey. This step can occur concurrently with the 1.1. This hotkey as key material:

- If stolen, can steal your vote. However this is a detectable theft.
- If lost, prevents you from further voting.

We believe that the voting usecase makes this permissible to not be long term backed up, or derived from your long term mnemonic. This is because the intended design make this only needed during the first online session. It merely should be stored in the apps local storage, and not deleted.

It can be re-used across voting round id's, it will just get re-authorized each time.

### 1.3 Construct "Setup hotkey" tx

The "setup hotkey tx" will have the user sign over a valid "dummy action", that binds their voting amount, note commitments being delegated, and

**Decisions to be made:** What is better UX? Having a 1 zatoshi action with a descriptive but unchecked memo, or two actions. one 1 zatoshi action, and one showing the amount being delegated? (But the amount being delegated is an input thats not from you)

- We will make this decision via looking at it.

#### 1.3.1 Derive note nullifiers

Derive the nullifiers for every note, as in standard zcash. Namely:

```
 DeriveNullifier_nk(ρ, ψ, cm) =
      ExtractP( [( PRF^nf_nk(ρ) + ψ ) mod q] * NullifierBase  +  cm )
```

#### 1.3.2 Derive governance authorization nullifier

We derive a governance nullifier for each note being delegated. This follows the approach from voting scheme round 4: we reuse the standard Zcash nullifier derivation but with a governance-specific personalization string.

`gov_null = Poseidon_nk("governance authorization", voting_round_id, note_nullifier)`

This ensures each note produces a unique, deterministic nullifier for the governance context that is distinct from its on-chain nullifier. The gov nullifier is published as a public input so the vote chain can prevent double-delegation. Note that this does not leak privacy even when the `note_null` ultimately gets revealed, as the gov null derivation depends on `nk`.

TODO: Finalize personalization string.

#### 1.3.3 Construct vote authority note

We construct a single "vote authority note" (VAN), representing the vote authority for all our notes combined. The VAN binds together the voting hotkey, the voting weight, and the voting round, along with permission to vote on all proposal. This note will later be inserted into the voting commitment tree.

First we sample commitment randomness `van_comm_rand`. (TODO: Note how to derive this from governance voting hotkey.) The wallet must store this.

We also construct available proposals, as MAX_PROPOSAL_AUTHORITY which is `2^16 - 1 = 65535`

`vote_authority_note = Poseidon(DOMAIN_VAN, voting_hotkey_pk, total_note_value, voting_round_id, MAX_PROPOSAL_AUTHORITY, van_comm_rand)`

(TBD, we should be able to pack `total_note_value` and `MAX_PROPOSAL_AUTHORITY` into one field elem)

### 1.3.4 Construct dummy signed action input

We need to prove knowledge of the input note + its nullifier. Because we are sampling this "dummy" note, we use this to bind our signature to the unique governance details we are working with. (ensuring this unusable in other votes, or on mainnet)

#### 1.3.4.1 Constraining rho (and thus nullifier)

The nullifier is signed over, so we will constrain the nullifier to be bound to our unique governance scope (Namely these notes, and this `voting_round_id`)

We cannot alter the derivation for the nullifier we sign over, it must follow the standard ZCash flow, namely:

```
 DeriveNullifier_nk(ρ, ψ, cm) =
      ExtractP( [( PRF^nf_nk(ρ) + ψ ) mod q] * NullifierBase  +  cm )
```

However, because it is a dummy note, we can arbitrarily choose `rho, psi`. As in ZCash, we leave `psi` fully random. Thus we constrain `rho`, this is similar in spirit to Quantum Recoverable Orchard.

We set `rho = Poseidon(note_comm_1, note_comm_2, note_comm_3, note_comm_4, van_cmx, vote_round_id)`, thus committing to each input we need. Here `van_cmx` is the commitment to the VAN (i.e. the Poseidon hash output from §1.3.3).

#### 1.3.4.2 Constructing other inputs + Note commitment

A note commitment is defined as

```
g_d := DiversifyHash(d)
cm = NoteCommit_rcm( repr(g_d), repr(pk_d), v, ρ, ψ )
```

So we define `rho` as above. We pick a random diversifier `d`, and then construct `g_d, pk_d` accordingly from our main pubkey. We set `v` to be 0. We choose `psi` at random.

### 1.3.5 Padded notes

If the number of real notes in a bundle is less than 4, we pad to 4 with "padded notes." Each padded note is a dummy constructed as follows:

- Choose `rho_i` , `rcm_i` and `psi_i` at random
- Choose a random diversifier `d_i`, derive `g_d_i` and `pk_d_i` from our key material (`ivk`)
- Set `v_i = 0`
- Compute `cm_i = NoteCommit_rcm_i(repr(g_d_i), repr(pk_d_i), 0, rho_i, psi_i)` as usual
- Derive `real_nf_i = DeriveNullifier_nk(rho_i, psi_i, cm_i)` — this is a random-looking nullifier that won't collide with any real on-chain nullifier
- Derive `gov_null_i = Poseidon_nk("governance authorization", voting_round_id, real_nf_i)` — still derived correctly so the circuit is uniform across all 4 slots

Each note slot in the circuit carries an `is_note_real_flag_i` (a private witness bit). When `is_note_real_flag_i = 0` (padded):

- `v_i` must be 0 (enforced in-circuit)
- The Merkle path validity check against `nc_root` is **skipped** (the padded note is not on-chain)
- The IMT non-membership check is still performed (the random nullifier won't be in the IMT, and this keeps the circuit uniform)
- The note commitment integrity, diversified address integrity, nullifier derivation, and gov nullifier derivation are all still checked (uniform circuit, no special-casing)

The `cmx_i` for padded notes is still hashed into `rho_signed` (condition 3). This is fine — it just means the rho commits to both real and padded note commitments.

### 1.3.6 Constructing the Action

We are now ready to construct the actions we sign over.

- The input is as defined above. What a keystone user sees is a 0 shielded value note tagged as "self" being spent.
- An output whose address is the delegation hotkey, also with 0 value
  - Memo is a sentence explaining the vote. e.g.
    - "I am going to be using this output's address for my vote using 100 ZEC. I will choose my votes using this hotkey"

### 1.3.7 A note on keystone verifiability

Inside of the keystone, you will see a memo explaining over what you are signing. And we hash this memo. Because the memo is hashed with blake2b, it is too hard to prove over for normal userflow. So we have the memo encode a user friendly description of what they are authorizing.

In the base proposal, we suggest that wallets give full flexibility of how you vote to the hotkey. However we can make an extension for power users, that restricts the hotkey's vote choice. Then in the event that the hotkey that should have been restricted (via ZKP). We view this outside of base scope, but can detail how to extend this into the `vote_authority_note` if time permits.

### 1.4 Sign vote tx

Sign vote tx is blocked on 1.3.

You pass it to Keystone. (Insert image of what this looks like)

## Phase 2 (ZKP 1)

### Intuition

Here we create the Delegation ZKP. There is two parts to this:

- gather required witness data to make the proof
- Do the ZKP

First we should understand intuitively what is this ZKP guaranteeing. Then I will write the ZKP conditions referencing the ZCash spec.

Call the dummy note that is signed over, from now on the "signed_note".

ZKP intuition

Public inputs to this ZKP:

- `signed_note` nullifier
- `rk` for who signed the note.
- `vote_authority_note`
- `gov_null_1`, `gov_null_2`, ..., `gov_null_4`
- `nullifier_imt_root`
- `vote_round_id`
- `nc_root`

We could in principle make `nullifier_imt_root` and `vote_round_id` constants, we deem that not worth it though.

ZKP statements (intuition)

- The input note im signing over has a validly constructed note commitment and nullifier.
  - This nullifier's `rho` value is further constrained as in section 1.3.4.1
- The output note commitment commits to the address `gov_hotkey_addr`
- I know (up to) 4 valid, unspent notes at the snapshot height.
  - If note count is < 4, we ensure dummy notes for padding have random `gov_null` and `0 value`.
- The `ak` key that signed this action is also the `ak` that owns each of those notes
- The `rk` revealed is correct randomization of `ak`
- For each note, here is a validly constructed `gov_null`
- `vote_authority_note` is constructed correctly
  - Uses `gov_hotkey_addr`
  - has total value that is the sum of the 4 notes values, cast to "ballots"
	  - We cast to ballots by floor-dividing by 12_500_000, and the quotient is the number of ballots.

Out of circuit, we check the signature of the SIGHASH relative to `rk`.

### ZKP Statements

I will reference the exact name from the ZCash spec if its a re-used component, and use **bold** if its brand new or altered.

**Public inputs:**

- `signed_note_nullifier` — nullifier of the dummy signed note
- `rk` — randomized signature verification key
- `nc_root` — note commitment tree anchor at snapshot height
- `nullifier_imt_root` — IMT root of all on-chain nullifiers at snapshot height
- `vote_authority_note` — the initial VAN commitment (binds hotkey, weight, round)
- `gov_null_1, ..., gov_null_k` — governance nullifiers for each note (up to 4)
- `vote_round_id`
- `cmx_new` — the output note commitment (to the gov hotkey address)

**Witness (private) inputs:**

- `ak` — spend auth validating key (Pallas point)
- `nk` — nullifier deriving key
- `rivk` — CommitIvk randomness
- `alpha` — spend auth randomizer (for the signed action)
- `vpk` — voting hotkey public key as a tuple (`vpk_d`, `vpk_pk_d`)
- `van_comm_rand` - randomness for the VAN commitment
- For the signed (dummy) note:
  - `d_signed, pk_d_signed, rho_signed, psi_signed, rcm_signed, cm_signed`
  - (recall `v_signed = 0`)
- For the output note:
  - `d_new = vpk_d, pk_d_new = vpk_pk_d`, `v_new (= 0), rho_new, psi_new, rcm_new`
  - i.e. the output note's address is the voting hotkey address — these are the same variables, not separate witnesses
- For each note `i` (exactly 4 slots — real notes plus padded notes per §1.3.5):
  - `is_note_real_flag_i` — 1 if this is a real note, 0 if padded
  - `d_i, pk_d_i, v_i, rho_i, psi_i, rcm_i` — full note data
  - `cm_i` — the note commitment (Pallas point)
  - `merkle_path_i, position_i` — Merkle path in the note commitment tree (ignored if padded)
  - `real_nf_i` — the note's nullifier (real on-chain nullifier, or random-derived if padded)
  - `imt_proof_i` — non-membership proof for `real_nf_i` in the nullifier IMT

**ZKP conditions:**

Conditions on the signed input note:

1. (Signed Note Commitment Integrity) `NoteCommit_rcm_signed(repr(g_d_signed), repr(pk_d_signed), 0, rho_signed, psi_signed) = cm_signed` — the signed note's commitment is correctly constructed. **We do not give an option for cm_signed to be null** (Same as Orchard spec §5.4.7.3, aside from null condition)

2. (Nullifier Integrity) `signed_note_nullifier = DeriveNullifier_nk(rho_signed, psi_signed, cm_signed)` — the public nullifier is correctly derived. (Same as Orchard spec §5.4.7.3)

3. **(Rho Binding) (NEW)** `rho_signed = Poseidon(cmx_1, cmx_2, cmx_3, cmx_4, van_cmx, vote_round_id)` — the signed note's rho is bound to the exact notes being delegated, the VAN commitment, and the round. This is what makes the keystone signature non-replayable and scoped. (Per §1.3.4.1)

4. (Spend Authority) `rk = SpendAuthSig.RandomizePublic(alpha, ak)` i.e. `rk = ak + [alpha] * G` — the public `rk` is a valid rerandomization of `ak`. Links to the keystone signature verified out-of-circuit. (Same as Orchard spec §5.4.7.3)

5. (Diversified Address Integrity) `pk_d_signed = [ivk] * g_d_signed` where `ivk = CommitIvk(rivk)(ExtractP(ak), nk)`. Proves the signed note's address belongs to the same key material `(ak, nk)`. This is where `ivk` is established — it will be reused for every real note ownership check below. (Same as Orchard spec §5.4.7.3)

Conditions on the signed output note:

6. (New Note Commitment Integrity) `ExtractP(NoteCommit_rcm_new(repr(g_d_new), repr(pk_d_new), 0, rho_new, psi_new)) ∈ {cmx_new, ⊥}` where `rho_new = signed_note_nullifier mod q`. (Same as Orchard spec §5.4.7.3)
   Note to reader, d_new, pk_d_new, which the user sees in the keystone UI, is **what** is checked in this step. This is also committed to within the VAN. So these variables are the same as in `vpk`, used in `vote_authority_note`.

Global conditions (computed once, not per-note):

7. **(VAN Integrity) (NEW)** `vote_authority_note = Poseidon(DOMAIN_VAN, voting_hotkey_pk, total_note_value, voting_round_id, MAX_PROPOSAL_AUTHORITY, van_comm_rand)`. The initial VAN correctly encodes the voting hotkey, total delegated weight, and round. Computed once over all notes. (Per §1.3.3)

8. **(Total Ballots Integrity) (NEW)** `snapshot_balance = sum(v_i for all notes) Num_ballots = floor(snapshot_balance / 12,500,000); num_ballots > 0`
	1. This prevents dust-delegations from bloating the chain state.
	2. This is computed by showing `num_ballots` is a small bit-range number (exact number TBD)
	3. There exists a 24 bit number 'remainder', s.t. `num_ballots * 12_500_000 + remainder = snapshot_balance`
		1. 24 bits allows remainder to be greater than 12_500_000, but that would basically be lowering your num_ballots number. num_ballots is not in any governance nullifier, so would cause someone to short themself one ballot. 

Conditions on each note `i` (all 4 slots — real and padded). Each slot has a private `is_note_real_flag_i` bit (per §1.3.5):

9. (Old Note Commitment Integrity) `NoteCommit_rcm_i(repr(g_d_i), repr(pk_d_i), v_i, rho_i, psi_i) ∈ {cm_i, ⊥}` — the note data is consistent with the commitment. Checked for both real and padded notes (uniform circuit). (Same as Orchard)

10. (Merkle Path Validity) If `is_note_real_flag_i = 1`: `(merkle_path_i, position_i)` is a valid Merkle path of depth 32 from `ExtractP(cm_i)` to `nc_root`. Proves the note exists on-chain at the snapshot height. If `is_note_real_flag_i = 0`: this check is **skipped** (the padded note is not on-chain). To skip this, we ignore the final MT root check. (Adapted from Orchard to have the is_note_real_flag skip)

11. (Diversified Address Integrity) `pk_d_i = [ivk] * g_d_i` — reusing the same `ivk` computed in condition (5). Since `ivk` is already constrained, this is a single scalar mul check per note. Proves each note is owned by the same `(ak, nk)` as the signed note. (Same as Orchard)

12. (Real Nullifier Derivation) (**private, not revealed**) `real_nf_i = DeriveNullifier_nk(rho_i, psi_i, cm_i)`. We derive but do NOT publish the real nullifier — it stays private.

13. **(IMT Non-Membership) (NEW)** `real_nf_i` is NOT in the nullifier imt at `nullifier_imt_root`. This is an imt exclusion proof: the Merkle path for `real_nf_i` leads to an empty leaf. Proves note was unspent at snapshot height.

14. **(Gov Nullifier Integrity) (NEW)** `gov_null_i = Poseidon_nk("governance authorization", voting_round_id, real_nf_i)`. A governance-domain nullifier derived from `nk`, the voting round, and the note's real nullifier (from condition 13). Deterministic and unique per note per round. Published to prevent double-delegation. Does not leak privacy even when `real_nf_i` is later revealed on-chain, because the derivation depends on `nk`. (Per §1.3.2)

15. **(Padded notes have 0 value) (NEW)** If `is_note_real_flag_i = 1` (real note): `v_i` is constrained via item 10. If `is_note_real_flag_i = 0` (padded note): `v_i = 0`. This ensures padded notes contribute zero weight to the VAN (condition 7). (Per §1.3.5)

**Out-of-circuit checks:**

- Verify `spendAuthSig` is a valid signature under `rk` over `sighash`
- `nc_root` matches the published anchor for this round's snapshot height
- `nullifier_imt_root` matches the published imt root for this round
- None of the `gov_null_i` have been seen before (double-delegation check)
- `vote_round_id` matches an active round

### 2.1 Gather required data

#### 2.1.1 Gather the note inclusion proof

We need to get an inclusion proof for the note. See [this obsidian spec](greg/bridge-tree-zashi) for it

#### 2.1.2 Gather the nullifier exclusion proof

We do a series of PIR queries to our nullifier exclusion imt. We leave detailing this for later.
TODO: Make a server that can just give this value w/ no cryptography first

### 2.2 Create the witness

Halo2 should have a function call for this

### 2.3 Make the ZKP

Do the Halo2 call for this.

### 2.4 Submit to vote-chain

We now submit the vote on-chain. The data we post on-chain for establishing the vote hotkey.

- Standard signed action data (standard zcash)
  - `rk`, `sig`, `signed_note_nullifier`, `cmx_new`, `enc_memo`, `tx_fee`, `transmission_key`
- gov nullifiers: `gov_null_1`, `gov_null_2`, ..., `gov_null_4`
- `vote_authority_note`
- ZKP

### 2.5 Chain verifies

The vote chain verifies that:
* none of the nullifiers have been seen before. 
	* Every nullifier in the tx is unique
	* No nullifier in the tx has appeared on chain
* Then it verifies the signature
* finally it verifies the ZKP. 

If all three these pass, the transaction gets on-chain. Then:
* The nullifiers are added to the chain's state.
* The `vote_action_note` is added to the chain's `vote_commitment_tree`. 
The chain's spam resistance is predicated on ZKP verification being cheap.

### Phase 3: Create Vote Commitments (Hot Wallet)

After creating the Vote Authority Note (Phase 1-2), the voting hotkey can vote on proposals or delegate, without further keystone interaction.

In this phase there are two user branches. Voting, or delegating. We currently only specify Voting, but delegating follows naturally if you understand [Voting design](Voting/Voting_design)

### 3.1 Collect Vote Commitment Tree witness

We need to collect the witness for our VAN within the Vote Commitment Tree. Currently we plan to re-use the logic within the wallet for downloading every block and updating this witness in the same way.

(TODO: Be more specific)

We do this in the background.

### 3.2 User chooses a vote

The user sees all proposals and selects their vote choice for each. They can browse and deliberate — no votes are sent until they confirm. The UI shows their total eligible voting weight (e.g. "You have 99 ZEC eligible for voting") but does not reveal the number of splits or notes.

If a user final confirms their vote, they are locked in on it. No vote changing allowed. Then in the background, for that vote, step 3.2 onwards begins.

### 3.3 User confirmed, Vote split differential privacy chosen

For each vote chosen, the wallet will independently sample a random distribution of votes to use.

For now, we set it to the binary decomposition, until I later figure out how we do this. (TODO: Choose random distribution. Read differential privacy literature)

The wallet decomposes the user's total delegated weight into powers of 2 via binary decomposition. E.g. 99 ZEC → `1100011` → 64 + 32 + 2 + 1 = 4 vote transactions per proposal.

Each 1 bit here becomes a voting share. So above we would make 4 voting shares, `64, 32, 2, 1`.

The user does not need to be aware of how many splits they have.

TODO: Consider DP randomness

#### 3.3.1 A note on maximum vote shares

We reduce to **4 shares** and replace the Merkle tree with a single hash over 4 encrypted shares. Instead of proving a tree circuit, ZKP #2 now only needs to verify 4 hash preimage checks — this dramatically reduces proving cost.

Each share is encrypted as an El Gamal ciphertext under the election authority's public key `ea_pk` (see §3.4.1). The vote commitment hashes over these 4 encrypted shares rather than a Merkle tree root.

Large holders who need finer granularity can split across multiple delegations (§6.0).

TODO: We consider changing this to 32 leaves to give more splits. This is delayed due to scope creep. We first want to get everything else working, and delegation. Then we consider the Merkle tree.

### 3.4 Construct new Vote Commitment

**Current design (4 encrypted shares in a flat hash):**

The user organizes their voting shares into a vector of 4 entries. Each share `v_i` is encrypted under `ea_pk` as an El Gamal ciphertext `enc_share_i` (see §3.4.1). The shares hash is simply:

`shares_hash = H(enc_share_1, enc_share_2, enc_share_3, enc_share_4)`

The vote commitment is then:

`vc = H(DOMAIN_VC, voting_round_id, shares_hash, proposal_id, vote_decision)`

This is cheap to prove — just 4 hash preimage checks, no tree circuit.

**Future direction (Merkle tree):**

The approach below describes using a Merkle tree of blinded shares as a zero-knowledge vector commitment, which would allow more shares and finer granularity. We keep this as the planned future direction once delegation and the rest of the system are working.

The user would organize their voting shares into a vector of N entries. We associate a blinding factor with each entry. Then makes a merkle tree of N leaves, where each leaf is `voting_share || blinding factor`. The blinding factor per leaf is a standard trick to make a merkle tree a zero-knowledge vector commitment, preventing an adversary from brute forcing other components of the tree.

TODO: How do we derive blinding factor. Natural choice is local-only hash(nullifier_key, rseed, index)

TODO: Consider making this a 4 -> 1 tree to save on circuit prover time. (~40%)

### 3.4.1 Homomorphically encrypting the voting share

Each voting share `v_i` is encrypted under the election authority's public key `ea_pk` using additively homomorphic El Gamal encryption (see Appendix A for formal definitions).

For each share `v_i` (where `i` ranges from 1 to 4):

1. Sample fresh randomness `r_i`
2. Compute the El Gamal ciphertext: `enc_share_i = (r_i * G, v_i * G + r_i * ea_pk)`

The ciphertext is a pair of curve points `(C1, C2)`.

The key property is **additive homomorphism**: given `enc(a) = (r_a * G, a * G + r_a * ea_pk)` and `enc(b) = (r_b * G, b * G + r_b * ea_pk)`, component-wise point addition yields:

`enc(a) + enc(b) = ((r_a + r_b) * G, (a + b) * G + (r_a + r_b) * ea_pk) = enc(a + b)`

This means anyone can publicly sum encrypted shares without decrypting them — only the election authority can decrypt the aggregate at tally time.

### 3.5 Construct new VAN

We construct a new Vote Authority Note.

Recall `vote_authority_note_old = Poseidon(DOMAIN_VAN, voting_hotkey_pk, total_note_value, voting_round_id, proposal_authority, van_comm_rand)`

We take the index of the proposal we are voting on. We subtract proposal_authority accordingly to get `proposal_authority_new`. We keep all other fields the same.

`vote_authority_note_new = Poseidon(DOMAIN_VAN, voting_hotkey_pk, total_note_value, voting_round_id, proposal_authority_new, van_comm_rand)`

### 3.6 Construct VAN nullifier

We derive a nullifier for the old Vote Authority Note. Following the same pattern as §1.3.2 (governance authorization nullifiers), we use a Poseidon hash with a vote-specific personalization string, keyed by `vsk.nk` (a nullifier deriving key derived from the voting hotkey secret key).

`van_nullifier = Poseidon_vsk.nk("vote authority spend", voting_round_id, vote_authority_note_old)`

Where:

- `vsk.nk` — nullifier deriving key derived from the voting hotkey secret key
- `voting_round_id` — scopes the nullifier to this round
- `vote_authority_note_old` — the VAN commitment being spent

This is deterministic and unique per VAN per round. The vote chain tracks these to prevent double-spending the same VAN (i.e. voting on the same proposal twice with the same authority). The nullifier is published as a public input.

Note: unlike the governance nullifiers in §1.3.2 which use `nk` (the mainchain nullifier deriving key), here we use `vsk.nk` (derived from the voting hotkey). This is because the VAN lives on the vote chain and spending it requires hotkey authority, not mainchain spending authority.
(TODO: Male th)

### 3.7 Sign the tx

Sample randomizer `alpha_v`, compute `r_vpk = voting_hotkey_pk + [alpha_v] * G`. Sign the transaction under the randomized key: `voteAuthSig = SpendAuthSig.Sign(vsk + alpha_v, sighash)`. The verifier checks `voteAuthSig` against `r_vpk` (out-of-circuit). This is the same rerandomization pattern as the keystone signature in Phase 2 (`rk = ak + [alpha] * G`).

### 3.8 Construct Vote Proof (ZKP #2)

Make ZKP. Then proceed to Phase 4 (delegate the notes)

Intuition:

- We must prove that `vote_authority_note_old` is in the `vote_commitment_tree` at the specified `vote_comm_tree_anchor_height`
- `voting_key` did sign over this tx.
- We have correctly constructed nullifier X
- The new output has its proposal authority decremented correctly
- All other fields of the new output are done correctly.
- Vote commitment is constructed correctly.
  - Namely correct proposal ID
  - Sum of voting shares = total note value
    - Each voting share is in `[0, 2^24)`
    - Each share is validly encrypted under `ea_pk` (El Gamal)
    - shares hash is constructed correctly over 4 encrypted shares

#### ZKP #2 Statements

This ZKP proves that a registered voter is casting a valid vote, without revealing which VAN they hold. The structure follows ZKP #1's pattern (§Phase 2).

**Public inputs:**

- `van_nullifier` — nullifier of the old VAN being spent (prevents double-vote on same proposal)
- `r_vpk` — randomized voting public key (analogous to `rk` in ZKP #1)
- `vote_authority_note_new` — the new VAN with decremented proposal authority
- `vote_commitment` — `H(DOMAIN_VC, voting_round_id, shares_hash, proposal_id, vote_decision)`
- `vote_comm_tree_root` — root of the vote commitment tree at `vote_commitment_tree_anchor_height` (the tree of all registered VANs)
- `vote_commitment_tree_anchor_height` — the vote-chain height at which we snapshot the vote commitment tree
- `proposal_id` — which proposal this vote is for
- `voting_round_id`

**Witness (private) inputs:**

- `vote_decision` — the voter's choice (hidden inside the vote commitment)
- `vsk` — voting hotkey secret key
- `vsk.nk` — nullifier deriving key derived from `vsk`
- `voting_hotkey_pk` — the voting hotkey public key (derivable from `vsk`)
- `alpha_v` — spend auth randomizer for the voting hotkey (analogous to `alpha` in ZKP #1)
- `total_note_value` — the voter's total delegated weight
- `proposal_authority_old` — remaining proposal authority in the old VAN
- `proposal_authority_new` — decremented proposal authority in the new VAN
- `van_comm_rand` — commitment randomness for the VAN
- `vote_comm_tree_path, vote_comm_tree_position` — Merkle path proving VAN membership
- `vote_authority_note_old` — the old VAN commitment
- For the vote commitment:
  - `shares_1, ..., shares_4` — the voting share vector (each in `[0, 2^24)`)
  - `r_1, ..., r_4` — El Gamal encryption randomness per share

**ZKP conditions:**

As in ZKP #1, we reference Orchard spec names where reused and use **bold** for new conditions.

VAN ownership and spending:

1. **(VAN Membership)** `(vote_comm_tree_path, vote_comm_tree_position)` is a valid Merkle path from `vote_authority_note_old` to `vote_comm_tree_root`. Proves the voter's VAN is registered, without revealing which one. (Analogous to ZKP #1 cond. 10 — Merkle Path Validity)

2. **(VAN Integrity)** `vote_authority_note_old = Poseidon(DOMAIN_VAN, voting_hotkey_pk, total_note_value, voting_round_id, proposal_authority_old, van_comm_rand)`. The old VAN (pre-delegation governance commitment) is correctly constructed from its components. (Analogous to ZKP #1 cond. 9 — Old Note Commitment Integrity)

3. **(Spend Authority)** `r_vpk = voting_hotkey_pk + [alpha_v] * G`. The public `r_vpk` is a valid rerandomization of `voting_hotkey_pk`. Links to the vote signature verified out-of-circuit. (Same pattern as ZKP #1 cond. 4 — Spend Authority)

4. **(VAN Nullifier Integrity)** `van_nullifier = Poseidon_vsk.nk("vote authority spend", voting_round_id, vote_authority_note_old)`. The published nullifier is correctly derived from `vsk.nk`. Prevents double-spending. (Analogous to ZKP #1 cond. 2 — Nullifier Integrity, and §1.3.2's nullifier pattern, and §3.6)

New VAN construction:

5. **(Proposal Authority Decrement)** Decompose `proposal_authority_old` into 16 bits, assert bit `proposal_id` is set, then `proposal_authority_new = proposal_authority_old - (1 << proposal_id)`. Clears exactly the voted proposal's bit; all other authority bits unchanged.

6. **(New VAN Integrity)** `vote_authority_note_new = Poseidon(DOMAIN_VAN, voting_hotkey_pk, total_note_value, voting_round_id, proposal_authority_new, van_comm_rand)`. All fields are the same as the old VAN except `proposal_authority` is decremented. (Analogous to ZKP #1 cond. 6 — New Note Commitment Integrity)

Vote commitment construction:

7. **(Shares Sum Correctness)** `sum(shares_1, ..., shares_4) = total_note_value`. The voting shares decomposition is consistent with the total delegated weight committed in the VAN.

8. **(Shares Range)** Each `shares_j` is in `[0, 2^24)`. Prevents overflow and ensures each share fits in the designated bit-width. (See §3.3.1)

9. **(Shares Hash Integrity)** `shares_hash = H(enc_share_1, enc_share_2, enc_share_3, enc_share_4)`. The shares hash is correctly computed over the 4 encrypted shares. No Merkle tree — just a single hash over 4 El Gamal ciphertexts.

10. **(Encryption Integrity)** Each `enc_share_i = ElGamal(shares_i, r_i, ea_pk)`, i.e. `enc_share_i = (r_i * G, shares_i * G + r_i * ea_pk)`. Proves each ciphertext is a valid El Gamal encryption of the corresponding plaintext share under the election authority's public key. (See §3.4.1, Appendix A)

11. **(Vote Commitment Integrity)** `vote_commitment = H(DOMAIN_VC, voting_round_id, shares_hash, proposal_id, vote_decision)`. The public vote commitment is correctly constructed from the round context, shares hash, and vote choice.

**Out-of-circuit checks:**

- Verify `voteAuthSig` is a valid signature under `r_vpk` over `sighash`
- `van_nullifier` has not been seen before (double-vote check per proposal)
- `vote_comm_tree_root` matches the published vote commitment tree root at `vote_commitment_tree_anchor_height`
- `proposal_id` is valid and the proposal is in its voting window
- `voting_round_id` matches an active round

### 3.8 Submit vote commitment tx

We submit the vote on-chain. The data posted for each vote commitment:

- `van_nullifier` — prevents re-spending the same VAN
- `vote_authority_note_new` — the new VAN (inserted into the vote commitment tree)
- `vote_commitment` — the committed vote with shares
- `proposal_id`
- ZKP #2 proof

- `r_vpk` — randomized voting public key
- `voteAuthSig` — signature under randomized voting key over `sighash`

The signature under `r_vpk` authorizes the transaction (analogous to `spendAuthSig` under `rk` in ZKP #1). The rerandomization prevents linking multiple votes to the same hotkey.

### 3.9 Vote chain verifies

The vote chain in order checks that:
* verifies that the `van_nullifier` has not been seen before (double-vote check).
* Then it verifies the `voteAuthSig` under `r_vpk` 
* Then it verifies ZKP #2. 
If these pass, then the tx gets on-chain. 

The logic to process the tx is:
- `van_nullifier` is added to the chain's nullifier set (prevents reuse)
- `vote_authority_note_new` is added to the vote commitment tree (the voter can spend it again for the next proposal)
- `vote_commitment` is recorded for the proposal's tally
- The chain accumulates the voting shares for tallying after the voting window closes

As with Phase 2 (§2.5), the chain's spam resistance is predicated on ZKP verification being cheap.

### 4. Create Vote TX instructions

We need our clients to be able to have other actors be able to make ZKP's on their behalf, in order to get them submitted at timing delays. (TODO improve this description.)

This requires sending a payload `delegated_voting_share_payload` to a server containing:

- Vote commitment data (so they can open it up inside a ZKP)
  - `(voting_round_id, shares_hash, proposal_id, vote_decision)`
- Position in the tree the vote commitment was created at
- ONE encrypted share
  - `enc_share_i = (C1_i, C2_i)` — the El Gamal ciphertext
  - `share_index` — which of the 4 shares this is

## Phase 5: Construct Vote TX (Server-side)

In this phase, you are a server who has received a `delegated_voting_share_payload` from Phase 4. As this server, you follow the vote chain and maintain the full vote commitment tree — i.e. all `vote_commitment`s and their Merkle paths. You receive ONE share per payload, and construct one vote reveal transaction per share.

The purpose of delegation to a server is temporal unlinkability. If the client submitted all shares itself, an observer could link them by timing. The server staggers share submissions across the voting window, breaking this correlation.

### 5.1 Randomly delay inbound tx

**Timing:** The server should stagger share submissions across the voting window. All shares from the same voter should NOT be submitted in a burst. The server will delay shares from many voters and submit them at randomized intervals. The exact scheduling strategy is out of scope for this document but is critical for temporal unlinkability.

### 5.2 Collect Vote Commitment Tree witness

The server maintains the full vote commitment tree (the same tree from §3.1). Upon receiving a `delegated_voting_share_payload`, the server looks up the vote commitment at the position specified in the payload and obtains its current Merkle path to the vote commitment tree root.

If the tree has grown since the payload was created, the server uses the latest anchor height for which it has a valid root.

(TODO: Coordinate anchor height policy — does the server always use the latest, or does the client specify?)

### 5.3 Derive share nullifier

We need a nullifier per share to prevent double-counting. The server derives:

`share_nullifier = H("share spend", vote_commitment, share_index, enc_share_i)`

Where:

- `vote_commitment` — the VC this share belongs to (known from the payload)
- `share_index` — which of the 4 shares (0..3)
- `enc_share_i` — the El Gamal ciphertext for this share; makes the nullifier infeasible to link back to a specific vote commitment without knowledge of the ciphertext

This is deterministic and unique per share per vote commitment. The server has all required inputs from the payload.

### 5.4 Construct Vote Reveal Proof (ZKP #3)

#### Intuition

This ZKP opens a single encrypted share from a registered vote commitment. Concretely:

- We prove a valid `vote_commitment` exists in the vote commitment tree — without revealing which one
- We reveal the El Gamal ciphertext `enc_share_i` — NOT the plaintext vote amount
- We reveal the `proposal_id` and `vote_decision` that were committed to
- We prove the ciphertext is one of the 4 committed in the shares hash
- We prove the share nullifier is correctly derived

The privacy property: the server reveals `(enc_share, proposal_id, decision)` but not _which_ vote commitment the share came from, nor the plaintext amount. The chain accumulates ciphertexts homomorphically — only the election authority can decrypt the aggregate at tally time.

#### ZKP #3 Statements

**Public inputs:**

- `share_nullifier` — prevents double-counting the same share
- `enc_share` — the El Gamal ciphertext `(C1, C2)` for this share (revealed for homomorphic accumulation — NOT the plaintext amount)
- `proposal_id` — which proposal this vote is for
- `vote_decision` — the voter's choice (revealed for tallying)
- `vote_comm_tree_root` — root of the vote commitment tree at the anchor height
- `vote_comm_tree_anchor_height` — the vote-chain height for the tree snapshot
- `voting_round_id`

**Witness (private) inputs:**

- `vote_commitment` — the vote commitment being opened (hidden — proven in-tree but not revealed)
- `vote_comm_tree_path, vote_comm_tree_position` — Merkle path proving VC membership
- `shares_hash` — hash of the 4 encrypted shares inside the vote commitment
- `share_index` — which of the 4 shares is being opened (0..3)
- `enc_share_1, ..., enc_share_4` — all 4 El Gamal ciphertexts (to recompute `shares_hash`)

**ZKP conditions:**

Vote commitment membership:

1. **(VC Membership)** `(vote_comm_tree_path, vote_comm_tree_position)` is a valid Merkle path from `vote_commitment` to `vote_comm_tree_root`. Proves the vote commitment is registered on the vote chain, without revealing which one. (Analogous to ZKP #2 cond. 1 — VAN Membership)

2. **(Vote Commitment Integrity)** `vote_commitment = H(DOMAIN_VC, voting_round_id, shares_hash, proposal_id, vote_decision)`. Opens the vote commitment, proving the public `proposal_id` and `vote_decision` match what was committed in Phase 3 and binding the commitment to the round. (Same structure as ZKP #2 cond. 12 — Vote Commitment Integrity)

Share opening:

3. **(Shares Hash Integrity)** `shares_hash = H(enc_share_1, enc_share_2, enc_share_3, enc_share_4)`. Recomputes the shares hash from the 4 encrypted shares in the witness.

4. **(Share Membership)** `enc_share = enc_share_{share_index}`. The publicly revealed ciphertext matches one of the 4 encrypted shares committed in the shares hash, at the position indicated by `share_index`.

Nullifier:

5. **(Share Nullifier Integrity)** `share_nullifier = H("share spend", vote_commitment, share_index, enc_share)`. Correctly derived, unique per share per vote commitment. The `enc_share` ciphertext prevents brute-force linking. (Analogous to ZKP #2 cond. 4 — VAN Nullifier Integrity, and §3.6's nullifier pattern)

**Out-of-circuit checks:**

- `share_nullifier` has not been seen before (double-count check)
- `vote_comm_tree_root` matches the published vote commitment tree root at `vote_comm_tree_anchor_height`
- `proposal_id` is valid for the current voting session
- `voting_round_id` matches an active round

### 5.5 Submit vote share tx

The server submits the vote share to the vote chain. Data posted:

- `share_nullifier` — prevents re-counting the same share
- `enc_share` — the El Gamal ciphertext `(C1, C2)` for this share (for homomorphic accumulation)
- `proposal_id`
- `vote_decision`
- `voting_round_id`
- ZKP #3 proof

No signature is needed — the ZKP proof itself serves as authorization (it proves the share came from a valid, registered vote commitment). The `share_nullifier` prevents replay.

**Timing:** The server should stagger share submissions across the voting window. All shares from the same voter should NOT be submitted in a burst. The server may mix shares from many voters and submit them at randomized intervals. The exact scheduling strategy is out of scope for this document but is critical for temporal unlinkability.

### 5.6 Vote chain verifies

The vote chain verifies that `share_nullifier` has not been seen before, then verifies ZKP #3. If both pass:

- `share_nullifier` is added to the chain's nullifier set (prevents reuse)
- `enc_share` is accumulated homomorphically into the tally for `(proposal_id, vote_decision)` — the chain performs component-wise point addition of El Gamal ciphertexts: `(sum(C1_i), sum(C2_i))`
- After the voting window closes, the election authority decrypts the aggregate ciphertext to determine the final result (see Appendix B: Tally)

As with Phases 2 and 3 (§2.5, §3.9), the chain's spam resistance is predicated on ZKP verification being cheap.

## Appendix A: Homomorphic Value Commitments (El Gamal)

**El Gamal encryption** over the Pallas curve. Let `G` be the generator of the prime-order subgroup, and let `R = ea_pk = ea_sk * G` be the election authority's public key.

**Encryption:** To encrypt a value `v` with randomness `r`:

`Enc(v, r) = (r * G, v * G + r * R)`

The ciphertext is a pair of Pallas points `(C1, C2)`.

**IMPORTANT:** `G` must be the generator of the prime-order subgroup. Using an arbitrary point would break the homomorphic property and security guarantees.

**Additive homomorphism:** Given two ciphertexts `Enc(a, r1)` and `Enc(b, r2)`, component-wise point addition yields a valid encryption of the sum:

`Enc(a, r1) + Enc(b, r2) = (r1 * G + r2 * G, a * G + r1 * R + b * G + r2 * R) = ((r1 + r2) * G, (a + b) * G + (r1 + r2) * R) = Enc(a + b, r1 + r2)`

This allows anyone to publicly sum encrypted vote shares without decryption.

**Decryption:** Given a ciphertext `(C1, C2)` and the secret key `ea_sk`:

`C2 - ea_sk * C1 = v * G + r * R - ea_sk * r * G = v * G + r * ea_sk * G - ea_sk * r * G = v * G`

This yields `v * G`, not `v` directly. To recover `v`, we perform a bounded discrete log lookup. This is feasible because the total vote weight is bounded (total ZEC supply is ~21M, so `v` is at most ~2.1 \* 10^15 zatoshi). In practice, for aggregate tallies, the value is small enough for baby-step giant-step or a precomputed lookup table.

## Appendix B: Tally

After the voting window closes, the tally proceeds as follows:

**Step 1: Public aggregation.** For each `(proposal_id, decision)` pair, everyone can publicly sum all submitted `enc_share` ciphertexts by component-wise point addition:

`agg_ciphertext = (sum(C1_i), sum(C2_i))`

This is fully verifiable — anyone can replay the addition from on-chain data and confirm the aggregate.

**Step 2: Election authority decrypts.** The election authority (tallier) decrypts the aggregate ciphertext using `ea_sk`:

`sum(C2) - ea_sk * sum(C1) = total_value * G`

Then recovers `total_value` via bounded discrete log lookup (feasible because the total vote weight is bounded by ZEC supply).

**Step 3: Publish result with proof.** The tallier publishes `total_value` along with a **proof of correct decryption** — a discrete log equality proof (Chaum-Pedersen protocol). This proves that the same `ea_sk` used to generate `ea_pk` was used to decrypt, without revealing `ea_sk`. Anyone can verify this proof against the published `ea_pk` and the on-chain aggregate ciphertext.

(TODO: Detail this proof, maybe a ZKP is quicker)

**Privacy guarantee:** Individual vote amounts are never revealed — only the aggregate total per `(proposal_id, decision)`.

### 6.0 (Optional) Delegation

### 7.0 (Optional) BG schedule
