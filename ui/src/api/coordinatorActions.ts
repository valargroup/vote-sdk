import type { CoordinatorAction } from "./chain";

export interface CoordinatorActionDetail {
  label: string;
  value: string;
  mono?: boolean;
}

export interface CoordinatorActionDescription {
  canApprove: boolean;
  rows: CoordinatorActionDetail[];
  error?: string;
}

interface ProtoField {
  number: number;
  wireType: number;
}

interface DecodedProposal {
  id: number;
  title: string;
  description: string;
  options: Array<{ index: number; label: string }>;
  zipNumber: string;
  forumURL: string;
}

const textDecoder = new TextDecoder();

class ProtoReader {
  private readonly bytes: Uint8Array;
  private offset = 0;

  constructor(bytes: Uint8Array) {
    this.bytes = bytes;
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
    let result = 0;
    let shift = 0;
    while (this.offset < this.bytes.length) {
      const b = this.bytes[this.offset++];
      result += (b & 0x7f) * 2 ** shift;
      if ((b & 0x80) === 0) return result;
      shift += 7;
      if (shift > 56) break;
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
        this.readVarint();
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

function detail(label: string, value: string | number | boolean | undefined, mono = false): CoordinatorActionDetail {
  const rendered = value === undefined || value === "" ? "(empty)" : String(value);
  return { label, value: rendered, mono };
}

function payloadTypeName(typeURL: string): string {
  const withoutPrefix = typeURL.replace(/^\//, "");
  const slash = withoutPrefix.lastIndexOf("/");
  return slash >= 0 ? withoutPrefix.slice(slash + 1) : withoutPrefix;
}

function formatUnix(seconds: number): string {
  if (!seconds) return "(empty)";
  return `${seconds} (${new Date(seconds * 1000).toLocaleString()})`;
}

function decodeUpdateVoteManagers(bytes: Uint8Array): CoordinatorActionDetail[] {
  const reader = new ProtoReader(bytes);
  let creator = "";
  const managers: string[] = [];
  let threshold = 1;
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
        threshold = reader.readVarint();
        break;
      default:
        reader.skip(field.wireType);
    }
  }
  return [
    detail("Creator", creator, true),
    detail("New threshold", threshold),
    detail("New managers", managers.join("\n"), true),
  ];
}

function decodeScheduleUpgrade(bytes: Uint8Array): CoordinatorActionDetail[] {
  const reader = new ProtoReader(bytes);
  let creator = "";
  let name = "";
  let height = 0;
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
        height = reader.readVarint();
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
        reader.skip(field.wireType);
    }
  }
  return [
    detail("Creator", creator, true),
    detail("Upgrade name", name),
    detail("Height", height),
    detail("Replace existing", replaceExisting ? "yes" : "no"),
    detail("Info", info),
  ];
}

function decodeCancelUpgrade(bytes: Uint8Array): CoordinatorActionDetail[] {
  const reader = new ProtoReader(bytes);
  let creator = "";
  while (!reader.eof()) {
    const field = reader.readField();
    if (!field) break;
    if (field.number === 1) {
      expectWire(field, 2, "creator");
      creator = reader.readString();
    } else {
      reader.skip(field.wireType);
    }
  }
  return [detail("Creator", creator, true), detail("Action", "Cancel scheduled upgrade")];
}

function decodeSetEndorser(bytes: Uint8Array): CoordinatorActionDetail[] {
  const reader = new ProtoReader(bytes);
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
        reader.skip(field.wireType);
    }
  }
  return [
    detail("Creator", creator, true),
    detail("Endorser ID", endorserID),
    detail("Address", address || "(clear mapping)", true),
  ];
}

function decodeAuthorizedSend(bytes: Uint8Array): CoordinatorActionDetail[] {
  const reader = new ProtoReader(bytes);
  let from = "";
  let to = "";
  let amount = "";
  let denom = "";
  while (!reader.eof()) {
    const field = reader.readField();
    if (!field) break;
    switch (field.number) {
      case 1:
        expectWire(field, 2, "from_address");
        from = reader.readString();
        break;
      case 2:
        expectWire(field, 2, "to_address");
        to = reader.readString();
        break;
      case 3:
        expectWire(field, 2, "amount");
        amount = reader.readString();
        break;
      case 4:
        expectWire(field, 2, "denom");
        denom = reader.readString();
        break;
      default:
        reader.skip(field.wireType);
    }
  }
  return [
    detail("From", from, true),
    detail("To", to, true),
    detail("Amount", amount),
    detail("Denom", denom),
  ];
}

