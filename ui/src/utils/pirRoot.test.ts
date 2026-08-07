import { describe, expect, it } from "vitest";
import { normalizePirRoot, type PirRootResponse } from "./pirRoot";

function response(patch: Partial<PirRootResponse> = {}): PirRootResponse {
  return {
    height: 4_200_000,
    num_ranges: 3_682,
    ...patch,
  };
}

describe("PIR root normalization", () => {
  it("uses semantic roots and runtime layout metadata", () => {
    expect(
      normalizePirRoot(response({
        pir_root: "pir",
        circuit_root: "circuit",
        pir_layout: {
          pir_depth: 19,
          tier0_layers: 12,
          tier1_layers: 7,
        },
      }))
    ).toEqual({
      pirRoot: "pir",
      circuitRoot: "circuit",
      layoutKey: "19:12:7",
      layoutLabel: "19 (12+7)",
    });
  });

  it("falls back to legacy root aliases and depth metadata", () => {
    expect(
      normalizePirRoot(response({
        root25: "legacy-pir",
        root29: "legacy-circuit",
        pir_depth: 25,
      }))
    ).toEqual({
      pirRoot: "legacy-pir",
      circuitRoot: "legacy-circuit",
      layoutKey: "depth:25",
      layoutLabel: "25",
    });
  });

  it("prefers semantic roots when both formats are present", () => {
    expect(
      normalizePirRoot(response({
        pir_root: "pir",
        circuit_root: "circuit",
        root25: "legacy-pir",
        root29: "legacy-circuit",
      }))
    ).toMatchObject({
      pirRoot: "pir",
      circuitRoot: "circuit",
    });
  });
});
