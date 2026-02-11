/**
 * API tests for delegation submission (MsgRegisterDelegation / ZKP #1).
 *
 * Prerequisites:
 *   1. Build Rust circuits: make circuits
 *   2. Regenerate fixtures: make fixtures
 *   3. Build chain with real RedPallas + Halo2 verification: make install-ffi
 *   4. Start chain: make init-ffi && make start
 *
 * The delegation payloads use:
 *   - Real RedPallas signatures (pre-computed fixtures) verified by the Rust FFI verifier
 *   - Real Halo2 toy proof (pre-computed fixture) verified by the Halo2 FFI verifier
 *
 * The toy circuit convention: gov_comm carries the Halo2 public input (c=252),
 * keeping rk free for RedPallas signature verification.
 */

import { describe, it, expect, beforeAll } from "vitest";
import {
  makeSetupRoundPayload,
  makeDelegationPayload,
  postJSON,
  sleep,
  BLOCK_WAIT_MS,
  repeatByte,
  toBase64,
} from "./helpers.js";

describe("Delegation", () => {
  // Each test group sets up its own round to avoid cross-test interference.

  describe("happy path", () => {
    let roundId: Uint8Array;

    beforeAll(async () => {
      const { body, roundId: rid } = makeSetupRoundPayload();
      roundId = rid;

      const res = await postJSON("/zally/v1/setup-round", body);
      expect(res.json.code).toBe(0);

      // Wait for the round to be committed
      await sleep(BLOCK_WAIT_MS);
    });

    it("should submit a delegation with real RedPallas signature and get code 0", async () => {
      const delegationBody = makeDelegationPayload(roundId);
      const { status, json } = await postJSON(
        "/zally/v1/submit-delegation",
        delegationBody,
      );

      expect(status).toBe(200);
      expect(json.code, `delegation rejected: ${json.log}`).toBe(0);
      expect(json.tx_hash).toBeTruthy();
    });
  });

  describe("invalid round ID", () => {
    it("should reject delegation for a non-existent round", async () => {
      // Use a random round ID that hasn't been set up
      const fakeRoundId = repeatByte(0xff, 32);
      const delegationBody = makeDelegationPayload(fakeRoundId);

      const { status, json } = await postJSON(
        "/zally/v1/submit-delegation",
        delegationBody,
      );

      // The REST layer returns 200 with the CometBFT broadcast result,
      // but code != 0 indicates the tx was rejected by the ante handler.
      expect(status).toBe(200);
      expect(json.code).not.toBe(0);
      expect(json.log).toBeTruthy();
    });
  });

  describe("duplicate nullifiers", () => {
    let roundId: Uint8Array;

    beforeAll(async () => {
      // Create a fresh round for this test group
      const { body, roundId: rid } = makeSetupRoundPayload();
      roundId = rid;

      const res = await postJSON("/zally/v1/setup-round", body);
      expect(res.json.code).toBe(0);

      await sleep(BLOCK_WAIT_MS);
    });

    it("should reject a second delegation that reuses the same nullifiers", async () => {
      // First delegation -- should succeed
      const delegation1 = makeDelegationPayload(roundId);
      const res1 = await postJSON(
        "/zally/v1/submit-delegation",
        delegation1,
      );
      expect(res1.json.code, `first delegation rejected: ${res1.json.log}`).toBe(0);

      // Wait for the first tx to be committed so nullifiers are recorded
      await sleep(BLOCK_WAIT_MS);

      // Second delegation reuses the SAME gov_nullifiers from the first one.
      // cmx_new is still unique; gov_comm is the same (Halo2 public input).
      const delegation2 = makeDelegationPayload(roundId);
      delegation2.gov_nullifiers = delegation1.gov_nullifiers; // reuse spent nullifiers

      const res2 = await postJSON(
        "/zally/v1/submit-delegation",
        delegation2,
      );

      // Should be rejected because gov_nullifiers are already spent
      expect(res2.status).toBe(200);
      expect(res2.json.code).not.toBe(0);
      expect(res2.json.log).toMatch(/nullifier/i);
    });
  });

  describe("validation errors", () => {
    it("should reject delegation with missing rk field", async () => {
      const { body, roundId } = makeSetupRoundPayload();
      await postJSON("/zally/v1/setup-round", body);
      await sleep(BLOCK_WAIT_MS);

      // Build a delegation with rk removed
      const delegation = makeDelegationPayload(roundId);
      const { rk, ...withoutRk } = delegation;

      const { status, json } = await postJSON(
        "/zally/v1/submit-delegation",
        withoutRk,
      );

      // Should fail ValidateBasic (rk must be 32 bytes)
      expect(status).toBe(400);
      expect(json.error).toMatch(/rk/i);
    });

    it("should reject delegation with empty proof", async () => {
      const { body, roundId } = makeSetupRoundPayload();
      await postJSON("/zally/v1/setup-round", body);
      await sleep(BLOCK_WAIT_MS);

      const delegation = {
        ...makeDelegationPayload(roundId),
        proof: "", // empty base64 = empty bytes
      };

      const { status, json } = await postJSON(
        "/zally/v1/submit-delegation",
        delegation,
      );

      expect(status).toBe(400);
      expect(json.error).toMatch(/proof/i);
    });
  });
});
