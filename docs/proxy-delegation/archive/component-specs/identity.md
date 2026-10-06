# Component spec: proxy-delegate identity, verifier service, on-chain registry, directory and profiles

Draft 1, 2026-10-05. Scope: section 7 of the proxy-delegation (B2) plan. This component covers delegate keys, proof formats, the verifier service, the on-chain registry contract, signed directory snapshots, profile pictures, curation and ops.

## 0. Conventions

**Labels.** **[code]** means verified in source at file:line. **[doc]** means a document or dossier claim. **[inference]** means my reasoning or design. Every normative rule in this spec is design ([inference]) unless it carries another label.

**Path prefixes:**
- `chain:` = `/Users/czstudio/Documents/vote-sdk/.claude/worktrees/vote-delegation-planning-29b451`
- `zv:` = `scratchpad/src/zcash_voting`
- `vizor:` = `scratchpad/src/vizor-wallet`
- `dls:` = `/Users/czstudio/Documents/vizor-deeplink-server`
- `dossier:` = `scratchpad/dossiers`
- Prototype: `scratchpad/proto-identity/proto.py` (bech32m, post length and fingerprint test vectors)

**Terminology (kept distinct everywhere):**

| Term | Meaning |
|---|---|
| delegation / ZKP1 / `MsgDelegateVote` | Existing meaning: notes move into a VAN bound to a voting hotkey. This component does not touch it. |
| proxy delegation | A delegator hands weight to a proxy delegate (DC/ZKP4, owned by other components). |
| proxy delegate ("delegate" in the UI) | A registered public person whose pool is routed by signed route messages. |
| `delegate_index` | u32 >= 1 assigned by the chain registry. Global, append-only, never reused. |
| DIK | Delegate identity key (Ed25519). |
| DRK | Delegate route key (Ed25519). |
| subject | The external account bound to a DIK: an X user, GitHub user or DNS domain. |
| verifier | Service that checks proofs and signs attestations. Its keys are registered on chain. |
| attestation | Verifier signature(s) that authorize one registry write. |
| directory | Off-chain signed documents (registry doc, profiles doc, avatars) published by the directory publisher. |

**User-facing naming proposal:**
- **Delegator side:** a "Delegates" section and a "Delegate your vote" CTA. Copy such as "Choose up to 10 delegates", "Keep the rest to vote yourself", and the poll-card state "Delegated to @alice".
- **Delegate side:**

  | UI copy | Action |
  |---|---|
  | "Become a delegate" | Onboarding |
  | "Delegate key phrase" | Backup |
  | "Vote as a delegate" | Public routes |
  | "Pause delegate voting" | Freeze |
  | "Retire delegate identity" | Revoke |

- **Rename existing ZKP1 copy.** Change "voting delegation" to "voting authorization" in today's ZKP1 copy. [code] Current strings in `vizor:lib/src/features/voting` include 'Voting delegation', 'Preparing voting delegation', 'Finishing voting delegation', and the Ledger notice "...authorizes this voting delegation". After the rename, "delegate" in the UI only ever means a person.
- **Code names.** New code uses `proxy_delegate_*`. ZKP1 identifiers are unchanged.

---

## 1. Key model

### 1.1 Decision
**Two Ed25519 keys per delegate, both app-level software keys, independent of any wallet account and of funds.** In B2 a delegate never holds VANs or proves anything; it only signs. So a delegate needs no ZEC, and hardware-wallet users can be delegates [inference].

| Key | Appears in | Signs | Exposure |
|---|---|---|---|
| **DIK** (identity) | X/GitHub/DNS proof, on-chain entry, directory | Registration PoP, delegate statement, DRK rotation, DIK change, revoke, freeze, cancel-pending | Used rarely; can be kept "cold" |
| **DRK** (route) | On-chain entry only | Route messages (one batch per round), freeze, cancel-pending | Hot: used on every route |

**Why two keys:**
- If the hot route key leaks, the delegate rotates it immediately with the DIK. The delegate keeps its `delegate_index`, its X proof and delegators' favorites, with no new post and no verifier round-trip.
- A cold DIK can stay offline, on a separate device, or later on a hardware Ed25519 signer.
- Rejected alternative: a single DK. A theft of a single DK lets the attacker do an immediate co-signed rotation and permanently lock the owner out of the entry.

**Why Ed25519:**
- The chain verifies it in Go stdlib (`crypto/ed25519`), which is cheap and needs no cgo. The repo already uses Ed25519 with a `{key_id, alg, sig}` envelope ([code] `chain:internal/pirupdate/attestation.go:6,20-45`).
- zcash_voting already links `ed25519-dalek` 2 ([code] `zv:zcash_voting/Cargo.toml:51`; `zv:zcash_voting/src/config/mod.rs:65`), so Vizor's Rust core can sign.
- The tooling is universal (CLI, WebCrypto, Nostr/Keybase-style ecosystems).
- Rejected alternatives:
  - RedPallas: verifiable via cgo (`sv_verify_redpallas_sig`, [code] `chain:ffi/redpallas/verify_ffi.go:27-60`), but it needs FFI on every route and gives no benefit because no circuit consumes DK.
  - secp256k1/ADR-036: would tie identity to Cosmos accounts, which the vote chain only creates through coordinators (dossier chain §8).
  - Orchard address keys: proving control needs a ZK construction (ZIP 304 covers Sapling only, [doc] dossier identity §9).

### 1.2 Encodings and fingerprints
- **Public keys.** 32-byte RFC 8032 Ed25519 keys, encoded as bech32m with HRP `zvdk` (DIK) or `zvdr` (DRK). The result is 63 characters.
  - HRPs are network-agnostic. Every signed digest carries the network string, so cross-network replay is impossible.
  - Test vector: pk = bytes 0x00..0x1f gives `zvdk1qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzs23v9ccrydpk8qarc0sxs34am` (prototype).
- **Key validity.** The chain, the verifier and the wallet all require a canonical point encoding that is not small-order. Use `filippo.io/edwards25519` `SetBytes` plus a cofactor-8 identity check in Go, and `VerifyingKey::from_bytes` plus `is_weak()` in Rust. DIK must differ from DRK.
- **Fingerprint.**
  - Definition: `fp` = top 60 bits of `BLAKE2b-256(personal = "ZcashVoteDlgFP__", data = dik_pk)`, written as 12 bech32-charset characters grouped `xxxx-xxxx-xxxx`.
  - Test vector: the key above gives `dz9z-7esv-stpz`.
  - Strength: matching a fingerprint by grinding keys costs about 2^60 keygens [inference].
- **Identicon.** A 5x5 mirrored grid plus a hue taken from the fingerprint bytes, rendered locally with no network.
- **Canonical display.** `#17 · dz9z-7esv-stpz`. The fingerprint is always shown next to the handle.

### 1.3 Derivation (SDK/Vizor convention, not consensus)
- **Delegate key phrase.** BIP-39, 24 words (256-bit entropy, English), with an empty passphrase, producing a 64-byte `seed`. [code] Vizor already depends on `bip0039` (`vizor:rust/Cargo.toml:29`).
- **Derivation functions:**
  - `dik_sk(i) = BLAKE2b-256(key = seed, personal = "ZcashVoteDIK_v1_", data = u32le(i))`
  - `drk_sk(i, j) = BLAKE2b-256(key = seed, personal = "ZcashVoteDRK_v1_", data = u32le(i) || u32le(j))`
  - Each output is used as an RFC 8032 Ed25519 secret seed.
  - `i` is the identity generation (bumped on a DIK change). `j` is the route generation (bumped on a DRK rotation).
- **Restore.** Derive `i` in [0,8) and `j` in [0,64), then match against the chain registry with `GET .../proxy-delegates/by-key/{pk}`. Delegates are public, so this lookup leaks nothing new.
- **Why not the wallet seed in v1 (rejected alternative):**
  - Hardware accounts have no seed in Vizor.
  - ZIP-32 registered derivation needs an assigned ZIP number ([doc] dossier client.summary).
  - Keeping it separate isolates the public persona from funds and allows a dedicated delegate device.
- **Bring your own key.** Consensus only ever sees public keys. The CLI (section 10) accepts external Ed25519 signers. Hardware DIK through Ed25519 off-chain-message apps is future work.

### 1.4 Storage in Vizor
- **Scope.** App-level secure storage, not per account:
  - `voting_proxy_delegate_root_v1` holds the BIP-39 entropy. Reading it needs explicit re-auth, like mnemonic access.
  - `voting_proxy_delegate_drk_v1` holds the current DRK seed. It is session-unlocked, like hotkeys ([code] `vizor:lib/src/core/storage/app_secure_store.dart:59,729-738`).
  - Non-secret metadata: network, index, `i`, `j`, `key_epoch`.
- **Signing.** All signing happens in Rust. Dart never sees secrets, matching today's signer boundary ([doc] dossier vizor §1.5).
- **Lifetime.** Account deletion does not remove the delegate profile. Only an explicit "Remove delegate profile from this device" does, and it requires confirming the phrase backup first.

### 1.5 Key lifecycle rules (normative; enforced on chain, section 3)

