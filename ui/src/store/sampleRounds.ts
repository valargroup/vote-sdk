import { v4 as uuidv4 } from "uuid";
import type { Proposal, ProposalType, VotingRound } from "../types";

export type SampleRoundTemplateId = string;

type SampleOptionTemplate = string | {
  label: string;
  description?: string;
};

interface SampleProposalTemplate {
  title: string;
  description: string;
  type: ProposalType;
  labels: SampleOptionTemplate[];
  zipNumber?: string;
  forumURL?: string;
}

export interface SampleRoundTemplate {
  id: SampleRoundTemplateId;
  name: string;
  summary: string;
  description: string;
  discussionURL?: string;
  proposals: SampleProposalTemplate[];
}

export const SAMPLE_ROUND_TEMPLATES: SampleRoundTemplate[] = [
  {
    id: "silly-governance",
    name: "(SAMPLE) Very Serious Snack Governance",
    summary:
      "A playful sample vote with binary and multi-choice questions for trying the builder.",
    description:
      "A silly sample round for testing the shielded vote builder without using real governance content.",
    proposals: [
      {
        title: "Official Snack of the Next Team Sync",
        description:
          "Which snack should be recognized as the official snack of the next team sync?",
        type: "multi-choice",
        labels: [
          {
            label: "Pizza",
            description: "Classic default for broad appeal.",
          },
          {
            label: "Tacos",
            description: "Flexible, configurable, and easy to share.",
          },
          {
            label: "Dumplings",
            description: "Compact, filling, and excellent for a team table.",
          },
          {
            label: "Waffles",
            description: "Breakfast energy with maximum surface area.",
          },
        ],
      },
      {
        title: "Tiny Hat Fridays",
        description:
          "Should all decorative desk statues be required to wear tiny hats on Fridays?",
        type: "binary",
        labels: [
          {
            label: "Yes",
            description:
              "Require decorative desk statues to wear tiny hats on Fridays.",
          },
          {
            label: "No",
            description: "Keep desk statue attire optional.",
          },
        ],
      },
      {
        title: "Espresso Machine Codename",
        description:
          "Pick the codename for the office espresso machine during the next release cycle.",
        type: "multi-choice",
        labels: [
          {
            label: "Bean Machine",
            description: "Straightforward and descriptive.",
          },
          {
            label: "The Wake Engine",
            description: "Dramatic, operational, and mildly overpowered.",
          },
          {
            label: "Professor Buzz",
            description: "Academic energy for caffeinated decisions.",
          },
          {
            label: "Steam Team",
            description: "A group-friendly name for the espresso corner.",
          },
        ],
      },
      {
        title: "What's the Coolest Zebra Name?",
        description: "Which name has the strongest stripes?",
        type: "multi-choice",
        labels: [
          {
            label: "Ziggy",
            description: "Friendly and instantly recognizable.",
          },
          {
            label: "Zephyr",
            description: "A lighter, more elegant name.",
          },
          {
            label: "Moxie",
            description: "Short, confident, and memorable.",
          },
        ],
      },
    ],
  },
  {
    id: "nu7-scope",
    name: "(SAMPLE) NU7 Scope Questions",
    summary:
      "Draft six-question NU7 scope poll covering NSM, Sprout, memo bundles, block times, and scheduling.",
    description: `Welcome

This poll resolves outstanding NU7 scope questions following the early-2026 sentiment polling.

Already in NU7 (established by prior consensus, not re-polled):
- Orchard Quantum Recoverability
- Explicit fees
- NSM fee burning
- Extensible transaction format

Not in NU7:
- Project Tachyon. Universal support; targeted for a later upgrade.
- Zcash Shielded Assets. Did not reach clear community support for NU7.

The 6 questions below cover the remaining open items.`,
    proposals: [
      {
        title: "NSM issuance smoothing",
        description: `The fee-burning component of the Network Sustainability Mechanism is already approved. The issuance smoothing component is unresolved (84% ZCAP support, 83.5% coinholder opposition).

Which approach do you support?

Refs: ZIP 233 (https://zips.z.cash/zip-0233), ZIP 234 (https://zips.z.cash/zip-0234)`,
        type: "multi-choice",
        labels: [
          {
            label: "Preserve halvings",
            description: "Keep the existing halving schedule for new ZEC. Only fees and donated funds are smoothed and reissued.",
          },
          {
            label: "Smooth issuance curve",
            description: "Replace halvings with a gradual issuance curve. NSM-recycled funds reissue along the same curve.",
          },
          {
            label: "Do not include issuance smoothing",
            description: "Do not include issuance smoothing in NU7. (Fee burning still proceeds.)",
          },
        ],
        zipNumber: "ZIP 233, ZIP 234",
      },
      {
        title: "When fee reissuance begins",
        description: `NSM has prior coinholder approval. This question concerns the start of fee reissuance under the mechanism.

When should NSM reissuance of transaction fees begin?`,
        type: "binary",
        labels: [
          {
            label: "Immediately upon NSM activation",
            description: "Start NSM reissuance of transaction fees as soon as NSM activates.",
          },
          {
            label: "After the fourth halving",
            description: "Start after the fourth halving, expected around 2032.",
          },
        ],
      },
      {
        title: "Sprout deprecation timing",
        description: `The Sprout pool was deprecated in 2018. Deposits are disabled, it holds 25,400 ZEC, and it accounts for under 0.1% of transaction volume. Deprecation of v4 transactions is now broadly accepted; only timing is open.

Affected funds will not be recycled via the NSM.

When should v4 transactions be disabled?`,
        type: "binary",
        labels: [
          {
            label: "Immediately at NU7 activation",
            description: "Disable v4 transactions when NU7 activates.",
          },
          {
            label: "One year after this poll concludes",
            description: "Delay disabling v4 transactions until one year after this poll concludes.",
          },
        ],
      },
      {
        title: "Memo bundles",
        description: `ZIP 231 (https://github.com/zcash/zips/blob/main/zips/zip-0231.md) has been updated with additional rationale since the prior poll, where results diverged (76.8% ZCAP support, 99.3% coinholder opposition on 62 ballots).

Do you support activating memo bundles for Orchard in NU7?`,
        type: "binary",
        labels: [
          {
            label: "Yes",
            description: "Activate memo bundles for Orchard in NU7.",
          },
          {
            label: "No",
            description: "Do not activate memo bundles for Orchard in NU7.",
          },
        ],
        zipNumber: "ZIP 231",
      },
      {
        title: "Faster block times",
        description: `Reduce block time from 75 s to 25 s, with per-pool action limits at 2 MB blocks?

Daily ZEC issuance unchanged.

Benefits:
- Faster confirmations
- Orchard TPS roughly 2×

Costs:
- Consensus bandwidth roughly 3×
- Sapling TPS down roughly 60% (reduces DOS surface)

Choose:

Ref: ZIP XXX (https://github.com/zcash/zips/pull/1215)`,
        type: "binary",
        labels: [
          {
            label: "Yes",
            description: "Reduce block time from 75 seconds to 25 seconds with the proposed per-pool limits.",
          },
          {
            label: "No",
            description: "Keep the current block time and limits.",
          },
        ],
        zipNumber: "ZIP XXX",
      },
      {
        title: "NU7 schedule",
        description: `Do you support scheduling NU7 as soon as feasible, with these contents?

Always included: Orchard Quantum Recoverability, explicit fees, NSM fee burning, extensible tx format.

Conditional on this poll:
- NSM issuance smoothing if Q1 is option 1 or 2
- Sprout deprecation at the block height implied by Q3
- Memo bundles if Q4 is Yes
- Faster block times and per-pool limits if Q5 is Yes

Readiness clause: A feature is "ready" when spec, audit, and testing are complete, as certified jointly by ZODLECC, the Zcash Foundation, and ZIP Editors. If a feature is not ready by July 15, 2026, it defers to the next upgrade rather than delaying NU7. Dependent features defer with their dependencies.`,
        type: "binary",
        labels: [
          {
            label: "Yes, proceed",
            description: "Schedule NU7 as soon as feasible with the always-included items and the features approved by this poll.",
          },
          {
            label: "No",
            description: "Do not proceed on this NU7 schedule.",
          },
        ],
      },
    ],
  },
];

