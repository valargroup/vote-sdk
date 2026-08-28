import type { Proposal } from "../types";
import { MAX_VOTE_OPTIONS, MIN_VOTE_OPTIONS } from "../constants/vote";

export function isProposalValid(p: Proposal): boolean {
  return (
    p.title.trim().length > 0 &&
    p.options.length >= MIN_VOTE_OPTIONS &&
    p.options.length <= MAX_VOTE_OPTIONS &&
    p.options.every((option) => option.label.trim().length > 0)
  );
}

export function buildChainOptions(
  p: Proposal
): Array<{ index: number; label: string; description: string }> {
  return p.options.map((opt, j) => ({
    index: j,
    label: opt.label,
    description: opt.description ?? "",
  }));
}
