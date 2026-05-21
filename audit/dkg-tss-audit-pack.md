# DKG / TSS AI Audit Pack

> **Purpose.** A single self-contained brief to feed into an AI auditor
> (Claude / GPT / Gemini) so it can review the vote-sdk Distributed Key
> Generation (DKG) and threshold-decryption ("TSS") stack with the right
> threat model, file pointers, and known-attack catalogue.
>
> **Motivation.** Triggered by ZCA-539 ("Recheck DKG code due to Thorchain
> bug"). Thorchain's TSS uses `bnb-chain/tss-lib` (GG18/GG20 threshold
> ECDSA over secp256k1). **vote-sdk does not use any of that stack.** The
> bug classes that hit Thorchain (Paillier modulus validation, GG20 ECDSA
> nonce-share leakage, etc.) **do not apply directly**, but the underlying
> *protocol family* — Joint-Feldman DKG + Shamir threshold — has its own
> well-known attack surface that must be reviewed independently.
>
> **Scope.** Election Authority (EA) key ceremony, threshold ElGamal
> partial decryption, and the on-chain state machine that drives them.
> *Not* in scope: vote ZK circuits (Halo2 / Orchard), validator
> consensus signing (Cosmos secp256k1), or the wallet/PIR stack.

---

## 1. Executive summary of the stack under audit

| Layer | Concretely | Where |
|-------|-----------|-------|
| DKG | **Joint-Feldman** (Gennaro et al. style — sum of per-contributor Shamir polynomials, Feldman commitments, no Pedersen commit phase) | `app/prepare_proposal_ceremony.go`, `x/vote/keeper/msg_server_ceremony.go`, `crypto/shamir/` |
| Secret sharing | **Shamir** `(t, n)` over the **Pallas scalar field Fq** | `crypto/shamir/shamir.go` |
| Commitments | **Feldman VSS** on Pallas (`C_{i,j} = a_{i,j} * G`) | `crypto/shamir/feldman.go` |
| Share transport | **ECIES** on Pallas (ephemeral-static ECDH → ChaCha20-Poly1305 AEAD) — *no signature on payload, only proposer-of-block authorization* | `crypto/ecies/ecies.go` |
| Threshold op | **Threshold ElGamal partial decryption** with Lagrange-in-the-exponent + Baby-Step Giant-Step | `crypto/elgamal/`, `app/prepare_proposal_partial_decrypt.go`, `x/vote/keeper/msg_server_tally_decrypt.go` |
| Correctness proof | **Chaum–Pedersen DLEQ** on Pallas (for `D_i = share_i · C1`) | `crypto/elgamal/dleq.go` |
| Transport | Messages ride **CometBFT blocks**; only the **block proposer** may inject ceremony txs via `PrepareProposal`; peers re-validate in `ProcessProposal` | `app/prepare_proposal*.go`, `app/process_proposal.go`, `app/ante_ceremony.go` |
| Persistence | **Disk**: `coeffs.<roundID>` (polynomial, mode 0600, zeroed+deleted on success), `share.<roundID>` (combined Shamir share, mode 0600). **On-chain KV**: per-validator commitments + ECIES payloads, combined `ea_pk`, combined Feldman commitments, threshold, partial decryptions. | `app/prepare_proposal_ceremony.go` `coeffsPathForRound` / `sharePathForRound`; KV in `x/vote/keeper/` |

**Key parameters.**

- **Curve:** Pallas (Pasta) via `github.com/mikelodder7/curvey v1.1.1`.
- **Tally threshold formula:** `ThresholdForN(n)` in
  `x/vote/keeper/keeper_ceremony.go:53` — `t = max(2, ceil(n/2))` for
  `n ≥ 2`; degenerates to `t = 1` for `n = 1`.
- **Ceremony quorum formula:** bounded-subset finalization uses a quorum
  separate from `t`:
  ```
  f(n) = floor((n - t(n)) / 2)
  x(n) = y(n) = f(n)
  required(n) = max(t(n) + x(n), n - y(n))
  ```
  This gives the invariant `required(n) - f(n) >= t(n)`, so one adversarial
  pool of size `f(n)` can be spent either before activation (non-contribution /
  non-ack) or after activation (partial-decryption withholding) without
  dropping below the tally threshold.
- **Quorum gate:** `min_ceremony_validators` (genesis param) before a round
  enters `REGISTERING` (`x/vote/keeper/keeper_min_validators.go`).
