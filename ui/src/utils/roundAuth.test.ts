import { describe, expect, it } from "vitest";
import {
  assertMatchingRoundAuthV2Response,
  canonicalPayloadV2,
} from "./roundAuth";

const layout = {
  pir_depth: 19,
  tier0_layers: 12,
  tier1_layers: 7,
  poly_len: 4096,
};

function toBase64(bytes: Uint8Array): string {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

describe("round-auth v2 response validation", () => {
  it("rejects an old v2 server payload that omits poly_len", () => {
    const payload = canonicalPayloadV2(
      "aa".repeat(32),
      toBase64(Uint8Array.from({ length: 32 }, (_, index) => index)),
      layout
    );
    const expectedPayloadB64 = toBase64(payload);
    const expectedPayloadHash = "expected-hash";

    expect(Array.from(payload.slice(-4))).toEqual([0, 16, 0, 0]);
    expect(() =>
      assertMatchingRoundAuthV2Response(
        {
          canonical_payload_b64: toBase64(payload.slice(0, -4)),
          signed_payload_hash: "legacy-hash",
          auth_version: 2,
        },
        expectedPayloadB64,
        expectedPayloadHash
      )
    ).toThrow(/does not bind/);
  });

  it("rejects a mismatched hash even when the payload matches", () => {
    expect(() =>
      assertMatchingRoundAuthV2Response(
        {
          canonical_payload_b64: "expected-payload",
          signed_payload_hash: "wrong-hash",
          auth_version: 2,
        },
        "expected-payload",
        "expected-hash"
      )
    ).toThrow(/hash/);
  });
});