export const DEFAULT_SAMPLE_ROUND_TEMPLATE_ID = SAMPLE_ROUND_TEMPLATES[0].id;

function optionTemplateFields(option: SampleOptionTemplate) {
  return typeof option === "string"
    ? { label: option, description: "" }
    : { label: option.label, description: option.description ?? "" };
}

function makeProposal(template: SampleProposalTemplate): Proposal {
  return {
    id: uuidv4(),
    title: template.title,
    description: template.description,
    type: template.type,
    options: template.labels.map((option) => ({
      id: uuidv4(),
      ...optionTemplateFields(option),
    })),
    zipNumber: template.zipNumber ?? "",
    forumURL: template.forumURL ?? "",
    metadata: [],
  };
}

export function createSampleRoundFromTemplate(
  templateId: SampleRoundTemplateId = DEFAULT_SAMPLE_ROUND_TEMPLATE_ID,
): VotingRound {
  const template =
    SAMPLE_ROUND_TEMPLATES.find((item) => item.id === templateId) ??
    SAMPLE_ROUND_TEMPLATES[0];
  const now = new Date().toISOString();

  return {
    id: uuidv4(),
    name: template.name,
    status: "draft",
    proposals: template.proposals.map(makeProposal),
    settings: {
      description: template.description,
      snapshotHeight: "",
      endTime: "",
      openUntilClosed: true,
      defaultProposalType: "binary",
      defaultLabels: ["Support", "Oppose"],
      discussionURL: template.discussionURL ?? "",
    },
    createdAt: now,
    updatedAt: now,
  };
}

