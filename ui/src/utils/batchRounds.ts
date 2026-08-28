import type { ChainRound } from "../api/chain";
import { isActiveRoundStatus } from "../api/chain";
import { normalizeRoundId } from "./attestEntry";

export function batchRoundNames(baseName: string, count: number): string[] {
  const base = baseName.trim();
  return Array.from({ length: count }, (_, i) => `${base} ${i + 1}`);
}

export interface BatchRoundMatch {
  roundIdHex: string | null;
  isActive: boolean;
  eaPk: string | null;
}

// Maps each batch name to the on-chain round carrying that title. Batch names
// are the only round-id capture mechanism (deriveRoundID ignores the title),
// so the caller must guard against pre-existing rounds with colliding titles.
export function matchBatchRounds(
  names: string[],
  chainRounds: ChainRound[] | null | undefined
): Map<string, BatchRoundMatch> {
  const matches = new Map<string, BatchRoundMatch>();
  for (const name of names) {
    const round = (chainRounds ?? []).find((r) => r.title === name);
    if (!round) continue;
    matches.set(name, {
      roundIdHex: normalizeRoundId(round.vote_round_id),
      isActive: isActiveRoundStatus(round.status),
      eaPk: round.ea_pk ?? null,
    });
  }
  return matches;
}

export interface BatchConfigPrIntentRound {
  round_id: string;
  signed_payload_hash: string;
  entry_sha256: string;
}

// Canonical JSON for the create_config_pr_batch intent. Field order must match
// the server's Go struct marshaling (internal/admin/config_pr.go
// configPRBatchIntentPayload) byte-for-byte or the canonicality check fails.
export function buildBatchConfigPrIntent(
  rounds: BatchConfigPrIntentRound[],
  timestamp: number
): string {
  return JSON.stringify({
    action: "create_config_pr_batch",
    rounds: rounds.map((r) => ({
      round_id: r.round_id,
      signed_payload_hash: r.signed_payload_hash,
      entry_sha256: r.entry_sha256,
    })),
    timestamp,
  });
}
