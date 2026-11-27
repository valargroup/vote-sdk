import type { CoordinatorAction } from "./chain";

export interface CoordinatorActionDetail {
  label: string;
  value: string;
  mono?: boolean;
  /** Shown as a warning: the approver must not miss it. */
  warning?: boolean;
}

export interface CoordinatorActionDescription {
  canApprove: boolean;
  rows: CoordinatorActionDetail[];
  json: {
    type_url: string;
    value: Record<string, unknown> | string;
  };
  jsonDecoded: boolean;
  error?: string;
  /**
   * The fewest current coordinator approvals the chain executes this action
   * with, whatever the policy threshold: PROXY_APPROVAL_FLOOR for a proxy
   * delegation payload or a flagged MsgCreateVotingSession, else 0.
   */
  approvalFloor: number;
}

interface DecodedCoordinatorActionPayload {
  rows: CoordinatorActionDetail[];
  value: Record<string, unknown>;
  approvalFloor?: number;
}

/**
 * The chain executes the three proxy delegation payloads, and a
 * MsgCreateVotingSession with proxy_delegation set, only with at least
 * max(2, threshold) distinct current coordinator approvals, so never inside
 * the proposing transaction (contracts-v1 §5.5; MinProxyCoordinatorApprovals
 * in x/vote/types).
 */
export const PROXY_APPROVAL_FLOOR = 2;

/** Actions of MsgSetDelegateStatus (contracts-v1 §2.4), by what each does to a delegate. */
const STATUS_ACTIONS: Record<number, { title: string; effect: string }> = {
  1: { title: "Suspend delegates", effect: "suspend" },
  2: { title: "Lift suspensions", effect: "lift the suspension" },
  3: { title: "Retire delegates for good", effect: "retire" },
};

/** Reason codes of MsgSetDelegateStatus (contracts-v1 §2.4). */
const STATUS_REASONS: Record<number, string> = {
  1: "impersonation",
  2: "key lost or compromised",
  3: "legal",
  4: "verifier review",
  99: "other",
};

interface ProtoField {
  number: number;
  wireType: number;
}

interface DecodedProposal {
  id: number;
  title: string;
  description: string;
  options: Array<{ index: number; label: string; description: string }>;
  zipNumber: string;
  forumURL: string;
}

const textDecoder = new TextDecoder();

class ProtoReader {
  private readonly bytes: Uint8Array;
  private offset = 0;
  // Shared with nested readers, so the top-level reader lists every field
  // the decoders skipped, as dotted paths such as "proposals.options.4".
  readonly unknownFields: string[];
  private readonly path: string;

  constructor(bytes: Uint8Array, unknownFields: string[] = [], path = "") {
    this.bytes = bytes;
    this.unknownFields = unknownFields;
    this.path = path;
  }

  /** Reads a nested message's bytes and returns a reader over them that records unknown fields under name. */
  nested(name: string): ProtoReader {
    return new ProtoReader(this.readBytes(), this.unknownFields, `${this.path}${name}.`);
  }

  /** Skips a field the decoder does not know, recording it so the payload cannot be approved unseen. */
  skipUnknown(field: ProtoField) {
    this.unknownFields.push(`${this.path}${field.number}`);
    this.skip(field.wireType);
  }

  eof(): boolean {
    return this.offset >= this.bytes.length;
  }

  readField(): ProtoField | null {
    if (this.eof()) return null;
    const key = this.readVarint();
    return { number: Math.floor(key / 8), wireType: key & 7 };
  }

  readVarint(): number {
    const value = this.readVarintBigInt();
    if (value > BigInt(Number.MAX_SAFE_INTEGER)) {
      throw new Error("varint exceeds JavaScript's safe integer range");
    }
    return Number(value);
  }

  /**
   * Reads a uint32 field. The chain's decoder keeps only the low 32 bits of a
   * longer varint, so a value past 32 bits is refused rather than shown as a
   * number the chain would not execute.
   */
  readUint32(): number {
    const value = this.readVarintBigInt();
    if (value > 0xffffffffn) {
      throw new Error("uint32 field exceeds 32 bits");
    }
    return Number(value);
  }

