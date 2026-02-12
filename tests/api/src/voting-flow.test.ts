/**
 * End-to-end integration test for the full voting flow.
 *
 * Exercises the complete lifecycle:
 *   1. MsgCreateVotingSession — create a round
 *   2. MsgDelegateVote        — delegate with real RedPallas sig + Halo2 proof (ZKP #1)
 *   3. MsgCastVote            — cast vote with mock proof (ZKP #2)
 *   4. MsgRevealShare         — reveal share with mock proof (ZKP #3)
 *   5. Query tally            — verify accumulated vote
 *   6. MsgSubmitTally         — finalize the session (TALLYING → FINALIZED)
 *
 * ZKP #2 and #3 proofs are mocked: the chain's MockVerifier accepts any bytes
 * in development mode.
 *
 * Prerequisites:
 *   1. Build chain: make install (or make install-ffi for real ZKP #1 verification)
 *   2. Start chain: make init && make start
 *   3. (Optional) Generate fixtures: make fixtures (for real Halo2 proof in delegation)
 */

import { describe, it, expect, beforeAll } from "vitest";
import {
  makeCreateVotingSessionPayload,
  makeDelegateVotePayload,
  makeCastVotePayload,
  makeRevealSharePayload,
  makeSubmitTallyPayload,
  postJSON,
  getJSON,
  sleep,
  BLOCK_WAIT_MS,
  toHex,
} from "./helpers.js";

// ---------------------------------------------------------------------------
// Helper: poll until a round reaches the expected status (integer enum value).
// ---------------------------------------------------------------------------

/** SessionStatus enum values (protobuf int32 serialized by encoding/json). */
const SESSION_STATUS = {
  UNSPECIFIED: 0,
  ACTIVE: 1,
  TALLYING: 2,
  FINALIZED: 3,
} as const;

/**
 * Poll GET /zally/v1/round/{roundIdHex} until the round reaches `expected`
 * status, or throw after `timeoutMs`.
 */
async function waitForRoundStatus(
  roundIdHex: string,
  expected: number,
  timeoutMs = 90_000,
  intervalMs = 3_000,
): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    const { json } = await getJSON(`/zally/v1/round/${roundIdHex}`);
    if (json.round?.status === expected) return;
    await sleep(intervalMs);
  }
  throw new Error(
    `Timed out waiting for round ${roundIdHex} to reach status ${expected}`,
  );
}

// ===========================================================================
// Suite 1: Voting flow (long-lived session — no tallying)
// ===========================================================================

