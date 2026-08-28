import { describe, expect, it } from "vitest";
import {
  batchRoundNames,
  buildBatchConfigPrIntent,
  matchBatchRounds,
} from "./batchRounds";

const ROUND_ID_A = "a".repeat(64);
const ROUND_ID_B = "b".repeat(64);

function base64FromHex(hex: string): string {
  const bytes = hex.match(/.{2}/g)!.map((pair) => parseInt(pair, 16));
  return btoa(String.fromCharCode(...bytes));
}

describe("batchRoundNames", () => {
  it("appends 1-based indexes to the trimmed base name", () => {
    expect(batchRoundNames(" Load test ", 3)).toEqual([
      "Load test 1",
      "Load test 2",
      "Load test 3",
    ]);
  });

  it("returns an empty list for zero rounds", () => {
    expect(batchRoundNames("x", 0)).toEqual([]);
  });
});

describe("matchBatchRounds", () => {
  it("matches rounds by exact title and normalizes ids", () => {
    const matches = matchBatchRounds(
      ["Load test 1", "Load test 2"],
      [
        {
          title: "Load test 1",
          vote_round_id: base64FromHex(ROUND_ID_A),
          status: "SESSION_STATUS_ACTIVE",
          ea_pk: "ea-key",
        },
        { title: "Unrelated", vote_round_id: base64FromHex(ROUND_ID_B) },
      ]
    );
    expect(matches.get("Load test 1")).toEqual({
      roundIdHex: ROUND_ID_A,
      isActive: true,
      eaPk: "ea-key",
    });
    expect(matches.has("Load test 2")).toBe(false);
  });

  it("reports pending rounds as not active without an ea_pk", () => {
    const matches = matchBatchRounds(
      ["Load test 1"],
      [
        {
          title: "Load test 1",
          vote_round_id: ROUND_ID_A,
          status: "SESSION_STATUS_PENDING",
        },
      ]
    );
    expect(matches.get("Load test 1")).toEqual({
      roundIdHex: ROUND_ID_A,
      isActive: false,
      eaPk: null,
    });
  });

  it("tolerates a missing round list", () => {
    expect(matchBatchRounds(["Load test 1"], undefined).size).toBe(0);
  });
});

describe("buildBatchConfigPrIntent", () => {
  it("serializes with the server's canonical field order", () => {
    const intent = buildBatchConfigPrIntent(
      [
        {
          round_id: ROUND_ID_A,
          signed_payload_hash: "hash-a",
          entry_sha256: "entry-a",
        },
      ],
      1700000000
    );
    expect(intent).toBe(
      `{"action":"create_config_pr_batch","rounds":[{"round_id":"${ROUND_ID_A}","signed_payload_hash":"hash-a","entry_sha256":"entry-a"}],"timestamp":1700000000}`
    );
  });
});