| Operation | Signed by | Effect | Timing | Can be cancelled by |
|---|---|---|---|---|
| Register | DIK + DRK (PoP) + verifier threshold | New entry, ACTIVE, `key_epoch = 0` | Immediate | n/a |
| Rotate route key | Current DIK + new DRK | DRK replaced, `key_epoch++`, FROZEN becomes ACTIVE | Immediate | n/a |
| Change identity (co-signed) | Current DIK + new DIK + new DRK | Pending | `identity_change_delay` (default 72 h) | Current DIK or current DRK |
| Recover identity (old DIK lost) | New DIK + new DRK + verifier `recover_threshold` (fresh proof by the same subject) | Pending | `identity_recovery_delay` (default 7 d) | Current DIK or current DRK |
| Cancel pending | Current DIK or DRK | Pending cleared; its reserved keys stay burned | Immediate | n/a |
| Freeze ("pause") | Current DIK or DRK | FROZEN: routes rejected | Immediate | Undone only by a route-key rotation (DIK) |
| Revoke ("retire") | Current DIK | REVOKED (terminal); pending cleared | Immediate | Never |
| Coordinator suspension | Coordinator N-of-M action | `coordinator_suspended`: routes rejected | Immediate | Coordinator action only |

**Rationale:**
- Every DIK change is delayed and publicly visible as a "Key change pending" warning in all wallets.
- Revocation is immediate and monotonic. Whoever holds the current DIK can always end the entry before a hostile DIK change takes effect.
- An attacker who steals keys can therefore at most route this round's un-routed proposals. The same damage results from a stolen DRK.

### 1.6 Compromise response matrix

| Event | Attacker can | Owner response | Delegator impact |
|---|---|---|---|
| DRK stolen | Route un-routed proposals now (final) | Rotate DRK with the DIK (immediate), or freeze with DRK then rotate later | Routes already made stand |
| DIK stolen (copy) | Rotate DRK; start a DIK change (72 h pending) | Cancel the pending change; if contested, **revoke** (irreversible) and re-register a new index with new keys and a new post | Un-routed proposals abstain after revoke; the wallet shows "Retired" and a "re-registered as #N" link (never auto-migrated) |
| X account taken over | Post a new key; request recovery (7 d pending) | Cancel with DIK or DRK; report; the verifier denylists the subject | Warning banner on the entry ("X account advertises a different key") |
| Both keys and phrase lost | Nothing | Recovery via a fresh X proof; nobody cancels, so it takes effect after 7 d | Entry keeps its index |
| Verifier key compromised | Register fake entries; start recoveries | Section 8 playbook | Recoveries are visible for 7 d; fake entries hidden by the publisher |

---

## 2. Proof formats

### 2.1 Marker line (shared by all providers)
- **Format:** the exact ASCII line `zcash-vote-delegate v1 <zvdk1...>`.
- **Parser regex:** `(?m)^[ \t]*zcash-vote-delegate v1 (zvdk1[qpzry9x8gf2tvdw0s3jn54khce6mua7l]{58})[ \t]*$`
- **Parse procedure:** normalize text to NFC with LF line endings, then require **exactly one distinct** matched key whose bech32m checksum is valid.
- **Design constraints:**
  - The marker is brand-neutral, contains no URL, no `.`, `#`, `$` or `@`, and has no `:` scheme form. X auto-linking therefore leaves it alone.

### 2.2 X post
**Template** (137 characters, measured in the prototype; well under 280):
```
I control this Zcash shielded voting delegate key.
zcash-vote-delegate v1 zvdk1qqqsyqcyq5rqwzqfpg9scrgwpugpzysnzs23v9ccrydpk8qarc0sxs34am
```

**Rules:**
- Only the marker line is parsed. Other text is free, and the first line is a suggestion.
- **Editing:** if the API reports an edited post, verify the latest version. A post whose latest edit dropped the marker is a missing proof.
- **Competing posts:** if the subject later posts a different key, the entry gets the flag `key_mismatch` and a warning. It does not re-bind until a chain-side change (co-signed or recovery) lands.
- **Withdrawal:** deleting the post withdraws the claim (section 4.6 grace periods apply).
- **Binding:** the binding is to the numeric X user id. The handle is display only ([doc] dossier identity §5 and §6).

### 2.3 GitHub gist
- **Proof location:** a public gist owned by the user, containing file `zcash-vote-delegate.txt` with the marker line.
- **Locator:** gist URL or id.
- **Subject:** the numeric `owner.id` from `GET https://api.github.com/gists/{id}`. Display it as `github:login`.

### 2.4 Domain
Either method below proves control:
- **DNS TXT** at `_zcash-vote-delegate.<domain>`. Value: the marker line.
- **HTTPS** `https://<domain>/.well-known/zcash-vote-delegate.json` containing `{"version":1,"keys":["zvdk1..."]}`.
  - No redirects, at most 4 KiB, valid TLS.

Verification rules:
- If both methods are present they must not conflict.
- DNS answers come from two independent DoH resolvers that must agree. DNSSEC is validated when present.

### 2.5 Subject canonical forms (hash inputs)

| Provider | Code | `subject_id` canonical ASCII |
|---|---|---|
| X | 1 | Decimal numeric user id, no leading zeros |
| GitHub | 2 | Decimal numeric user id |
| DNS | 3 | Lowercase A-label FQDN, no trailing dot. It must be a registrable domain or a subdomain of one per the Public Suffix List; IP literals are rejected. |

### 2.6 Signed digests (exact layouts)

**Conventions** (consistent with chain sighashes, [code] `chain:x/vote/types/sighash.go:11-20,33-58`):
- Hash: BLAKE2b-256, unkeyed, unpersonalized.
- The ASCII domain comes first with no terminator; fields follow in the order listed.
- Encodings: `u8`, `u32le` and `u64le` are fixed width; keys are 32 raw bytes; `net` = `u8 len || ASCII network` with network in `{"main","test","regtest"}`.
- Every Ed25519 signature is pure Ed25519 over the 32-byte digest.

| Digest | Domain | Fields | Signed by |
|---|---|---|---|
| `subject_commit` | `SVOTE_PROXY_DELEGATE_SUBJECT_V1` | `u8 provider, u16le len, subject_id, salt[32]` | Hash only |
| `reg` | `SVOTE_PROXY_DELEGATE_REGISTER_V1` | `net, dik, drk, u8 provider, subject_commit, attestation_id[32]` | DIK and DRK (PoP plus the subject binding) |
| `attest` | `SVOTE_PROXY_DELEGATE_ATTEST_V1` | `net, u8 kind (1=REGISTER, 2=RECOVER), u32 delegate_index (0 for REGISTER), dik, drk, u8 provider, subject_commit, attestation_id, u64 not_before, u64 expires_at` | Each verifier |
| `rotate_route` | `SVOTE_PROXY_DELEGATE_ROTATE_ROUTE_KEY_V1` | `net, u32 index, u32 key_epoch, new_drk` | Current DIK, new DRK |
| `change_identity` | `SVOTE_PROXY_DELEGATE_CHANGE_IDENTITY_V1` | `net, u32 index, u32 key_epoch, new_dik, new_drk` | Current DIK, new DIK, new DRK |
| `recover_identity` | `SVOTE_PROXY_DELEGATE_RECOVER_IDENTITY_V1` | `net, u32 index, u32 key_epoch, new_dik, new_drk, attestation_id` | New DIK, new DRK |
| `pending_id` | `SVOTE_PROXY_DELEGATE_PENDING_ID_V1` | `u32 index, u32 key_epoch, new_dik, new_drk, u64 submitted_height` | Hash only |
| `cancel_pending` | `SVOTE_PROXY_DELEGATE_CANCEL_PENDING_V1` | `net, u32 index, u32 key_epoch, pending_id` | Current DIK or DRK |
| `freeze` | `SVOTE_PROXY_DELEGATE_FREEZE_V1` | `net, u32 index, u32 key_epoch` | Current DIK or DRK |
| `revoke` | `SVOTE_PROXY_DELEGATE_REVOKE_V1` | `net, u32 index, u32 key_epoch` | Current DIK |
| `route` (contract offered to the chain/route component) | `SVOTE_PROXY_DELEGATE_ROUTE_V1` | `round_id[32], u32 index, u32 key_epoch, u32 n, n x (u32 proposal_id, u32 decision)` | Current DRK |

**Notes on the digests:**
- `key_epoch` provides replay protection across key changes. Every key change bumps it, so pre-signed freeze or route signatures die on rotation.
- `attestation_id` is 32 random bytes chosen by the client and is single-use on chain.
- The numeric `x_user_id` binding is two-way:
  - X to key: the post contains the DIK.
  - Key to X: the DIK signs `reg`, which commits to `subject_commit = H(provider, subject_id, salt)`.
- Rejected alternative: canonical-JSON (JCS) signatures. The JSON documents below are transport and evidence only; signatures are always over these fixed binary digests, which avoids cross-language canonicalization bugs.

**Off-chain domains** (wallet and verifier only):
- Each signature is Ed25519 over `domain || "\n" || exact body bytes`. The bodies are stored verbatim, so no canonicalization is needed.

