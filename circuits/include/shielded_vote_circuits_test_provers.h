/*
 * shielded_vote_circuits_test_provers.h — C header for the test-only provers
 * of the shielded-vote-circuits `test-provers` cargo feature
 * (circuits/src/test_provers.rs, which documents each function).
 *
 * Only the library that `make circuits-test-provers` builds exports these
 * symbols; the production libshielded_vote_circuits.a does not. Used by the
 * Go bindings in testutil/proxyprover/.
 */

#ifndef SHIELDED_VOTE_CIRCUITS_TEST_PROVERS_H
#define SHIELDED_VOTE_CIRCUITS_TEST_PROVERS_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Length of one share record: C1, C2, blind and commitment (32 bytes each),
 * then the plaintext as a little-endian u64. */
#define SV_TEST_SHARE_RECORD_LEN (4 * 32 + 8)

/* Output length of sv_test_prove_proxy_delegation. */
#define SV_TEST_PROXY_DELEGATION_OUT_LEN (7 * 32 + 16 * SV_TEST_SHARE_RECORD_LEN)

/* Output length of sv_test_prove_vote. */
#define SV_TEST_VOTE_OUT_LEN (6 * 32 + 16 * SV_TEST_SHARE_RECORD_LEN)

/* Writes the full-authority VAN commitment of a test identity. */
int32_t sv_test_van_commitment(
    const uint8_t* key_seed,
    uint64_t van_ballots,
    const uint8_t* van_comm_rand,
    const uint8_t* round_id,
    uint8_t* van_out
);

/* Builds and proves one ZKP4 delegation from a 772-byte Merkle path. */
int32_t sv_test_prove_proxy_delegation(
    const uint8_t* key_seed,
    uint64_t van_ballots,
    const uint8_t* van_comm_rand,
    const uint8_t* round_id,
    const uint8_t* merkle_path,
    size_t merkle_path_len,
    uint64_t anchor_height,
    const uint8_t* ea_pk,
    uint64_t alpha,
    uint32_t delegate_index,
    uint64_t delegated_ballots,
    uint8_t dc_slot,
    const uint8_t* dc_seed,
    bool single_share,
    uint8_t* proof_out,
    size_t proof_out_capacity,
    size_t* proof_len_out,
    uint8_t* out,
    size_t out_len
);

/* Builds and proves one ZKP2 vote from a 772-byte Merkle path. */
int32_t sv_test_prove_vote(
    const uint8_t* key_seed,
    uint64_t van_ballots,
    const uint8_t* van_comm_rand,
    const uint8_t* round_id,
    const uint8_t* merkle_path,
    size_t merkle_path_len,
    uint64_t anchor_height,
    uint32_t proposal_id,
    uint32_t vote_decision,
    uint64_t proposal_authority,
    const uint8_t* ea_pk,
    uint64_t alpha,
    uint8_t* proof_out,
    size_t proof_out_capacity,
    size_t* proof_len_out,
    uint8_t* out,
    size_t out_len
);

/* Signs msg with a RedPallas SpendAuth key; writes its key and signature. */
int32_t sv_test_redpallas_sign(
    const uint8_t* sk,
    const uint8_t* msg,
    size_t msg_len,
    uint8_t* vk_out,
    uint8_t* sig_out
);

#ifdef __cplusplus
}
#endif

#endif /* SHIELDED_VOTE_CIRCUITS_TEST_PROVERS_H */
