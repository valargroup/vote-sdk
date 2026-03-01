# TSS EA Key Ceremony

This document describes the threshold secret sharing (TSS) upgrade to the EA key ceremony. The ceremony establishes the election authority public key `ea_pk` for each voting round. TSS prevents any single non-dealer validator from decrypting individual votes — only the aggregate tally is recoverable, and only with cooperation from at least `t` validators.

## Background: Legacy Ceremony

In the original (legacy) ceremony the block proposer generates `ea_sk` in memory, ECIES-encrypts the full key to every ceremony validator, and publishes `ea_pk = ea_sk * G`. Every validator who acks receives and stores the full `ea_sk`. Any single validator can decrypt all votes.

TSS replaces the single-key distribution with Shamir secret sharing. The full key never leaves the dealer's memory after the ceremony. No non-dealer validator can decrypt on their own.

## Step 1 (current): Threshold Secret Sharing

**Trust model:** trust dealer + trust validators. The dealer generates the polynomial and knows `ea_sk`. Any `t` validators acting together can reconstruct `ea_sk * C1`, but fewer than `t` learn nothing.

### Threshold value

For a ceremony with `n` validators:

```
t = ceil(n/3) + 1    (for n >= 2)
t = 0                (legacy mode for n < 2)
```

`t` is clamped to `n` for very small validator sets. `t = 0` signals legacy single-key mode.

| n | t |
|---|---|
| 1 | 0 (legacy) |
| 2 | 2 |
| 3 | 2 |
| 4 | 3 |
| 6 | 3 |
| 9 | 4 |

### Ceremony state machine

The ceremony state machine is unchanged at the structural level. The new fields on `VoteRound` carry the TSS data:

```
PENDING (REGISTERING) ──[auto-deal]──> PENDING (DEALT) ──[auto-ack ×n]──> ACTIVE
```

New `VoteRound` fields:

| Field | Type | Description |
|---|---|---|
| `threshold` | `uint32` | Minimum shares required to reconstruct (`t`). `0` = legacy mode. |
| `verification_keys` | `repeated bytes` | `VK_i = f(i)*G` per validator (32-byte compressed Pallas points, parallel to `ceremony_validators`). |

### Deal phase (`PrepareProposal` — auto-deal)

When a block proposer detects a PENDING round in REGISTERING status and is a ceremony validator:

1. Generate a fresh `ea_sk` and `ea_pk = ea_sk * G`.
2. Compute `t = ceil(n/3) + 1`.
3. Build a degree-`(t-1)` polynomial `f(x)` over Pallas Fq with `f(0) = ea_sk`:
   ```
   f(x) = ea_sk + a_1*x + a_2*x^2 + ... + a_{t-1}*x^{t-1}
   ```
   where `a_1 ... a_{t-1}` are uniformly random scalars.
