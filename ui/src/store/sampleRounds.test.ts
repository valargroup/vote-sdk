import { describe, expect, it } from "vitest";
import {
  applySampleTemplateOptionDescriptions,
  createSampleRoundFromTemplate,
} from "./sampleRounds";

describe("sample round templates", () => {
  it("creates sample options with template descriptions", () => {
    const round = createSampleRoundFromTemplate("silly-governance");
    const proposal = round.proposals.find(
      (item) => item.title === "Official Snack of the Next Team Sync",
    );

    expect(proposal?.options.map((option) => option.description)).toEqual([
      "Classic default for broad appeal.",
      "Flexible, configurable, and easy to share.",
      "Compact, filling, and excellent for a team table.",
      "Breakfast energy with maximum surface area.",
    ]);
  });

  it("hydrates missing descriptions in stored sample rounds", () => {
    const round = createSampleRoundFromTemplate("silly-governance");
    const legacyRound = {
      ...round,
      proposals: round.proposals.map((proposal) => ({
        ...proposal,
        options: proposal.options.map((option) => ({
          ...option,
          description: "",
        })),
      })),
    };

    const [hydrated] = applySampleTemplateOptionDescriptions([legacyRound]);

    expect(hydrated.proposals[0].options[0].description).toBe(
      "Classic default for broad appeal.",
    );
    expect(hydrated.proposals[1].options[0].description).toBe(
      "Require decorative desk statues to wear tiny hats on Fridays.",
    );
  });

  it("does not overwrite existing option descriptions", () => {
    const round = createSampleRoundFromTemplate("silly-governance");
    const editedRound = {
      ...round,
      proposals: round.proposals.map((proposal, proposalIndex) => ({
        ...proposal,
        options: proposal.options.map((option, optionIndex) => ({
          ...option,
          description:
            proposalIndex === 0 && optionIndex === 0
              ? "Custom snack rationale."
              : "",
        })),
      })),
    };

    const [hydrated] = applySampleTemplateOptionDescriptions([editedRound]);

    expect(hydrated.proposals[0].options[0].description).toBe(
      "Custom snack rationale.",
    );
    expect(hydrated.proposals[0].options[1].description).toBe(
      "Flexible, configurable, and easy to share.",
    );
  });
});
