# Distributed Key Generation (DKG) El-Gamal Key Ceremony

## Step-by-Step Flow

1. Before the ceremony, each validator registers their ECIES Pallas public key via `MsgRegisterPallasKey`

2. Vote manager creates a round via `MsgCreateVotingSession`

3. Register DKG contributions, as each validator becomes block proposer: 
   * Creates a random secret `s_A` and polynomial `f_A(x)` via `shamir.Split(s_A, t, n)`
   * Computes Feldman commitments `C_{A,k} = a_{A,j} * G` via `FeldmanCommit`
   * Saves the polynomial coefficients to disk
   * ECIES-ecnryptes for reach validator i other than itself, ECIES encrypts f_A(i) to validator i's Pallas public key.
   * Injects `MsgContributeDKG` via `PrepareProposals`, containing the commitment vector + encrypted payloads.

4. Once the last validator submits their DKG contribution:
   * Commitments are combined via `CombineCommitment`
   * Ceremony transitions to `DEALT` phase.

5. Reconstruct share and ack, as each validator becomes block proposer:
   * Let `j` be current validator index
   * In `PrepareProposal`, load validator `j`'s coefficients from disk
   * Compute own partial `own_partial = f_j(shamirIndex_j)`
   * For each **other** contributor's `DKGContribution`:
      * Find the ECIES envelope addressed to A
      * Decrypt with A's Pallas SK -> for each other validator i and my index j, get `f_i(shamirIndex_j)`
   * Compute combines share:
      * `combined_share_j = f_j(index_j) + for each other validator i, f_i(index_j)`
   * Write `combines_share_j` to disk
   * Inject `MsgAckExecutiveAuthorityKey`

## State Machine

### Key Generation

```
REGISTERING ──[n × MsgContributeDKG]──> DEALT ──[n × MsgAck]──> CONFIRMED ──> ACTIVE
```

### Round Completion and Tallying

```
ACTIVE ──[VoteEndTime expires]──> TALLYING ──[≥t × MsgSubmitPartialDecryption + MsgSubmitTally]──> FINALIZED
                                      │
                                      └──[timeout, < t partials]──> FINALIZED (tally_timed_out)
```

- **ACTIVE**: voters submit `MsgRevealShare`; encrypted votes accumulate via homomorphic addition.
- **TALLYING**: each ceremony validator auto-injects `MsgSubmitPartialDecryption` (`D_i = share_i × C1` + DLEQ proof) when they propose a block. Once `≥ t` partials exist, the next proposer combines them via Lagrange interpolation in the exponent, solves the discrete log (BSGS), and injects `MsgSubmitTally` → FINALIZED.
- **Timeout**: if fewer than `t` partials arrive before the tally deadline, EndBlocker force-finalizes with `tally_timed_out = true`.

## Feldman Commitments

### Setup

Input: polynomial coefficients

Produce: `for each j = 0..n-1, C_j = a_j * G` where n is the number of coefficients.

These commitments are broadcast publicly. They revelea nothing about the actual coefficients because of the discrete log hardness.

### Verification

Input: 

## FAQ

- What do Feldman commitments protect against?
   * Dishonest share contributor sending **incorrect shares** to other validators.

- Why does every validator create their own polynomial in Joint DKG Shamir TSS?
   * In single-dealer Shamir TSS, the dealer creates a secret polynomial, computes shares and distributes them across participants.
   * In Joint DKG, every validator becomes a dealer simultaneously:
      * Each computes a secret `s_i`, builds their own degree `t-1` polynomial `f_i(x)` where `f_i(0) = s_i`
      * Sends share `f_i(j)` to every other validator `j` (encrypted)
      * Publishes Feldman commitments for their polynomial
   * The combined secret becomes `S = s_1 + s_2 + ... + s_n`
      * No single validator knows `S`
      * Any `t` validators can reconstruct `S` (or perform threshold operations without materializing it) using their combined shares `combined_share_j = sum_i f_i(j)`
      * Recall, given t points (x_1, y_1), ..., (x_t, y_t) on a degree t-1 polynomial, Lagrange interpolation reconstructs f(x) for any x.

- What if more than t participants contribute their share?
   * This is expected and, in fact, happens in most cases. Over-contribution just makes it such that we have more points on the polynomial but that does not change the fact that we can rederive it. Lagrange interpolation evaluations the polynomial at `f(0)`. That's exactly where we assume the secret to be.

- What happens if at least one participant sends a corrupted share? How does the protocol make progress?

Feldman verification at ack time, per-validator. Ack fails, resulting in a liveness issue.

We could not naively skip the offending validator because they could also send valid shares to some but wrong to others. In that case, the accepted sets of validators would not match.
The correct way to handle would be:
j sends bad shares to k validators, good to the rest:
k validators ack with {skip j}, the other n-k ack with {skip nobody}
If k < n/2: majority says no skip, strip the k who disagreed. Remaining: n-k > n/2 >= t. Confirms.
If k >= n/2: majority says skip j, strip the n-k who disagree. Remaining: k >= n/2 >= t. Confirms (without j).
If k = n-1 (bad to everyone): unanimous skip j. Confirms trivially.
Similar in-protocol detection of the violator +  jailing.

-  `VerifyFeldmanShare` fails and `PrepareProposalHandler` does not inject an ack tx.

- When does a tally become available for recomputation by a public observer?
   * Technically, `MsgSubmitTally` is not necessary for the tally to be recomputable. As long as t validators have submitted their partial decryptions, anyone can already rebuild the tally from state. `MsgSubmitTally` is mainly an on-chain attestation and state transition. `MsgSubmitPartialDecryption` are the ones providing the partial decryptions and triggerred by the `PrepareProposal` determining `VoteEndTime` expiry.
   


## TODO

- What if one validator is down during the `REGISTERING` phase?
   * Current DKG may hang. Need to design a mechanism around this.