| Domain | Signed by |
|---|---|
| `zcash-shielded-vote:delegate-statement:v1` | DIK |
| `zcash-shielded-vote:delegate-link:v1` | DIK |
| `zcash-shielded-vote:delegate-xdata-removal:v1` | DIK |
| `zcash-shielded-vote:delegate-directory:v1` | Directory key |
| `zcash-shielded-vote:delegate-directory-key:v1` | Offline directory key (key certificates and revocations) |

The style matches [code] `zv:zcash_voting/src/round_auth.rs:8` (`zcash-shielded-vote:round-auth:v2`).

### 2.7 Registration flow (delegate wallet, two steps)
The client cannot know its numeric X id from a post URL, so registration takes two calls.

**Step 1: resolve the proof.**
1. `POST /v1/proofs/resolve` with body `{"network":"main","provider":"x","locator":"https://x.com/alice/status/1844..."}`.
2. Response: `{provider, subject_id, handle, proof_ref, marker_dik, account_created_at, eligible, listing_preview, reasons[]}`.

**Step 2: sign and request the attestation.**
1. The wallet draws a random 32-byte `salt` and a random 32-byte `attestation_id`.
2. It computes `subject_commit` and `reg`, and signs `reg` with the DIK and the DRK.
3. It sends `POST /v1/attestations`:
   ```json
   {"network":"main","kind":"register","dik":"zvdk1...","drk":"zvdr1...",
    "provider":"x","subject_id":"1234567890","subject_salt":"<64 hex>",
    "proof_ref":"1844...","attestation_id":"<64 hex>",
    "dik_sig":"<base64>","drk_sig":"<base64>","delegate_index":null}
   ```
4. Response: `{"attestation":{"kind":1,"attestation_id":"..","not_before":..,"expires_at":..,"signatures":[{"verifier_id":"valar-1","sig":"<base64>"}]},"subject_commit":"<hex>"}`.
5. With t-of-n verifiers, the wallet calls each verifier listed in the static config (section 5.4) and merges the signatures.

**Step 3: submit on chain.**
1. The wallet submits `MsgProxyDelegateRegistry{register}` (section 3) through the vote-server REST.
2. It learns `delegate_index` from the tx event, as it learns leaf indices today.

**Recovery** uses the same calls with `"kind":"recover"` and `delegate_index` set. The verifier then additionally requires:
- the same subject as the entry (recomputed with the stored salt);
- a proof post that contains the new DIK and was created after the entry's last key change;
- manual review if the entry is featured.

### 2.8 Off-chain statements and secondary links
**Delegate statement.** A delegate-authored profile text, replacing the X bio so that no X bio is stored.
- Body: `{"v":1,"network":"main","seq":N,"text":"<= 500 chars","accepting":true,"updated_at":unix}`.
- Signed by the DIK.
- `seq` is strictly increasing.
- At most one URL. It is shown as text and opens externally.

**Secondary links** (for example GitHub alongside X):
- Body: `{"v":1,"network":"main","provider":2,"subject_id":"583231","proof_ref":"<gist id>"}`.
- Signed by the DIK and verified off-chain.
- They never touch the chain; they are shown as extra badges.

---

## 3. On-chain registry (contract for the chain component)

### 3.1 On-chain versus off-chain split
- **On chain:** `delegate_index`, DIK, DRK, `key_epoch`, status, coordinator suspension, provider code, salted `subject_commit`, pending change, heights, the verifier set and params.
- **Never on chain:** handle, name, pfp, bio, post id or user id.
  - Tx bytes are retained forever ([doc] dossier chain §7: blocks retained, state never pruned), so attestations carry only the salted commit.
  - Deleting the salt from the mutable store makes the on-chain value unlinkable (crypto-shredding).

### 3.2 Messages
**Wire format.** Proposed wire tag **0x0B** (the integrator allocates the final value; `0x09`, `0x0B`, `0x0C` and `0x0F` are free and `0x0A` is forbidden).
- [code] `chain:api/codec.go:25-26`: 0x0A is forbidden.
- [code] `:57-59`: `IsVoteTag` is a contiguous range check, so it needs an explicit branch for the new tag.
- [code] `:142-153`: canonical encoding is enforced only for 0x06/0x07. Add this tag to that set.

**Gas and fees.** The message is fee-less with infinite gas, like other custom tags ([code] `chain:app/ante.go:128-129`). It is not round-scoped, so the ante path must skip `ValidateRoundForVoting`.

```proto
message MsgProxyDelegateRegistry {
  oneof op {
    ProxyDelegateRegister        register         = 1;
    ProxyDelegateRotateRouteKey  rotate_route_key = 2;
    ProxyDelegateChangeIdentity  change_identity  = 3;
    ProxyDelegateRecoverIdentity recover_identity = 4;
    ProxyDelegateCancelPending   cancel_pending   = 5;
    ProxyDelegateFreeze          freeze           = 6;
    ProxyDelegateRevoke          revoke           = 7;
  }
}
message ProxyDelegateVerifierSig { string verifier_id = 1; bytes sig = 2; }   // sig 64 B
message ProxyDelegateAttestation {
  uint32 kind = 1;                 // 1 REGISTER, 2 RECOVER
  bytes  attestation_id = 2;       // 32 B, nonzero, single use
  uint64 not_before = 3;           // unix s
  uint64 expires_at = 4;           // unix s
  repeated ProxyDelegateVerifierSig signatures = 5;   // 1..16, unique verifier_id
}
message ProxyDelegateRegister {
  bytes dik_pk = 1; bytes drk_pk = 2; uint32 provider = 3; bytes subject_commit = 4;
  ProxyDelegateAttestation attestation = 5; bytes dik_sig = 6; bytes drk_sig = 7;
}
message ProxyDelegateRotateRouteKey  { uint32 delegate_index = 1; uint32 key_epoch = 2; bytes new_drk_pk = 3; bytes dik_sig = 4; bytes new_drk_sig = 5; }
message ProxyDelegateChangeIdentity  { uint32 delegate_index = 1; uint32 key_epoch = 2; bytes new_dik_pk = 3; bytes new_drk_pk = 4; bytes dik_sig = 5; bytes new_dik_sig = 6; bytes new_drk_sig = 7; }
message ProxyDelegateRecoverIdentity { uint32 delegate_index = 1; uint32 key_epoch = 2; bytes new_dik_pk = 3; bytes new_drk_pk = 4; ProxyDelegateAttestation attestation = 5; bytes new_dik_sig = 6; bytes new_drk_sig = 7; }
message ProxyDelegateCancelPending   { uint32 delegate_index = 1; uint32 key_epoch = 2; bytes pending_id = 3; uint32 signer_role = 4; /*1 DIK, 2 DRK*/ bytes sig = 5; }
message ProxyDelegateFreeze          { uint32 delegate_index = 1; uint32 key_epoch = 2; uint32 signer_role = 3; bytes sig = 4; }
message ProxyDelegateRevoke          { uint32 delegate_index = 1; uint32 key_epoch = 2; bytes dik_sig = 3; }

enum ProxyDelegateStatus { PROXY_DELEGATE_STATUS_UNSPECIFIED = 0; ACTIVE = 1; FROZEN = 2; REVOKED = 3; }
message ProxyDelegatePending { bytes pending_id = 1; uint32 kind = 2; /*1 COSIGNED, 2 RECOVERY*/ bytes new_dik_pk = 3; bytes new_drk_pk = 4; uint64 effective_time = 5; uint64 submitted_height = 6; }
message ProxyDelegateEntry {
  uint32 delegate_index = 1; bytes dik_pk = 2; bytes drk_pk = 3; uint32 key_epoch = 4;
  ProxyDelegateStatus status = 5; bool coordinator_suspended = 6; uint32 suspension_reason = 7;
  uint32 provider = 8; bytes subject_commit = 9;
  uint64 registered_height = 10; uint64 registered_time = 11; uint64 updated_height = 12;
  ProxyDelegatePending pending = 13; uint32 key_change_count = 14; uint64 revoked_height = 15;
}
message ProxyDelegateVerifier    { string verifier_id = 1; /*[a-z0-9-]{1,64}*/ bytes ed25519_pk = 2; string label = 3; }
message ProxyDelegateVerifierSet { repeated ProxyDelegateVerifier verifiers = 1; uint32 register_threshold = 2; uint32 recover_threshold = 3; }
message ProxyDelegateParams {
  string network = 1;                          // "main" | "test" | "regtest"; used in every digest
  bool   registration_enabled = 2;             // gates REGISTER and RECOVER only
  uint64 identity_change_delay_secs = 3;       // default 259200 (72 h)
  uint64 identity_recovery_delay_secs = 4;     // default 604800 (7 d)
  uint64 max_attestation_validity_secs = 5;    // default 259200
  uint32 max_key_changes = 6;                  // default 64 per delegate lifetime
  uint32 max_registry_ops_per_block = 7;       // default 64
  uint32 max_delegates = 8;                    // default 1,000,000 (< 2^20)
}
```

