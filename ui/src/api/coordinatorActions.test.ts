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

function varint(value: number | bigint): Uint8Array {
  const out: number[] = [];
  let remaining = BigInt(value);
  while (remaining >= 0x80n) {
    out.push(Number((remaining & 0x7fn) | 0x80n));
    remaining >>= 7n;
  }
  out.push(Number(remaining));
  return new Uint8Array(out);
}

function fieldKey(fieldNumber: number, wireType: number): Uint8Array {
  return varint(fieldNumber * 8 + wireType);
}

function varintField(fieldNumber: number, value: number | bigint): Uint8Array {
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
      varintField(4, 3),
    ])));

    expect(description.canApprove).toBe(true);
    expect(valueFor(description, "Creator")).toBe("svote1creator");
    expect(valueFor(description, "New threshold")).toBe("2");
    expect(valueFor(description, "New min ceremony validators")).toBe("3");
    expect(valueFor(description, "New managers")).toContain("svote1manager2");
    expect(description.json).toEqual({
      type_url: "type.googleapis.com/svote.v1.MsgUpdateVoteManagers",
      value: {
        creator: "svote1creator",
        new_vote_managers: ["svote1manager1", "svote1manager2"],
        new_threshold: 2,
        new_min_ceremony_validators: 3,
      },
    });
    expect(description.jsonDecoded).toBe(true);
  });

  it("shows create-vote session details before approval", () => {
    const proposal = concat([
      varintField(1, 7),
      stringField(2, "ZIP vote"),
      stringField(3, "Proposal body"),
      bytesField(4, concat([varintField(1, 0), stringField(2, "Support"), stringField(3, "Vote yes.")])),
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
    expect(valueFor(description, "Proposals")).toContain("options: 0: Support (Vote yes.), 1: Oppose");
    expect(valueFor(description, "Snapshot blockhash")).toBe("aabb");
    expect(valueFor(description, "Proposals hash")).toBe("ccdd");
    expect(description.json).toEqual({
      type_url: "type.googleapis.com/svote.v1.MsgCreateVotingSession",
      value: {
        creator: "svote1creator",
        title: "Round title",
        description: "Round description",
        discussion_url: "https://forum.example/round",
        snapshot_height: "123",
        vote_end_time: "1778000000",
        proposals: [
          {
            id: 7,
            title: "ZIP vote",
            description: "Proposal body",
            options: [
              { index: 0, label: "Support", description: "Vote yes." },
              { index: 1, label: "Oppose", description: "" },
            ],
            zip_number: "ZIP-999",
            forum_url: "https://forum.example/proposal",
          },
        ],
        proposals_hash: "ccdd",
        snapshot_blockhash: "aabb",
        nullifier_imt_root: "0102",
        nc_root: "0304",
      },
    });
    expect(description.jsonDecoded).toBe(true);
  });

  it("preserves exact 64-bit values above Number.MAX_SAFE_INTEGER", () => {
    const snapshotHeight = 18_446_744_073_709_551_615n;
    const voteEndTime = 9_007_199_254_740_995n;
    const upgradeHeight = 9_223_372_036_854_775_807n;

    const session = describeCoordinatorActionPayload(action(
      "svote.v1.MsgCreateVotingSession",
      concat([
        varintField(2, snapshotHeight),
        varintField(5, voteEndTime),
      ]),
    ));
    expect(valueFor(session, "Snapshot height")).toBe(snapshotHeight.toString());
    expect(valueFor(session, "Vote end time")).toBe(voteEndTime.toString());
    expect(session.json.value).toMatchObject({
      snapshot_height: snapshotHeight.toString(),
      vote_end_time: voteEndTime.toString(),
    });

    const upgrade = describeCoordinatorActionPayload(action(
      "svote.v1.MsgScheduleUpgrade",
      varintField(3, upgradeHeight),
    ));
    expect(valueFor(upgrade, "Height")).toBe(upgradeHeight.toString());
    expect(upgrade.json.value).toMatchObject({
      height: upgradeHeight.toString(),
    });
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
    expect(description.json.value).toEqual({
      creator: "svote1creator",
      to_address: "svote1to",
      amount: "1000000",
    });
  });

  it("builds decoded JSON for upgrade and endorser actions", () => {
    const schedule = describeCoordinatorActionPayload(action("svote.v1.MsgScheduleUpgrade", concat([
      stringField(1, "svote1creator"),
      stringField(2, "v1.5.0"),
      varintField(3, 4_200_000),
      stringField(4, "sha256:abc123"),
      varintField(5, 1),
    ])));
    expect(schedule.json.value).toEqual({
      creator: "svote1creator",
      name: "v1.5.0",
      height: "4200000",
      info: "sha256:abc123",
      replace_existing: true,
    });

    const cancel = describeCoordinatorActionPayload(action(
      "svote.v1.MsgCancelUpgrade",
      stringField(1, "svote1creator"),
    ));
    expect(cancel.json.value).toEqual({ creator: "svote1creator" });

    const endorser = describeCoordinatorActionPayload(action("svote.v1.MsgSetEndorser", concat([
      stringField(1, "svote1creator"),
      stringField(2, "zodl"),
      stringField(3, "svote1endorser"),
    ])));
    expect(endorser.json.value).toEqual({
      creator: "svote1creator",
      endorser_id: "zodl",
      address: "svote1endorser",
    });
  });

  it("blocks approval for unsupported action payloads", () => {
    const description = describeCoordinatorActionPayload(action("svote.v1.MsgEndorseRound", stringField(1, "svote1creator")));

    expect(description.canApprove).toBe(false);
    expect(description.error).toContain("unsupported action type");
    expect(description.json).toEqual({
      type_url: "type.googleapis.com/svote.v1.MsgEndorseRound",
      value: expect.any(String),
    });
    expect(description.jsonDecoded).toBe(false);
  });
});