function decodeVoteOption(bytes: Uint8Array): { index: number; label: string } {
  const reader = new ProtoReader(bytes);
  let index = 0;
  let label = "";
  while (!reader.eof()) {
    const field = reader.readField();
    if (!field) break;
    switch (field.number) {
      case 1:
        expectWire(field, 0, "option.index");
        index = reader.readVarint();
        break;
      case 2:
        expectWire(field, 2, "option.label");
        label = reader.readString();
        break;
      default:
        reader.skip(field.wireType);
    }
  }
  return { index, label };
}

function decodeProposal(bytes: Uint8Array): DecodedProposal {
  const reader = new ProtoReader(bytes);
  let id = 0;
  let title = "";
  let description = "";
  const options: Array<{ index: number; label: string }> = [];
  let zipNumber = "";
  let forumURL = "";
  while (!reader.eof()) {
    const field = reader.readField();
    if (!field) break;
    switch (field.number) {
      case 1:
        expectWire(field, 0, "proposal.id");
        id = reader.readVarint();
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
        options.push(decodeVoteOption(reader.readBytes()));
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
        reader.skip(field.wireType);
    }
  }
  return { id, title, description, options, zipNumber, forumURL };
}

function describeProposals(proposals: DecodedProposal[]): string {
  if (proposals.length === 0) return "(empty)";
  return proposals.map((p) => {
    const options = p.options.map((o) => `${o.index}: ${o.label}`).join(", ");
    return [
      `${p.id}: ${p.title || "(untitled)"}`,
      p.description ? `description: ${p.description}` : "",
      p.zipNumber ? `ZIP: ${p.zipNumber}` : "",
      p.forumURL ? `forum: ${p.forumURL}` : "",
      options ? `options: ${options}` : "",
    ].filter(Boolean).join(" | ");
  }).join("\n");
}

function decodeCreateVotingSession(bytes: Uint8Array): CoordinatorActionDetail[] {
  const reader = new ProtoReader(bytes);
  let creator = "";
  let snapshotHeight = 0;
  let snapshotBlockhash = "";
  let proposalsHash = "";
  let voteEndTime = 0;
  let nullifierImtRoot = "";
  let ncRoot = "";
  const proposals: DecodedProposal[] = [];
  let description = "";
  let title = "";
  let discussionURL = "";
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
        snapshotHeight = reader.readVarint();
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
        voteEndTime = reader.readVarint();
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
        proposals.push(decodeProposal(reader.readBytes()));
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
      default:
        reader.skip(field.wireType);
    }
  }
  return [
    detail("Creator", creator, true),
    detail("Title", title),
    detail("Description", description),
    detail("Discussion URL", discussionURL),
    detail("Snapshot height", snapshotHeight),
    detail("Vote end time", formatUnix(voteEndTime)),
    detail("Proposals", describeProposals(proposals)),
    detail("Proposals hash", proposalsHash, true),
    detail("Snapshot blockhash", snapshotBlockhash, true),
    detail("Nullifier IMT root", nullifierImtRoot, true),
    detail("NC root", ncRoot, true),
  ];
}

export function describeCoordinatorActionPayload(action: CoordinatorAction): CoordinatorActionDescription {
  const typeURL = action.payload?.type_url ?? "";
  const value = action.payload?.value ?? "";
  if (!typeURL || !value) {
    return {
      canApprove: false,
      error: "Cannot approve: action payload is missing.",
      rows: [detail("Payload", "missing")],
    };
  }

  try {
    const bytes = base64ToBytes(value);
    const typeName = payloadTypeName(typeURL);
    switch (typeName) {
      case "svote.v1.MsgCreateVotingSession":
        return { canApprove: true, rows: decodeCreateVotingSession(bytes) };
      case "svote.v1.MsgUpdateVoteManagers":
        return { canApprove: true, rows: decodeUpdateVoteManagers(bytes) };
      case "svote.v1.MsgScheduleUpgrade":
        return { canApprove: true, rows: decodeScheduleUpgrade(bytes) };
      case "svote.v1.MsgCancelUpgrade":
        return { canApprove: true, rows: decodeCancelUpgrade(bytes) };
      case "svote.v1.MsgSetEndorser":
        return { canApprove: true, rows: decodeSetEndorser(bytes) };
      case "svote.v1.MsgAuthorizedSend":
        return { canApprove: true, rows: decodeAuthorizedSend(bytes) };
      default:
        return {
          canApprove: false,
          error: `Cannot approve: unsupported action type ${typeURL}.`,
          rows: [detail("Payload type", typeURL, true)],
        };
    }
  } catch (err) {
    return {
      canApprove: false,
      error: `Cannot approve: failed to decode payload (${err instanceof Error ? err.message : String(err)}).`,
      rows: [detail("Payload type", typeURL, true)],
    };
  }
}
