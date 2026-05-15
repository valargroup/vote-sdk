import { v4 as uuidv4 } from "uuid";
import type { Proposal, ProposalType, VotingRound } from "../types";

export type SampleRoundTemplateId = string;

interface SampleProposalTemplate {
  title: string;
  description: string;
  type: ProposalType;
  labels: string[];
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
        labels: ["Pizza", "Tacos", "Dumplings", "Waffles"],
      },
      {
        title: "Tiny Hat Fridays",
        description:
          "Should all decorative desk statues be required to wear tiny hats on Fridays?",
        type: "binary",
        labels: ["Yes", "No"],
      },
      {
        title: "Espresso Machine Codename",
        description:
          "Pick the codename for the office espresso machine during the next release cycle.",
        type: "multi-choice",
        labels: [
          "Bean Machine",
          "The Wake Engine",
          "Professor Buzz",
          "Steam Team",
        ],
      },
      {
        title: "What's the Coolest Zebra Name?",
        description: "Which name has the strongest stripes?",
        type: "multi-choice",
        labels: ["Ziggy", "Zephyr", "Moxie"],
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
          "Preserve halvings. Keep the existing halving schedule for new ZEC. Only fees and donated funds are smoothed and reissued.",
          "Smooth issuance curve. Replace halvings with a gradual issuance curve. NSM-recycled funds reissue along the same curve.",
          "Do not include issuance smoothing in NU7. (Fee burning still proceeds.)",
        ],
        zipNumber: "ZIP 233, ZIP 234",
      },
      {
        title: "When fee reissuance begins",
        description: `NSM has prior coinholder approval. This question concerns the start of fee reissuance under the mechanism.

When should NSM reissuance of transaction fees begin?`,
        type: "binary",
        labels: [
          "Immediately upon NSM activation",
          "After the fourth halving (around 2032)",
        ],
      },
      {
        title: "Sprout deprecation timing",
        description: `The Sprout pool was deprecated in 2018. Deposits are disabled, it holds 25,400 ZEC, and it accounts for under 0.1% of transaction volume. Deprecation of v4 transactions is now broadly accepted; only timing is open.

Affected funds will not be recycled via the NSM.

When should v4 transactions be disabled?`,
        type: "binary",
        labels: [
          "Immediately at NU7 activation",
          "One year after this poll concludes",
        ],
      },
      {
        title: "Memo bundles",
        description: `ZIP 231 (https://github.com/zcash/zips/blob/main/zips/zip-0231.md) has been updated with additional rationale since the prior poll, where results diverged (76.8% ZCAP support, 99.3% coinholder opposition on 62 ballots).

Do you support activating memo bundles for Orchard in NU7?`,
        type: "binary",
        labels: ["Yes", "No"],
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
        labels: ["Yes", "No"],
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
        labels: ["Yes, proceed", "No"],
      },
    ],
  },
];

export const DEFAULT_SAMPLE_ROUND_TEMPLATE_ID = SAMPLE_ROUND_TEMPLATES[0].id;

function makeProposal(template: SampleProposalTemplate): Proposal {
  return {
    id: uuidv4(),
    title: template.title,
    description: template.description,
    type: template.type,
    options: template.labels.map((label) => ({ id: uuidv4(), label })),
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
