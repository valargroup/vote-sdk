// Chain API client for the Zally voting chain REST endpoints.

const CHAIN_URL_KEY = "zally-chain-url";
const DEFAULT_CHAIN_URL = "http://localhost:1318";

export function getChainUrl(): string {
  return localStorage.getItem(CHAIN_URL_KEY) || DEFAULT_CHAIN_URL;
}

export function setChainUrl(url: string) {
  localStorage.setItem(CHAIN_URL_KEY, url);
}

// Whether to use the Vite proxy (relative URLs) or direct chain URL.
// In dev mode with Vite proxy configured, we can use relative paths.
// In production or when pointing at a remote chain, use the full URL.
function apiBase(): string {
  const url = getChainUrl();
  // If the chain URL points to the same host as the UI, use relative path via Vite proxy
  if (
    url === DEFAULT_CHAIN_URL &&
    typeof window !== "undefined" &&
    window.location.port === "5173"
  ) {
    return "";
  }
  return url;
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

export interface BroadcastResult {
  tx_hash: string;
  code: number;
  log?: string;
}

// -- API methods --

export async function getCeremonyState(): Promise<CeremonyState> {
  return fetchJson<CeremonyState>("/zally/v1/ceremony");
}

// Alias: test connection by fetching ceremony state.
export const testConnection = getCeremonyState;

export async function getVoteManager(): Promise<{ address: string }> {
  return fetchJson<{ address: string }>("/zally/v1/vote-manager");
}

export async function setVoteManager(
  creator: string,
  newManager: string
): Promise<BroadcastResult> {
  return fetchJson<BroadcastResult>("/zally/v1/set-vote-manager", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ creator, new_manager: newManager }),
  });
}

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

export interface SubmitSessionPayload {
  creator: string;
  snapshot_height: number;
  vote_end_time: number;
  description: string;
  proposals: Array<{
    id: number;
    title: string;
    description: string;
  }>;
}

export async function submitSession(
  payload: SubmitSessionPayload
): Promise<BroadcastResult> {
  return fetchJson<BroadcastResult>("/zally/v1/submit-session", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(payload),
  });
}