**Coordinator payloads.** These are new members of the closed payload set ([code] `chain:x/vote/keeper/msg_server_coordinator_actions.go:18-32,228-287`; 7-day TTL at `:16`):
- `MsgSetProxyDelegateVerifiers{creator, verifiers, register_threshold, recover_threshold}`:
  - 1..16 verifiers.
  - Unique ids **and unique pubkeys** (dedupe by signer key).
  - Thresholds between 1 and `len(verifiers)`.
- `MsgSetProxyDelegateParams{creator, params}`.
- `MsgSetProxyDelegateSuspension{creator, delegate_index, suspended, reason_code}`:
  - Reason codes: 1 impersonation, 2 reported key compromise, 3 legal order, 4 verifier-compromise review, 99 other.
  - Suspension is the coordinators' only power over an entry. It cannot create, alter keys or re-route.

### 3.3 Validation rules
**Stateless (`ValidateBasic`):**
- All keys are 32 bytes, canonical and not small-order. DIK must differ from DRK, and new keys must differ from each other.
- All signatures are 64 bytes.
- `provider` is in 1..3. `subject_commit` is nonzero.
- Attestation: `kind` matches the op, `attestation_id` is nonzero, `not_before < expires_at <= not_before + 7 d`, and there are 1..16 signatures with unique, well-formed `verifier_id`s.
- `signer_role` is in {1, 2}.

**Stateful (CheckTx and DeliverTx, using the effective view from 3.5 at block time):**
- **Feature gate.** The dormant flag must be enabled. `registration_enabled` gates REGISTER and RECOVER only. Freeze, revoke, cancel and rotate are always allowed, so delegates can protect themselves while registration is paused.
- **Attestation:**
  - `not_before <= blockTime + 300` and `blockTime < expires_at`, with `expires_at - not_before <= max_attestation_validity_secs`.
  - `attestation_id` unused.
  - **Every** signature must come from a `verifier_id` in the current set and verify over `attest`; any unknown or invalid signature rejects the tx.
  - The count of distinct verifiers is at least `register_threshold` or `recover_threshold`.
  - There is no admin bypass: no code path writes an entry without this check, except genesis import.
- **Register:**
  - `next_index <= max_delegates`.
  - The DIK and DRK do not appear in the key reverse index (keys are never reused).
  - `dik_sig` and `drk_sig` verify over `reg`.
- **Rotate route key:**
  - Status ACTIVE or FROZEN, `key_epoch` equals the effective epoch, and `key_change_count < max_key_changes`.
  - The new DRK is unused, and both signatures verify.
- **Change or recover identity:**
  - Status ACTIVE or FROZEN, no pending change, epoch matches, and the change count is under the cap.
  - The new keys are unused, and the signatures verify.
  - Recovery additionally checks the attestation (kind 2) with `delegate_index = index`, `dik/drk = new keys`, and `provider/subject_commit` taken **from the entry**.
- **Cancel pending:** a pending change exists, `blockTime < effective_time`, `pending_id` matches, the epoch matches, and the signature is by the effective DIK (role 1) or DRK (role 2).
- **Freeze:** status ACTIVE; the signature is by DIK or DRK.
- **Revoke:** status is not REVOKED; the DIK signature verifies.
- **CheckTx cost.** At most 18 Ed25519 verifications (about 1-2 ms). Bad signatures fail CheckTx and are not gossiped.

### 3.4 State keys
Proposed family prefix **0x1A**. [code] Prefixes 0x01-0x19 except 0x09 and 0x0D are in use (`chain:x/vote/types/keys.go:98-200`). The route and pool components should use a different family, for example 0x1B.

| Key | Value |
|---|---|
| `0x1A 0x01 u32be(index)` | `ProxyDelegateEntry` |
| `0x1A 0x02 pk[32]` | `u32be(index) u8(role)`: every DIK or DRK ever submitted, including burned pending keys |
| `0x1A 0x03 attestation_id[32]` | `u32be(index)` |
| `0x1A 0x04` | `next_index u32be` (genesis value **1**; index 0 is reserved and never assigned) |
| `0x1A 0x05` | `ProxyDelegateVerifierSet` |
| `0x1A 0x06` | `ProxyDelegateParams` |
| `0x1A 0x07 u64be(seq)` | Phase 2 directory anchor `sha256` (section 5.6) |

This follows the reverse-index uniqueness precedent in [code] `chain:x/vote/keeper/keeper_pallas_registry.go:33-47,96-120`.

### 3.5 Effective view (lazy activation; no EndBlock work)
Pending identity changes take effect lazily on read, so no EndBlock work is needed.

```
Effective(e, t):
  if e.status != REVOKED and e.pending != nil and t >= e.pending.effective_time:
      e.dik_pk, e.drk_pk = e.pending.new_dik_pk, e.pending.new_drk_pk
      e.key_epoch += 1; e.status = ACTIVE; e.pending = nil   // coordinator_suspended untouched
  return e
```

- Every handler, route check and query uses `Effective(entry, blockTime)`. Handlers that write materialize it.
- **Why lazy:** an EndBlock error halts the chain ([doc] dossier chain §1.7), and lazy activation needs no EndBlock work.
- **Change feeds:** `updated_since_height` results must always include entries with a non-nil pending change, so indexers see time-based activations.

### 3.6 Queries, REST, capabilities and events
**REST endpoints:**
- `GET /shielded-vote/v1/proxy-delegates?from_index=&limit<=1000&updated_since_height=` returns `{height, block_time, next_index, entries[] (effective view)}`.
- `GET /shielded-vote/v1/proxy-delegates/by-key/{hex}` (delegate tooling only; wallets acting as delegators must page the whole list instead).
- `GET /shielded-vote/v1/proxy-delegate-verifiers` and `GET /shielded-vote/v1/proxy-delegate-params`.
- `POST /shielded-vote/v1/proxy-delegate-registry`: strict JSON, wraps the wire tx.

**`ProtocolCapabilities`** ([code] `chain:proto/svote/v1/query.proto:73-77`) gains `bool proxy_delegate_registry = 4; uint32 proxy_delegate_registry_wire_tag = 5;`.

**Events** (all with `delegate_index` and `key_epoch`):
- `proxy_delegate_registered{dik, drk, provider, subject_commit, attestation_id, verifier_ids}`
- `proxy_delegate_route_key_rotated`
- `proxy_delegate_identity_change_pending{kind, pending_id, effective_time}`
- `proxy_delegate_pending_cancelled{signer_role}`
- `proxy_delegate_frozen{signer_role}`
- `proxy_delegate_revoked`
- `proxy_delegate_suspension_set{suspended, reason_code}`
- `proxy_delegate_verifiers_set`

### 3.7 Spam bounds (chain has no fees and no rate limiting, [doc] dossier chain §6)
- **New entries** need a threshold attestation (spam is bounded by verifier issuance policy) and are capped by `max_delegates`.
- **Key growth** per delegate is capped at 64 lifetime changes, at 64 bytes of reverse index each.
- **Freeze, revoke and cancel** are state transitions that are idempotent or terminal.
- **Per-block cap.** `max_registry_ops_per_block` is enforced in PrepareProposal and ProcessProposal, following the `MaxVoteShareSubmissionsPerBlock = 256` precedent ([code] `chain:app/vote_share_submission_proposal.go:10-13`).
- **Size.** A message is at most about 1.4 KB.

### 3.8 Interactions with other chain features (contracts)
- **Routes.** Authenticate with `Effective(e, blockTime).drk_pk` over the `route` digest, including the effective `key_epoch`. Accept only if the status is ACTIVE and `!coordinator_suspended`.
- **Reveals.** A `MsgRevealShare` with `proposal_id = 0` must accept `vote_decision` in `[1, next_index)`, **regardless of the delegate's status**. Pools of frozen, suspended or revoked delegates simply abstain where un-routed.
  - Rejecting such shares would not save the weight, and it would desynchronize share accounting.
  - Decisions outside the range are rejected, so attackers cannot mint accumulators.
- **Proxy delegation txs.** The DC hides `delegate_index` until reveal, so the chain **cannot** reject delegations to revoked, frozen or suspended delegates. The wallet must pre-check (section 5.5).
- **Genesis.** Export and import every 0x1A key. Indices and used attestation ids must survive chain resets, which have happened historically ([doc] dossier chain §7).
- **Rollout.** Use the dormant-flag pattern (`ProxyDelegateRegistryEnabled`) plus an activation upgrade between rounds ([doc] dossier chain §5). The registry does not depend on ZKP4 or routes and can ship first.

---

## 4. Verifier service

### 4.1 Architecture
- **`verifier-api`** (stateless, horizontally scaled; Rust/axum recommended):
  - Endpoints: `/v1/proofs/resolve`, `/v1/attestations`, `/v1/statements`, `/v1/links`, `/v1/xdata-removal`, `/v1/status/{dik}`.
  - Uses a shared Rust crate `zcash_vote_delegate` (in the zcash_voting workspace) for encodings, digests, the marker parser, fingerprints and UTS #39 skeletons. Vizor and the CLI use the same crate.
  - The chain mirrors the digests in Go, with a shared JSON test-vector fixture (the pattern used by e2e sighash mirroring).
