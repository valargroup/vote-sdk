# Delegation signing vectors

`delegation_tx1_effects_v1.json` is the existing wallet-generated V6/NU6.3
authorization vector. It is retained only to test rejection of the retired
profile and to prove that changing its version byte cannot reuse its signature.

`delegation_tx1_effects_v2.json` uses the same action layout with profile byte
`2` and a disposable fixture verification key. Its expected digest was generated
independently of the chain implementation using published Common 4.0.0:
`voting-crypto-deps = 0.4.0` with `validator` and `rand`, and
`zakura-protocol = 4.0.0`.

To reconstruct the vector with that library:

1. Decode the effects and read the action from bytes after the version byte
   using `read_action_without_auth`.
2. Construct an `EffectsOnly` Ironwood V3 bundle with flags `0x07`, balance
   `+1`, and zero anchor. Compute its V6 commitment.
3. Compute the header with `TxIdDigester::digest_header` using `TxVersion::V6`,
   `BranchId::Nu7` (`0x77190ad9`), zero lock time, and zero expiry.
4. Pass the header, no transparent/Sapling/Orchard digests, and the Ironwood
   commitment to `to_txid(TxVersion::V6, BranchId::Nu7, ...)`.
5. Verify the saved SpendAuth signature over the resulting digest.

The fixture signing key consists of 32 bytes each equal to `1`, interpreted by
`reddsa::SigningKey<orchard::SpendAuth>::try_from`. It is public test material
and must never hold funds. Only the resulting verification key, digest, and
signature are serialized. Generating a fresh signature changes its bytes but
does not change the expected digest.

The transaction module accepts version 2 only and uses the published library
transaction digester with the fixed NU7 consensus branch.
The dependency graphs are aligned with voting-crypto-deps 0.4.0, which uses
Common 4 in the default backend and the matching upstream libraries in the
other backend. Both backend families must match the NU7 vector and reject the
legacy vector.
