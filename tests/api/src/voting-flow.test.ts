/**
 * End-to-end integration test for the full voting flow.
 *
 * Exercises the complete lifecycle:
 *   1. MsgCreateVotingSession — create a round
 *   2. MsgDelegateVote        — delegate with real RedPallas sig + Halo2 proof (ZKP #1)
 *   3. MsgCastVote            — cast vote with mock proof (ZKP #2)
 *   4. MsgRevealShare         — reveal share with mock proof (ZKP #3)
 *   5. Query tally            — verify accumulated vote
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
  postJSON,
  getJSON,
  sleep,
  BLOCK_WAIT_MS,
  toHex,
} from "./helpers.js";

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
