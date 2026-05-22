import { describe, expect, it } from "vitest";
import { buildHandoffMessage, buildTrustedKeyEntry } from "./voteManagerHandoff";

describe("buildTrustedKeyEntry", () => {
  it("formats the trusted_keys[] entry with key_id, alg, pubkey, and notes", () => {
    const entry = buildTrustedKeyEntry({
      signerId: "keplr:sv1abc",
      publicKeyB64: "AAAA",
      sourceAddress: "sv1abc",
    });
    expect(JSON.parse(entry)).toEqual({
      key_id: "keplr:sv1abc",
      alg: "ed25519",
      pubkey: "AAAA",
      notes: "Vote-manager Keplr-derived key for sv1abc",
    });
  });
});

describe("buildHandoffMessage", () => {
  const baseInput = {
    sv1Address: "sv1abcxyz",
    trustedKeyEntryJSON: '{"key_id":"keplr:sv1abcxyz"}',
    staticConfigPath: "prod/static-voting-config.json",
  };

  it("includes the on-chain address and trusted key entry", () => {
    const msg = buildHandoffMessage(baseInput);
    expect(msg).toContain("sv1abcxyz");
    expect(msg).toContain('{"key_id":"keplr:sv1abcxyz"}');
  });

  it("references both target locations the recipient will update", () => {
    const msg = buildHandoffMessage(baseInput);
    expect(msg).toContain("vote_manager_addresses");
    expect(msg).toContain("trusted_keys[]");
  });

  it("uses the env-specific static config path", () => {
    expect(buildHandoffMessage(baseInput)).toContain(
      "prod/static-voting-config.json"
    );
    expect(
      buildHandoffMessage({
        ...baseInput,
        staticConfigPath: "stage/static-voting-config.json",
      })
    ).toContain("stage/static-voting-config.json");
  });
});
