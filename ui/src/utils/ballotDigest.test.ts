import { describe, expect, it } from "vitest";
import sharedVectors from "../../../x/vote/types/testdata/ballot_digest_v1.json";
import { ballotDigest, canonicalBallotEncoding, type BallotInput } from "./ballotDigest";

// Shared with the chain, the voting-config signer, and the zcash_voting wallet
// library; every implementation must reproduce these bytes.
interface VectorBallot {
  title: string;
  description: string;
  discussion_url: string;
  proposals: Array<{
    id: number;
    title: string;
    description: string;
    zip_number: string;
    forum_url: string;
    options: Array<{ index: number; label: string; description: string }>;
  }>;
}

const vectors: {
  ballot_digest_v1: Array<{ name: string; ballot: VectorBallot; encoding_hex: string; digest_hex: string }>;
} = sharedVectors;

function toInput(ballot: VectorBallot): BallotInput {
  return {
    title: ballot.title,
    description: ballot.description,
    discussionURL: ballot.discussion_url,
    proposals: ballot.proposals.map((p) => ({
      id: p.id,
      title: p.title,
      description: p.description,
      zipNumber: p.zip_number,
      forumURL: p.forum_url,
      options: p.options,
    })),
  };
}

const hex = (bytes: Uint8Array) => Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");

describe("ballotDigest", () => {
  it("reproduces every shared vector", () => {
    expect(vectors.ballot_digest_v1.length).toBeGreaterThan(0);
    for (const vector of vectors.ballot_digest_v1) {
      const input = toInput(vector.ballot);
      expect(hex(canonicalBallotEncoding(input)), vector.name).toBe(vector.encoding_hex);
      expect(hex(ballotDigest(input)), vector.name).toBe(vector.digest_hex);
    }
  });

  it("treats absent strings as empty", () => {
    const full = vectors.ballot_digest_v1.find((v) => v.name === "empty_round_metadata_single_proposal")!;
    const sparse: BallotInput = {
      proposals: [{ id: 1, title: "Q", options: [{ index: 0, label: "Yes" }, { index: 1, label: "No" }] }],
    };
    expect(hex(ballotDigest(sparse))).toBe(full.digest_hex);
  });
});