- **`attest-signer`** (isolated host):
  - Holds the verifier key in an HSM or KMS.
  - Accepts only mTLS requests carrying `(attest digest, issuance record id)` from the API.
  - Enforces its own circuit breaker (default 200 per hour, 2,000 per day) and appends every signature to an append-only log mirrored to object storage.
- **Store** (Managed Postgres). Tables:
  - `subjects(provider, subject_id PK, handle, handle_history, display_name, account_created_at, followers, protected, suspended, deleted_at, last_user_check, xdata_purged_at)`
  - `proofs(provider, subject_id, proof_ref, marker_dik, state, last_ok_at, last_check_at, fail_count, last_error_class)`
  - `issuances(attestation_id PK, kind, delegate_index, dik, drk, provider, subject_id, salt, subject_commit, issued_at, expires_at, state)`
  - `entries` (chain mirror)
  - `listing(delegate_index, listing, featured, flags, lookalike_of, dormant_since)`
  - `avatars(sha256_256 PK, sha256_64, phash, state)`
  - `statements`
  - `moderation_actions`
  - `reports`
  - `snapshots(seq, sha256, prev_sha256)`
  - `audit_log`
- **`chain-indexer`.** Pages `/proxy-delegates?updated_since_height=`, reconciles issuances to registrations, and alerts on any on-chain registration whose `attestation_id` is missing from `issuances` (a sign of verifier-key compromise).
- **`refresher` workers.** Run the X, GitHub and DNS cadences (4.5), compliance purges and the pfp pipeline (section 6).
- **`publisher`.** Builds and signs directory documents every 6 h, and within 15 min of moderation, deletion or registration events.
- **`admin-console`.** SSO plus hardware MFA. Two-person approval for featured changes and coordinator-suspension recommendations. Every action is audit-logged.

**Independent verifiers (t-of-n).** Each operator runs only `verifier-api` and `attest-signer` with **its own** X developer app. Tokens are never shared or forwarded (the ICNS bearer-token fan-out is avoided, [doc] dossier identity §1.5). Independent verifiers read issuance state from the public directory and the chain.

### 4.2 Attestation algorithm (register)
1. Validate the schema; the network must equal the deployment network.
2. Check keys are canonical, not small-order and distinct.
3. Apply rate limits:
   - per subject: 3 per 24 h;
   - per IP: 10 per hour (IPs retained 7 days);
   - per DIK: 3 per 24 h;
   - global daily cap (default 500).
4. Fetch the proof, reusing a resolve observation that is at most 10 min old. Run the provider rules in 4.3.
5. The subject must match the author, and the marker key must equal the DIK.
6. Check account eligibility:
   - X: not protected, not suspended, account age >= 30 days.
   - GitHub: account age >= 30 days.
7. Recompute `subject_commit` and `reg`, then verify `dik_sig` and `drk_sig`.
8. Check uniqueness:
   - The subject has no non-revoked entry and no unexpired issuance.
   - DIK and DRK are absent from the chain (`by-key`) and the store.
   - The `attestation_id` has never been seen.
9. Check denylists: subject, DIK and the avatar pHash of the subject.
10. Check `registration_enabled` (chain param) and the operator kill switch.
11. Write-ahead: insert `issuances(state = issued)`, then call the signer.
    - `not_before = now - 60`, `expires_at = now + 24h`.
    - Return the result.

**Recovery** adds the requirements in section 2.7 and needs `recover_threshold` signatures. The default policy for featured entries is a manual review with a 24 h SLA.

### 4.3 Provider rules
**X (official API, pay-per-use, primary).**
- Single fetch:
  ```
  GET https://api.x.com/2/tweets/{id}?tweet.fields=author_id,created_at,edit_history_tweet_ids,note_tweet,text&expansions=author_id&user.fields=id,username,name,created_at,protected,profile_image_url,public_metrics
  ```
- Bulk fetches: `GET /2/users?ids=<=100` and `GET /2/tweets?ids=<=100`.
- Text source: `note_tweet.text` if present, otherwise `text`.
- If the last of `edit_history_tweet_ids` differs from the requested id, re-fetch that last id.
- Require `data.author_id == includes.users[0].id == subject_id`.

**oEmbed fallback.**
- Request: `GET https://publish.x.com/oembed?url=https%3A%2F%2Fx.com%2Fi%2Fstatus%2F{id}&omit_script=true&dnt=true`. It is published, unauthenticated and not rate limited ([doc] dossier identity §3.2).
- **Use it only for re-verification of existing entries,** and only while the API returns 5xx, 429 or billing/auth errors.
- Parse `author_url` for the handle. HTML-unescape and strip tags from `html`, then run the marker regex.
- Accept only if the handle equals the last API-confirmed handle for that subject id, confirmed within 7 days. Otherwise mark `needs_recheck` and change no state.
- A non-JSON response counts as `UPSTREAM_ERROR`, never `NOT_FOUND`.
- New registrations during an API outage are queued (`UPSTREAM_UNAVAILABLE` with `retry_after`), never attested through oEmbed, because oEmbed has no numeric id.

**Never use the syndication endpoint** (`cdn.syndication.twimg.com`). It is undocumented, and the X ToS forbids non-published interfaces and scraping ([doc] dossier identity §3.3).

**GitHub.**
- Calls: `GET /gists/{id}` (authenticated, 5,000 requests per hour), `GET /user/{id}`, and the raw file via `raw_url` if the content is truncated.
- Require `owner.id == subject_id`.

**DNS.** Rules as in section 2.4.

### 4.4 Failure classes

| Class | Trigger | State change |
|---|---|---|
| `NOT_FOUND` | API 404 / "Not Found Error" for the post | Proof enters `missing_grace` |
| `PROTECTED` | Authorization error on the post | Proof enters `unverifiable` (grace as for `NOT_FOUND`) |
| `ACCOUNT_GONE` | User suspended or deleted | Immediate; purge X data within 24 h |
| `RATE_LIMITED` | 429 | None; honor `x-rate-limit-reset` |
| `UPSTREAM_ERROR` | 5xx, timeout, network failure, non-JSON oEmbed | None; exponential backoff; alert if longer than 24 h |
| `BILLING_OR_AUTH` | 401, 402 or 403 for **our** app | None; page on-call; switch rechecks to oEmbed |
| `PARSE_ERROR` | Unexpected payload | None; alert |

**Mass-failure circuit breaker.** If `NOT_FOUND` or `ACCOUNT_GONE` in one run exceeds 5% of checks, or 3x the 7-day baseline, the run applies no state changes and pages on-call. An X-side bug therefore cannot mass-delist delegates.

### 4.5 Re-verification cadence

| Item | Featured | Listed | Lookup-only | Dormant |
|---|---|---|---|---|
| X user object (handle, name, pfp, protected, suspended) | Every 6 h | Daily | Daily | Not hydrated (X data purged) |
| X proof post | Daily | Weekly, plus within 12 h of a new round appearing | Weekly, plus pre-round | None |
| GitHub / DNS | Daily | Weekly | Weekly | None |
| `missing_grace` rechecks | 1 h, 6 h, 24 h, 72 h, then `missing` | Same | Same | n/a |

- Pay-per-use resources are deduplicated per UTC day ([doc] dossier identity §3.1), so the 6-hourly featured checks cost nothing extra.
- **Dormant** means the entry is not featured, made no route in the last 3 finished rounds, and has been registered for at least 90 days. Its X data is purged, and the delegate's "Refresh my listing" action (a DIK-signed statement bump) re-hydrates it.

### 4.6 Lifecycle state machine
**Issuance:** `resolved -> issued -> registered` (when the chain event is seen), or `issued -> expired`.

**Proof:**
- `ok -> missing_grace -> missing`, and `ok -> unverifiable -> missing`.
- `* -> key_mismatch` when the subject now advertises a different key.
- `* -> outdated` when the on-chain DIK changed and no post containing the new DIK appears within 14 days. Transitive trust (old post plus co-signed chain change) is honored until then.
- `missing`, `unverifiable` and `outdated` return to `ok` on re-proof.

**Account:** `ok`, `protected`, `gone` (X data purged), `dormant`.

**Listing** (recomputed at every publish):
```
hidden      if chain REVOKED (history only) or impersonation_confirmed or account gone
lookup_only if coordinator_suspended or FROZEN or pending change or proof in {missing, unverifiable, key_mismatch, outdated}
              or lookalike flag or dormant or registered < 24 h or below listing thresholds (7.4)
featured    if curated and none of the above
listed      otherwise
```

---

## 5. Directory snapshots

### 5.1 Layers
1. **Chain registry (authoritative).** Keys, status, pending change, suspension and `subject_commit`.
2. **Registry doc (binding layer, public, cross-wallet).** Ids only (X user id, post id, salt), listing, flags and activity. No hydrated X content.
3. **Profiles doc (presentation layer).** Handle, display name, account month, avatar hashes and the delegate statement. These are X user-object fields, kept current, with a 30 h expiry.
4. **Avatars.** Content-addressed WebP files and packs (section 6).