describe("E2E Voting Flow", () => {
  // Shared state across sequential test steps.
  let roundId: Uint8Array;
  let roundIdHex: string;
  let anchorHeight: number;

  // -------------------------------------------------------------------------
  // Step 1: Create voting session
  // -------------------------------------------------------------------------

  beforeAll(async () => {
    const { body, roundId: rid } = makeCreateVotingSessionPayload();
    roundId = rid;
    roundIdHex = toHex(roundId);

    const res = await postJSON("/zally/v1/create-voting-session", body);
    expect(res.json.code, `create session rejected: ${res.json.log}`).toBe(0);

    // Wait for the session creation tx to be included in a block.
    await sleep(BLOCK_WAIT_MS);
  });

  // -------------------------------------------------------------------------
  // Step 2: Delegate vote (ZKP #1 — real RedPallas sig + Halo2 proof)
  // -------------------------------------------------------------------------

  it("step 1: delegate vote succeeds", async () => {
    const delegationBody = makeDelegateVotePayload(roundId);
    const { status, json } = await postJSON(
      "/zally/v1/delegate-vote",
      delegationBody,
    );

    expect(status).toBe(200);
    expect(json.code, `delegation rejected: ${json.log}`).toBe(0);
    expect(json.tx_hash).toBeTruthy();

    // Wait for delegation tx to be included — EndBlocker computes tree root.
    await sleep(BLOCK_WAIT_MS);
  });

  // -------------------------------------------------------------------------
  // Step 3: Query commitment tree for anchor height
  // -------------------------------------------------------------------------

  it("step 2: commitment tree has a computed root after delegation", async () => {
    const { status, json } = await getJSON(
      "/zally/v1/commitment-tree/latest",
    );

    expect(status).toBe(200);
    expect(json.tree).toBeTruthy();
    expect(json.tree.height).toBeGreaterThan(0);

    // Save anchor height for CastVote and RevealShare.
    anchorHeight = json.tree.height;
  });

  // -------------------------------------------------------------------------
  // Step 4: Cast vote (ZKP #2 — mock proof)
  // -------------------------------------------------------------------------

  it("step 3: cast vote succeeds with mock proof", async () => {
    const castBody = makeCastVotePayload(roundId, anchorHeight);
    const { status, json } = await postJSON(
      "/zally/v1/cast-vote",
      castBody,
    );

    expect(status).toBe(200);
    expect(json.code, `cast vote rejected: ${json.log}`).toBe(0);
    expect(json.tx_hash).toBeTruthy();

    // Wait for cast-vote tx to be included — EndBlocker updates tree root.
    await sleep(BLOCK_WAIT_MS);
  });

  // -------------------------------------------------------------------------
  // Step 5: Query updated commitment tree for reveal anchor
  // -------------------------------------------------------------------------

  it("step 4: commitment tree updated after cast vote", async () => {
    const { status, json } = await getJSON(
      "/zally/v1/commitment-tree/latest",
    );

    expect(status).toBe(200);
    expect(json.tree).toBeTruthy();
    expect(json.tree.height).toBeGreaterThanOrEqual(anchorHeight);

    // Update anchor height for RevealShare.
    anchorHeight = json.tree.height;
  });

  // -------------------------------------------------------------------------
  // Step 6: Reveal share (ZKP #3 — mock proof)
  // -------------------------------------------------------------------------

  it("step 5: reveal share succeeds with mock proof", async () => {
    const revealBody = makeRevealSharePayload(roundId, anchorHeight, {
      voteAmount: 1000,
      proposalId: 0,
      voteDecision: 1,
    });
    const { status, json } = await postJSON(
      "/zally/v1/reveal-share",
      revealBody,
    );

    expect(status).toBe(200);
    expect(json.code, `reveal share rejected: ${json.log}`).toBe(0);
    expect(json.tx_hash).toBeTruthy();

    // Wait for reveal tx to be included.
    await sleep(BLOCK_WAIT_MS);
  });

  // -------------------------------------------------------------------------
  // Step 7: Query tally and verify accumulated vote
  // -------------------------------------------------------------------------

  it("step 6: tally reflects the revealed vote", async () => {
    const { status, json } = await getJSON(
      `/zally/v1/tally/${roundIdHex}/0`,
    );

    expect(status).toBe(200);
    expect(json.tally).toBeTruthy();
    // Decision 1 should have accumulated 1000 zatoshi for proposal 0.
    expect(json.tally["1"]).toBe(1000);
  });

  // -------------------------------------------------------------------------
  // Negative paths: duplicate nullifier rejection
  // -------------------------------------------------------------------------

  it("step 7: duplicate cast-vote nullifier is rejected", async () => {
    // Build a cast-vote payload with the same structure — but the chain
    // already recorded the previous van_nullifier. We need to reuse the
    // exact same nullifier to trigger rejection, so we build manually.
    const castBody = makeCastVotePayload(roundId, anchorHeight);
    const res1 = await postJSON("/zally/v1/cast-vote", castBody);
    expect(res1.json.code, `first cast should succeed: ${res1.json.log}`).toBe(0);

    await sleep(BLOCK_WAIT_MS);

    // Resubmit with the SAME van_nullifier — should be rejected.
    const duplicate = { ...makeCastVotePayload(roundId, anchorHeight) };
    duplicate.van_nullifier = castBody.van_nullifier;

    const res2 = await postJSON("/zally/v1/cast-vote", duplicate);
    expect(res2.status).toBe(200);
    expect(res2.json.code).not.toBe(0);
    expect(res2.json.log).toMatch(/nullifier/i);
  });

  it("step 8: duplicate reveal-share nullifier is rejected", async () => {
    const revealBody = makeRevealSharePayload(roundId, anchorHeight, {
      voteAmount: 500,
      proposalId: 0,
      voteDecision: 1,
    });
    const res1 = await postJSON("/zally/v1/reveal-share", revealBody);
    expect(res1.json.code, `first reveal should succeed: ${res1.json.log}`).toBe(0);

    await sleep(BLOCK_WAIT_MS);

    // Resubmit with the SAME share_nullifier — should be rejected.
    const duplicate = { ...makeRevealSharePayload(roundId, anchorHeight) };
    duplicate.share_nullifier = revealBody.share_nullifier;

    const res2 = await postJSON("/zally/v1/reveal-share", duplicate);
    expect(res2.status).toBe(200);
    expect(res2.json.code).not.toBe(0);
    expect(res2.json.log).toMatch(/nullifier/i);
  });
});

// ===========================================================================
// Suite 2: Tallying & finalization lifecycle
//
// Uses a short-lived session (30s expiry) so the EndBlocker transitions
// the round from ACTIVE → TALLYING within the test timeout.
// ===========================================================================

