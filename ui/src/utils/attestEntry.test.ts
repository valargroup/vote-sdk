import { beforeEach, describe, expect, it, vi } from "vitest";
import { ed25519 } from "@noble/curves/ed25519.js";
import { generateKeypair } from "../api/votingKey";
import * as chainApi from "../api/chain";
import {
  buildSignedRoundEntry,
  base64ToBytes,
  bytesToBase64,
  normalizeRoundId,
  sha256Hex,
  validateEaPK,
} from "./attestEntry";
import { canonicalPayloadV2 } from "./roundAuth";

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
    const signature = base64ToBytes(signed.entry.signatures[0].sig);
    const publicKey = base64ToBytes(key.publicKeyB64);
    expect(ed25519.verify(signature, canonicalPayloadV2(ROUND_ID, EA_PK, {
      pir_depth: 20, tier0_layers: 12, tier1_layers: 8, poly_len: 4096,
    }), publicKey)).toBe(true);
    expect(ed25519.verify(signature, canonicalPayloadV2(ROUND_ID, EA_PK, {
      pir_depth: 19, tier0_layers: 12, tier1_layers: 7, poly_len: 4096,
    }), publicKey)).toBe(false);
  });

  it("discards a server response that still authorizes the old layout", async () => {
    const oldPayload = canonicalPayloadV2(ROUND_ID, EA_PK, {
      pir_depth: 19, tier0_layers: 12, tier1_layers: 7, poly_len: 4096,
    });
    vi.mocked(chainApi.attestRoundEntry).mockResolvedValue({
      canonical_payload_b64: bytesToBase64(oldPayload),
      signed_payload_hash: await sha256Hex(oldPayload),
      auth_version: 2,
    });
    const signed = await buildSignedRoundEntry(ROUND_ID, EA_PK, generateKeypair("test-signer"));
    expect(signed.usedLocalFallback).toBe(true);
    expect(signed.signedPayloadHash).not.toBe(await sha256Hex(oldPayload));
    expect(chainApi.attestRoundEntry).toHaveBeenCalledWith({
      round_id: ROUND_ID,
      ea_pk: EA_PK,
      auth_version: 2,
      pir_layout: { pir_depth: 20, tier0_layers: 12, tier1_layers: 8, poly_len: 4096 },
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