### 5.2 X Developer Policy compliance ([doc] dossier identity §3.3)
- **Keep-current:** daily hydration (4.5), a profiles-doc `expires_at` of at most issued time + 30 h, and a rule that wallets purge expired profiles and unreferenced avatars.
- **Delete within 24 h** (target 6 h) on account deletion or suspension, or on a delegate's DIK-signed X-data removal request. The purge covers:
  - DB fields;
  - avatar objects in storage plus a CDN purge;
  - the subject id, salt and post id in the next registry doc. The entry becomes "identity removed", fingerprint only.
- **Nothing X-derived in immutable stores.** The chain holds only the salted commit. Git (if used for anchors) holds only hashes.
- **Redistribution.** The registry doc carries ids only, the form X permits third parties to receive. Whether other wallets may consume the profiles doc, or must hydrate with their own developer app, is open question 6. X ToS changes take effect 2026-10-09 and need legal review.
- **Links.** "View on X" links open in the external browser (existing pattern, [doc] dossier vizor §4).

### 5.3 Schemas
**Signed envelope** for `index.json`:
- Shape: `{"payload": base64(bytes), "signatures": [{"key_id","alg":"ed25519","sig"}]}`.
- Signed bytes: `"zcash-shielded-vote:delegate-directory:v1\n" || payload_bytes`.
- Payload:
```json
{"schema":"zcash-shielded-vote/delegate-directory-index/v1","network":"main",
 "seq":1234,"prev_sha256":"<sha256 of previous payload bytes>",
 "issued_at":1791000000,"expires_at":1791108000,"chain_height":4567890,
 "documents":{"registry":{"sha256":"..","bytes":1532211,"path":"registry/<sha>.json.gz"},
              "profiles":{"sha256":"..","bytes":612345,"path":"profiles/<sha>.json.gz"},
              "avatar_packs":[{"name":"listed-64","sha256":"..","bytes":4200000}],
              "featured_log":{"sha256":"..","path":"featured-log/<sha>.json"}},
 "verifier_set_sha256":"<hash of the chain verifier set the publisher saw>"}
```
- Document hashes cover the stored (gzip) object bytes.
- After decompression, documents are capped at 32 MiB.

**Registry doc entry:**
```json
{"i":17,"dik":"zvdk1..","drk":"zvdr1..","key_epoch":1,"chain_status":"active","suspended":false,
 "suspension_reason":0,"pending":null,"registered_height":4000000,"registered_at":1790000000,
 "provider":"x","subject":{"id":"1234567890","salt":"<64 hex>"},
 "proof":{"ref":"1844...","state":"ok","last_ok_at":1791000000},
 "listing":"listed","flags":["lookalike:5","key_changed:1790900000","recently_registered"],
 "predecessor":null,"links":[{"provider":"github","id":"583231","state":"ok"}],
 "activity":{"last_round":"<round id>","routed":14,"proposals":15}}
```
- `subject`, `proof` and `links` become `null` after a purge.

**Profiles doc entry:**
```json
{"i":17,"handle":"alice","handle_prev":[{"handle":"alice_old","until":1792000000}],
 "display_name":"Alice","account_since":"2015-03",
 "avatar":{"s64":"<sha256>","s256":"<sha256>"},"avatar_state":"ok",
 "statement":{"body":"<exact signed body>","sig":"<base64>"},"checked_at":1791000000}
```
- The profiles doc header carries `seq`, `registry_seq`, `issued_at` and `expires_at`.

### 5.4 Keys and the static-config field
There are three purpose-separated key roles:
1. **Round `trusted_keys`** (existing). They authenticate rounds only, 1-of-N ([code] `zv:zcash_voting/src/config/mod.rs:1092-1143`, with `return true` at `:1139`).
2. **Verifier attestation keys.** These live on chain and are coordinator-managed.
3. **Directory keys.** These are new and live in static config:
   - An online key signs `index.json`.
   - An offline key signs only directory-key certificates and revocations: `{"v":1,"network","key_id","pubkey","not_after","revokes":[key_ids]}` under the `delegate-directory-key` domain. This lets a compromised online key be replaced without an app release.

**New optional static-config section,** published at a new hash pin in token-holder-voting-config. Old builds keep their old pins, and the wire structs have no `deny_unknown_fields` ([code] `zv:zcash_voting/src/config/mod.rs:855-884`), so Zodl and old Vizor are unaffected:
```json
"proxy_delegation": {
  "network": "main",
  "directory_urls": ["https://delegates.valargroup.dev/v1/main/", "https://functions.vizor.cash/v1/delegates/main/"],
  "directory_keys": [{"key_id":"valar-dir-online-1","alg":"ed25519","pubkey":"..","notes":"online"},
                     {"key_id":"valar-dir-offline-1","alg":"ed25519","pubkey":"..","notes":"certs and revocations only"}],
  "verifier_api_urls": [{"verifier_id":"valar-1","url":"https://delegates-api.valargroup.dev/v1/"}]
}
```

**Rejected alternative:** signing the directory with round `trusted_keys`, or pinning it inside `RoundEntry`. `verify_round_entry` returns false for any `auth_version != 2` (`:1098-1101`), so a new round-auth version would make old wallets skip rounds.

### 5.5 Wallet verification algorithm
1. Fetch `index.json` from mirror 0, falling back to mirror 1. Use `NetworkHttpClient` (GET works under Tor, [code] `vizor:lib/src/core/network/network_http_client.dart:420`).
2. Verify the signature with a directory key that is valid per the latest offline-signed cert or revocation document.
3. Require `expires_at > now`, and `seq >= last_seen_seq`.
4. Detect equivocation:
   - Same `seq` with a different hash is an equivocation alarm: refuse the directory.
   - Once a day, fetch the index from both mirrors. Equal `seq` with unequal hash is also an alarm.
5. Fetch the documents by hash and verify their sha256.
6. Overlay the chain:
   - Maintain a local mirror of the chain registry. The first load pages `GET /proxy-delegates` in full (about 2 MB at 10k entries). Later loads use `updated_since_height`.
   - Never query per index as a delegator; that would leak intent to the vote server.
7. The chain is authoritative for keys, status, pending change and suspension. Hide an entry, and record a local alarm, if:
   - `subject_commit` != `H(provider, subject.id, subject.salt)` (identity-binding mismatch); or
   - the snapshot's DIK != the chain's effective DIK when the chain has no pending change.
8. **Pre-sign check (contract with the client and ZKP4 components).** Immediately before building a proxy-delegation tx:
   - Re-fetch `updated_since_height`.
   - Block signing if any chosen delegate is not ACTIVE, is `coordinator_suspended`, or has a pending change.
   - Persist per delegation: `{delegate_index, dik, fingerprint, handle_at_delegation, directory_seq, index_sha256}`.

### 5.6 Per-round pinning (refines the baseline)
- There is **no per-round frozen directory hash in the signed round config.** It would break old wallets (see 5.4) and conflict with the 24 h deletion duty.
- What gives per-round consistency instead:
  - the on-chain registry (keys, status and subject commits are consensus state);
  - hash-chained, monotonic `seq` snapshots with dual-mirror equivocation checks;
  - each wallet pinning its own delegations (5.5 step 8).
- **Phase 2 hardening.** A coordinator-created "directory publisher" account posts a standard Cosmos `MsgAnchorDelegateDirectory{creator, network, seq, sha256}` daily, following the endorser precedent ([code] `chain:x/vote/keeper/msg_server_endorsements.go:13-57`). The value is stored at `0x1A 0x07`, and wallets check `index.json` against the anchors.

### 5.7 Mirrors, caching, size and sharding
- **Mirrors.** The primary is `delegates.valargroup.dev` (CDN over object storage). The mirror is `functions.vizor.cash/v1/delegates/...`, a pull-through cache that clients treat as untrusted, because all bytes are signed or content-addressed. Do not use GitHub Pages: history is immutable and the content is X-derived.
- **Caching.** Wallets refresh `index.json` when the delegates UI opens or the app returns to the foreground, at most every 15 min, and download documents only when their hash changes.

**Approximate sizes:**

| Delegates | Registry doc | Profiles doc |
|---|---|---|
| 1k | about 0.15 MB gzip | about 0.06 MB gzip |
| 10k | about 1.5 MB gzip | about 0.6 MB gzip |

**Sharding above 50k delegates:**
- `registry/` and `profiles/` split by index range (10k each), listed in `index.json`.
- A compact `search` document holds `(i, handle, skeleton, listing)` at about 60 B per entry.
- Exact-lookup-only entries live in shards keyed by the first byte of `sha256(lowercase handle)`, so a lookup fetches one shard and stays k-anonymous.

---

## 6. Profile pictures
1. **Source.**
   - X: the `profile_image_url` from the API, with `_normal.` replaced by `_400x400.` (fall back to `_normal` on 404) [doc] dossier identity §1.2 and §4.
   - GitHub: `avatar_url`.
   - DNS: none (identicon) unless the delegate is featured, in which case a curator-uploaded image is allowed.
