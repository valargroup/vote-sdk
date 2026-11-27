import { sha256 } from "@noble/hashes/sha2.js";

/** Domain tag of the canonical ballot encoding. */
export const BALLOT_DOMAIN_TAG_V1 = "zcash-shielded-vote:ballot:v1";

export interface BallotOptionInput {
  index: number;
  label: string;
  description?: string;
}

export interface BallotProposalInput {
  id: number;
  title: string;
  description?: string;
  zipNumber?: string;
  forumURL?: string;
  options: BallotOptionInput[];
}

export interface BallotInput {
  title?: string;
  description?: string;
  discussionURL?: string;
  proposals: BallotProposalInput[];
}

/**
 * Canonical ballot encoding, identical to the chain's
 * `types.CanonicalBallotEncoding` and the wallets' `Ballot::canonical_encoding`:
 *
 *   tag || str(title) || str(description) || str(discussion_url)
 *     || u64(len(proposals))
 *     || per proposal: u32(id) || str(title) || str(description)
 *          || str(zip_number) || str(forum_url) || u64(len(options))
 *          || per option: u32(index) || str(label) || str(description)
 *
 * with str(s) = u64(UTF-8 byte length) || UTF-8 bytes, integers little-endian,
 * and absent strings encoded as empty.
 */
export function canonicalBallotEncoding(ballot: BallotInput): Uint8Array {
  const encoder = new TextEncoder();
  const parts: Uint8Array[] = [encoder.encode(BALLOT_DOMAIN_TAG_V1)];
  const u32 = (value: number) => {
    const bytes = new Uint8Array(4);
    new DataView(bytes.buffer).setUint32(0, value, true);
    parts.push(bytes);
  };
  const u64 = (value: number) => {
    const bytes = new Uint8Array(8);
    new DataView(bytes.buffer).setBigUint64(0, BigInt(value), true);
    parts.push(bytes);
  };
  const str = (value: string | undefined) => {
    const bytes = encoder.encode(value ?? "");
    u64(bytes.length);
    parts.push(bytes);
  };

  str(ballot.title);
  str(ballot.description);
  str(ballot.discussionURL);
  u64(ballot.proposals.length);
  for (const proposal of ballot.proposals) {
    u32(proposal.id);
    str(proposal.title);
    str(proposal.description);
    str(proposal.zipNumber);
    str(proposal.forumURL);
    u64(proposal.options.length);
    for (const option of proposal.options) {
      u32(option.index);
      str(option.label);
      str(option.description);
    }
  }

  const out = new Uint8Array(parts.reduce((total, part) => total + part.length, 0));
  let offset = 0;
  for (const part of parts) {
    out.set(part, offset);
    offset += part.length;
  }
  return out;
}

/**
 * The value MsgCreateVotingSession.proposals_hash must carry: SHA-256 of the
 * canonical ballot encoding. Uses @noble/hashes so it also works on plain-HTTP
 * dev origins where crypto.subtle is undefined.
 */
export function ballotDigest(ballot: BallotInput): Uint8Array {
  return sha256(canonicalBallotEncoding(ballot));
}
