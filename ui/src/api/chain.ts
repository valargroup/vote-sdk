// Chain API client for the Shielded-Vote chain REST endpoints.

const CHAIN_URL_KEY = "shielded-vote-chain-url";
const DEFAULT_CHAIN_URL = import.meta.env.VITE_CHAIN_URL || "http://localhost:1318";

export function getChainUrl(): string {
  return localStorage.getItem(CHAIN_URL_KEY) || DEFAULT_CHAIN_URL;
}

export function setChainUrl(url: string) {
  localStorage.setItem(CHAIN_URL_KEY, url);
}

// In dev mode the Vite proxy forwards /shielded-vote/* and /cosmos/* to the chain
// (relative paths). The proxy target is set at Vite startup from VITE_CHAIN_URL.
// If the user has explicitly saved a chain URL via the Settings UI, use it
// directly so that changing the URL at runtime actually takes effect (the Vite
// proxy target is static and won't follow runtime changes).
function apiBase(): string {
  // In dev mode always use the Vite proxy (relative paths). The proxy
  // forwards /shielded-vote/* and /cosmos/* server-side to the chain, so a stored
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
  // /api/* routes are Vercel Edge Functions served from the same origin —
  // never prefix them with the chain URL.
  const base = path.startsWith("/api/") ? "" : apiBase();
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
  title?: string;
  created_at_height?: string;
  proposals?: Array<{
    id: number;
    title: string;
    description: string;
  }>;
  proposals_hash?: string;
  ea_pk?: string;
  // Per-round ceremony fields (populated when status = PENDING).
  ceremony_status?: string | number;
  ceremony_validators?: Array<{
    validator_address: string;
    pallas_pk: string;
  }>;
  ceremony_dealer?: string;
  ceremony_phase_start?: string;
  ceremony_phase_timeout?: string;
  ceremony_log?: string[];
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

export interface HelperTreeStatus {
  leaf_count: number;
  anchor_height: number;
}

export interface HelperStatus {
  status: string;
  tree?: HelperTreeStatus;
}

// -- Cosmos SDK staking types --

export interface ValidatorDescription {
  moniker?: string;
  identity?: string;
  website?: string;
  security_contact?: string;
  details?: string;
}

export interface ValidatorCommission {
  commission_rates?: {
    rate?: string;       // decimal string e.g. "0.100000000000000000"
    max_rate?: string;
    max_change_rate?: string;
  };
  update_time?: string;
}

export interface Validator {
  operator_address?: string;
  consensus_pubkey?: { "@type"?: string; key?: string };
  jailed?: boolean;
  status?: string;           // BOND_STATUS_BONDED | BOND_STATUS_UNBONDING | BOND_STATUS_UNBONDED
  tokens?: string;           // total delegated tokens (raw amount)
  delegator_shares?: string;
  description?: ValidatorDescription;
  unbonding_height?: string;
  unbonding_time?: string;
  commission?: ValidatorCommission;
  min_self_delegation?: string;
}

// -- API methods --

export async function getCeremonyState(): Promise<CeremonyState> {
  return fetchJson<CeremonyState>("/shielded-vote/v1/ceremony");
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
  return fetchJson<{ address: string }>("/shielded-vote/v1/vote-manager");
}

export async function getHelperStatus(): Promise<HelperStatus> {
  return fetchJson<HelperStatus>("/shielded-vote/v1/status");
}

export interface NullifierStatus {
  latest_height: number | null;
  nullifier_count: number;
}

export async function getNullifierStatus(): Promise<NullifierStatus> {
  // The PIR server exposes /root with {height, num_ranges, ...}.
  // Map to the NullifierStatus shape expected by the UI.
  const pir = await fetchJson<{ height: number | null; num_ranges: number }>("/nullifier/root");
  return {
    latest_height: pir.height,
    nullifier_count: pir.num_ranges,
  };
}

// setVoteManager was removed: MsgSetVoteManager is now a standard Cosmos SDK
// transaction signed client-side. See cosmosTx.ts.

export async function listRounds(): Promise<{ rounds: ChainRound[] | null }> {
  return fetchJson<{ rounds: ChainRound[] | null }>("/shielded-vote/v1/rounds");
}

export async function getRound(
  roundIdHex: string
): Promise<{ round: ChainRound }> {
  return fetchJson<{ round: ChainRound }>(`/shielded-vote/v1/round/${roundIdHex}`);
}

export async function getTallyResults(
  roundIdHex: string
): Promise<{ results: TallyResult[] | null }> {
  return fetchJson<{ results: TallyResult[] | null }>(
    `/shielded-vote/v1/tally-results/${roundIdHex}`
  );
}

export async function getVoteSummary(
  roundIdHex: string
): Promise<VoteSummaryResponse> {
  return fetchJson<VoteSummaryResponse>(
    `/shielded-vote/v1/vote-summary/${roundIdHex}`
  );
}

export async function getValidators(): Promise<{ validators: Validator[]; pagination?: { total?: string } }> {
  // Fetch all bonded validators first, then unbonding/unbonded.
  const bonded = await fetchJson<{ validators: Validator[]; pagination?: { total?: string } }>(
    "/cosmos/staking/v1beta1/validators?status=BOND_STATUS_BONDED&pagination.limit=200"
  );
  let all = bonded.validators ?? [];

  // Also fetch unbonding + unbonded so the page is complete.
  try {
    const [unbonding, unbonded] = await Promise.all([
      fetchJson<{ validators: Validator[] }>(
        "/cosmos/staking/v1beta1/validators?status=BOND_STATUS_UNBONDING&pagination.limit=200"
      ),
      fetchJson<{ validators: Validator[] }>(
        "/cosmos/staking/v1beta1/validators?status=BOND_STATUS_UNBONDED&pagination.limit=200"
      ),
    ]);
    all = [...all, ...(unbonding.validators ?? []), ...(unbonded.validators ?? [])];
  } catch {
    // If the extra queries fail (e.g. custom chain without these statuses), just use bonded.
  }

  return { validators: all };
}

export interface PallasKeyEntry {
  validator_address: string;
  pallas_pk: string; // base64
}

export async function getPallasKeys(): Promise<{ validators: PallasKeyEntry[] }> {
  const resp = await fetchJson<{ validators?: PallasKeyEntry[] }>("/shielded-vote/v1/pallas-keys");
  return { validators: resp.validators ?? [] };
}

// -- Snapshot management --

export interface SnapshotStatus {
  phase: "serving" | "rebuilding" | "error";
  height: number | null;
  num_ranges: number | null;
  zcash_tip?: number | null;
  target_height?: number;
  progress?: string;
  progress_pct?: number;
  message?: string;
}

export async function getSnapshotStatus(): Promise<SnapshotStatus> {
  return fetchJson<SnapshotStatus>("/nullifier/snapshot/status");
}

export async function prepareSnapshot(height: number): Promise<{ status: string; target_height: number }> {
  return fetchJson<{ status: string; target_height: number }>("/nullifier/snapshot/prepare", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ height }),
  });
}