2. **Fetch** (server only):
   - HTTPS GET, host allowlist {`pbs.twimg.com`, `avatars.githubusercontent.com`}, no redirects off the allowlist.
   - 5 s connect timeout, 10 s total, 2 MiB maximum.
3. **Sandboxed decode:**
   - A separate worker process with no network namespace, a seccomp allowlist, RLIMIT_AS of 512 MiB, 2 s CPU, a read-only filesystem, running as nobody.
   - **Pure-Rust, memory-safe decoders only:** `image` with `zune-jpeg`, `png`, `gif` (first frame) and `image-webp`.
   - Sniff magic bytes first. Reject images larger than 4096x4096 from the header before decoding, and cap allocation at 64 MiB.
4. **Re-encode:**
   - Strip all metadata and ICC data, convert to sRGB 8-bit, flatten alpha onto #808080, center-crop square, and resize with Lanczos3 to 256 and 64 px.
   - Encode WebP lossy at q = 80. The libwebp *encoder* is acceptable because its input is our own pixel buffer, and it runs inside the sandbox.
   - Typical sizes: about 2-3 KB at 64 px, 10-15 KB at 256 px.
5. **Content addressing.** Files are stored at `avatars/{64,256}/<sha256>.webp` with `Cache-Control: public, max-age=31536000, immutable`. Deletion means a storage delete plus a CDN purge.
6. **Packs.** Container format `ZVDP1` with `u32 count` and entries `[sha256[32], u32 len, bytes]`.
   - One pack holds all featured and listed 64 px avatars (about 1-2k at 10k delegates, about 5 MB), fetched when the picker first opens.
   - Lookup-only avatars come from 256 prefix-shard packs (k-anonymous).
   - Wallets verify each entry's hash against the profiles doc and ignore extra entries.
7. **Client decode** (contract with Vizor):
   - Bytes come only via `NetworkHttpClient` from the directory origins; never `Image.network` (it bypasses Tor; Vizor loads no remote images today, [doc] dossier vizor §4).
   - Verify the sha256 against the signed profiles doc, then decode **in Rust** with `image-webp` into RGBA. Exact dimensions (64 or 256) are required.
   - Hand pixels to Dart via `ui.decodeImageFromPixels`, so platform codecs (Skia, libwebp, ImageIO) never parse network bytes.
