import { describe, expect, it } from "vitest";
import parityVector from "../../../internal/keplrderive/testdata/parity_vector.json";
import { deriveEd25519FromKeplrSignature } from "./votingKey";

describe("deriveEd25519FromKeplrSignature", () => {
  it("matches the Go CLI parity vector", () => {
    const key = deriveEd25519FromKeplrSignature({
      address: parityVector.expected_address,
      chainId: parityVector.chain_id,
      signatureB64: parityVector.expected_signature_b64,
    });

    expect(key.signerId).toBe(`keplr:${parityVector.expected_address}`);
    expect(key.seedB64).toBe(parityVector.expected_ed25519_seed_b64);
    expect(key.publicKeyB64).toBe(parityVector.expected_ed25519_pub_b64);
  });
});
