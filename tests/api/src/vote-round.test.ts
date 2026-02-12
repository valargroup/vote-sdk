/**
 * API tests for vote round setup and querying.
 *
 * Prerequisites: chain running locally (make init && make start).
 */

import { describe, it, expect } from "vitest";
import {
  BASE_URL,
  makeCreateVotingSessionPayload,
  getElGamalFixtures,
  postJSON,
  getJSON,
  toHex,
  sleep,
  BLOCK_WAIT_MS,
} from "./helpers.js";

const fixtures = getElGamalFixtures();
if (!fixtures) {
  throw new Error(
    "ElGamal fixtures not found. Run: go test -run TestGenerateElGamalFixtures ./crypto/elgamal/",
  );
}
const EA_PK = new Uint8Array(Buffer.from(fixtures.ea_pk, "base64"));

describe("Vote Round", () => {
  it("should set up a vote round and return tx_hash with code 0", async () => {
    const { body } = makeCreateVotingSessionPayload(EA_PK);
    const { status, json } = await postJSON("/zally/v1/create-voting-session", body);

    expect(status).toBe(200);
    expect(json.code).toBe(0);
    expect(json.tx_hash).toBeTruthy();
    expect(typeof json.tx_hash).toBe("string");
    expect(json.tx_hash.length).toBeGreaterThan(0);
  });

  it("should query the round after creation", async () => {
    // Use a single payload for both submit and query so the round ID matches.
    const { body, roundId } = makeCreateVotingSessionPayload(EA_PK);
    const roundIdHex = toHex(roundId);

    // Submit the round
    const submitRes = await postJSON("/zally/v1/create-voting-session", body);
    expect(submitRes.status).toBe(200);
    expect(submitRes.json.code).toBe(0);

    // Wait for the tx to be included in a block
    await sleep(BLOCK_WAIT_MS);

    // Query the round by hex-encoded ID
    const { status, json } = await getJSON(`/zally/v1/round/${roundIdHex}`);

    expect(status).toBe(200);
    // The response wraps the round data under a "round" key.
    const round = json.round;
    expect(round).toBeDefined();
    expect(round.snapshot_height).toBeDefined();
    expect(round.vote_end_time).toBeDefined();
    expect(round.creator).toBe(body.creator);
  });

  it("should reject create voting session with missing fields (HTTP 400)", async () => {
    const incompleteBody = {
      creator: "zvote1admin",
      // Missing all required byte fields and vote_end_time
    };

    const { status, json } = await postJSON(
      "/zally/v1/create-voting-session",
      incompleteBody,
    );

    expect(status).toBe(400);
    expect(json.error).toBeTruthy();
  });

  it("should reject create voting session with empty body (HTTP 400)", async () => {
    const res = await fetch(`${BASE_URL}/zally/v1/create-voting-session`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: "",
    });
    const json = await res.json();

    expect(res.status).toBe(400);
    expect(json.error).toMatch(/empty request body/);
  });
});