export function createSampleRounds(): VotingRound[] {
  return SAMPLE_ROUND_TEMPLATES.map((template) =>
    createSampleRoundFromTemplate(template.id),
  );
}

export function applySampleTemplateOptionDescriptions(
  rounds: VotingRound[],
): VotingRound[] {
  const templatesByName = new Map(
    SAMPLE_ROUND_TEMPLATES.map((template) => [template.name, template]),
  );
  let roundsChanged = false;

  const hydratedRounds = rounds.map((round) => {
    const template = templatesByName.get(round.name);
    if (!template) return round;

    const proposalsByTitle = new Map(
      template.proposals.map((proposal) => [proposal.title, proposal]),
    );
    let proposalsChanged = false;

    const proposals = round.proposals.map((proposal) => {
      const templateProposal = proposalsByTitle.get(proposal.title);
      if (!templateProposal) return proposal;

      const descriptionsByLabel = new Map<string, string>();
      for (const templateOption of templateProposal.labels) {
        const { label, description } = optionTemplateFields(templateOption);
        if (description) descriptionsByLabel.set(label, description);
      }
      if (descriptionsByLabel.size === 0) return proposal;

      let optionsChanged = false;
      const options = proposal.options.map((option) => {
        if ((option.description ?? "").trim() !== "") return option;
        const description = descriptionsByLabel.get(option.label);
        if (!description) return option;
        optionsChanged = true;
        return { ...option, description };
      });

      if (!optionsChanged) return proposal;
      proposalsChanged = true;
      return { ...proposal, options };
    });

    if (!proposalsChanged) return round;
    roundsChanged = true;
    return { ...round, proposals };
  });

  return roundsChanged ? hydratedRounds : rounds;
}
