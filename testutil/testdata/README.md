# Delegation signing vectors

`delegation_tx1_effects_v1.json` is the existing wallet-generated V6/NU6.3
authorization vector. Keep it unchanged when adding signing profiles.

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

The NU7 composition in the chain exists because its pinned protocol dependency
still assigns the proposed `0xffffffff` ID to `BranchId::Nu7`. It uses the
assigned ID without updating the proof circuits or their verifying keys.
The frozen Common 4 vector tests agreement across that dependency boundary.
