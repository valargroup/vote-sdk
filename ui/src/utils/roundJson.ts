import type {
  ProposalMetadata,
  ProposalOption,
  ProposalType,
  RoundSettings,
  VotingRound,
} from "../types";

const ROUND_JSON_SCHEMA = "v1";

type ImportedOption = Omit<ProposalOption, "id">;

interface ImportedProposal {
  title: string;
  description: string;
  type: ProposalType;
  options: ImportedOption[];
  zipNumber: string;
  forumURL: string;
  metadata: ProposalMetadata[];
}

/** Editable round data decoded from an exported round JSON file. */
export interface ImportedRound {
  name: string;
  settings: RoundSettings;
  proposals: ImportedProposal[];
}

type JsonObject = Record<string, unknown>;

function asObject(value: unknown, path: string): JsonObject {
  if (typeof value !== "object" || value === null || Array.isArray(value)) {
    throw new Error(`${path} must be an object.`);
  }
  return value as JsonObject;
}

function asArray(value: unknown, path: string): unknown[] {
  if (!Array.isArray(value)) {
    throw new Error(`${path} must be an array.`);
  }
  return value;
}

function asString(value: unknown, path: string): string {
  if (typeof value !== "string") {
    throw new Error(`${path} must be a string.`);
  }
  return value;
}

function asOptionalString(value: unknown, path: string): string {
  return value === undefined ? "" : asString(value, path);
}

function asBoolean(value: unknown, path: string): boolean {
  if (typeof value !== "boolean") {
    throw new Error(`${path} must be true or false.`);
  }
  return value;
}

function asProposalType(value: unknown, path: string): ProposalType {
  if (value !== "binary" && value !== "multi-choice") {
    throw new Error(`${path} must be "binary" or "multi-choice".`);
  }
  return value;
}

function parseSettings(value: unknown): RoundSettings {
  const settings = asObject(value, "round.settings");
  const defaultLabels = asArray(
    settings.defaultLabels,
    "round.settings.defaultLabels",
  );
  if (defaultLabels.length !== 2) {
    throw new Error("round.settings.defaultLabels must contain two labels.");
  }

  return {
    description: asString(
      settings.description,
      "round.settings.description",
    ),
    snapshotHeight: asString(
      settings.snapshotHeight,
      "round.settings.snapshotHeight",
    ),
    endTime: asString(settings.endTime, "round.settings.endTime"),
    openUntilClosed: asBoolean(
      settings.openUntilClosed,
      "round.settings.openUntilClosed",
    ),
    defaultProposalType: asProposalType(
      settings.defaultProposalType,
      "round.settings.defaultProposalType",
    ),
    defaultLabels: [
      asString(defaultLabels[0], "round.settings.defaultLabels[0]"),
      asString(defaultLabels[1], "round.settings.defaultLabels[1]"),
    ],
    discussionURL: asString(
      settings.discussionURL,
      "round.settings.discussionURL",
    ),
  };
}

function parseMetadata(value: unknown, proposalPath: string): ProposalMetadata[] {
  if (value === undefined) return [];

  return asArray(value, `${proposalPath}.metadata`).map((item, index) => {
    const path = `${proposalPath}.metadata[${index}]`;
    const metadata = asObject(item, path);
    return {
      key: asString(metadata.key, `${path}.key`),
      value: asString(metadata.value, `${path}.value`),
    };
  });
}

function parseProposal(value: unknown, index: number): ImportedProposal {
  const path = `round.proposals[${index}]`;
  const proposal = asObject(value, path);
  const options = asArray(proposal.options, `${path}.options`).map(
    (item, optionIndex) => {
      const optionPath = `${path}.options[${optionIndex}]`;
      const option = asObject(item, optionPath);
      return {
        label: asString(option.label, `${optionPath}.label`),
        description: asOptionalString(
          option.description,
          `${optionPath}.description`,
        ),
      };
    },
  );

  return {
    title: asString(proposal.title, `${path}.title`),
    description: asString(proposal.description, `${path}.description`),
    type: asProposalType(proposal.type, `${path}.type`),
    options,
    zipNumber: asOptionalString(proposal.zipNumber, `${path}.zipNumber`),
    forumURL: asOptionalString(proposal.forumURL, `${path}.forumURL`),
    metadata: parseMetadata(proposal.metadata, path),
  };
}

/** Builds the canonical JSON-safe v1 representation of a local round. */
export function generateRoundExport(
  round: VotingRound,
  generatedAt = new Date().toISOString(),
) {
  return {
    schema: ROUND_JSON_SCHEMA,
    round: {
      id: round.id,
      name: round.name,
      status: round.status,
      settings: round.settings,
      proposals: round.proposals.map((proposal, index) => ({
        index,
        id: proposal.id,
        title: proposal.title,
        description: proposal.description,
        type: proposal.type,
        options: proposal.options.map((option) => ({
          id: option.id,
          label: option.label,
          description: option.description ?? "",
        })),
        zipNumber: proposal.zipNumber,
        forumURL: proposal.forumURL,
        metadata: proposal.metadata,
      })),
    },
    generatedAt,
  };
}

/** Decodes a v1 export or its unwrapped round object into editable round data. */
export function parseRoundJson(json: string): ImportedRound {
  let parsed: unknown;
  try {
    parsed = JSON.parse(json);
  } catch {
    throw new Error("The selected file isn't valid JSON.");
  }

  const root = asObject(parsed, "JSON root");
  let roundValue: unknown = root;

  if ("schema" in root) {
    if (root.schema !== ROUND_JSON_SCHEMA) {
      throw new Error(
        `Unsupported round JSON schema. Expected "${ROUND_JSON_SCHEMA}".`,
      );
    }
    roundValue = root.round;
  } else if ("round" in root) {
    roundValue = root.round;
  }

  const round = asObject(roundValue, "round");
  return {
    name: asString(round.name, "round.name"),
    settings: parseSettings(round.settings),
    proposals: asArray(round.proposals, "round.proposals").map(parseProposal),
  };
}
