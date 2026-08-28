import { beforeEach, describe, expect, it, vi } from "vitest";
import { generateKeypair } from "../api/votingKey";
import * as chainApi from "../api/chain";
import {
  buildSignedRoundEntry,
  normalizeRoundId,
  validateEaPK,
} from "./attestEntry";

vi.mock("../api/chain", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api/chain")>();
  return { ...actual, attestRoundEntry: vi.fn() };
});

const ROUND_ID = "a".repeat(64);
const EA_PK = btoa(String.fromCharCode(...new Uint8Array(32).fill(1)));

describe("normalizeRoundId", () => {
  it("accepts 64-char lowercase hex directly", () => {
    expect(normalizeRoundId(` ${ROUND_ID} `)).toBe(ROUND_ID);
  });

  it("converts base64-encoded 32-byte ids to hex", () => {
    const b64 = btoa(String.fromCharCode(...new Uint8Array(32).fill(0xaa)));
    expect(normalizeRoundId(b64)).toBe("aa".repeat(32));
  });

  it("rejects malformed values", () => {
    expect(normalizeRoundId(undefined)).toBeNull();
    expect(normalizeRoundId("nope")).toBeNull();
  });
});

describe("validateEaPK", () => {
  it("accepts base64 32-byte keys and rejects others", () => {
    expect(validateEaPK(EA_PK)).toBe(true);
    expect(validateEaPK(btoa("short"))).toBe(false);
    expect(validateEaPK("!not-base64!")).toBe(false);
  });
});

describe("buildSignedRoundEntry", () => {
  beforeEach(() => {
    vi.mocked(chainApi.attestRoundEntry).mockReset();
  });

  it("falls back to the local v2 payload when the server is unavailable", async () => {
    vi.mocked(chainApi.attestRoundEntry).mockRejectedValue(new Error("down"));
    const key = generateKeypair("test-signer");
    const signed = await buildSignedRoundEntry(ROUND_ID, EA_PK, key);
    expect(signed.usedLocalFallback).toBe(true);
    expect(signed.signedPayloadHash).toMatch(/^[0-9a-f]{64}$/);
    expect(signed.entry).toMatchObject({
      auth_version: 2,
      ea_pk: EA_PK,
      signatures: [{ key_id: "test-signer", alg: "ed25519" }],
    });
  });

  it("rejects a server payload that does not bind the requested round", async () => {
    vi.mocked(chainApi.attestRoundEntry).mockResolvedValue({
      canonical_payload_b64: btoa("tampered"),
      signed_payload_hash: "f".repeat(64),
      auth_version: 2,
    });
    const key = generateKeypair("test-signer");
    const signed = await buildSignedRoundEntry(ROUND_ID, EA_PK, key);
    // Mismatched server response must be discarded for the local payload.
    expect(signed.usedLocalFallback).toBe(true);
    expect(signed.signedPayloadHash).not.toBe("f".repeat(64));
  });

  it("uses the matching server response without fallback", async () => {
    vi.mocked(chainApi.attestRoundEntry).mockImplementation(async () => {
      const local = await buildLocalResponse();
      return local;
    });
    const key = generateKeypair("test-signer");
    const signed = await buildSignedRoundEntry(ROUND_ID, EA_PK, key);
    expect(signed.usedLocalFallback).toBe(false);
  });
});

async function buildLocalResponse(): Promise<chainApi.AttestRoundEntryResponse> {
  const { canonicalPayloadV2 } = await import("./roundAuth");
  const { bytesToBase64, sha256Hex, AUTHORIZATION_PIR_LAYOUT } = await import(
    "./attestEntry"
  );
  const payload = canonicalPayloadV2(ROUND_ID, EA_PK, AUTHORIZATION_PIR_LAYOUT);
  return {
    canonical_payload_b64: bytesToBase64(payload),
    signed_payload_hash: await sha256Hex(payload),
    auth_version: 2,
  };
}
