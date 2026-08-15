import type { ChainRound } from "../api/chain";

export const COMPLETED_ROUNDS_PAGE_SIZE = 10;

function optionalRoundNumber(value: string | number | undefined): number {
  if (value === undefined || value === "") return Number.NEGATIVE_INFINITY;
  const parsed = typeof value === "number" ? value : Number(value);
  return Number.isFinite(parsed) ? parsed : Number.NEGATIVE_INFINITY;
}

function compareRoundsNewestFirst(a: ChainRound, b: ChainRound): number {
  const aHeight = optionalRoundNumber(a.created_at_height);
  const bHeight = optionalRoundNumber(b.created_at_height);
  if (aHeight !== bHeight) return bHeight - aHeight;

  const aEndTime = optionalRoundNumber(a.vote_end_time);
  const bEndTime = optionalRoundNumber(b.vote_end_time);
  if (aEndTime === bEndTime) return 0;
  return bEndTime - aEndTime;
}

export function isTerminalVoteRoundStatus(status: unknown): boolean {
  const normalized = String(status ?? "").trim().toLowerCase();
  return (
    normalized === "3" ||
    normalized === "5" ||
    normalized === "finalized" ||
    normalized === "ceremony_failed" ||
    normalized === "session_status_finalized" ||
    normalized === "session_status_ceremony_failed"
  );
}

export function shouldEagerlyLoadVoteSummary(status: unknown): boolean {
  const normalized = String(status ?? "").trim().toLowerCase();
  return (
    normalized === "1" ||
    normalized === "2" ||
    normalized === "active" ||
    normalized === "tallying" ||
    normalized === "session_status_active" ||
    normalized === "session_status_tallying"
  );
}

export function partitionVoteStatusRounds(rounds: ChainRound[]): {
  currentRounds: ChainRound[];
  completedRounds: ChainRound[];
} {
  const newestFirst = [...rounds].sort(compareRoundsNewestFirst);
  return {
    currentRounds: newestFirst.filter((round) => !isTerminalVoteRoundStatus(round.status)),
    completedRounds: newestFirst.filter((round) => isTerminalVoteRoundStatus(round.status)),
  };
}