describe("E2E Tallying Lifecycle", () => {
  const SESSION_CREATOR = "zvote1admin";
  const SHORT_EXPIRY_SEC = 30; // session expires 30s after creation

  let roundId: Uint8Array;
  let roundIdHex: string;
  let anchorHeight: number;

  // -------------------------------------------------------------------------
  // Setup: create a short-lived session, run through voting flow, wait for
  // the EndBlocker to transition the round to TALLYING.
  // -------------------------------------------------------------------------

  beforeAll(async () => {
    // 1. Create session with short expiry
    const { body, roundId: rid } = makeCreateVotingSessionPayload({
      expiresInSec: SHORT_EXPIRY_SEC,
    });
    roundId = rid;
    roundIdHex = toHex(roundId);

    const createRes = await postJSON("/zally/v1/create-voting-session", body);
    expect(createRes.json.code, `create session rejected: ${createRes.json.log}`).toBe(0);
    await sleep(BLOCK_WAIT_MS);

    // 2. Delegate vote
    const delegationBody = makeDelegateVotePayload(roundId);
    const delRes = await postJSON("/zally/v1/delegate-vote", delegationBody);
    expect(delRes.json.code, `delegation rejected: ${delRes.json.log}`).toBe(0);
    await sleep(BLOCK_WAIT_MS);

    // 3. Get anchor height
    const treeRes = await getJSON("/zally/v1/commitment-tree/latest");
    expect(treeRes.json.tree).toBeTruthy();
    anchorHeight = treeRes.json.tree.height;

    // 4. Cast vote
    const castBody = makeCastVotePayload(roundId, anchorHeight);
    const castRes = await postJSON("/zally/v1/cast-vote", castBody);
    expect(castRes.json.code, `cast vote rejected: ${castRes.json.log}`).toBe(0);
    await sleep(BLOCK_WAIT_MS);

    // 5. Update anchor
    const tree2 = await getJSON("/zally/v1/commitment-tree/latest");
    anchorHeight = tree2.json.tree.height;

    // 6. Reveal share (during ACTIVE phase)
    const revealBody = makeRevealSharePayload(roundId, anchorHeight, {
      voteAmount: 500,
      proposalId: 0,
      voteDecision: 1,
    });
    const revealRes = await postJSON("/zally/v1/reveal-share", revealBody);
    expect(revealRes.json.code, `reveal share rejected: ${revealRes.json.log}`).toBe(0);
    await sleep(BLOCK_WAIT_MS);

    // 7. Wait for the round to transition to TALLYING (EndBlocker fires once
    //    blockTime >= vote_end_time).
    await waitForRoundStatus(roundIdHex, SESSION_STATUS.TALLYING);
  }, 120_000); // generous timeout for beforeAll

  // -------------------------------------------------------------------------
  // Step 1: Round is confirmed TALLYING
  // -------------------------------------------------------------------------

  it("step 1: round status is TALLYING after expiry", async () => {
    const { status, json } = await getJSON(`/zally/v1/round/${roundIdHex}`);

    expect(status).toBe(200);
    expect(json.round).toBeTruthy();
    expect(json.round.status).toBe(SESSION_STATUS.TALLYING);
  });

  // -------------------------------------------------------------------------
  // Step 2: Reveals still accepted during TALLYING
  // -------------------------------------------------------------------------

  it("step 2: reveal share succeeds during TALLYING", async () => {
    const revealBody = makeRevealSharePayload(roundId, anchorHeight, {
      voteAmount: 250,
      proposalId: 0,
      voteDecision: 1,
    });
    const { status, json } = await postJSON("/zally/v1/reveal-share", revealBody);

    expect(status).toBe(200);
    expect(json.code, `reveal during TALLYING rejected: ${json.log}`).toBe(0);

    await sleep(BLOCK_WAIT_MS);
  });

  // -------------------------------------------------------------------------
  // Step 3: Tally reflects all accumulated reveals
  // -------------------------------------------------------------------------

  it("step 3: tally reflects accumulated reveals", async () => {
    const { status, json } = await getJSON(`/zally/v1/tally/${roundIdHex}/0`);

    expect(status).toBe(200);
    expect(json.tally).toBeTruthy();
    // 500 (beforeAll reveal) + 250 (step 2 reveal) = 750 for decision 1.
    expect(json.tally["1"]).toBe(750);
  });

  // -------------------------------------------------------------------------
  // Step 4: Delegations rejected during TALLYING
  // -------------------------------------------------------------------------

  it("step 4: delegate vote rejected during TALLYING", async () => {
    const delegationBody = makeDelegateVotePayload(roundId);
    const { status, json } = await postJSON("/zally/v1/delegate-vote", delegationBody);

    expect(status).toBe(200);
    expect(json.code).not.toBe(0);
    // The ante handler rejects non-ACTIVE messages for delegation.
    expect(json.log).toBeTruthy();
  });

  // -------------------------------------------------------------------------
  // Step 5: Cast vote rejected during TALLYING
  // -------------------------------------------------------------------------

  it("step 5: cast vote rejected during TALLYING", async () => {
    const castBody = makeCastVotePayload(roundId, anchorHeight);
    const { status, json } = await postJSON("/zally/v1/cast-vote", castBody);

    expect(status).toBe(200);
    expect(json.code).not.toBe(0);
    expect(json.log).toBeTruthy();
  });

  // -------------------------------------------------------------------------
  // Step 6: Submit tally with wrong creator is rejected
  // -------------------------------------------------------------------------

  it("step 6: submit tally with wrong creator is rejected", async () => {
    const tallyBody = makeSubmitTallyPayload(roundId, "zvote1imposter");
    const { status, json } = await postJSON("/zally/v1/submit-tally", tallyBody);

    expect(status).toBe(200);
    expect(json.code).not.toBe(0);
    expect(json.log).toMatch(/creator/i);
  });

  // -------------------------------------------------------------------------
  // Step 7: Submit tally by correct creator succeeds
  // -------------------------------------------------------------------------

  it("step 7: submit tally finalizes the round", async () => {
    const tallyBody = makeSubmitTallyPayload(roundId, SESSION_CREATOR);
    const { status, json } = await postJSON("/zally/v1/submit-tally", tallyBody);

    expect(status).toBe(200);
    expect(json.code, `submit tally rejected: ${json.log}`).toBe(0);
    expect(json.tx_hash).toBeTruthy();

    await sleep(BLOCK_WAIT_MS);
  });

  // -------------------------------------------------------------------------
  // Step 8: Round status is FINALIZED
  // -------------------------------------------------------------------------

  it("step 8: round status is FINALIZED after submit tally", async () => {
    const { status, json } = await getJSON(`/zally/v1/round/${roundIdHex}`);

    expect(status).toBe(200);
    expect(json.round).toBeTruthy();
    expect(json.round.status).toBe(SESSION_STATUS.FINALIZED);
  });

  // -------------------------------------------------------------------------
  // Step 9: Tally preserved after finalization
  // -------------------------------------------------------------------------

  it("step 9: tally is preserved after finalization", async () => {
    const { status, json } = await getJSON(`/zally/v1/tally/${roundIdHex}/0`);

    expect(status).toBe(200);
    expect(json.tally).toBeTruthy();
    // Same accumulated total as step 3 — finalization doesn't alter the tally.
    expect(json.tally["1"]).toBe(750);
  });

  // -------------------------------------------------------------------------
  // Step 10: Reveals rejected after FINALIZED
  // -------------------------------------------------------------------------

  it("step 10: reveal share rejected after finalization", async () => {
    const revealBody = makeRevealSharePayload(roundId, anchorHeight, {
      voteAmount: 100,
      proposalId: 0,
      voteDecision: 1,
    });
    const { status, json } = await postJSON("/zally/v1/reveal-share", revealBody);

    expect(status).toBe(200);
    expect(json.code).not.toBe(0);
    // Round is FINALIZED — no further reveals accepted.
    expect(json.log).toBeTruthy();
  });

  // -------------------------------------------------------------------------
  // Step 11: Submit tally again is rejected (already finalized)
  // -------------------------------------------------------------------------

  it("step 11: submit tally on finalized round is rejected", async () => {
    const tallyBody = makeSubmitTallyPayload(roundId, SESSION_CREATOR);
    const { status, json } = await postJSON("/zally/v1/submit-tally", tallyBody);

    // The chain rejects the tx because the round is FINALIZED, not TALLYING.
    // Depending on how the error surfaces through BroadcastTxSync, the API
    // may return either:
    //   a) HTTP 200 with json.code != 0  (ante rejection in CheckTx response)
    //   b) HTTP 502 with an error body   (broadcast-level error)
    const rejected =
      status !== 200 || (json.code !== undefined && json.code !== 0);
    expect(rejected, `expected rejection but got status=${status} code=${json.code}`).toBe(true);
  });

  // -------------------------------------------------------------------------
  // Step 12: Submit tally on an ACTIVE round is rejected
  // -------------------------------------------------------------------------

  it("step 12: submit tally on ACTIVE round is rejected", async () => {
    // Create a fresh long-lived session (default 1h expiry).
    const { body, roundId: activeRoundId } = makeCreateVotingSessionPayload();
    const createRes = await postJSON("/zally/v1/create-voting-session", body);
    expect(createRes.json.code, `create session rejected: ${createRes.json.log}`).toBe(0);
    await sleep(BLOCK_WAIT_MS);

    // Try to submit tally on the ACTIVE round — should be rejected.
    const tallyBody = makeSubmitTallyPayload(activeRoundId, SESSION_CREATOR);
    const { status, json } = await postJSON("/zally/v1/submit-tally", tallyBody);

    expect(status).toBe(200);
    expect(json.code).not.toBe(0);
    expect(json.log).toMatch(/tallying/i);
  });
}, 180_000); // extended timeout for the tallying suite (includes waiting for expiry)