export async function getActiveRound(): Promise<{ round: ChainRound | null }> {
  try {
    const resp = await fetchJson<{ round?: ChainRound }>("/shielded-vote/v1/rounds/active");
    return { round: resp.round ?? null };
  } catch {
    return { round: null };
  }
}

// -- Edge Config management --

export interface ServiceEntry {
  url: string;
  label: string;
  operator_address?: string;
}

export interface VotingConfig {
  version: number;
  vote_servers: ServiceEntry[];
  pir_servers: ServiceEntry[];
}

export interface PendingRegistration {
  operator_address: string;
  url: string;
  moniker: string;
  timestamp: number;
  signature: string;
  pub_key: string;
  expires_at: number;
}

/**
 * Fetch the current voting-config from the Vercel API.
 * Works in both dev (proxied) and production (direct) mode.
 */
export async function getVotingConfig(): Promise<VotingConfig | null> {
  try {
    return await fetchJson<VotingConfig>("/api/voting-config");
  } catch {
    return null;
  }
}

export interface UpdateVotingConfigParams {
  payload: VotingConfig;
  signature: string;
  pubKey: string;
  signerAddress: string;
}

/**
 * Update the voting-config in Edge Config via the authenticated Vercel API route.
 * Requires a wallet signature for vote-manager authorization.
 */
export async function updateVotingConfig(params: UpdateVotingConfigParams): Promise<{ status: string }> {
  return fetchJson<{ status: string }>("/api/update-voting-config", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(params),
  });
}

// -- Validator self-registration --

/**
 * Fetch pending validator registrations from Edge Config.
 */
export async function getPendingRegistrations(): Promise<PendingRegistration[]> {
  try {
    return await fetchJson<PendingRegistration[]>("/api/pending-registrations");
  } catch {
    return [];
  }
}

export interface ApproveRegistrationParams {
  payload: { action: "approve" | "reject"; operator_address: string };
  signature: string;
  pubKey: string;
  signerAddress: string;
}

/**
 * Approve a pending validator registration (vote-manager only).
 * Moves the entry from pending-registrations to vote_servers in voting-config.
 */
export async function approveRegistration(params: ApproveRegistrationParams): Promise<{ status: string }> {
  return fetchJson<{ status: string }>("/api/approve-registration", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(params),
  });
}

/**
 * Reject a pending validator registration (vote-manager only).
 * Removes the entry from pending-registrations without adding to vote_servers.
 */
export async function rejectRegistration(params: ApproveRegistrationParams): Promise<{ status: string }> {
  return fetchJson<{ status: string }>("/api/approve-registration", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(params),
  });
}

// -- Server heartbeat / approved-servers --

/** Persistent list of admin-approved servers (survives pulse gaps). */
export async function getApprovedServers(): Promise<ServiceEntry[]> {
  try {
    return await fetchJson<ServiceEntry[]>("/api/approved-servers");
  } catch {
    return [];
  }
}

export interface RemoveApprovedServerParams {
  payload: { action: "remove-approved"; operator_address: string };
  signature: string;
  pubKey: string;
  signerAddress: string;
}

/**
 * Remove a server from approved-servers (and vote_servers + server-pulses).
 * Requires a wallet signature for vote-manager authorization.
 */
export async function removeApprovedServer(params: RemoveApprovedServerParams): Promise<{ status: string }> {
  return fetchJson<{ status: string }>("/api/remove-approved-server", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(params),
  });
}

/** Pulse timestamps: { [url]: unix_timestamp }. */
export type ServerPulses = Record<string, number>;

export async function getServerPulses(): Promise<ServerPulses> {
  try {
    return await fetchJson<ServerPulses>("/api/server-pulses");
  } catch {
    return {};
  }
}

// submitSession was removed: MsgCreateVotingSession is now a standard Cosmos
// SDK transaction signed client-side. See cosmosTx.ts.