- **Ceremony state machine:**
  `REGISTERING → DEALT → CONFIRMED → ACTIVE → TALLYING → COMPLETE`
  driven by `EndBlock` timeouts + `MsgContributeDKG` / `MsgAck…` counts. The
  all-honest fast path still uses all `n` contributors/ackers; timeout paths
  may strip non-participants only when the remaining subset meets
  `required(n)`.

| n | t(n) | f=x=y | required(n) | required - t |
|---|------|-------|-------------|--------------|
| 1 | 1 | 0 | 1 | 0 |
| 2 | 2 | 0 | 2 | 0 |
| 3 | 2 | 0 | 3 | 1 |
| 4 | 2 | 1 | 3 | 1 |
| 5 | 3 | 1 | 4 | 1 |
| 6 | 3 | 1 | 5 | 2 |
| 7 | 4 | 1 | 6 | 2 |
| 8 | 4 | 2 | 6 | 2 |
| 9 | 5 | 2 | 7 | 2 |
| 10 | 5 | 2 | 8 | 3 |
| 11 | 6 | 2 | 9 | 3 |
| 12 | 6 | 3 | 9 | 3 |
| 13 | 7 | 3 | 10 | 3 |
| 14 | 7 | 3 | 11 | 4 |
| 15 | 8 | 3 | 12 | 4 |
| 16 | 8 | 4 | 12 | 4 |
| 17 | 9 | 4 | 13 | 4 |
| 18 | 9 | 4 | 14 | 5 |
| 19 | 10 | 4 | 15 | 5 |
| 20 | 10 | 5 | 15 | 5 |

**What's *not* in this codebase (sanity for the auditor):**

- No `bnb-chain/tss-lib` / multi-party-ecdsa / multi-party-sig.
- No GG18 / GG20 / FROST / CGGMP21 / DKLS.
- No Paillier encryption (so no CVE-2023-33241 family).
- No threshold *signing* of secp256k1/ECDSA — this is threshold
  *decryption* of ElGamal.
- No libp2p / direct P2P TSS protocol — every ceremony message is a
  Cosmos tx inside a CometBFT block.

---

## 2. Files to feed the AI (read order matters)

### A. Specifications — read first

1. `docs/tss-ceremony.md` — primary protocol spec.
2. `docs/tss-ceremony-math.md` — worked numeric example mapped to source.
3. `docs/session-status-lifecycle.md` — failure / jailing / audit-preservation behavior.
4. `crypto/shamir/README.md` — Shamir + Feldman API and math.
5. `crypto/ecies/README.md` — ECIES design notes.

### B. Crypto primitives — review for soundness

6. `crypto/shamir/shamir.go` — `Split`, `Reconstruct`, `LagrangeCoefficients`, `EvalPolynomial`.
7. `crypto/shamir/feldman.go` — `FeldmanCommit`, `VerifyFeldmanShare`, `CombineCommitments`, `EvalCommitmentPolynomial`.
8. `crypto/shamir/partial_decrypt.go` — `PartialDecrypt`, `CombinePartials`.
9. `crypto/ecies/ecies.go` + `crypto/ecies/serialize.go` — share transport.
10. `crypto/elgamal/elgamal.go`, `elgamal/dleq.go`, `elgamal/bsgs.go`, `elgamal/serialize.go`.

### C. Protocol orchestration — where bugs usually live

11. `app/prepare_proposal_ceremony.go` — DKG contribute + ack injectors (this is the heart of the ceremony; **highest-risk file**).
12. `app/prepare_proposal_partial_decrypt.go` — partial decryption injector.
13. `app/prepare_proposal.go` — composed pipeline + tally injector (Lagrange + BSGS).
14. `app/process_proposal.go` — `validateInjectedDKGContribution` etc. (defense-in-depth).
15. `app/ante_ceremony.go` — `CeremonyValidatorDecorator` (bonded gate).
16. `x/vote/keeper/msg_server_ceremony.go` — on-chain `ContributeDKG`, `finalizeDKG`, `AckExecutiveAuthorityKey`, Pallas registry.
17. `x/vote/keeper/msg_server_tally_decrypt.go` — partial decrypt + DLEQ + threshold tally verify.
18. `x/vote/keeper/keeper_ceremony.go` — `ThresholdForN`, `HalfAcked`, helpers.
19. `x/vote/keeper/keeper_ceremony_jailing.go` — non-contributor jailing on timeout.
20. `x/vote/keeper/keeper_pallas_registry.go` — global Pallas PK registry + per-round snapshot.
21. `x/vote/module.go` — EndBlock timeouts (REGISTERING / DEALT), confirm/activate transitions.

