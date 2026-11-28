//! Verifying-key fingerprints of the four vote circuits, which capability
//! field 5 publishes (contracts-v1 §8, §10).

use std::sync::OnceLock;

use voting_crypto_deps::halo2_proofs::{pasta::EqAffine, plonk::VerifyingKey};

use crate::{delegation, proxy_delegation, share_reveal, vote_proof};

/// Returns the verifying-key fingerprints of ZKP #1, #2, #3 and #4, in that
/// order: BLAKE2b-256 of `format!("{:?}", vk.pinned())`, the values
/// voting-circuits' `vk_fingerprint_unchanged` tests pin.
///
/// The first call generates any keys not yet cached; the result, or the first
/// key generation error, is kept for the life of the process.
pub fn circuit_fingerprints() -> Result<[[u8; 32]; 4], String> {
    static FINGERPRINTS: OnceLock<Result<[[u8; 32]; 4], String>> = OnceLock::new();
    FINGERPRINTS
        .get_or_init(|| {
            let fingerprint = |vk: &VerifyingKey<EqAffine>| -> [u8; 32] {
                let pinned = format!("{:?}", vk.pinned());
                blake2b_simd::Params::new()
                    .hash_length(32)
                    .hash(pinned.as_bytes())
                    .as_bytes()
                    .try_into()
                    .expect("a 32-byte hash")
            };
            let keygen = |name: &str, err: voting_circuits::ProveError| {
                format!("{name}: key generation failed: {err}")
            };
            Ok([
                fingerprint(
                    &delegation::delegation_cached_keys()
                        .map_err(|e| keygen("zkp1", e))?
                        .2,
                ),
                fingerprint(
                    &vote_proof::vote_proof_cached_keys()
                        .map_err(|e| keygen("zkp2", e))?
                        .2,
                ),
                fingerprint(
                    &share_reveal::share_reveal_cached_keys()
                        .map_err(|e| keygen("zkp3", e))?
                        .2,
                ),
                fingerprint(
                    &proxy_delegation::proxy_delegation_cached_keys()
                        .map_err(|e| keygen("zkp4", e))?
                        .2,
                ),
            ])
        })
        .clone()
}

#[cfg(test)]
mod tests {
    /// The fingerprints are those voting-circuits pins for ZKP #1-#4.
    #[test]
    fn circuit_fingerprints_match_pinned_values() {
        let fingerprints = super::circuit_fingerprints().expect("fingerprints");
        assert_eq!(
            fingerprints.map(hex::encode),
            [
                "e1773ecd2eb8531cc54c1df5b935a978788ef4eea4949cb45a00edf550ecee81",
                "95d9d97b0818ffe3f355bf0656aed154dbbffd9bfde4158766aac0bc4977595d",
                "e7165da431d5c6f8396d082aba6abfd0213c567071ca4435b38d092242cc4837",
                "4ec7e24b9bbf275807a613d3d3ea94cfae215083b7aa91374b79de52bf7d35aa",
            ]
        );
    }
}
