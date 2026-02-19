// Chain API client for the Zally voting chain REST endpoints.

const CHAIN_URL_KEY = "zally-chain-url";
const DEFAULT_CHAIN_URL = import.meta.env.VITE_CHAIN_URL || "http://localhost:1318";

export function getChainUrl(): string {
  return localStorage.getItem(CHAIN_URL_KEY) || DEFAULT_CHAIN_URL;
}

export function setChainUrl(url: string) {
  localStorage.setItem(CHAIN_URL_KEY, url);
}

// In dev mode the Vite proxy forwards /zally/* and /cosmos/* to the chain
// (relative paths). The proxy target is set at Vite startup from VITE_CHAIN_URL.
// If the user has explicitly saved a chain URL via the Settings UI, use it
// directly so that changing the URL at runtime actually takes effect (the Vite
// proxy target is static and won't follow runtime changes).
function apiBase(): string {
  // In dev mode always use the Vite proxy (relative paths). The proxy
  // forwards /zally/* and /cosmos/* server-side to the chain, so a stored
  // "localhost:1318" from the Settings UI would be wrong for remote browsers.
  if (import.meta.env.DEV) {
    return "";
  }
  return localStorage.getItem(CHAIN_URL_KEY) || DEFAULT_CHAIN_URL;
}

/** Return the resolved API base URL for use by other modules (e.g. cosmosTx). */
export function getApiBase(): string {
  return apiBase();
}

async function fetchJson<T>(path: string, init?: RequestInit): Promise<T> {
  const base = apiBase();
  const resp = await fetch(`${base}${path}`, init);
  if (!resp.ok) {
    const body = await resp.text();
    let msg = `HTTP ${resp.status}`;
    try {
      const parsed = JSON.parse(body);
      if (parsed.error) msg = parsed.error;
    } catch {
      if (body) msg = body;
    }
    throw new Error(msg);
  }
  return resp.json();
}

// -- Types matching the chain REST API responses --

export interface CeremonyState {
  ceremony?: {
    status?: string;
    ea_pk?: string; // base64
    validators?: Array<{
      validator_address: string;
      pallas_pk: string;
    }>;
    dealer?: string;
    phase_start?: string;
    phase_timeout?: string;
  };
}

export interface ChainRound {
  vote_round_id?: string; // base64
  snapshot_height?: string;
  vote_end_time?: string;
  creator?: string;
  status?: string;
  description?: string;
  proposals?: Array<{
    id: number;
    title: string;
    description: string;
  }>;
  proposals_hash?: string;
  ea_pk?: string;
}

export interface TallyResult {
  vote_round_id?: string;
  proposal_id?: number;
  vote_decision?: number;
  total_value?: string;
}

export interface VoteSummaryOptionResponse {
  index?: number;
  label?: string;
  ballot_count?: number | string; // uint64: encoding/json serializes as number
  total_value?: number | string;  // uint64: encoding/json serializes as number
}

export interface VoteSummaryProposalResponse {
  id?: number;
  title?: string;
  description?: string;
  options?: VoteSummaryOptionResponse[];
}

export interface VoteSummaryResponse {
  vote_round_id?: string; // base64
  status?: string | number;
  description?: string;
  vote_end_time?: number | string; // uint64: encoding/json serializes as number
  proposals?: VoteSummaryProposalResponse[];
}

export interface BroadcastResult {
  tx_hash: string;
  code: number;
  log?: string;
}

export interface HelperQueueStatus {
  total: number;
  pending: number;
  submitted: number;
  failed: number;
}

export interface HelperTreeStatus {
  leaf_count: number;
  anchor_height: number;
}

export interface HelperStatus {
  status: string;
  queues: Record<string, HelperQueueStatus>;
  tree?: HelperTreeStatus;
}

// -- API methods --

export async function getCeremonyState(): Promise<CeremonyState> {
  return fetchJson<CeremonyState>("/zally/v1/ceremony");
}

// Alias: test connection by fetching ceremony state.
export const testConnection = getCeremonyState;

export interface LatestBlockInfo {
  chainId: string;
  height: number;
}

export async function getLatestBlock(): Promise<LatestBlockInfo> {
  const data = await fetchJson<{
    block?: { header?: { chain_id?: string; height?: string } };
  }>("/cosmos/base/tendermint/v1beta1/blocks/latest");
  return {
    chainId: data.block?.header?.chain_id ?? "",
    height: parseInt(data.block?.header?.height ?? "0", 10),
  };
}

export async function getVoteManager(): Promise<{ address: string }> {
  return fetchJson<{ address: string }>("/zally/v1/vote-manager");
}

export async function getHelperStatus(): Promise<HelperStatus> {
  return fetchJson<HelperStatus>("/api/v1/status");
}

export interface NullifierStatus {
  latest_height: number | null;
  nullifier_count: number;
}

export async function getNullifierStatus(): Promise<NullifierStatus> {
  return fetchJson<NullifierStatus>("/nullifier/status");
}

// setVoteManager was removed: MsgSetVoteManager is now a standard Cosmos SDK
// transaction signed client-side. See cosmosTx.ts.

export async function listRounds(): Promise<{ rounds: ChainRound[] | null }> {
  return fetchJson<{ rounds: ChainRound[] | null }>("/zally/v1/rounds");
}

export async function getRound(
  roundIdHex: string
): Promise<{ round: ChainRound }> {
  return fetchJson<{ round: ChainRound }>(`/zally/v1/round/${roundIdHex}`);
}

export async function getTallyResults(
  roundIdHex: string
): Promise<{ results: TallyResult[] | null }> {
  return fetchJson<{ results: TallyResult[] | null }>(
    `/zally/v1/tally-results/${roundIdHex}`
  );
}

export async function getVoteSummary(
  roundIdHex: string
): Promise<VoteSummaryResponse> {
  return fetchJson<VoteSummaryResponse>(
    `/zally/v1/vote-summary/${roundIdHex}`
  );
}

// submitSession was removed: MsgCreateVotingSession is now a standard Cosmos
// SDK transaction signed client-side. See cosmosTx.ts.