  // Return decimal text so 64-bit protobuf values remain exact in JSON.
  readUint64(): string {
    return this.readVarintBigInt().toString();
  }

  // Protobuf int64 uses the same varint bytes interpreted as signed two's complement.
  readInt64(): string {
    return BigInt.asIntN(64, this.readVarintBigInt()).toString();
  }

  private readVarintBigInt(): bigint {
    let result = 0n;
    for (let shift = 0n; shift <= 63n && this.offset < this.bytes.length; shift += 7n) {
      const b = this.bytes[this.offset++];
      if (shift === 63n && (b & 0xfe) !== 0) break;
      result |= BigInt(b & 0x7f) << shift;
      if ((b & 0x80) === 0) return result;
    }
    throw new Error("invalid varint");
  }

  readBytes(): Uint8Array {
    const length = this.readVarint();
    const end = this.offset + length;
    if (length < 0 || end > this.bytes.length) {
      throw new Error("invalid length-delimited field");
    }
    const out = this.bytes.slice(this.offset, end);
    this.offset = end;
    return out;
  }

  readString(): string {
    return textDecoder.decode(this.readBytes());
  }

  skip(wireType: number) {
    switch (wireType) {
      case 0:
        this.readVarintBigInt();
        return;
      case 1:
        this.offset += 8;
        break;
      case 2:
        this.readBytes();
        return;
      case 5:
        this.offset += 4;
        break;
      default:
        throw new Error(`unsupported wire type ${wireType}`);
    }
    if (this.offset > this.bytes.length) {
      throw new Error("field extends past payload");
    }
  }
}

function base64ToBytes(value: string): Uint8Array {
  const raw = atob(value);
  const out = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i += 1) {
    out[i] = raw.charCodeAt(i);
  }
  return out;
}

