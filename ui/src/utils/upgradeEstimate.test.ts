import { describe, expect, it } from "vitest";
import { estimateUpgradeHeight, sampleHeightForWindow } from "./upgradeEstimate";

describe("estimateUpgradeHeight", () => {
  it("estimates a future target height from sampled block speed", () => {
    const estimate = estimateUpgradeHeight({
      latestHeight: 1_000,
      latestTimeMs: Date.parse("2026-05-05T00:00:50Z"),
      sampleHeight: 950,
      sampleTimeMs: Date.parse("2026-05-05T00:00:00Z"),
      targetTimeMs: Date.parse("2026-05-05T00:02:50Z"),
    });

    expect(estimate.sampledBlocks).toBe(50);
    expect(estimate.averageSecondsPerBlock).toBe(1);
    expect(estimate.blocksUntilTarget).toBe(120);
    expect(estimate.targetHeight).toBe(1_120);
    expect(new Date(estimate.estimatedTimeMs).toISOString()).toBe("2026-05-05T00:02:50.000Z");
  });

  it("rounds up partial blocks so the estimate is not before the requested time", () => {
    const estimate = estimateUpgradeHeight({
      latestHeight: 20,
      latestTimeMs: 30_000,
      sampleHeight: 10,
      sampleTimeMs: 10_000,
      targetTimeMs: 35_001,
    });

    expect(estimate.averageSecondsPerBlock).toBe(2);
    expect(estimate.blocksUntilTarget).toBe(3);
    expect(estimate.targetHeight).toBe(23);
  });

  it("rejects a target at or before the latest sampled block time", () => {
    expect(() =>
      estimateUpgradeHeight({
        latestHeight: 20,
        latestTimeMs: 20_000,
        sampleHeight: 10,
        sampleTimeMs: 10_000,
        targetTimeMs: 20_000,
      }),
    ).toThrow("target time must be after the latest block time");
  });

  it("rejects invalid samples", () => {
    expect(() =>
      estimateUpgradeHeight({
        latestHeight: 20,
        latestTimeMs: 10_000,
        sampleHeight: 20,
        sampleTimeMs: 5_000,
        targetTimeMs: 30_000,
      }),
    ).toThrow("sample height must be below latest height");

    expect(() =>
      estimateUpgradeHeight({
        latestHeight: 20,
        latestTimeMs: 10_000,
        sampleHeight: 10,
        sampleTimeMs: 10_000,
        targetTimeMs: 30_000,
      }),
    ).toThrow("sample block time must be before latest block time");
  });
});

describe("sampleHeightForWindow", () => {
  it("uses a configurable block window", () => {
    expect(sampleHeightForWindow(1_000, 50)).toBe(950);
    expect(sampleHeightForWindow(1_000, 20)).toBe(980);
  });

  it("does not sample below height 1", () => {
    expect(sampleHeightForWindow(10, 50)).toBe(1);
  });
});
