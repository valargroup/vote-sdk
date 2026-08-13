import { describe, expect, it } from "vitest";
import {
  normalizePirRoot,
  pirLayoutsDiverge,
  type PirRootResponse,
} from "./pirRoot";

function response(patch: Partial<PirRootResponse> = {}): PirRootResponse {
  return {
    height: 4_200_000,
    num_ranges: 3_682,
    dataset_version: 2,
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
          poly_len: 4096,
        },
      }))
    ).toEqual({
      pirRoot: "pir",
      circuitRoot: "circuit",
      layoutDepth: 19,
      layoutKey: "19:12:7:4096",
      layoutLabel: "19 (12+7) · poly_len 4096",
    });
  });

  it("falls back to legacy root aliases and depth metadata", () => {
    expect(
      normalizePirRoot(response({
        dataset_version: 1,
        root25: "legacy-pir",
        root29: "legacy-circuit",
        pir_depth: 25,
      }))
    ).toEqual({
      pirRoot: "legacy-pir",
      circuitRoot: "legacy-circuit",
      layoutDepth: 25,
      layoutKey: undefined,
      layoutLabel: "25",
    });
  });

  it("selects roots according to the dataset version", () => {
    const roots = {
      pir_root: "pir",
      circuit_root: "circuit",
      root25: "legacy-pir",
      root29: "legacy-circuit",
    };

    expect(normalizePirRoot(response({ dataset_version: 1, ...roots }))).toMatchObject({
      pirRoot: "legacy-pir",
      circuitRoot: "legacy-circuit",
    });
    expect(normalizePirRoot(response({ dataset_version: 2, ...roots }))).toMatchObject({
      pirRoot: "pir",
      circuitRoot: "circuit",
    });
  });

  it("treats a missing tier split as unknown when the known depths agree", () => {
    const legacy = normalizePirRoot(response({ pir_depth: 19 }));
    const detailed = normalizePirRoot(response({
      pir_layout: {
        pir_depth: 19,
        tier0_layers: 12,
        tier1_layers: 7,
        poly_len: 4096,
      },
    }));

    expect(pirLayoutsDiverge([legacy, detailed])).toBe(false);
  });

  it("detects different known depths or detailed tier splits", () => {
    const depth19 = normalizePirRoot(response({ pir_depth: 19 }));
    const depth25 = normalizePirRoot(response({ pir_depth: 25 }));
    const split12And7 = normalizePirRoot(response({
      pir_layout: {
        pir_depth: 19,
        tier0_layers: 12,
        tier1_layers: 7,
        poly_len: 4096,
      },
    }));
    const split13And6 = normalizePirRoot(response({
      pir_layout: {
        pir_depth: 19,
        tier0_layers: 13,
        tier1_layers: 6,
        poly_len: 4096,
      },
    }));

    expect(pirLayoutsDiverge([depth19, depth25])).toBe(true);
    expect(pirLayoutsDiverge([split12And7, split13And6])).toBe(true);
  });

  it("detects different polynomial lengths for the same tier layout", () => {
    const poly2048 = normalizePirRoot(response({
      pir_layout: {
        pir_depth: 19,
        tier0_layers: 12,
        tier1_layers: 7,
        poly_len: 2048,
      },
    }));
    const poly4096 = normalizePirRoot(response({
      pir_layout: {
        pir_depth: 19,
        tier0_layers: 12,
        tier1_layers: 7,
        poly_len: 4096,
      },
    }));

    expect(pirLayoutsDiverge([poly2048, poly4096])).toBe(true);
  });
});