function bytesToHex(bytes: Uint8Array): string {
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

function expectWire(field: ProtoField, expected: number, name: string) {
  if (field.wireType !== expected) {
    throw new Error(`${name} has wire type ${field.wireType}, expected ${expected}`);
  }
}

function detail(
  label: string,
  value: string | number | boolean | undefined,
  mono = false,
  warning = false,
): CoordinatorActionDetail {
  const rendered = value === undefined || value === "" ? "(empty)" : String(value);
  return { label, value: rendered, mono, warning };
}

function payloadTypeName(typeURL: string): string {
  const withoutPrefix = typeURL.replace(/^\//, "");
  const slash = withoutPrefix.lastIndexOf("/");
  return slash >= 0 ? withoutPrefix.slice(slash + 1) : withoutPrefix;
}

function formatUnix(seconds: string): string {
  if (seconds === "0") return "(empty)";
  const value = Number(seconds);
  if (!Number.isSafeInteger(value)) return seconds;
  const date = new Date(value * 1000);
  if (Number.isNaN(date.getTime())) return seconds;
  return `${seconds} (${date.toLocaleString()})`;
}

function decodeUpdateVoteManagers(reader: ProtoReader): DecodedCoordinatorActionPayload {
  let creator = "";
  const managers: string[] = [];
  let threshold = 1;
  let minCeremonyValidators = 1;
  while (!reader.eof()) {
    const field = reader.readField();
    if (!field) break;
    switch (field.number) {
      case 1:
        expectWire(field, 2, "creator");
        creator = reader.readString();
        break;
      case 2:
        expectWire(field, 2, "new_vote_managers");
        managers.push(reader.readString());
        break;
      case 3:
        expectWire(field, 0, "new_threshold");
        threshold = reader.readUint32();
        break;
      case 4:
        expectWire(field, 0, "new_min_ceremony_validators");
        minCeremonyValidators = reader.readUint32();
        break;
      default:
        reader.skipUnknown(field);
    }
  }
  return {
    rows: [
      detail("Creator", creator, true),
      detail("New threshold", threshold),
      detail("New min ceremony validators", minCeremonyValidators || 1),
      detail("New managers", managers.join("\n"), true),
    ],
    value: {
      creator,
      new_vote_managers: managers,
      new_threshold: threshold,
      new_min_ceremony_validators: minCeremonyValidators || 1,
    },
  };
}

function decodeScheduleUpgrade(reader: ProtoReader): DecodedCoordinatorActionPayload {
  let creator = "";
  let name = "";
  let height = "0";
  let info = "";
  let replaceExisting = false;
  while (!reader.eof()) {
    const field = reader.readField();
    if (!field) break;
    switch (field.number) {
      case 1:
        expectWire(field, 2, "creator");
        creator = reader.readString();
        break;
      case 2:
        expectWire(field, 2, "name");
        name = reader.readString();
        break;
      case 3:
        expectWire(field, 0, "height");
        height = reader.readInt64();
        break;
      case 4:
        expectWire(field, 2, "info");
        info = reader.readString();
        break;
      case 5:
        expectWire(field, 0, "replace_existing");
        replaceExisting = reader.readVarint() !== 0;
        break;
      default:
        reader.skipUnknown(field);
    }
  }
  return {
    rows: [
      detail("Creator", creator, true),
      detail("Upgrade name", name),
      detail("Height", height),
      detail("Replace existing", replaceExisting ? "yes" : "no"),
      detail("Info", info),
    ],
    value: {
      creator,
      name,
      height,
      info,
      replace_existing: replaceExisting,
    },
  };
}

function decodeCancelUpgrade(reader: ProtoReader): DecodedCoordinatorActionPayload {
  let creator = "";
  while (!reader.eof()) {
    const field = reader.readField();
    if (!field) break;
    if (field.number === 1) {
      expectWire(field, 2, "creator");
      creator = reader.readString();
    } else {
      reader.skipUnknown(field);
    }
  }
  return {
    rows: [detail("Creator", creator, true), detail("Action", "Cancel scheduled upgrade")],
    value: { creator },
  };
}

function decodeSetEndorser(reader: ProtoReader): DecodedCoordinatorActionPayload {
  let creator = "";
  let endorserID = "";
  let address = "";
  while (!reader.eof()) {
    const field = reader.readField();
    if (!field) break;
    switch (field.number) {
      case 1:
        expectWire(field, 2, "creator");
        creator = reader.readString();
        break;
      case 2:
        expectWire(field, 2, "endorser_id");
        endorserID = reader.readString();
        break;
      case 3:
        expectWire(field, 2, "address");
        address = reader.readString();
        break;
      default:
        reader.skipUnknown(field);
    }
  }
  return {
    rows: [
      detail("Creator", creator, true),
      detail("Endorser ID", endorserID),
      detail("Address", address || "(clear mapping)", true),
    ],
    value: {
      creator,
      endorser_id: endorserID,
      address,
    },
  };
}

function decodeAuthorizedSend(reader: ProtoReader): DecodedCoordinatorActionPayload {
  let creator = "";
  let to = "";
  let amount = "";
  while (!reader.eof()) {
    const field = reader.readField();
    if (!field) break;
    switch (field.number) {
      case 1:
        expectWire(field, 2, "creator");
        creator = reader.readString();
        break;
      case 2:
        expectWire(field, 2, "to_address");
        to = reader.readString();
        break;
      case 3:
        expectWire(field, 2, "amount");
        amount = reader.readString();
        break;
      default:
        reader.skipUnknown(field);
    }
  }
  return {
    rows: [
      detail("Creator", creator, true),
      detail("Funding source", "vote_funding"),
      detail("To", to, true),
      detail("Amount", amount ? `${amount} usvote` : ""),
    ],
    value: {
      creator,
      to_address: to,
      amount,
    },
  };
}

function decodeVoteOption(reader: ProtoReader): { index: number; label: string; description: string } {
  let index = 0;
  let label = "";
  let description = "";
  while (!reader.eof()) {
    const field = reader.readField();
    if (!field) break;
    switch (field.number) {
      case 1:
        expectWire(field, 0, "option.index");
        index = reader.readUint32();
        break;
      case 2:
        expectWire(field, 2, "option.label");
        label = reader.readString();
        break;
      case 3:
        expectWire(field, 2, "option.description");
        description = reader.readString();
        break;
      default:
        reader.skipUnknown(field);
    }
  }
  return { index, label, description };
}

function decodeProposal(reader: ProtoReader): DecodedProposal {
  let id = 0;
  let title = "";
  let description = "";
  const options: Array<{ index: number; label: string; description: string }> = [];
  let zipNumber = "";
  let forumURL = "";
  while (!reader.eof()) {
    const field = reader.readField();
    if (!field) break;
    switch (field.number) {
      case 1:
        expectWire(field, 0, "proposal.id");
        id = reader.readUint32();
        break;
      case 2:
        expectWire(field, 2, "proposal.title");
        title = reader.readString();
        break;
      case 3:
        expectWire(field, 2, "proposal.description");
        description = reader.readString();
        break;
      case 4:
        expectWire(field, 2, "proposal.options");
        options.push(decodeVoteOption(reader.nested("options")));
        break;
      case 5:
        expectWire(field, 2, "proposal.zip_number");
        zipNumber = reader.readString();
        break;
      case 6:
        expectWire(field, 2, "proposal.forum_url");
        forumURL = reader.readString();
        break;
      default:
        reader.skipUnknown(field);
    }
  }
  return { id, title, description, options, zipNumber, forumURL };
}

function describeProposals(proposals: DecodedProposal[]): string {
  if (proposals.length === 0) return "(empty)";
  return proposals.map((p) => {
    const options = p.options
      .map((o) => `${o.index}: ${o.label}${o.description ? ` (${o.description})` : ""}`)
      .join(", ");
    return [
      `${p.id}: ${p.title || "(untitled)"}`,
      p.description ? `description: ${p.description}` : "",
      p.zipNumber ? `ZIP: ${p.zipNumber}` : "",
      p.forumURL ? `forum: ${p.forumURL}` : "",
      options ? `options: ${options}` : "",
    ].filter(Boolean).join(" | ");
  }).join("\n");
}

function decodeCreateVotingSession(reader: ProtoReader): DecodedCoordinatorActionPayload {
  let creator = "";
  let snapshotHeight = "0";
  let snapshotBlockhash = "";
  let proposalsHash = "";
  let voteEndTime = "0";
  let nullifierImtRoot = "";
  let ncRoot = "";
  const proposals: DecodedProposal[] = [];
  let description = "";
  let title = "";
  let discussionURL = "";
  let proxyDelegation = false;
  while (!reader.eof()) {
    const field = reader.readField();
    if (!field) break;
    switch (field.number) {
      case 1:
        expectWire(field, 2, "creator");
        creator = reader.readString();
        break;
      case 2:
        expectWire(field, 0, "snapshot_height");
        snapshotHeight = reader.readUint64();
        break;
      case 3:
        expectWire(field, 2, "snapshot_blockhash");
        snapshotBlockhash = bytesToHex(reader.readBytes());
        break;
      case 4:
        expectWire(field, 2, "proposals_hash");
        proposalsHash = bytesToHex(reader.readBytes());
        break;
      case 5:
        expectWire(field, 0, "vote_end_time");
        voteEndTime = reader.readUint64();
        break;
      case 6:
        expectWire(field, 2, "nullifier_imt_root");
        nullifierImtRoot = bytesToHex(reader.readBytes());
        break;
      case 7:
        expectWire(field, 2, "nc_root");
        ncRoot = bytesToHex(reader.readBytes());
        break;
      case 8:
        expectWire(field, 2, "proposals");
        proposals.push(decodeProposal(reader.nested("proposals")));
        break;
      case 9:
        expectWire(field, 2, "description");
        description = reader.readString();
        break;
      case 10:
        expectWire(field, 2, "title");
        title = reader.readString();
        break;
      case 11:
        expectWire(field, 2, "discussion_url");
        discussionURL = reader.readString();
        break;
      case 12:
        expectWire(field, 0, "proxy_delegation");
        proxyDelegation = reader.readVarint() !== 0;
        break;
      default:
        reader.skipUnknown(field);
    }
  }
  return {
    rows: [
      detail("Creator", creator, true),
      detail("Title", title),
      detail(
        "Proxy delegation",
        proxyDelegation
          ? `Yes. A proxy delegation round executes only with at least ${PROXY_APPROVAL_FLOOR} coordinator approvals.`
          : "No",
        false,
        proxyDelegation,
      ),
      detail("Description", description),
      detail("Discussion URL", discussionURL),
      detail("Snapshot height", snapshotHeight),
      detail("Vote end time", formatUnix(voteEndTime)),
      detail("Proposals", describeProposals(proposals)),
      detail("Proposals hash", proposalsHash, true),
      detail("Snapshot blockhash", snapshotBlockhash, true),
      detail("Nullifier IMT root", nullifierImtRoot, true),
      detail("NC root", ncRoot, true),
    ],
    value: {
      creator,
      title,
      description,
      discussion_url: discussionURL,
      snapshot_height: snapshotHeight,
      vote_end_time: voteEndTime,
      proposals: proposals.map((proposal) => ({
        id: proposal.id,
        title: proposal.title,
        description: proposal.description,
        options: proposal.options.map((option) => ({
          index: option.index,
          label: option.label,
          description: option.description,
        })),
        zip_number: proposal.zipNumber,
        forum_url: proposal.forumURL,
      })),
      proposals_hash: proposalsHash,
      snapshot_blockhash: snapshotBlockhash,
      nullifier_imt_root: nullifierImtRoot,
      nc_root: ncRoot,
      proxy_delegation: proxyDelegation,
    },
    approvalFloor: proxyDelegation ? PROXY_APPROVAL_FLOOR : 0,
  };
}

/** Decodes one ProxyVerifier of MsgSetProxyDelegationParams. */
function decodeProxyVerifier(reader: ProtoReader): { verifier_id: number; pubkey: string } {
  let verifierID = 0;
  let pubkey = "";
  while (!reader.eof()) {
    const field = reader.readField();
    if (!field) break;
    switch (field.number) {
      case 1:
        expectWire(field, 0, "verifier.verifier_id");
        verifierID = reader.readUint32();
        break;
      case 2:
        expectWire(field, 2, "verifier.pubkey");
        pubkey = bytesToHex(reader.readBytes());
        break;
      default:
        reader.skipUnknown(field);
    }
  }
  return { verifier_id: verifierID, pubkey };
}

/** Decodes MsgSetProxyDelegationParams (contracts-v1 §2.4), which replaces the verifier set, threshold and registry size. */
function decodeSetProxyDelegationParams(reader: ProtoReader): DecodedCoordinatorActionPayload {
  let creator = "";
  const verifiers: Array<{ verifier_id: number; pubkey: string }> = [];
  let verifierThreshold = 0;
  let maxDelegates = 0;
  while (!reader.eof()) {
    const field = reader.readField();
    if (!field) break;
    switch (field.number) {
      case 1:
        expectWire(field, 2, "creator");
        creator = reader.readString();
        break;
      case 2:
        expectWire(field, 2, "verifiers");
        verifiers.push(decodeProxyVerifier(reader.nested("verifiers")));
        break;
      case 3:
        expectWire(field, 0, "verifier_threshold");
        verifierThreshold = reader.readUint32();
        break;
      case 4:
        expectWire(field, 0, "max_delegates");
        maxDelegates = reader.readUint32();
        break;
      default:
        reader.skipUnknown(field);
    }
  }
  return {
    rows: [
      detail("Creator", creator, true),
      detail(
        "Verifiers",
        verifiers.length > 0
          ? verifiers.map((v) => `${v.verifier_id}: ${v.pubkey || "(empty key)"}`).join("\n")
          : "None. Delegate registration closes.",
        true,
      ),
      detail("Verifier threshold", verifierThreshold),
      detail("Max delegates", maxDelegates),
      detail("Pause", "Unchanged. Only a pause action changes it."),
    ],
    value: {
      creator,
      verifiers,
      verifier_threshold: verifierThreshold,
      max_delegates: maxDelegates,
    },
    approvalFloor: PROXY_APPROVAL_FLOOR,
  };
}

/** Decodes MsgSetProxyDelegationPause (contracts-v1 §2.4), which pauses or resumes new proxy delegation batches. */
function decodeSetProxyDelegationPause(reader: ProtoReader): DecodedCoordinatorActionPayload {
  let creator = "";
  let paused = false;
  while (!reader.eof()) {
    const field = reader.readField();
    if (!field) break;
    switch (field.number) {
      case 1:
        expectWire(field, 2, "creator");
        creator = reader.readString();
        break;
      case 2:
        expectWire(field, 0, "paused");
        paused = reader.readVarint() !== 0;
        break;
      default:
        reader.skipUnknown(field);
    }
  }
  return {
    rows: [
      detail("Creator", creator, true),
      detail(
        "Action",
        paused ? "Pause new proxy delegations" : "Resume new proxy delegations",
      ),
      detail("Not affected", "Delegate registrations, ballots and the rest of the params"),
    ],
    value: { creator, paused },
    approvalFloor: PROXY_APPROVAL_FLOOR,
  };
}

/**
 * Decodes MsgSetDelegateStatus (contracts-v1 §2.4, §5.5): one action,
 * suspend, lift or retire, applied to every listed delegate or to none.
 */
function decodeSetDelegateStatus(reader: ProtoReader): DecodedCoordinatorActionPayload {
  let creator = "";
  const delegateIndexes: number[] = [];
  let action = 0;
  let reason = 0;
  while (!reader.eof()) {
    const field = reader.readField();
    if (!field) break;
    switch (field.number) {
      case 1:
        expectWire(field, 2, "creator");
        creator = reader.readString();
        break;
      case 2:
        // A repeated uint32 is packed by default, and may also come one
        // varint per field.
        if (field.wireType === 2) {
          const packed = reader.nested("delegate_indexes");
          while (!packed.eof()) delegateIndexes.push(packed.readUint32());
        } else {
          expectWire(field, 0, "delegate_indexes");
          delegateIndexes.push(reader.readUint32());
        }
        break;
      case 3:
        expectWire(field, 0, "action");
        action = reader.readUint32();
        break;
      case 4:
        expectWire(field, 0, "reason");
        reason = reader.readUint32();
        break;
      default:
        reader.skipUnknown(field);
    }
  }
  const known = STATUS_ACTIONS[action];
  return {
    rows: [
      detail("Creator", creator, true),
      // Retiring cannot be undone, so it is flagged.
      detail("Action", known ? known.title : `${action} (unknown action)`, false, action === 3),
      detail(
        "Delegates",
        delegateIndexes.map((index) => `delegate ${index}: ${known ? known.effect : "unknown action"}`).join("\n"),
        true,
      ),
      detail("Reason", `${reason} (${STATUS_REASONS[reason] ?? "unknown reason code"})`),
      detail("Applies", "Every delegate, or none if one fails"),
    ],
    value: { creator, delegate_indexes: delegateIndexes, action, reason },
    approvalFloor: PROXY_APPROVAL_FLOOR,
  };
}

/** Decoders by payload type name; a type missing here cannot be approved from this page. */
const PAYLOAD_DECODERS = new Map<string, (reader: ProtoReader) => DecodedCoordinatorActionPayload>([
  ["svote.v1.MsgCreateVotingSession", decodeCreateVotingSession],
  ["svote.v1.MsgUpdateVoteManagers", decodeUpdateVoteManagers],
  ["svote.v1.MsgScheduleUpgrade", decodeScheduleUpgrade],
  ["svote.v1.MsgCancelUpgrade", decodeCancelUpgrade],
  ["svote.v1.MsgSetEndorser", decodeSetEndorser],
  ["svote.v1.MsgAuthorizedSend", decodeAuthorizedSend],
  ["svote.v1.MsgSetProxyDelegationParams", decodeSetProxyDelegationParams],
  ["svote.v1.MsgSetProxyDelegationPause", decodeSetProxyDelegationPause],
  ["svote.v1.MsgSetDelegateStatus", decodeSetDelegateStatus],
]);

function rawPayloadJson(action: CoordinatorAction): CoordinatorActionDescription["json"] {
  return {
    type_url: action.payload?.type_url ?? "",
    value: action.payload?.value ?? "",
  };
}

/** Returns a base64 payload as hex, or says it is not base64 and quotes it. */
function rawPayloadHex(value: string): string {
  try {
    return bytesToHex(base64ToBytes(value));
  } catch {
    return `not base64: ${value}`;
  }
}

/**
 * Describes a payload this page cannot fully decode: never approvable here,
 * and never blank, since it shows a warning, the payload type and the raw
 * bytes as hex.
 */
function undecodedDescription(
  action: CoordinatorAction,
  error: string,
  warning: string,
): CoordinatorActionDescription {
  return {
    canApprove: false,
    error,
    rows: [
      detail("Warning", warning, false, true),
      detail("Payload type", action.payload?.type_url ?? "", true),
      detail("Payload (hex)", rawPayloadHex(action.payload?.value ?? ""), true),
    ],
    json: rawPayloadJson(action),
    jsonDecoded: false,
    approvalFloor: 0,
  };
}

/**
 * Decodes a pending action's payload so an approver sees exactly what an
 * approval signs. Only a payload decoded in full, every field known, can be
 * approved from this page; anything else shows a warning and its raw bytes.
 */
export function describeCoordinatorActionPayload(action: CoordinatorAction): CoordinatorActionDescription {
  const typeURL = action.payload?.type_url ?? "";
  const value = action.payload?.value ?? "";
  if (!typeURL || !value) {
    return undecodedDescription(action, "Cannot approve: action payload is missing.", "This action has no payload to show.");
  }
  const decode = PAYLOAD_DECODERS.get(payloadTypeName(typeURL));
  if (!decode) {
    return undecodedDescription(
      action,
      `Cannot approve: unsupported action type ${typeURL}.`,
      "This page cannot decode this action type. Check the raw bytes before approving it from the CLI.",
    );
  }

  let bytes: Uint8Array;
  let reader: ProtoReader;
  let payload: DecodedCoordinatorActionPayload;
  try {
    bytes = base64ToBytes(value);
    reader = new ProtoReader(bytes);
    payload = decode(reader);
  } catch (err) {
    return undecodedDescription(
      action,
      `Cannot approve: failed to decode payload (${err instanceof Error ? err.message : String(err)}).`,
      "The payload does not decode as its type. Check the raw bytes before approving it from the CLI.",
    );
  }

  const description: CoordinatorActionDescription = {
    canApprove: true,
    rows: payload.rows,
    json: { type_url: typeURL, value: payload.value },
    jsonDecoded: true,
    approvalFloor: payload.approvalFloor ?? 0,
  };
  if (reader.unknownFields.length === 0) {
    return description;
  }
  return {
    ...description,
    canApprove: false,
    error: `Cannot approve: the payload has fields this page does not show (${reader.unknownFields.join(", ")}).`,
    rows: [
      detail(
        "Warning",
        "The payload has fields this page does not know, so the details below are incomplete. Check the raw bytes before approving it from the CLI.",
        false,
        true,
      ),
      ...payload.rows,
      detail("Payload (hex)", bytesToHex(bytes), true),
    ],
  };
}
