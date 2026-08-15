import { describe, expect, it } from "vitest";
import type { ChainRound } from "../api/chain";
import {
  isTerminalVoteRoundStatus,
  partitionVoteStatusRounds,
  shouldEagerlyLoadVoteSummary,
} from "./voteStatus";

describe("vote status round grouping", () => {
  it("recognizes finalized and ceremony-failed terminal statuses", () => {
    expect(isTerminalVoteRoundStatus(3)).toBe(true);
    expect(isTerminalVoteRoundStatus("SESSION_STATUS_FINALIZED")).toBe(true);
    expect(isTerminalVoteRoundStatus(5)).toBe(true);
    expect(isTerminalVoteRoundStatus("SESSION_STATUS_CEREMONY_FAILED")).toBe(true);
    expect(isTerminalVoteRoundStatus(1)).toBe(false);
    expect(isTerminalVoteRoundStatus("SESSION_STATUS_TALLYING")).toBe(false);
    expect(isTerminalVoteRoundStatus("SESSION_STATUS_PENDING")).toBe(false);
  });

  it("eagerly loads only active and tallying summaries", () => {
    expect(shouldEagerlyLoadVoteSummary(1)).toBe(true);
    expect(shouldEagerlyLoadVoteSummary("SESSION_STATUS_ACTIVE")).toBe(true);
    expect(shouldEagerlyLoadVoteSummary(2)).toBe(true);
    expect(shouldEagerlyLoadVoteSummary("SESSION_STATUS_TALLYING")).toBe(true);
    expect(shouldEagerlyLoadVoteSummary("SESSION_STATUS_PENDING")).toBe(false);
    expect(shouldEagerlyLoadVoteSummary("SESSION_STATUS_FINALIZED")).toBe(false);
  });

  it("separates current and completed rounds newest first without mutating input", () => {
    const oldestFinalized: ChainRound = {
      vote_round_id: "old-finalized",
      status: 3,
      created_at_height: "10",
    };
    const active: ChainRound = {
      vote_round_id: "active",
      status: "SESSION_STATUS_ACTIVE",
      created_at_height: "20",
    };
    const newestFinalized: ChainRound = {
      vote_round_id: "new-finalized",
      status: "SESSION_STATUS_FINALIZED",
      created_at_height: "30",
    };
    const tallying: ChainRound = {
      vote_round_id: "tallying",
      status: 2,
      created_at_height: "40",
    };
    const input = [oldestFinalized, active, newestFinalized, tallying];

    expect(partitionVoteStatusRounds(input)).toEqual({
      currentRounds: [tallying, active],
      completedRounds: [newestFinalized, oldestFinalized],
    });
    expect(input).toEqual([oldestFinalized, active, newestFinalized, tallying]);
  });

  it("uses vote end time when creation heights are unavailable", () => {
    const earlier: ChainRound = {
      vote_round_id: "earlier",
      status: 3,
      vote_end_time: "100",
    };
    const later: ChainRound = {
      vote_round_id: "later",
      status: 3,
      vote_end_time: "200",
    };

    expect(partitionVoteStatusRounds([earlier, later]).completedRounds).toEqual([
      later,
      earlier,
    ]);
  });
});
