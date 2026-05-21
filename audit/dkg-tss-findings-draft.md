# DKG / TSS Findings — Draft (Self-Audit Pass 1)

> Companion to `audit/dkg-tss-audit-pack.md`. This is a **first-pass
> self-audit** by an LLM auditor (Claude Opus 4.7) following the prompt
> in §6 of the audit pack and citing file:line from the working tree at
> commit `HEAD` on 2026-05-20.
>
> **Not a substitute for** an independent third-party crypto audit
> (Trail of Bits, Least Authority, Cure53, Veridise, ZK Security, etc.).
> The function of this document is to (a) baseline the stack so the
> third-party auditor doesn't waste time rediscovering the obvious, and
> (b) raise any items that need attention before that engagement.
>
> Severity scale: **CRITICAL** (immediate vote-integrity or
> confidentiality break) → **HIGH** (exploitable by realistic threat
> model) → **MEDIUM** (exploitable under restrictive conditions or
> requiring privileged position) → **LOW** (defense-in-depth) →
> **INFORMATIONAL** (clarity / maintainability).

---

## TL;DR

| Class | Title | Verdict | Severity |
|------|-------|---------|----------|
| A | Tally threshold + timeout quorum bound | OK after quorum-hardening design | — |
| A2 | `min_ceremony_validators` default = 1 | FINDING | MEDIUM |
| B | Last-mover bias on Joint-Feldman | DESIGN-ACCEPTED | INFORMATIONAL |
| C | No Schnorr PoK on polynomial coefficients | FINDING | MEDIUM |
| D | Feldman verification + Pallas point validation | MOSTLY OK | LOW |
| E | ECIES (KDF, nonce, AEAD, subgroup) | MOSTLY OK | LOW |
| F | Persisted secret material lifetime | MOSTLY OK | LOW |
| G | ProcessProposal does not crypto-validate DKG contributions | FINDING | MEDIUM |
| G2 | Threshold-sized survivor set can time out at tally | FINDING; mitigated by `required(n)` quorum | MEDIUM |
| H | Proposer-equivocation safety | OK | — |
| I | Ack "signature" is a public digest | OK (by design) | INFORMATIONAL |
| J | DLEQ transcript binding | MOSTLY OK | LOW |
| K | Audit preservation of failed ceremony state | OK | — |
| L | Curve / library (`mikelodder7/curvey`) | OPEN | INFORMATIONAL |
| M | Concentration / governance | OPEN | INFORMATIONAL |
| — | Thorchain bug families (CVE-2023-33241, Trail of Bits 2024) | NOT APPLICABLE | — |

**Counts:** 0 CRITICAL · 0 HIGH · **4 MEDIUM** · **5 LOW** · 5 INFORMATIONAL/OPEN.

Suggested remediation order: **A2 → G2 → C → G → J → E → F → D → L/M**.

---

## Class A — Tally threshold + timeout quorum bound  →  OK after quorum hardening

`x/vote/keeper/keeper_ceremony.go:53-65` defines the threshold:

```52:65:x/vote/keeper/keeper_ceremony.go
func ThresholdForN(n int) (int, error) {
	if n < 1 {
		return 0, fmt.Errorf("ThresholdForN: n must be >= 1, got %d", n)
	}
	if n == 1 {
		return 1, nil
	}
	t := (n + 1) / 2
	if t < 2 {
		t = 2
	}
	return t, nil
}
```

`t` is the tally reconstruction threshold, not the ceremony activation quorum.
The quorum-hardening design adds:

```
f(n) = floor((n - t(n)) / 2)
x(n) = y(n) = f(n)
required(n) = max(t(n) + x(n), n - y(n))
```

Enumeration `n ∈ [2..16]`:

| n | t | f=x=y | required | required - f >= t? |
|---|---|-------|----------|--------------------|
| 2 | 2 | 0 | 2 | 2 >= 2 ✓ |
| 3 | 2 | 0 | 3 | 3 >= 2 ✓ |
| 4 | 2 | 1 | 3 | 2 >= 2 ✓ |
| 5 | 3 | 1 | 4 | 3 >= 3 ✓ |
| 6 | 3 | 1 | 5 | 4 >= 3 ✓ |
| 7 | 4 | 1 | 6 | 5 >= 4 ✓ |
| 8 | 4 | 2 | 6 | 4 >= 4 ✓ |
| 9 | 5 | 2 | 7 | 5 >= 5 ✓ |
| 10 | 5 | 2 | 8 | 6 >= 5 ✓ |
| 11 | 6 | 2 | 9 | 7 >= 6 ✓ |
| 12 | 6 | 3 | 9 | 6 >= 6 ✓ |
| 13 | 7 | 3 | 10 | 7 >= 7 ✓ |
| 14 | 7 | 3 | 11 | 8 >= 7 ✓ |
| 15 | 8 | 3 | 12 | 9 >= 8 ✓ |
| 16 | 8 | 4 | 12 | 8 >= 8 ✓ |

The `n = 1` degenerate case (`t = 1`) collapses threshold security entirely. This is gated by the
`min_ceremony_validators` genesis parameter — **but see Class A2 for
the default-value issue**.

There is **no other code path that writes `VoteRound.Threshold`** other
than `finalizeDKG` (`x/vote/keeper/msg_server_ceremony.go:253`), which
uses the value computed above. ✓

---

## Class A2 — Default `min_ceremony_validators = 1` is insecure  →  MEDIUM