4. Evaluate shares: `share_i = f(i)` for `i = 1..n`. Polynomial coefficients are zeroed after use.
5. Compute verification keys: `VK_i = share_i * G` (one compressed Pallas point per validator).
6. ECIES-encrypt `share_i` to `pk_i` (each validator's registered Pallas key) to produce `payload_i`.
7. Inject `MsgDealExecutiveAuthorityKey` containing:
   - `ea_pk` — the public key corresponding to `ea_sk = f(0)`
   - `payloads` — one ECIES envelope per validator
   - `threshold` — the value `t`
   - `verification_keys` — `VK_1 ... VK_n`

In **legacy mode** (`n < 2`, `t = 0`): ECIES-encrypts the full `ea_sk` to the single validator as before. `threshold` and `verification_keys` are zero/empty.

### Ack phase (`PrepareProposal` — auto-ack)

When a block proposer detects a PENDING round in DEALT status and has not yet acked:

1. Find and decrypt the proposer's ECIES payload to recover the secret bytes.
2. Parse the secret bytes as a Pallas scalar.
3. **Threshold mode** (`round.Threshold > 0`):
   - Find the validator's index `i` in `ceremony_validators`.
   - Compute `s * G` and compare with `round.VerificationKeys[i]`.
   - Reject (skip ack) if `s * G != VK_i` — the dealer sent an inconsistent share.
   - Write the 32-byte share scalar to `<ea_sk_dir>/share.<hex(round_id)>`.
4. **Legacy mode** (`round.Threshold == 0`):
   - Verify `s * G == ea_pk`.
   - Reject if mismatch.
   - Write the 32-byte `ea_sk` scalar to `<ea_sk_dir>/ea_sk.<hex(round_id)>`.
5. Compute `ack_signature = SHA256("ack" || ea_pk || validator_address)`.
6. Inject `MsgAckExecutiveAuthorityKey`.

The dealer acks through the same flow as every other validator — the deal handler does not write any key material to disk. The dealer's share is written when they are next the block proposer after DEALT status is set.

### On-disk key files

| Mode | File | Contents |
|---|---|---|
| Threshold | `share.<hex(round_id)>` | 32-byte Pallas Fq scalar `f(i)` — the validator's Shamir share |
| Legacy | `ea_sk.<hex(round_id)>` | 32-byte Pallas Fq scalar `ea_sk` — the full election authority key |

Both files are written mode `0600`. The tally injector reads whichever file is present for a given round.

### ECIES encryption scheme

The same scheme is used in both modes. The generator `G` is SpendAuthG (Orchard's `spend_auth_g`), shared with the ElGamal encryption used for votes.

```
E   = e * G                        (ephemeral public key, fresh per payload)
S   = e * pk_i                     (ECDH shared secret)
k   = SHA256(E_compressed || S.x)  (32-byte symmetric key)
ct  = ChaCha20-Poly1305(k, nonce=0, plaintext)
```

The plaintext is `share_i.Bytes()` (32 bytes) in threshold mode, or `ea_sk.Bytes()` (32 bytes) in legacy mode.

### Security properties

| Property | Legacy | Step 1 (TSS) |
|---|---|---|
| Who knows `ea_sk` | Every validator who acked | Dealer only (in memory, during deal block) |
| Single non-dealer can decrypt | Yes | No |
| Malicious validator can sabotage tally | N/A | Yes (no proof of correct share, fixed in Step 2) |
| Malicious dealer can send bad shares | N/A | Yes (no polynomial consistency check, fixed in Step 3) |

## Roadmap

### Step 2: DLEQ proofs (correctness vs. validators)

Add a non-interactive zero-knowledge proof to each `PartialDecryptionEntry` proving that the validator used the same scalar for their verification key and their partial decryption:

```
DLEQ: log_G(VK_i) == log_{C1}(D_i)
```

The chain verifies the proof before storing the partial decryption. A malicious validator with a fake share cannot forge a valid proof against their published `VK_i`.

New field: `dleq_proof bytes` in `PartialDecryptionEntry` (currently reserved/empty in Step 1).

### Step 3: Feldman commitments (correctness vs. dealer)

Replace the per-validator `verification_keys` list with `t` **Feldman polynomial commitments**:

```
C_j = a_j * G    for j = 0..t-1
```

Validators verify their share satisfies:
```
share_i * G == sum(C_j * i^j)    for j = 0..t-1
```

This proves consistency across shares — a malicious dealer cannot send conflicting shares to different validators without being detected at ack time.

`ea_pk` becomes derivable as `C_0` (the constant term commitment), so it no longer needs to be published separately.

### Step 4: Pedersen DKG (eliminates the dealer)

Replace the single-dealer model with a full distributed key generation protocol. Each validator generates their own polynomial, publishes Feldman commitments, and distributes encrypted shares to all other validators. The combined public key is the sum of all `C_{i,0}` terms; no single party ever knows `ea_sk`.

```
REGISTERING → COMMITTING → SHARING → CONFIRMED
```

The tally pipeline (partial decryptions, Lagrange interpolation, BSGS) is identical to Steps 1–3.
