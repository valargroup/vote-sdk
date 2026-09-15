import { describe, it, expect } from "vitest";
import { resolvePIRUpdateScope, PIR_SCOPE_LABELS } from "./pirUpdateScope";

describe("resolvePIRUpdateScope", () => {
  it("resolves the production chain to the prod scope", () => {
    expect(resolvePIRUpdateScope("zvote-1")).toEqual({
      status: "ready",
      scope: "prod",
      chainId: "zvote-1",
    });
  });

  it("resolves the staging chain to the stage scope", () => {
    expect(resolvePIRUpdateScope("svote-1")).toEqual({
      status: "ready",
      scope: "stage",
      chainId: "svote-1",
    });
  });

  it("reports loading while the chain ID is unresolved", () => {
    expect(resolvePIRUpdateScope(null)).toEqual({ status: "loading" });
    expect(resolvePIRUpdateScope(undefined)).toEqual({ status: "loading" });
    expect(resolvePIRUpdateScope("")).toEqual({ status: "loading" });
  });

  it("refuses to guess a scope for an unrecognised chain", () => {
    expect(resolvePIRUpdateScope("upgrade-test-1")).toEqual({
      status: "unknown",
      chainId: "upgrade-test-1",
    });
  });

  it("never falls back to prod for an unknown chain", () => {
    const state = resolvePIRUpdateScope("localnet-1");
    expect(state.status).toBe("unknown");
    expect(state).not.toHaveProperty("scope");
  });

  it("labels both scopes", () => {
    expect(PIR_SCOPE_LABELS.prod).toBe("production");
    expect(PIR_SCOPE_LABELS.stage).toBe("staging");
  });
});