8. **Moderation:**
   - Compute a 64-bit DCT pHash.
   - Denylist by sha256 and by pHash (Hamming distance <= 6).
   - A pHash close to a featured delegate's avatar (Hamming <= 6) triggers the `lookalike` flag and replaces the avatar with an identicon pending review.
   - CSAM hash matching (PhotoDNA, Thorn Safer, or Cloudflare's scanning tool if CDN is on Cloudflare) runs before publish. Report matches as legally required.
   - An optional NSFW classifier routes images to `pending_review`.
   - Any pfp change on a **featured** delegate goes to `pending_review`: an identicon is shown until a curator approves (SLA 24 h). The old pfp is never shown, which keeps the data current.
9. **Why this design (decoder safety):**
   - Untrusted images are a known zero-click vector. libwebp CVE-2023-4863 was exploited in the wild in the BLASTPASS chain ([doc] dossier identity §4).
   - With this design, an attacker would need to compromise the directory signing key **and** find a bug in a pure-Rust decoder before any wallet parses attacker-influenced bytes. Wallets never contact X.
10. **Tor.** Avatars come from the same origin as snapshots, in a few large GETs, which suits Tor latency. Avatars of the delegates a wallet actually delegated to are cached locally.

---

## 7. Curation

### 7.1 Featured tier
**Criteria (all required):**
- Registered at least 30 days, and proof `ok`.
- Either (a) an X account at least 1 year old with at least 1,000 followers, or (b) a documented ecosystem role (for example ZF, ZCG, ECC, Shielded Labs, Valar, Vizor, or a known community contributor).
- A delegate statement is present.
- The fingerprint was confirmed out of band (curator contact through a second channel).
- No open impersonation or takeover report.
- Routed at least 50% of proposals in each of the last 2 rounds in which the delegate was registered, or is new and not yet past 2 rounds.

**Governance:**
- A curator group of at least 3 people across Valar and Vizor. Each change needs 2 approvals, and a curator never approves themselves.
- Every add, remove or hold goes into a signed public `featured-log` (index, action, reason code, date; no personal data).
- Quarterly review, and a recommended cap of 30 featured delegates.
- **Removal triggers:** misses on the routing criterion, a confirmed report, key compromise, or a request by the delegate.
- **Disclaimer copy:** "Featured means verified and active, not endorsed."

### 7.2 Search and ranking (on device; no server queries)
**Query normalization:** trim, strip a leading `@`, apply NFKC, lowercase.

**Recognized query forms:**
- `@handle`, `x:handle`, `github:login`, a domain;
- `#17`;
- a fingerprint `xxxx-xxxx-xxxx`;
- a full `zvdk1...` key.

**Result tiers:**
1. Exact handle, login or domain match, including lookup-only entries (hidden entries never appear).
2. Exact index, fingerprint or key match.
3. Featured entries matching by prefix or substring.
4. Listed entries matching by prefix or substring.

**Ordering and empty query:**
- An empty query shows featured entries, then a sample of 20 recently active listed entries.
- Within tiers 3 and 4, order uses weighted-random keys `-ln(u)/w`:
  - `u = HMAC(daily per-install seed, index)` mapped to (0, 1].
  - `w = 1 + [routed >= 50% of proposals in the latest finished round] + [statement present]`.
- Never sort by follower count or by (hidden) pool size. Show account age, not followers.

### 7.3 Lookalike detection (UTS #39)
**Handles** (ASCII `[A-Za-z0-9_]{1,15}`):
- Skeleton: lowercase; map `0->o`, `1->l`, `i->l`, `rn->m`, `vv->w`, `cl->d`; drop `_`.
- Flag when the skeleton equals, or is Damerau-Levenshtein distance 1 from, any featured handle or any listed handle registered earlier.

**Display names:**
- NFKC, casefold, then the UTS #39 skeleton (`confusables.txt`, Unicode 16) after removing default-ignorables, whitespace and punctuation.
- Apply the mixed-script restriction check: anything below "Highly Restrictive" is flagged `mixed_script_name`.
- Compare against featured handles and featured display names.

**Avatars:** pHash rule as in section 6.

**Effect:**
- The listing drops to `lookup_only`.
- The wallet shows a red banner, "Looks similar to @zooko (featured). This is a different delegate.", and on exact lookups the featured entry is ranked first.
- The client also skeletonizes the query and offers "Did you mean @zooko (featured)?".
- Use the `unicode-security` crate in both the server and the client.

### 7.4 Anti-spam and listing thresholds

| Gate | X | GitHub | DNS |
|---|---|---|---|
| Can register (attestation) | Account age >= 30 d, public, not suspended | Account age >= 30 d | Consistent resolution |
| Listed (browse and partial search) | Age >= 180 d **and** followers >= 100, registered >= 24 h | Age >= 365 d and (followers >= 25 or public repos >= 5) | Featured only |
| Lookup-only | All other non-hidden entries: exact handle, fingerprint and deeplink lookups still work | Same | Same |

Rate limits are in 4.2. Thresholds are verifier policy (off-chain), adjustable without a chain change.

### 7.5 Reports, takedown and disputes
**Intake.** In-app "Report delegate" opens the external browser at `https://delegates.valargroup.dev/report?i=17`. That page is a web form with a privacy-preserving challenge. There is no in-app POST, which keeps Vizor's posture of no backend calls on user behavior.

**Categories and SLAs:**

| Category | SLA |
|---|---|
| Impersonation | 24 h triage |
| Key compromise / takeover | 24 h triage |
| Offensive avatar or name | Blank within 24 h |
| Spam | Best effort |
| Legal (trademark, court order) | Counsel process |
| Other | Best effort |

**Actions (most to least severe):**
1. Recommend a coordinator suspension: threshold action, reason code public.
2. Mark `impersonation_confirmed`: listing becomes hidden, with a banner for existing delegators.
3. Refuse further attestations for the subject or DIK (verifier denylist).
4. Delist (`lookup_only`).
5. Hide the display name.
6. Blank the avatar.

**Disputes:**
- Two accounts claiming one real-world person are resolved by the featured decision; the others carry lookalike banners.
- Handle reassignment (the X Handle Marketplace, or release 30 days after deactivation) is safe because binding is by numeric id. The new owner of a handle is a new subject, and the old entry shows "formerly @x" for 30 days.

**Appeals** go by email. Decisions are recorded in the public moderation log by reason code.

---

## 8. Ops

### 8.1 Hosting
**Recommendation: valargroup infrastructure,** on the same DigitalOcean estate as the vote chain and `voting.valargroup.dev` pins ([doc] chain `docs/production-setup.md` uses DO Spaces).
- `delegates-api.valargroup.dev`: `verifier-api` (2 containers) and workers (1 container).
- `delegates.valargroup.dev`: Spaces plus CDN for directory objects.
- Managed Postgres with a standby and PITR.
- A separate `test` deployment with separate keys.

**Why not functions.vizor.cash:** this is protocol, cross-wallet infrastructure whose keys are registered on the vote chain. `functions.vizor.cash` serves only as an untrusted mirror. The deeplink server stays stateless per its charter ([code] `dls:README.md:9-12`).

**Secrets.** API tokens and DB credentials come from Infisical at runtime and are never logged. Signing keys are **not** Infisical secrets (section 8.4).

### 8.2 Cost
**X API** (reads: posts $0.005, users $0.010, deduplicated per UTC day; [doc] dossier identity §3.1). Assumptions: daily user refresh, 6.33 post rechecks per month (weekly plus 2 pre-round), and new registrations equal to 10% of N per month at 3 attempts x $0.015 each.

| Hydrated delegates | Users | Proof rechecks | Registrations | Total per month |
|---|---|---|---|---|
| 100 | $30 | $3 | $0.5 | **about $34** |
| 1,000 | $300 | $32 | $4.5 | **about $337** |
| 10,000 | $3,000 | $317 | $45 | **about $3,360** |
| 10,000 with 70% dormant (de-hydrated) | $900 | $95 | $45 | **about $1,040** |

**Infrastructure:** about $120-250 per month in total.
- Containers: $40-60.
- Postgres with standby: $30-60.
- Spaces plus CDN: about $5 plus egress. Worst-case egress is 50k wallets x 2 MB per day = 3 TB per month, about $20; zero on R2.
- KMS: about $5.
- CSAM scanning: free tiers.

**Budget guard.** A daily spend meter with alerts at 80% and 100% of budget. Over budget, defer non-featured profile refreshes but **never** deletion detection.

### 8.3 Monitoring and alerts

| Metric | Alert |
|---|---|
| Attestations issued and denied, by reason | Spike in denials |
| X API 429/5xx rate | Over 5% for 30 min |
| `BILLING_OR_AUTH` | Any (page) |
| Spend vs budget | 80% / 100% |
| Max age of hydrated profile data | Over 20 h |
| Deletion purge lag | Over 12 h (page) |
| Snapshot `seq` age | Over 8 h |
| Mirror `seq` divergence | Over 1 h |
| Chain-indexer lag | Over 10 min |
| On-chain registration without a matching issuance | Any (page: key compromise) |
| Recovery submitted for a featured entry | Any (review) |
| pfp pipeline failures, CSAM hits | Any CSAM hit (page) |
| Report queue age | Over 24 h |
| Mass-failure circuit breaker tripped | Any (page) |

A public `status.json` publishes `seq`, `issued_at` and the verifier set hash.

### 8.4 Key custody
- **Verifier attestation key.**
  - Held in an HSM or KMS with non-exportable Ed25519 keys: YubiHSM 2 on a Valar-controlled host, or a cloud KMS with Ed25519 support (GCP Cloud KMS `EC_SIGN_ED25519`; AWS KMS if its Ed25519 key spec is available) [unverified at time of writing].
  - Only `attest-signer` can use it, through a dedicated IAM role.
  - A second, pre-registered verifier key held offline allows instant rotation through the coordinator action.
- **Directory online key.** A separate HSM or KMS key, usable only by the publisher.
- **Directory offline key.** A hardware token (YubiKey) in a safe, with a sealed backup. It signs only key certificates and revocations.
- **Coordinator keys** (on-chain verifier set, params, suspensions): the existing N-of-M vote managers.
- **Signing logs.** All signing operations go to append-only logs mirrored to object storage. Signatures are published on chain for attestations and in `index.json` for directory documents.

### 8.5 Incident playbooks
1. **Verifier key compromise.**
   - Detect: an on-chain registration or recovery without a matching issuance, or signer-log anomalies.
   - Contain: a coordinator `MsgSetProxyDelegateVerifiers` removing the key (on-call coordinators pre-arranged for fast approval); pause issuance.
   - Mitigate:
     - Publisher marks every entry attested by that key since the suspected time as `lookup_only` with "verification under review".
     - Every pending recovery attested by that key is cancelled by its owner, with outreach to the delegate, or reviewed.
     - Recommend coordinator suspension for confirmed fakes.
     - Re-attest legitimate entries off-chain (directory flag only; no chain change needed).
2. **Directory online-key compromise.**
   - The offline key signs a revocation and certifies a new online key, then the directory is republished.
   - Wallet chain-overlay checks (5.5 step 7) already limit forged snapshots to presentation-only lies, for example mislabeling a handle for some id.
   - A static-config re-pin follows in the next app release.
3. **X API outage or billing cutoff.**
   - No state changes. Re-verification switches to oEmbed; new registrations are queued.
   - Profiles stay served until expiry. Beyond 24 h, show "X data refresh delayed" on profiles.
   - Prolonged loss of access, or termination of the X agreement: purge X data, push GitHub and DNS onboarding, and show fingerprint plus statement only. **On-chain keys and routes are unaffected.**
4. **Mass handle takeover** (X breach, marketplace reassignment wave).
   - Detect: a spike in `key_mismatch`, recoveries, or handle changes among featured delegates.
   - Respond:
     - Pause RECOVER issuance (verifier).
     - Optionally raise `identity_recovery_delay_secs` (coordinator param).
     - Flip affected entries to `lookup_only` with a banner.
     - Announce through official channels.
   - Existing entries are protected by the 7-day pending window plus owner cancellation.
5. **Directory or CDN outage.** Mirror fallback, then the cached snapshot until expiry, then degraded mode:
   - existing delegations and routes stay viewable by index and fingerprint from the chain;
   - new proxy delegation is blocked with "Delegate directory unavailable".
6. **Offensive content wave.** Bulk-blank avatars by pHash cluster, add to the denylist, and raise the NSFW classifier threshold.

---

## 9. Identity UX contract for Vizor (identity-related states only)

**Onboarding flow:**
```
intro -> phrase_create -> phrase_confirm -> provider_choose -> post_compose
  (copy text / open the x.com intent URL in the external browser)
-> post_link_input -> resolving -> resolved_confirm (handle, account age, key fingerprint)
-> attesting -> submitting -> registered(#index)
```

**Onboarding errors:**

| Error | Copy / behavior |
|---|---|
| `UPSTREAM_UNAVAILABLE` | "X is not responding. Try again in N min." |
| `PROOF_NOT_FOUND` | Post not found |
| `MARKER_MISSING` | "The post must contain this exact line." |
| `MARKER_AMBIGUOUS` | Post contains more than one key |
| `ACCOUNT_TOO_NEW` | Account below the registration age gate |
| `SUBJECT_ALREADY_REGISTERED` | "This X account is already delegate #17. Restore your key phrase or start recovery." |
| `RATE_LIMITED` | Try again later |
| `ATTESTATION_EXPIRED` | Re-requested automatically |
| `CHAIN_REJECTED(reason)` | Chain rejection reason shown |

**Delegate home:**
- Listing state with reasons, fingerprint and proof status.
- A pending-change banner with Cancel.
- Actions: Rotate route key; Pause (freeze); Retire (revoke, with typed confirmation); Edit statement; Add GitHub or domain; Remove my X data; Restore from phrase.

**Delegator delegate row:**
- Avatar or identicon, display name, provider icon with `@handle`, and `#index · fingerprint`.
- Badges: Featured, Registered N ago, Voted on N of M last round.

**Warning copy** (exact):

| Condition | Copy |
|---|---|
| Lookalike | "Looks similar to @zooko (featured). This is a different delegate." |
| Key changed (30 days) | "Key changed on Oct 3. Check the new fingerprint with @alice before delegating." |
| Pending change | "Key change pending until Oct 10. You can't delegate to @alice until it completes." |
| Proof removed | "@alice's verification post was removed." |
| Frozen | "Paused by the delegate. Proposals they have not voted on will abstain." |
| Suspended | "Suspended by network coordinators (impersonation). Proposals they have not voted on will abstain." |
| Revoked | "Retired. Proposals they did not vote on will abstain." |
| Account gone | "X account no longer available." |

**Deeplink:**
- Format: `https://link.vizor.cash/delegate#v1=<zvdk1...>`. The payload is in the fragment and is never sent to the server.
- Needs one new exact path in both registries ([code] `dls:src/routes.ts:10-19`; README `:88-90` forbids wildcards).
- The wallet resolves by DIK only and never trusts a handle in a link.
- Desktop fallback: a copyable "delegate code".

---

## 10. Rollout and compatibility
**Phase 1 (independent of ZKP4 and routes):**
- chain registry (dormant flag, then an activation upgrade between rounds);
- verifier, publisher and directory;
- the static-config pin;
- Vizor "Become a delegate" and the directory browser.

Delegates can register before proxy delegation launches.

**Phase 2:**
- routes and ZKP4 (other components) consume the registry;
- the directory anchor message;
- a second independent verifier (2-of-3);
- the `zcash-vote-delegate` CLI, including an `audit` command that re-checks the directory against the chain and X oEmbed.

**Compatibility:**
- Zodl and old Vizor never see the new tag, REST routes or static-config section.
- No existing message, VK or round-auth payload changes.