`x/vote/keeper/keeper_min_validators.go:9` defines
`defaultMinCeremonyValidators uint32 = 1`. When the genesis param is
unset or zero, `genesis.go:97-103` and `module.go:714` both default to
`1`.

```9:22:x/vote/keeper/keeper_min_validators.go
const defaultMinCeremonyValidators uint32 = 1

// GetMinCeremonyValidators reads the minimum ceremony validator count from KV.
// Returns defaultMinCeremonyValidators (1) if the key has not been set.
func (k *Keeper) GetMinCeremonyValidators(kvStore store.KVStore) (uint32, error) {
	// ...
	return defaultMinCeremonyValidators, nil
	// ...
}
```

With `n = 1`, `ThresholdForN(1) = 1`, meaning **a single validator
holds the entire ElGamal secret key** — no threshold security at all.
Ballot privacy reduces to trusting one operator.

`docs/tss-ceremony.md:5` does acknowledge this:

> Set to 2 or higher on mainnet for real threshold security.

But this is a **documentation footnote**, not a code-enforced guard.
A genesis operator who follows the default values gets a single-trust
deployment without warning.

**Existing test confirms the risk.** `x/vote/keeper/msg_server_test.go:375`
explicitly exercises *"happy path: 1 validator with default
min_ceremony_validators=1"*, demonstrating the default is intended to
be usable — fine for `app/abci_test.go:1174` test setups, dangerous
for any production deployment.

