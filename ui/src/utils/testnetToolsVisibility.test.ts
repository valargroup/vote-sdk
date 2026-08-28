import { describe, expect, it } from "vitest";
import { resolveTestnetToolsVisibility } from "./testnetToolsVisibility";

describe("resolveTestnetToolsVisibility", () => {
  it("is loading until the chain id is detected", () => {
    expect(resolveTestnetToolsVisibility(null)).toBe("loading");
  });

  it("is hidden on production", () => {
    expect(resolveTestnetToolsVisibility("zvote-1")).toBe("hidden");
  });

  it("is visible on staging and local chains", () => {
    expect(resolveTestnetToolsVisibility("svote-1")).toBe("visible");
    expect(resolveTestnetToolsVisibility("svote-local")).toBe("visible");
  });
});
