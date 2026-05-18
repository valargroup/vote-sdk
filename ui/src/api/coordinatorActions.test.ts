import { describe, expect, it } from "vitest";
import type { CoordinatorAction } from "./chain";
import { describeCoordinatorActionPayload } from "./coordinatorActions";

const encoder = new TextEncoder();

function concat(chunks: Uint8Array[]): Uint8Array {
  const length = chunks.reduce((sum, chunk) => sum + chunk.length, 0);
  const out = new Uint8Array(length);
  let offset = 0;
  for (const chunk of chunks) {
    out.set(chunk, offset);
    offset += chunk.length;
  }
  return out;
}

function varint(value: number): Uint8Array {
  const out: number[] = [];
  let remaining = value;
  while (remaining >= 0x80) {
    out.push((remaining & 0x7f) | 0x80);
    remaining = Math.floor(remaining / 0x80);
  }
  out.push(remaining);
  return new Uint8Array(out);
}

function fieldKey(fieldNumber: number, wireType: number): Uint8Array {
  return varint(fieldNumber * 8 + wireType);
}

function varintField(fieldNumber: number, value: number): Uint8Array {
  return concat([fieldKey(fieldNumber, 0), varint(value)]);
}

function stringField(fieldNumber: number, value: string): Uint8Array {
  return bytesField(fieldNumber, encoder.encode(value));
}

function bytesField(fieldNumber: number, value: Uint8Array): Uint8Array {
  return concat([fieldKey(fieldNumber, 2), varint(value.length), value]);
}

function base64(bytes: Uint8Array): string {
  let raw = "";
  bytes.forEach((byte) => {
    raw += String.fromCharCode(byte);
  });
  return btoa(raw);
}

function action(typeName: string, value: Uint8Array): CoordinatorAction {
  return {
    payload: {
      type_url: `type.googleapis.com/${typeName}`,
      value: base64(value),
    },
  };
}

function valueFor(description: ReturnType<typeof describeCoordinatorActionPayload>, label: string): string {
  return description.rows.find((row) => row.label === label)?.value ?? "";
}

describe("describeCoordinatorActionPayload", () => {
  it("shows manager update details before approval", () => {
    const description = describeCoordinatorActionPayload(action("svote.v1.MsgUpdateVoteManagers", concat([
      stringField(1, "svote1creator"),
      stringField(2, "svote1manager1"),
      stringField(2, "svote1manager2"),
      varintField(3, 2),
    ])));

    expect(description.canApprove).toBe(true);
    expect(valueFor(description, "Creator")).toBe("svote1creator");
    expect(valueFor(description, "New threshold")).toBe("2");
    expect(valueFor(description, "New managers")).toContain("svote1manager2");
  });

  it("shows create-vote session details before approval", () => {
    const proposal = concat([
      varintField(1, 7),
      stringField(2, "ZIP vote"),
      stringField(3, "Proposal body"),
      bytesField(4, concat([varintField(1, 0), stringField(2, "Support")])),
      bytesField(4, concat([varintField(1, 1), stringField(2, "Oppose")])),
      stringField(5, "ZIP-999"),
      stringField(6, "https://forum.example/proposal"),
    ]);

    const description = describeCoordinatorActionPayload(action("svote.v1.MsgCreateVotingSession", concat([
      stringField(1, "svote1creator"),
      varintField(2, 123),
      bytesField(3, new Uint8Array([0xaa, 0xbb])),
      bytesField(4, new Uint8Array([0xcc, 0xdd])),
      varintField(5, 1_778_000_000),
      bytesField(6, new Uint8Array([0x01, 0x02])),
      bytesField(7, new Uint8Array([0x03, 0x04])),
      bytesField(8, proposal),
      stringField(9, "Round description"),
      stringField(10, "Round title"),
      stringField(11, "https://forum.example/round"),
    ])));

    expect(description.canApprove).toBe(true);
    expect(valueFor(description, "Title")).toBe("Round title");
    expect(valueFor(description, "Description")).toBe("Round description");
    expect(valueFor(description, "Discussion URL")).toBe("https://forum.example/round");
    expect(valueFor(description, "Snapshot height")).toBe("123");
    expect(valueFor(description, "Proposals")).toContain("ZIP-999");
    expect(valueFor(description, "Proposals")).toContain("options: 0: Support, 1: Oppose");
    expect(valueFor(description, "Snapshot blockhash")).toBe("aabb");
    expect(valueFor(description, "Proposals hash")).toBe("ccdd");
  });

  it("shows coordinator send details before approval", () => {
    const description = describeCoordinatorActionPayload(action("svote.v1.MsgAuthorizedSend", concat([
      stringField(1, "svote1creator"),
      stringField(2, "svote1to"),
      stringField(3, "1000000"),
    ])));

    expect(description.canApprove).toBe(true);
    expect(valueFor(description, "Creator")).toBe("svote1creator");
    expect(valueFor(description, "Funding source")).toBe("vote_funding");
    expect(valueFor(description, "To")).toBe("svote1to");
    expect(valueFor(description, "Amount")).toBe("1000000 usvote");
  });

  it("blocks approval for unsupported action payloads", () => {
    const description = describeCoordinatorActionPayload(action("svote.v1.MsgEndorseRound", stringField(1, "svote1creator")));

    expect(description.canApprove).toBe(false);
    expect(description.error).toContain("unsupported action type");
  });
});