### D. Types, wire, persistence

22. `proto/svote/v1/types.proto` (`DKGContribution`, `VoteRound.dkg_contributions`, `feldman_commitments`, `threshold`).
23. `proto/svote/v1/tx.proto` (`MsgContributeDKG`, `MsgAckExecutiveAuthorityKey`, `DealerPayload`).
24. `api/codec.go` (`TagContributeDKG=0x0E`, `TagAckExecutiveAuthorityKey=0x08`, custom non-Cosmos-tx encoding).
25. `cmd/svoted/cmd/pallas_keygen.go` + `cmd/svoted/cmd/commands.go` (key generation, `ea_sk_path` / `pallas_sk_path` config).
26. `scripts/init.sh`, `scripts/init_multi.sh`, `docker/entrypoint.sh` (operational provisioning).

### E. Tests — read to understand intended invariants

27. `crypto/shamir/shamir_test.go`, `crypto/shamir/feldman_test.go`, `crypto/shamir/partial_decrypt_test.go` (incl. *malicious dealer / wrong partial* cases).
28. `crypto/elgamal/dleq_test.go` (partial-decrypt DLEQ + threshold E2E).
29. `app/abci_test.go::TestDKGFullLifecycle`.
30. `app/ceremony_deal_test.go`, `app/prepare_proposal_ceremony_unit_test.go`, `app/prepare_proposal_partial_decrypt_test.go`, `app/prepare_proposal_tally_threshold_test.go`, `app/threshold_tally_e2e_test.go`.
31. `x/vote/keeper/msg_server_ceremony_test.go`, `x/vote/keeper/msg_server_tally_decrypt_test.go`.

### F. Existing audit material

32. `audit/fractal-development-audit-2026q1.pdf` — Fractal development audit (read for known issues, scope drift).
33. `.zcash-review/review-packets/` — currently only contains `sdk-android/`; no DKG/TSS packet yet.

---

## 3. The Thorchain bug — what it actually was and why it doesn't directly map

Thorchain runs `bnb-chain/tss-lib` (GG18/GG20 threshold ECDSA over secp256k1).
The well-known TSS bug families that have hit that stack include:

