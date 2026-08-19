import { describe, expect, it } from "vitest";
import type { VotingRound } from "../types";
import { generateRoundExport, parseRoundJson } from "./roundJson";

const round: VotingRound = {
  id: "round-id",
  name: "Community priorities",
  status: "published",
  settings: {
    description: "Choose the next priorities.",
    snapshotHeight: "3150000",
    endTime: "2026-09-01T18:00:00.000Z",
    openUntilClosed: true,
    defaultProposalType: "binary",
    defaultLabels: ["Support", "Oppose"],
    discussionURL: "https://forum.example/round",
  },
  proposals: [
    {
      id: "proposal-id",
      title: "Fund documentation",
      description: "Expand the operator guide.",
      type: "multi-choice",
      options: [
        {
          id: "option-a",
          label: "Full grant",
          description: "Fund the complete proposal.",
        },
        {
          id: "option-b",
          label: "Pilot",
          description: "Start with a smaller pilot.",
        },
      ],
      zipNumber: "321",
      forumURL: "https://forum.example/proposal",
      metadata: [{ key: "team", value: "docs" }],
    },
  ],
  createdAt: "2026-08-01T00:00:00.000Z",
  updatedAt: "2026-08-02T00:00:00.000Z",
};

describe("round JSON", () => {
  it("decodes an exported round into the same editable content", () => {
    const exported = generateRoundExport(
      round,
      "2026-08-19T12:00:00.000Z",
    );

    expect(parseRoundJson(JSON.stringify(exported))).toEqual({
      name: round.name,
      settings: round.settings,
      proposals: [
        {
          title: round.proposals[0].title,
          description: round.proposals[0].description,
          type: round.proposals[0].type,
          options: [
            {
              label: round.proposals[0].options[0].label,
              description: round.proposals[0].options[0].description,
            },
            {
              label: round.proposals[0].options[1].label,
              description: round.proposals[0].options[1].description,
            },
          ],
          zipNumber: round.proposals[0].zipNumber,
          forumURL: round.proposals[0].forumURL,
          metadata: round.proposals[0].metadata,
        },
      ],
    });
  });

  it("accepts an unwrapped exported round and older options without descriptions", () => {
    const exported = generateRoundExport(round);
    const unwrapped = JSON.parse(JSON.stringify(exported.round)) as {
      proposals: Array<{ options: Array<{ description?: string }> }>;
    };
    delete unwrapped.proposals[0].options[0].description;

    const imported = parseRoundJson(JSON.stringify(unwrapped));

    expect(imported.proposals[0].options[0].description).toBe("");
  });

  it("rejects unsupported schemas and malformed round fields", () => {
    const exported = generateRoundExport(round);

    expect(() =>
      parseRoundJson(JSON.stringify({ ...exported, schema: "v2" })),
    ).toThrow(/Expected "v1"/);
    expect(() =>
      parseRoundJson(
        JSON.stringify({
          ...exported,
          round: { ...exported.round, proposals: "not an array" },
        }),
      ),
    ).toThrow(/round\.proposals must be an array/);
  });
});
