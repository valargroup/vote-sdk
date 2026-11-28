import { describe, expect, it } from "vitest";
import { encodeCreateVotingSessionPayload, type CreateVotingSessionValue } from "./cosmosTx";

/** A MsgCreateVotingSession payload as the publish flow builds it. */
function session(proxyDelegation: boolean): CreateVotingSessionValue {
  return {
    creator: "sv1creator",
    snapshotHeight: 1000,
    snapshotBlockhash: new Uint8Array(32).fill(1),
    proposalsHash: new Uint8Array(32).fill(2),
    voteEndTime: 1_893_456_000,
    nullifierImtRoot: new Uint8Array(32).fill(3),
    ncRoot: new Uint8Array(32).fill(4),
    proposals: [{ id: 1, title: "Upgrade", description: "", options: [{ index: 0, label: "Yes" }, { index: 1, label: "No" }] }],
    description: "",
    title: "Round",
    discussionURL: "",
    proxyDelegation,
  };
}

describe("encodeCreateVotingSessionPayload", () => {
  it("sets field 12 only for a proxy delegation round", () => {
    const plain = encodeCreateVotingSessionPayload(session(false));
    const proxy = encodeCreateVotingSessionPayload(session(true));
    expect(Array.from(proxy.slice(plain.length))).toEqual([0x60, 0x01]);
    expect(Array.from(proxy.slice(0, plain.length))).toEqual(Array.from(plain));
  });
});