**Impact.** Any operator setting up production from a default genesis
manifest (e.g., via `init.sh` / `init_multi.sh`) without explicitly
overriding `min_ceremony_validators` gets an insecure-by-default
chain. This is exactly the failure mode that motivated CVE-2023-33241
attention (config-level escape hatches that aren't validated).

**Remediation.**

1. **Code:** change `defaultMinCeremonyValidators` to a value that
   reflects the *production* policy. Either:
   - Hard floor at 3 (typical minimum for any Byzantine-fault tolerant
     threshold scheme), with single-validator behavior gated by an
     explicit `chain_id == "test"` or `enable_unsafe_singleton`
     flag; or
   - Keep the default at 1 but emit a **prominent startup log
     warning** ("WARN: min_ceremony_validators=1 — single-validator
     mode, NO threshold security") on every `app.New` for production
     binaries; and
2. **Genesis runbook:** `docs/runbooks/genesis-setup.md` must require
   operators to explicitly set `min_ceremony_validators` ≥ 3 before
   `genesis-validate`.
3. **CI:** add a genesis-validation test that fails if
   `chain_id != "test*"` and `min_ceremony_validators < 3`.

**Severity rationale.** Not exploitable through code paths (the value
*is* honored when explicitly set), but the default is unsafe in a way
that has historically caused production incidents in similar systems.

---

## Class B — Last-mover bias on Joint-Feldman  →  DESIGN-ACCEPTED · INFORMATIONAL

**Background.** Joint-Feldman DKG without a Pedersen commit-reveal phase
is biased: the joint secret `s = Σ s_i` is *not* uniformly distributed
from the perspective of an adversary controlling some contributors,
because each colluding party can decide whether to broadcast or abort
*after* observing earlier contributions. The canonical analysis is in
Gennaro et al. 2007.

**In this codebase.** Contributions land in CometBFT blocks in proposer
order. A colluding subset `K` of contributors can rush-grind: each can
attempt some bounded number of `(secret, polynomial)` candidates locally
(by repeatedly proposing blocks with different `MsgContributeDKG`),
and choose to *not* be the last one to inject if the resulting joint
key would be unfavorable. With `|K| = k` colluding validators, the
attacker has at most a `k`-way choice over a polynomially-bounded set
of joint-secret outcomes (one extra bit per colluding last-mover that
chooses abort-or-not).

**Why the design accepts this.** `ea_pk` is used only for ElGamal
encryption of votes during a single round; it is **not** a long-lived
custody key. It is replaced with a fresh DKG every round
(`docs/tss-ceremony.md`). The attacker gains *log-bounded* bias on
`ea_pk`; this does not break ballot secrecy (encryption is still
IND-CPA against any fixed key) and does not enable arbitrary
decryption.

**Where to be careful.** If `ea_pk` is ever reused across rounds, or
if it is used for any purpose beyond per-round ElGamal encryption
(e.g., as a long-term identity, or in a hash that an attacker can
predict), this finding upgrades to MEDIUM. Confirmed in code that
`finalizeDKG` overwrites `round.EaPk` and that `EaPk` is per-round.

**Action:** none required if scope holds. Document the scope
constraint in `docs/tss-ceremony.md` explicitly. ✓ Partially done.

---

## Class C — No Schnorr PoK on polynomial coefficients  →  MEDIUM

**Issue.** `MsgContributeDKG` carries Feldman commitments and ECIES
share payloads but **no proof of knowledge of the secret coefficients**
`a_{i,0..t-1}` that produced those commitments. See
`proto/svote/v1/tx.proto::MsgContributeDKG` and
`x/vote/keeper/msg_server_ceremony.go:144-189`.

A malicious contributor who runs *after* honest contributors can
pick their commitment vector `C_i` adversarially relative to the
partial sum `C_{0..i-1} = Σ_{j<i} C_j`. Concretely:

- Adversary observes `C_{0..i-1}[0] = ea_pk_partial`.
- Adversary computes `target_ea_pk = X` (their choice).
- Adversary sets `C_i[0] = X - ea_pk_partial`.
- For this to be consistent with a valid secret coefficient
  `a_{i,0}`, the adversary must know `s_i = log_G(C_i[0])`.
  Without PoK, the chain has no way to verify this.

**Why it's still bounded in practice.** If `a_{i,0}` is set to a
*specific* target without the adversary knowing its discrete log, the
adversary's own share `s_i` is unknown to them, and the Feldman
verification in `ackDKGRound`
(`app/prepare_proposal_ceremony.go:530`) **will fail for the
adversary themselves** because they cannot produce a share that
satisfies `share * G == EvalCommitmentPolynomial(C_i, idx)` without
knowing `a_{i,0}`. So this is **not directly exploitable as a key
takeover** — but it is exploitable as a **biasing primitive**:

- With `k ≥ ⌈n/2⌉ + 1` colluding contributors (i.e. an honest
  minority of `≤ ⌊n/2⌋ − 1` honest validators), the colluders can
  collectively learn the joint secret via their own shares.
- More importantly: the colluders can *also* steer `ea_pk` to a
  specific known value by setting `C_n[0] = target − C_{0..n-1}[0]`
  where the final adversary *does* know the discrete log of that
  difference because they control all earlier adversarial
  contributions.

For an election system where the threat model assumes Byzantine-honest
majority, this is **not exploitable for confidentiality**. But it does
mean that the codebase tolerates **adaptive `ea_pk` choice by
collusion** that a PoK-extended DKG would not. Standard FROST and
Pedersen-DKG implementations include exactly this PoK to close the gap.

**Reference.**
[Sigma Prime — Rogue Key Attack on Gennaro et al. DKG](https://blog.sigmaprime.io/dkg-rogue-key.html).

**Remediation (recommended).** Add a Schnorr proof of knowledge of
`a_{i,0}` (and ideally of all `a_{i,j}` for `j ∈ [0..t-1]`) to
`MsgContributeDKG`. The proof is a single Schnorr signature on
`SHA256("svote-dkg-pok-v1" || round_id || valoper || C_i[0..t-1])`
with verification key `C_i[0]`, costing one extra `Mul + Hash` per
contribution and per verification. Reject contributions with
invalid PoK in *both* `ProcessProposal` and `ContributeDKG`.

**Severity rationale.** Not directly exploitable for vote secrecy
under the stated trust assumption (Byzantine-honest majority + per-round
keys), but reduces robustness margin and would have been a Trail of
Bits / Least Authority finding by default. Closing the gap is cheap.

---

## Class D — Feldman verification + Pallas point validation  →  MOSTLY OK · LOW

### D1. Per-contributor and combined Feldman check

`ackDKGRound` (`app/prepare_proposal_ceremony.go:480-559`) verifies
each contributor's share against *their* commitment vector, then
verifies the summed `combined_share` against `round.FeldmanCommitments`
(the combined vector):

```530:541:app/prepare_proposal_ceremony.go
ok, err := shamir.VerifyFeldmanShare(G, contribCommitments, shamirIndex, shareSk.Scalar)
// ...
if !ok {
    return nil, nil, fmt.Errorf("contributor %s: share failed Feldman verification", contrib.ValidatorAddress)
}
combinedShare = combinedShare.Add(shareSk.Scalar)
```

Both checks are present and correct shape. ✓

### D2. Pallas point validation

Every entry point that ingests a Pallas point validates it:

- `elgamal.UnmarshalPublicKey` (`crypto/elgamal/serialize.go:96-108`):
  enforces 32 bytes + on-curve via `FromAffineCompressed` + non-identity.
- `elgamal.DecompressPallasPoint` (`crypto/elgamal/serialize.go:65-82`):
  enforces on-curve; **allows identity** by checking the all-zeros
  sentinel.
- `elgamal.UnmarshalPoint` (`crypto/elgamal/serialize.go:87-92`):
  same as `DecompressPallasPoint` but with a length check; allows
  identity (used for partial decryption `D_i`, where identity is a
  valid value when `share == 0` — see D3).

Feldman commitments are validated as `UnmarshalPublicKey` (non-identity)
in `ContributeDKG` (`msg_server_ceremony.go:149-154`). Combined
commitments are *not* re-validated after `CombineCommitments`, but the
sum of valid points is itself a valid point on a prime-order curve, so
this is safe.

### D3. Identity-point handling on `D_i`

`UnmarshalPoint` accepts the identity. `D_i = share_i * C1`; if
`share_i` were zero (which is rejected by `UnmarshalSecretKey` and is
overwhelmingly improbable as a random Pallas scalar), `D_i` would be
identity. Allowing identity here is fine — but
`VerifyPartialDecryptDLEQ` (`crypto/elgamal/dleq.go:225-230`) does
*not* check `D_i` is non-identity:

```225:230:crypto/elgamal/dleq.go
if Di == nil {
    return fmt.Errorf("elgamal: VerifyPartialDecryptDLEQ: D_i must not be nil")
}
if !Di.IsOnCurve() {
    return fmt.Errorf("elgamal: VerifyPartialDecryptDLEQ: D_i must be on the Pallas curve")
}
```

Compare with the explicit non-identity check on `VK_i` and `C1` two
lines above. **LOW finding.** A `D_i = O` (identity) would imply
`share == 0`, which never occurs honestly. It can still validate
against a DLEQ proof with `share = 0`, but in that case `VK_i` would
also be the identity, which *is* rejected. So this is defense-in-depth,
not a bypass — but the asymmetry should be made consistent.

### D4. Curve cofactor

Pallas is a **prime-order curve** (cofactor 1), so there is no
small-subgroup attack to worry about. This is a curve-level property
of the Pasta curves and is documented in
[Hopwood's writeup](https://github.com/zcash/pasta) — confirm by
inspecting `mikelodder7/curvey`'s Pallas implementation (see Class L).

**Remediation:** add the symmetrical `IsIdentity` check on `D_i` in
`VerifyPartialDecryptDLEQ`.

---

## Class E — ECIES (KDF, nonce, AEAD, subgroup)  →  MOSTLY OK · LOW

`crypto/ecies/ecies.go` is small (~196 lines), let me cite the
relevant blocks.

### E1. KDF

`deriveKey(E, S)` (`crypto/ecies/ecies.go:158-169`):

```158:169:crypto/ecies/ecies.go
func deriveKey(E, S curvey.Point) [32]byte {
	eBytes := E.ToAffineCompressed()
	sX := xCoordinate(S)

	h := sha256.New()
	h.Write(eBytes)
	h.Write(sX)
	// ...
}
```

**No domain separator string** ("svote-ecies-v1" or similar). The
input is `E_compressed (32B) || S.x_only (32B)`, both Pallas-curve
quantities. The recipient's PK is not in the KDF input but is
implicit through `S = e * PK_recipient`. This works because the
ECDH binding ensures only the recipient (who knows `sk_recipient`)
can recompute `S` from `E`.

**Why it's not exploitable.** The KDF inputs are unique per
encryption (fresh ephemeral `e` per `Encrypt` call). The lack of a
domain string only matters if another protocol on the *same*
curve+library reuses the same `(E, S.x) → SHA256 → 32-byte key`
construction and shares any of the inputs — unlikely, but a
domain separator is free.

**LOW finding.** Add a domain tag.

### E2. Nonce = zero is safe by construction

Each `Encrypt` call derives a fresh `key` from a fresh ephemeral
scalar `e`, so the `(key, nonce)` pair is never reused even with
`nonce = 0`. This is the textbook "single-use key" pattern. Code
documents this clearly at `ecies.go:107-111` and `139-143`. ✓

### E3. ECDH shared secret integrity

Both `encryptWithEphemeral` and `decryptWithCheckedInputs` check the
shared secret is non-identity (`ecies.go:95-97` and `127-129`). This
is exactly what's required to prevent degenerate-key attacks. ✓

### E4. Point validation on the ephemeral key

`validatePoint` (`ecies.go:184-195`) checks non-nil, non-identity,
on-curve for both the recipient PK (`Encrypt` line 38) and the
ephemeral PK during decrypt (`Decrypt` line 73). ✓ Pallas is
prime-order so no subgroup check is needed beyond on-curve.

### E5. Ephemeral key derivation

`Encrypt` reads 64 bytes from the RNG and hashes via
`ScalarPallas.Hash` (`ecies.go:51-55`). This is **wide-input
hash-to-scalar** which avoids the "curvey Random() silently returns
nil on RNG failure" footgun called out in the code comment.
Acceptable. ✓

### E6. `xCoordinate` derivation

```174:180:crypto/ecies/ecies.go
func xCoordinate(p curvey.Point) []byte {
	compressed := p.ToAffineCompressed()
	x := make([]byte, elgamal.CompressedPointSize)
	copy(x, compressed)
	x[31] &= 0x7F // clear sign bit
	return x
}
```

This takes the 32-byte compressed Pallas encoding and clears the
sign bit. Standard for ECDH key derivation. Implies `S` and `-S`
derive the same key — but in single-recipient ECDH this is irrelevant
(only the recipient who chose `sk` can compute `S`).

### E7. Wire serialization

`UnmarshalEnvelope` (`crypto/ecies/serialize.go:41-65`) enforces
**fixed expected length** (`32 + ciphertextLen`) and rejects identity
ephemeral pk — protecting against `S = identity` decryption. ✓

### E8. Caveat — Rust port

`e2e-tests/src/ecies.rs` is a Rust reimplementation used by
integration tests. Its module doc states explicitly that it is
**not audited**. This port must **never** be used in production
client code. Confirm via grep that no production binary depends on
this Rust path (e2e-tests is dev-only). ✓ confirmed by directory.

**Remediation:**

1. Add `"svote-ecies-v1"` domain tag to the SHA256 input in
   `deriveKey`. Bump a versioned constant in `ecies.go`.
2. (Optional) Also include `roundID` and recipient `valoper_address`
   in the KDF input, to make share confusion across rounds /
   recipients infeasible at the transcript level.

---

## Class F — Persisted secret material lifetime  →  MOSTLY OK · LOW

### F1. Permissions and zeroization

- `coeffs.<roundID>`: written via `writeCoeffs`
  (`app/prepare_proposal_ceremony.go:89-95`) with mode `0600`. Zeroed
  and removed on success in `zeroAndDeleteCoeffsFile` (`125-149`).
- `share.<roundID>`: written via `os.WriteFile(..., 0600)`
  (`prepare_proposal_ceremony.go:399`). Zeroed and removed via
  `zeroAndDeleteShareFile` (`588-609`).
- In-memory scalars zeroed via `zeroScalar`
  (`prepare_proposal_ceremony.go:615-619`) using `Field4.SetZero()`
  on the underlying limbs.

### F2. No `fsync` between write and ack

`os.WriteFile` does *not* `fsync`. If the validator crashes between
the write of `coeffs.*` (or `share.*`) and the corresponding ack /
finalize, the file may not be on disk. On reboot the ack injector
will fail with "load coefficients" / "load share" errors, and the
round will time out — **liveness impact, no security impact**. ✓

### F3. Zeroize before delete

`zeroAndDeleteCoeffsFile` opens with `O_WRONLY` (no `O_TRUNC`) and
writes exactly `info.Size()` zeros, then `Sync()`, then `Remove()`.
This *should* overwrite the full file, but it relies on `info.Size()`
matching the current on-disk size, which it does given the write was
a single `os.WriteFile`. ✓

**Subtlety.** On a journaled / COW filesystem (ZFS, Btrfs, modern
ext4 with `data=journal`, APFS), the overwrite may write to *new*
blocks while the original blocks linger in the journal until reclaim.
This is a **filesystem-level secret-zeroization limitation** that
applies to virtually all crypto software; the only true mitigation is
disk-level encryption (LUKS / FileVault), which is a deploy concern,
not a code concern. **INFORMATIONAL** — document in the operator
runbook.

### F4. Empty `ceremonyDir` footgun

```125:149:app/prepare_proposal_ceremony.go
func zeroAndDeleteCoeffsFile(dir string, roundID []byte, logger log.Logger) {
	if dir == "" {
		return
	}
	// ...
}
```

If `vote.ea_sk_path` is unset, `ceremonyDir` is empty and **all
cleanup is silently skipped**. Concurrently, the DKG-contribute and
ack injectors are guarded by `loadPallasSk()` (which loads
`vote.pallas_sk_path`, *not* `ea_sk_path`), so it is possible to have
`pallas_sk_path` set but `ea_sk_path` unset — in which case the
validator would *successfully* contribute and ack but **never persist
coeffs/share to disk**, breaking partial decryption later.

`app/prepare_proposal_ceremony.go:247-259` does guard the write with
`if ceremonyDir != ""`, so if `ceremonyDir` is empty, no
`coeffs.<roundID>` is written, but the validator *still injects*
`MsgContributeDKG`. Then at ack time, `ackDKGRound` calls
`loadCoeffs(coeffsPathForRound("", round.VoteRoundId), t)` →
opens path `coeffs.<hex>` in the *current working directory* (no
join error because `filepath.Join("", x) = x`), which will return
"file does not exist", aborting the ack — round times out.

**LOW finding.** Operationally this manifests as a silent stall.
Should fail loudly at startup if `ea_sk_path` is empty *and* the
validator is configured to participate in ceremonies.

### F5. Round cancellation cleanup  →  OK

When a round transitions to `CEREMONY_FAILED` via timeout
(`module.go` EndBlocker lines 544, 603, 648), or to `FINALIZED`
via `SubmitTally`, the share cleanup happens inside the partial-decrypt
PrepareProposal injector:

```81:97:app/prepare_proposal_partial_decrypt.go
shareCacheMu.Lock()
for roundHex, share := range shareCache {
    roundID, err := hex.DecodeString(roundHex)
    if err != nil {
        delete(shareCache, roundHex)
        continue
    }
    r, err := voteKeeper.GetVoteRound(kvStore, roundID)
    if err != nil ||
        r.Status == types.SessionStatus_SESSION_STATUS_FINALIZED ||
        r.Status == types.SessionStatus_SESSION_STATUS_CEREMONY_FAILED {
        zeroScalar(share.Scalar)
        delete(shareCache, roundHex)
        zeroAndDeleteShareFile(ceremonyDir, roundID, logger)
    }
}
shareCacheMu.Unlock()

cleanOrphanedShareFiles(ceremonyDir, voteKeeper, kvStore, logger)
```

The cleanup runs on every `PrepareProposal` invocation, sweeping any
terminal-state round. The companion `cleanOrphanedShareFiles` handles
share files for which the in-memory cache no longer has an entry
(e.g., after a validator restart). ✓

**Caveat.** This sweep only runs while the validator is *proposing*
or otherwise running `PrepareProposal`. If a validator goes down
permanently with terminal-state rounds outstanding, their share files
linger on disk until the operator manually cleans them up. Acceptable
because:

- The disk is presumed to be operator-controlled (`0600` perms).
- The shares are useless without the matching `pallas.sk`.

**REGISTERING-timeout `CEREMONY_FAILED` does not require cleanup.**
At that phase no `share.<roundID>` file has been written yet (it's
only written during the ack phase). The coefficients file may have
been written though — it is cleaned up by the natural completion
path (`zeroAndDeleteCoeffsFile` at `ackDKGRound` exit). On
REGISTERING timeout, the round never reaches ack, so `coeffs.*`
files may persist for the round. **LOW** — would be cleaner if the
partial-decrypt sweep also called a `zeroAndDeleteCoeffsFile`
counterpart on `CEREMONY_FAILED` rounds.

**Remediation:**

1. Validator-side health-check at startup: if `pallas_sk_path` is
   set, require `ea_sk_path` to also be set (and writable), else fail
   loudly. (F4)
2. Add a coefficients sweep in `prepare_proposal_partial_decrypt.go`
   mirroring the share sweep, to clean `coeffs.<roundID>` for
   `CEREMONY_FAILED` rounds. (F5)
3. Document the FS-zeroization limitation (F3) in the operator
   runbook.

---

## Class G — ProcessProposal does not crypto-validate DKG contributions  →  MEDIUM

`validateInjectedDKGContribution` (`app/process_proposal.go:231-269`)
checks:

- round status PENDING + ceremony REGISTERING
- creator is a ceremony validator
- no duplicate contribution
- creator matches block proposer

It does **not** check:

- `len(msg.FeldmanCommitments) == expectedThreshold`
- each commitment is a valid non-identity Pallas point
- `len(msg.Payloads) == n - 1`
- payloads cover all other validators exactly once
- each payload's `EphemeralPk` is a valid Pallas point
- each payload's `Ciphertext` has length 48 (= 32 + 16)

By contrast, `ContributeDKG` (`x/vote/keeper/msg_server_ceremony.go:137-189`)
performs *all* of these checks at FinalizeBlock time.

**Impact.** A malicious block proposer (or a faulty mempool client
that bypasses validation) can include a syntactically-malformed
`MsgContributeDKG` in their proposed block. `ProcessProposal`
accepts the block. `FinalizeBlock` then records the tx as failed and
**does not append the contribution** to `round.DkgContributions`. The
malicious proposer has used their slot without contributing.

CometBFT round-robin proposer rotation means the bad actor can stall
the ceremony only when they're proposer — but if they're a ceremony
validator, **they can repeatedly inject garbage every time they
propose**, never contributing valid material. The round eventually
times out and the EndBlocker jails them
(`x/vote/keeper/keeper_ceremony_jailing.go`), but only after
`DefaultContributionTimeout = 600 s` of stalling.

A simple symmetry — running the same syntactic / Pallas-point checks
in `ProcessProposal` — would *reject the block* outright and skip to
the next proposer, cutting the stall from `n × block_time` to one
block.

**Severity.** MEDIUM. Soft liveness DoS, not safety. But trivial to
fix and aligns with the existing pattern for partial-decrypt and
tally messages (which *do* delegate to `ValidateRoundForTally` /
`ValidateTallyCompleteness`).

**Remediation.** Hoist the syntactic checks from
`msg_server_ceremony.go:137-189` (commitment count, payload count and
coverage, Pallas-point validation) into
`validateInjectedDKGContribution`. To avoid duplication, extract a
shared `ValidateDKGContributionShape(round, msg)` helper used by both
`ProcessProposal` and `ContributeDKG`.

---

## Class G2 — Threshold-sized survivor set can time out at tally  →  MEDIUM

**Issue.** The DEALT timeout path can strip non-ackers and activate a round
with only exactly `t` surviving validators. If one survivor is malicious and
later withholds partial decryptions, the honest survivors have only `t-1`
partials. The tally phase cannot reach `CountPartialDecryptionValidators >=
threshold`, and the round eventually finalizes with `TallyTimedOut=true`.

This is a liveness variant of the corrupted-share gap: a malicious contributor
can submit structurally valid commitments and malformed encrypted shares to
selected victims. Victims fail Feldman verification in `ackDKGRound` and do not
ack, but the attacker can remain in the acked set.

**Mitigation.** Use a timeout quorum distinct from the tally threshold:

```
f(n) = floor((n - t(n)) / 2)
x(n) = y(n) = f(n)
required(n) = max(t(n) + x(n), n - y(n))
```

Both bounded-subset contribution finalization (vote-sdk#323) and DEALT ack
timeout activation should require at least `required(n)` survivors. For `n=5`,
this gives `t=3` and `required=4`, so one surviving partial-decryption
withholder still leaves three honest partials.

**Residual risk.** This does not prove encrypted-share correctness. It only
ensures the activated survivor set has liveness slack. The direct cryptographic
fix remains per-recipient verifiable encryption in `MsgContributeDKG`, or a
complaint/justification protocol that excludes and slashes bad dealers before
activation.

---

## Class H — Proposer-equivocation safety  →  OK

`ValidateProposerIsCreator` (`x/vote/keeper/keeper_ceremony.go:182-199`)
resolves the block proposer at FinalizeBlock time from
`sdkCtx.BlockHeader().ProposerAddress` (consensus-bound, not from the
tx itself):

```182:199:x/vote/keeper/keeper_ceremony.go
proposerConsAddr := sdk.ConsAddress(sdkCtx.BlockHeader().ProposerAddress)
val, err := k.stakingKeeper.GetValidatorByConsAddr(ctx, proposerConsAddr)
if err != nil {
    return fmt.Errorf("%w: failed to resolve block proposer: %v", types.ErrInvalidField, err)
}
if val.OperatorAddress != creator {
    return fmt.Errorf(...)
}
```

If a validator equivocates (proposes two distinct blocks at the same
height), CometBFT's evidence pipeline + the Cosmos `slashing` module
handle double-sign slashing. The DKG state machine is per-round, not
per-block; a duplicate contribution from the same valoper is rejected
by `FindContributionInRound`
(`msg_server_ceremony.go:133-135`). ✓

The handler also rejects `CheckTx` / `ReCheckTx` mempool submission
(line 185-187), so no path exists to inject ceremony txs through the
mempool. ✓

---

## Class I — Ack "signature" is a public digest  →  OK (by design) · INFORMATIONAL

```398:408:x/vote/keeper/msg_server_ceremony.go
func sha256AckDigest(eaPk []byte, validatorAddress string) []byte {
	h := sha256.New()
	h.Write([]byte(types.AckDigestDomain))
	h.Write(eaPk)
	h.Write([]byte(validatorAddress))
	return h.Sum(nil)
}
```

This is a deterministic public function. Any observer of `ea_pk` can
compute the "ack signature" for any validator. The keeper relies on
two facts for safety:

1. Only the block proposer may inject `MsgAckExecutiveAuthorityKey`
   (`ValidateProposerIsCreator`).
2. The proposer of block `H` is determined by consensus and cannot
   be impersonated.

So the digest is a **commitment witness** (the proposer asserts
"validator X has received and verified their share for ea_pk Y"), not
a signature. Replay across rounds is impossible because `ea_pk`
differs (DKG output) and round IDs are unique.

**Potential confusion.** The field name `AckSignature` in
`MsgAckExecutiveAuthorityKey` suggests cryptographic signing.
**INFORMATIONAL** — consider renaming to `AckDigest` /
`AckCommitment` in a future proto bump for clarity. Current behavior
is safe.

---

## Class J — DLEQ transcript binding  →  MOSTLY OK · LOW

### J1. Partial-decryption DLEQ transcript

```261:272:crypto/elgamal/dleq.go
func pdDleqChallenge(G, VKi, C1, Di, R1, R2 curvey.Point) curvey.Scalar {
	h, _ := blake2b.New256(nil)
	h.Write([]byte(pdDleqDomainTag))
	h.Write(G.ToAffineCompressed())
	h.Write(VKi.ToAffineCompressed())
	h.Write(C1.ToAffineCompressed())
	h.Write(Di.ToAffineCompressed())
	h.Write(R1.ToAffineCompressed())
	h.Write(R2.ToAffineCompressed())
	digest := h.Sum(nil)
	return new(curvey.ScalarPallas).Hash(digest)
}
```

Bound: `G, VK_i, C1, D_i, R1, R2 + domain tag "svote-pd-dleq-v1"`.

**Not bound directly:** `round_id`, `validator_index`,
`(proposal_id, vote_decision)`. These are *implicitly* bound through
cryptographic relationships:

- `VK_i` = `EvalCommitmentPolynomial(round.FeldmanCommitments, i)`
  → implicitly binds `round_id` (the round's commitments) and
  `validator_index` (`i`).
- `C1` is the C1 component of the accumulator ciphertext for
  `(round, proposal_id, vote_decision)` → implicitly binds the
  ballot accumulator.

For two DLEQ proofs from the same validator on the same accumulator
to be confusable, the attacker would need a collision on
`(VK_i, C1)` across rounds or accumulators — overwhelmingly improbable
(2^-256-ish) given Pallas's scalar field size and the freshness of
DKG outputs and `r * G` randomizers in ElGamal.

**LOW finding.** The implicit binding is sound but fragile against
future refactors (e.g., if `VK_i` ever stops being round-specific, or
if some shared cache reuses `(VK_i, C1)` across contexts). Adding
explicit `round_id || validator_index || proposal_id || vote_decision`
into the Fiat-Shamir transcript is a one-line change and makes the
binding self-evident.

### J2. Tally-level DLEQ (`dleqChallenge`) — unused in threshold mode

The `dleqChallenge` / `GenerateDLEQProof` / `VerifyDLEQProof` family
(`crypto/elgamal/dleq.go:131-142`) is for **full-key** ElGamal
decryption (`log_G(ea_pk) = log_C1(C2 - v*G)`). In threshold mode,
the full `ea_sk` is never reconstructed, and only the per-validator
`PartialDecryptDLEQ` is used at the keeper level
(`msg_server_tally_decrypt.go:288`).

Confirmed by grep — `VerifyDLEQProof` is referenced only in tests
and in the legacy single-dealer code path (which `SubmitTally`
no longer invokes for threshold rounds). ✓

### J3. Domain-tag separation between the two DLEQ variants

Two distinct tags are used: `"svote-dleq-v1"` (full-key) and
`"svote-pd-dleq-v1"` (per-validator). This prevents cross-protocol
replay between the two even though the proof structure is identical.
✓

**Remediation.**

1. Extend `pdDleqChallenge` to bind `round_id` and `validator_index`
   explicitly. Backwards-incompatible change — gate by a chain
   upgrade height or version constant.
2. (Optional) Bind `proposal_id`, `vote_decision` for full
   accumulator binding.

---

## Class K — Audit preservation of failed ceremony state  →  OK

`docs/session-status-lifecycle.md` and module EndBlocker behavior
preserve `round.DkgContributions` (including ECIES payloads and
Feldman commitments) on `CEREMONY_FAILED`. The transition is
`PENDING → CEREMONY_FAILED` rather than deletion. Grep for
`DkgContributions = nil` returns no matches. ✓

This means a post-hoc auditor can:

1. Recover every commitment vector and every encrypted payload.
2. With operator cooperation (Pallas secret keys), decrypt every
   share and verify or reproduce the failure deterministically.

This is a property strictly stronger than what most TSS systems
provide and is a deliberate design strength.

---

## Class L — Curve / library (`mikelodder7/curvey`)  →  OPEN · INFORMATIONAL

The codebase pins `github.com/mikelodder7/curvey v1.1.1`. This is a
fork of the original `coinbase/kryptology` codebase. Open items the
third-party auditor should address (not directly verifiable from
this repo):

1. **Constant-time scalar operations.** Pallas scalar mul should be
   constant-time. Verify `curvey` uses Montgomery ladder or constant-time
   point multiplication.
2. **Fuzzing / known issues.** Search GitHub Issues / Advisories on
   `mikelodder7/curvey` for crypto-relevant bugs. Compare with
   upstream `coinbase/kryptology` for any unbackported fixes.
3. **Random scalar generation.** `Random(rng)` may silently return
   `nil` on RNG failure — code defensively uses `Hash(seed[:])`
   pattern to avoid this. Confirm this is correct.
4. **Pallas conformance.** Test vectors against the Pasta reference
   implementation (`zcash/pasta_curves`) — see
   `crypto/elgamal/crossval_test.go`. Should be expanded.

---

## Class M — Concentration / governance  →  OPEN · INFORMATIONAL

1. The **block proposer** is the sole source of ceremony messages
   each round. A long-run malicious proposer rotation could stall
   rounds (Class G), but cannot break safety.
2. The existing `audit/fractal-development-audit-2026q1.pdf` (April
   2026) predates the threshold-DKG redesign in `prepare_proposal_ceremony.go`.
   The relevant single-dealer cmd `cmd/svoted/cmd/encrypt_ea_key.go`
   is **legacy** and not on the live ceremony path. The auditor
   should request whether Fractal's scope explicitly covered the
   threshold path.
3. `min_ceremony_validators` is a genesis parameter and not
   adjustable post-genesis. **OPEN** — should it be govern-mutable
   to allow validator-set growth?

---

## Thorchain bug families  →  NOT APPLICABLE

Three families to explicitly rule out:

1. **CVE-2023-33241 — Paillier modulus validation in tss-lib.**
   ```bash
   $ rg -n 'paillier|Paillier' .
   ```
   Returns: zero matches in production code. **Confirmed N/A.**

2. **Trail of Bits Feb 2024 — complaint-round DoS in
   FROST/GG18/GG20/DMZ21.** vote-sdk has **no complaint round** —
   the protocol aborts the entire ceremony on first Feldman
   verification failure (`ackDKGRound` returns error → ack tx never
   injected → round times out via EndBlocker). The specific
   "raise-threshold" attack lever is therefore absent, though the
   broader "single bad party can stall the ceremony" class is
   present (Class G mitigates).

3. **MPC ECDSA nonce-share leakage (GG18/GG20).** vote-sdk performs
   no threshold *signing*. There is no MPC ECDSA, no MPC Schnorr,
   no nonce-sharing protocol. **Confirmed N/A.**

---

## Severity table

| Severity | Count | Items |
|----------|-------|-------|
| CRITICAL | 0 | — |
| HIGH | 0 | — |
| MEDIUM | 4 | A2 (insecure default `min_ceremony_validators=1`), G2 (threshold-sized survivor liveness), C (no Schnorr PoK), G (ProcessProposal symmetry) |
| LOW | 5 | D3 (D_i identity check), E1 (KDF domain tag), F4 (empty ea_sk_path), F5 (coeffs cleanup on CEREMONY_FAILED), J1 (DLEQ explicit round/validator binding) |
| INFORMATIONAL / OPEN | 5 | B (last-mover bias scope), I (rename `AckSignature`), L (curvey diligence), M (governance), F3 (FS zeroization caveat) |

---

## Suggested remediation order

1. **MEDIUM-A2:** Make `defaultMinCeremonyValidators` ≥ 3 (or
   require explicit opt-in for single-validator test chains), and
   add genesis-validation CI that fails for mainnet-style
   `chain_id` with `< 3` validators. One-line code change plus
   doc/runbook update.
2. **MEDIUM-G2:** Wire `required(n) = max(t + f, n - f)` into
   contribution-timeout and DEALT ack-timeout activation so timeout
   finalization never leaves only exactly `t` survivors.
3. **MEDIUM-C:** Add Schnorr PoK on `a_{i,0}` in `MsgContributeDKG`.
   Single new field, deterministic verify, requires a chain upgrade.
4. **MEDIUM-G:** Extract `ValidateDKGContributionShape` and call from
   both `ProcessProposal` and keeper. Pure refactor; no new field.
5. **LOW-J1:** Bind `round_id` and `validator_index` into the
   partial-decrypt DLEQ Fiat-Shamir transcript. Version-gate the
   change.
6. **LOW-E1:** Add `"svote-ecies-v1"` domain tag (+ optional
   `round_id` / `recipient_valoper`) to `deriveKey`. Breaks wire
   compatibility — version-gate.
7. **LOW-F4/F5:** Startup health-check on `ea_sk_path`; add coeffs
   cleanup sweep on `CEREMONY_FAILED`.
8. **LOW-D3:** Symmetrical `IsIdentity` check on `D_i` in
   `VerifyPartialDecryptDLEQ`.
9. **INFO-L/M:** Brief independent diligence on `mikelodder7/curvey`
   and update Fractal audit scope notes.

---

## Things this self-audit could NOT verify (require external review)

1. Constant-time-ness of `mikelodder7/curvey`'s Pallas scalar
   multiplication.
2. Side-channel resistance of the validator binary (cache timing,
   power analysis — out of scope for source review anyway).
3. Pallas conformance with the official Pasta reference at full
   precision (only smoke-tested in
   `crypto/elgamal/crossval_test.go`).
4. Whether Fractal's `fractal-development-audit-2026q1.pdf` scope
   actually covered the threshold-DKG redesign (PDF date predates
   most of `prepare_proposal_ceremony.go`).
5. Real-world byzantine simulation against the EndBlocker
   timeout/jailing path under proposer-rotation adversaries.

---

*Generated: 2026-05-20 — companion to `audit/dkg-tss-audit-pack.md`
and Linear ZCA-539. Author: in-IDE LLM auditor (Claude Opus 4.7).
Treat as a triage baseline, not a substitute for independent
third-party crypto audit.*