- **CVE-2023-33241 (Fireblocks, May 2023):** parties accepted Paillier
  moduli that were not biprimes or had small factors, leaking ECDSA
  secret-share material across signing sessions. Fix: CGGMP21-style
  zero-knowledge proofs of `N`. → **Not applicable.** vote-sdk has no
  Paillier and no MPC ECDSA. See
  [Fireblocks technical report](https://www.fireblocks.com/blog/gg18-and-gg20-paillier-key-vulnerability-technical-report)
  and [GitLab Advisory GMS-2023-2159](https://advisories.gitlab.com/pkg/golang/github.com/bnb-chain/tss-lib/).

- **Trail of Bits "Breaking the shared key in TSS" (Feb 2024):** a
  single malicious participant could raise the effective threshold
  beyond the protocol's `t`, locking the group key. Affects
  GG18/GG20/DMZ21/FROST Pedersen-DKG implementations that didn't
  validate per-party complaint behavior. →
  [Trail of Bits writeup](https://blog.trailofbits.com/2024/02/20/breaking-the-shared-key-in-threshold-signature-schemes/).
  **Partially applicable** — vote-sdk has no complaint round and aborts
  the whole ceremony on any verification failure (see §4-G), so the
  specific DoS lever is different but the same class of "single bad
  party kills the ceremony" risk exists by design.

- **Rogue-key / bias on Joint-Feldman (Gennaro 1999 / Sigma Prime
  blog):** a colluding minority `> n - t` can predict or steer the
  joint public key. → **Directly applicable** to this codebase; see §4-A.

- **Last-mover bias on Joint-Feldman without commit-reveal:** the
  final contributor sees all other commitments before committing their
  own polynomial and can grind their randomness to bias `ea_pk`. →
  **Directly applicable**; documented as accepted in
  `docs/tss-ceremony.md`. The auditor should sanity-check that
  acceptance.

The **lesson from Thorchain** that *does* generalize is procedural:
*"library-level cryptographic correctness has been wrong in production
for years before being caught."* The same scrutiny is warranted here
even though the algorithm is different.

---

## 4. Vulnerability classes for the auditor to enumerate

For each class below: read the indicated files, then either confirm the
mitigation is sound or write up the gap. **Numbered so the AI can return
findings keyed to them.**

### A. Tally threshold and ceremony quorum bounds

- **Why:** `t` is the privacy/reconstruction threshold: any `t` validators can
  produce threshold decryptions, while fewer than `t` should not. The ceremony
  timeout quorum is a separate liveness threshold: it must leave enough slack
  above `t` that a surviving withholder cannot force `TallyTimedOut=true`.
- **In code:** `x/vote/keeper/keeper_ceremony.go::ThresholdForN` →
  `t = max(2, ceil(n/2))` for `n ≥ 2`. The intended timeout quorum is
  `required(n) = max(t + f, n - f)` with `f = floor((n - t) / 2)`.
  - `n=2 → t=2, required=2`
  - `n=3 → t=2, required=3`
  - `n=4 → t=2, required=3`
  - `n=5 → t=3, required=4`
  - `n=8 → t=4, required=6`
- **Check:** the auditor must (i) recompute `t`, `f`, and `required` for every
  `n`, (ii) verify there is *no other code path* that builds a round with a
  different tally threshold (search for direct writes to `VoteRound.Threshold`),
  (iii) confirm contribution and ack timeout paths use `required(n)` rather
  than bare `HalfAcked` or `nAcks >= t`, and (iv) confirm
  `min_ceremony_validators ≥ 2` at genesis.

### B. Bias by aborting last-mover

- **Why:** Joint-Feldman without Pedersen commit-reveal lets the last
  contributor see all others' Feldman commitments before broadcasting
  their own. They can abort or grind their randomness to bias `ea_pk`.
- **In code:** there is no commit phase. Contributions land in block
  order; `finalizeDKG` runs on the `n`-th contribution.
- **Check:** is the acceptance in `docs/tss-ceremony.md` justified by
  the threat model? If `ea_pk` only ever encrypts votes (not funds),
  what's the impact of partial bias? Are non-contributors penalized
  enough (`keeper_ceremony_jailing.go`) to deter selective aborts?
  Re-derive the bias bound: with `k` malicious last-movers, the
  attacker can bias `ea_pk` over `2^k` choices — is that meaningful for
  ballot privacy?

### C. Proof of knowledge of polynomial coefficients

- **Why:** Without a Schnorr PoK on `a_0`, a malicious contributor can
  set `a_0` adversarially relative to others' published commitments
  (rogue-key-style adaptive choice). FROST and BLS DKG add this.
- **In code:** no PoK present. `MsgContributeDKG` carries only
  Feldman commitments + ECIES payloads.
- **Check:** does the chain's block-ordering + proposer-only injection
  make adaptive `a_0` infeasible? (The proposer commits inside a block
  before seeing later commitments — so adaptive within the same height
  is impossible, but adaptive *across* heights is possible since
  contributions span multiple blocks.) The AI should reason about this
  ordering carefully.

### D. Feldman verification correctness

- **Per-contributor:** `app/prepare_proposal_ceremony.go::ackDKGRound`
  calls `shamir.VerifyFeldmanShare` against the *individual*
  contributor's commitments before adding to the combined share.
  ✓ correct shape.
- **Combined:** after summing, the same function is called against
  `round.FeldmanCommitments` (the combined vector). ✓ correct shape.
- **Check:** are *Pallas point validity* checks (on-curve, non-identity,
  correct subgroup) applied to **every** received commitment and
  ephemeral pk *before* any group operation? Look for missing
  `IsOnCurve` / `IsIdentity` calls in `elgamal.UnmarshalPublicKey`
  and `DecompressPallasPoint`. Confirm Pallas has cofactor 1 (so no
  small-subgroup attack) or that subgroup checks exist.

### E. ECIES share transport

- **Shape:** ephemeral-static ECDH on Pallas → KDF → ChaCha20-Poly1305.
- **Check:**
  - Is the ephemeral public key freshly random per share, never reused?
  - Is the KDF domain-separated (per-recipient, per-round, per-tx)?
  - Is the AEAD nonce derived deterministically from the ephemeral key
    only, or randomized? Any nonce-reuse risk?
  - Is the recipient's Pallas PK fully validated *before* DH (defends
    against twist / small-subgroup if any)?
  - Are decryption errors surfaced *non-ambiguously*? (Ack handler
    aborts on first failure — confirm no oracle leakage.)
  - Rust port at `e2e-tests/src/ecies.rs` is **explicitly not audited**
    — flag that it MUST NOT be relied on for production.

### F. Persisted secret material lifetime

- `coeffs.<roundID>`: written 0600, zeroed and deleted on ack success.
- `share.<roundID>`: written 0600, intended to be deleted on round
  finalize.
- **Check:**
  - `os.WriteFile` does not `fsync` — survival of crashes between
    write and ack/finalize?
  - `zeroAndDeleteCoeffsFile` opens with `O_WRONLY` (no `O_TRUNC`);
    confirm the write fully covers `info.Size()` and that there is no
    earlier larger version.
  - Does cleanup actually run when `ceremonyDir == ""`? (No — both
    helpers early-return; flag this as a config footgun.)
  - When a round is *cancelled* (timeout → jailing), are `share.*` and
    `coeffs.*` files purged on every validator?
  - Are temp files / swap / journaled FS leaks considered?

### G. Liveness / DoS levers

- **Single bad contribution can stall a round.** Until the n-th
  contribution lands, `finalizeDKG` doesn't run; if even one validator
  refuses to contribute, the round times out (EndBlock) and jails
  them.
- **Check:** Are jail conditions strong enough to make this unprofitable?
  Can a validator inject an *invalid* `MsgContributeDKG` that passes
  `process_proposal` checks but fails later, soft-bricking the round
  without being jailed? (Look at `validateInjectedDKGContribution` —
  does it run the full Feldman dimension + Pallas-point check on every
  contribution, *and* reject duplicate / unknown-validator payloads,
  exactly mirroring `ContributeDKG`?)
- **Check contribution timeout path:** if bounded-subset finalization is enabled
  per vote-sdk#323, does it require `len(contributors) >= required(n)`, strip
  non-contributors before combining commitments, and preserve original
  `shamir_index` values?
- **Check ack timeout path:** does the timeout code in `module.go` require
  `len(ackers) >= required(n)` before stripping non-ackers and activating? A
  bare `≥ 1/2` or `≥ t` quorum can leave exactly threshold-sized survivors and
  allow one later withholder to force `TallyTimedOut=true`.

### H. Proposer-only injection vs. proposer-equivocation

- All ceremony msgs are injected by the **block proposer** of that
  height (`ValidateProposerIsCreator`). Direct mempool submission is
  rejected.
- **Check:** What happens if the same validator proposes two blocks at
  the same height (CometBFT equivocation)? Could two distinct
  `MsgContributeDKG` records from the same validator be appended,
  even though the keeper checks `FindContributionInRound` for
  duplicates? Confirm the equivocation evidence path and that
  Cosmos's double-sign slashing covers this.

### I. Ack signature is not a signature

- `MsgAckExecutiveAuthorityKey.AckSignature = SHA256("ack" ‖ ea_pk ‖ valoper_bech32)`.
  It is a *deterministic public digest*. Any validator (or any observer
  of the chain) can compute the ack for any other validator.
- **Why this is OK in this design:** only the block proposer can inject
  the ack, and the proposer's identity is verified by
  `ValidateProposerIsCreator`. The ack is bound to `ea_pk` so a stale
  ack from a previous failed ceremony cannot be replayed onto a new
  one with a different `ea_pk`.
- **Check:** is `ea_pk` guaranteed to differ between any two ceremonies
  on the same `(round_id, validator)` pair such that replay is
  impossible? (Round IDs are unique; `ea_pk` is freshly DKG'd; the
  digest changes — confirm `AppendCeremonyLog` and `round.EaPk` are
  in fact rewritten on retry, not kept stale.)

### J. Threshold ElGamal partial-decryption integrity

- Each validator submits `D_i = share_i · C1` + Chaum-Pedersen DLEQ
  proving `log_G(VK_i) = log_C1(D_i)`.
- `VK_i = EvalCommitmentPolynomial(round.FeldmanCommitments, i)` is
  *derived on the fly*, not stored — confirm it always matches what
  the contributor computed during DKG.
- **Check:**
  - Is the DLEQ Fiat-Shamir transcript domain-separated and does it
    include `round_id`, `validator_index`, `C1`, and **both** group
    elements `VK_i` and `D_i`?
  - Can a malicious validator submit a `D_i` that interpolates
    correctly with other honest `D_j`s only on *some* ciphertexts?
    (Lagrange-in-the-exponent + DLEQ on `VK_i` should make this
    infeasible — verify the proof.)
  - On `SubmitTally`, the keeper recomputes the tally from `≥ t`
    partials and verifies homomorphically — confirm this check exists
    and is mandatory.

### K. Public verifiability / audit preservation

- On ceremony failure, all `dkg_contributions` (commitments + ECIES
  payloads) are kept on chain so anyone can reproduce the failure
  later.
- **Check:** is failed-ceremony state actually preserved (not pruned)?
  Are there any code paths that delete or rewrite
  `round.DkgContributions` post-failure?

### L. Curve & library

- Pallas via `mikelodder7/curvey v1.1.1`. This is a smaller library
  than `gtank/ristretto255` or `dalek-cryptography`.
- **Check:**
  - Recent commit activity / known issues on the dependency?
  - Constant-time scalar ops?
  - Hash-to-curve (used anywhere here? — currently no, but flag if
    introduced).

### M. Concentration / governance risks (informational)

- Block proposer is the sole source of ceremony messages. A long-run
  malicious proposer rotation could repeatedly stall rounds.
- Single PDF audit (`audit/fractal-development-audit-2026q1.pdf`)
  predates the DKG redesign — confirm it covered the *threshold* path
  or flag it as legacy single-dealer.

---

## 5. Required external reading for the AI

Paste these URLs (or fetch their content) into the AI's context so it
can reason from primary sources rather than memory:

1. Gennaro et al., *Secure Distributed Key Generation for Discrete-Log
   Based Cryptosystems*, J. Cryptology 2007 — the **canonical Joint-Feldman bias paper**.
   `https://link.springer.com/article/10.1007/s00145-006-0347-3`

2. Sigma Prime, *Rogue Key Attack on Gennaro et al. DKG for Polynomials
   of Excessive Degree* — concrete attack walkthrough.
   `https://blog.sigmaprime.io/dkg-rogue-key.html`

3. Trail of Bits, *Breaking the shared key in threshold signature
   schemes* — the Feb 2024 DoS-by-complaint family that motivated
   reviewing DKG broadly.
   `https://blog.trailofbits.com/2024/02/20/breaking-the-shared-key-in-threshold-signature-schemes/`

4. Fireblocks, *GG18 and GG20 Paillier Key Vulnerability (CVE-2023-33241)*
   — for completeness; **does not apply** to vote-sdk but the AI should
   read it to confirm that and rule it out.
   `https://www.fireblocks.com/blog/gg18-and-gg20-paillier-key-vulnerability-technical-report`

5. 0xRafa, *Threshold Signature Ceremony Attacks: Single Malicious
   Participant Biases Key Generation in FROST* — last-mover bias class.
   `https://0xrafasec.com/en/posts/threshold-signature-ceremony-attacks-how-a-single-malicious-participant-biases-key-generation-in-frost`

6. Hexens, *Attacks on Threshold Schemes: Part 1* — catalogue of
   wrong-degree polynomial and other implementation-level bugs.
   `https://hexens.io/blog/mpc-attacks-p1`

7. THORChain Dev Docs, *TSS* — context for the ZCA-539 trigger.
   `https://dev.thorchain.org/bifrost/tss.html`

8. `mikelodder7/curvey` Pallas implementation (dependency under audit).
   `https://github.com/mikelodder7/curvey`

9. Chaum-Pedersen DLEQ original paper / good modern writeup, for §4-J.
   `https://www.chaum.com/publications/Pedersen.pdf`

---

## 6. Ready-to-paste AI auditor prompt

> Use the prompt below verbatim with Claude / GPT / Gemini after
> attaching the files in §2-B, §2-C, §2-D, §2-E (the docs in §2-A are
> already summarized in §1, but attach them too if context budget
> allows).

```
You are a senior cryptography auditor with deep experience in
threshold cryptography, distributed key generation, and Cosmos SDK
chains. Audit the DKG and threshold-decryption stack of vote-sdk.

STACK SUMMARY
- Joint-Feldman DKG on the Pallas curve. No Pedersen commit-reveal.
- (t, n) Shamir over Pallas Fq. t = max(2, ceil(n/2)) for n >= 2.
- Ceremony timeout quorum: required(n) = max(t + f, n - f), where
  f = floor((n - t) / 2).
- ECIES (ephemeral-static ECDH + ChaCha20-Poly1305) for share transport.
- Threshold ElGamal decryption with Lagrange-in-the-exponent + BSGS.
- Chaum-Pedersen DLEQ proofs for partial decryptions.
- Messages ride CometBFT blocks; only the block proposer may inject
  ceremony txs via PrepareProposal; peers re-validate in ProcessProposal.
- Stack is NOT GG18 / GG20 / FROST / Paillier / tss-lib.

YOUR TASK
For each numbered vulnerability class A–M in `audit/dkg-tss-audit-pack.md`
§4, produce one of:
- "OK — <one-paragraph justification with file:line citations>", or
- "FINDING <severity> — <one-paragraph description with file:line
   citations, reproduction sketch, and concrete remediation>".

REQUIREMENTS
1. Every claim must cite a file:line in the attached source. If you
   need a file that wasn't attached, ask for it; do not assume.
2. Enumerate t, f, and required(n) for n in [2..16]. Verify timeout
   activation leaves required(n) - f >= t and that implementation paths use
   required(n), not only t or HalfAcked.
3. For Joint-Feldman bias (class B), produce a concrete attacker model:
   how much bias is achievable with k colluding last-movers, given the
   block-order constraint and proposer-only injection?
4. For ECIES (class E), trace the exact byte format from
   `crypto/ecies/ecies.go` and `crypto/ecies/serialize.go` and check
   nonce derivation, KDF domain separation, and AEAD usage against
   RFC-style ECIES (or call out divergence).
5. For DLEQ (class J), confirm the Fiat-Shamir transcript binds
   round_id, validator index, C1, VK_i, and D_i. If any of those is
   missing, that is a HIGH finding.
6. Confirm the Thorchain-style Paillier / GG20 bugs DO NOT apply, by
   showing the absence of Paillier and the absence of MPC ECDSA in
   the dependency graph (`go.mod` + crypto package imports).
7. End with a SEVERITY TABLE: count of CRITICAL / HIGH / MEDIUM / LOW
   / INFORMATIONAL findings, plus a recommended remediation order.

DO NOT
- Speculate about code you haven't been shown.
- Confuse this stack with tss-lib / GG20.
- Treat threshold ElGamal *decryption* as threshold *signing* — there
  is no signing happening here.
- Skip subgroup / on-curve checks on the assumption that "Pallas is
  prime-order" without verifying it from the dependency.

REFERENCES
Treat §5 of `audit/dkg-tss-audit-pack.md` as required reading. Cite
those references in findings where applicable.
```

---

## 7. Suggested verification harness (post-audit)

Independent of the AI audit, the following property-based tests would
catch most §4 regressions and can be added in a follow-up:

- **§4-A.** Property: for every `n ∈ [1..64]`, `ThresholdForN(n) >
  n / 2` whenever `n ≥ 2`.
- **§4-D.** Property: any randomly-modified Feldman commitment causes
  `VerifyFeldmanShare` to return `false`.
- **§4-E.** Property: any 1-byte tweak of the ECIES ciphertext causes
  `Decrypt` to fail (AEAD integrity).
- **§4-F.** Test that `share.<roundID>` and `coeffs.<roundID>` files
  do not exist after `EndBlocker` finalizes / cancels a round.
- **§4-G.** Adversarial test: a contributor that publishes
  syntactically valid but Feldman-inconsistent commitments must be
  caught at `ackDKGRound` and the round must time out — not silently
  finalize a corrupted `ea_pk`.
- **§4-J.** Property: a `D_i` that is *not* the Lagrange-consistent
  partial for `share_i` must fail DLEQ verification.

---

## 8. Open questions to confirm with the dev team before starting

1. What is the **declared threat model** for the EA key? Specifically,
   what's the worst-case impact of `ea_pk` bias from §4-B in terms of
   ballot privacy or tally manipulation?
2. Is there a planned migration to **commit-reveal Joint-Feldman**
   (Pedersen-DKG) or to **DKG with Schnorr PoK** (FROST-style)? If yes,
   the audit should also cover the migration path.
3. Are partial decryptions ever requested **outside the
   block-proposer path** (e.g., for off-chain audit recomputation)? If
   yes, that path needs equivalent DLEQ verification.
4. Has Pallas been independently audited as constant-time in
   `mikelodder7/curvey`? If not, side-channel resistance is an open
   item.

---

*Last updated: 2026-05-20 — companion to ZCA-539.*
