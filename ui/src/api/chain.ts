// Chain API client for the Shielded-Vote chain REST endpoints.

const CHAIN_URL_KEY = "shielded-vote-chain-url";

// Clear stale localhost defaults saved by earlier builds. The UI is now served
// in-process by svoted, so same-origin (empty base) is the correct default.
if (typeof window !== "undefined") {
  const stored = localStorage.getItem(CHAIN_URL_KEY);
  if (stored && /^https?:\/\/localhost[:/]/.test(stored)) {
    localStorage.removeItem(CHAIN_URL_KEY);
  }
}

export function getChainUrl(): string {
  return localStorage.getItem(CHAIN_URL_KEY) || import.meta.env.VITE_CHAIN_URL || window.location.origin;
}

export function setChainUrl(url: string) {
  if (url) {
    localStorage.setItem(CHAIN_URL_KEY, url);
  } else {
    localStorage.removeItem(CHAIN_URL_KEY);
  }
}

// The UI is served in-process by the same svoted that hosts the API, so
// same-origin (empty base) works for both dev and production. A localStorage
// override is still respected for advanced/remote setups.
function apiBase(): string {
  return localStorage.getItem(CHAIN_URL_KEY) || "";
}

class HTTPError extends Error {
  readonly status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = "HTTPError";
    this.status = status;
  }
}

/** Return the resolved API base URL for use by other modules (e.g. cosmosTx). */
export function getApiBase(): string {
  return apiBase();
}

const NULLIFIER_URL_KEY = "shielded-vote-nullifier-url";

export const LOCAL_PIR_URL = "/nullifier";
export const DEFAULT_PIR_URL = "https://pir.valargroup.org";

function storedNullifierUrl(): string {
  if (typeof window === "undefined") return "";
  return localStorage.getItem(NULLIFIER_URL_KEY) || "";
}

export function getNullifierUrl(): string {
  return storedNullifierUrl() || DEFAULT_PIR_URL;
}

export function setNullifierUrl(url: string) {
  if (url && url !== DEFAULT_PIR_URL) {
    localStorage.setItem(NULLIFIER_URL_KEY, url);
  } else {
    localStorage.removeItem(NULLIFIER_URL_KEY);
  }
}

function nullifierBase(): string {
  return getNullifierUrl();
}

/** Resolved nullifier API base for direct fetch calls (always returns a usable value). */
export function getNullifierApiBase(): string {
  return getNullifierUrl();
}

async function fetchJson<T>(path: string, init?: RequestInit): Promise<T> {
  let url: string;
  if (path.startsWith("/nullifier/") && nullifierBase()) {
    url = `${nullifierBase()}${path.replace(/^\/nullifier/, "")}`;
  } else if (path.startsWith("/api/")) {
    url = path;
  } else {
    url = `${apiBase()}${path}`;
  }
  return fetchJsonAtUrl<T>(url, init);
}

async function fetchJsonAtUrl<T>(url: string, init?: RequestInit): Promise<T> {
  const resp = await fetch(url, init);
  return decodeJsonResponse<T>(resp);
}

async function decodeJsonResponse<T>(resp: Response): Promise<T> {
  const body = await resp.text();
  if (!resp.ok) {
    let msg = `HTTP ${resp.status}`;
    try {
      const parsed = JSON.parse(body);
      if (parsed.error) msg = parsed.error;
    } catch {
      if (body) msg = body;
    }
    throw new HTTPError(resp.status, msg);
  }
  try {
    return JSON.parse(body) as T;
  } catch {
    throw new Error(
      `Expected JSON response from ${resp.url || "request"}, got ${body.slice(0, 40)}`
    );
  }
}

function nullifierURL(base: string, path: "/root" | "/snapshot/status"): string {
  return `${base.replace(/\/+$/, "")}${path}`;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function assertPirRootResponse(value: unknown): asserts value is { height: number | null; num_ranges: number } {
  if (!isRecord(value)) throw new Error("Invalid PIR root response");
  if (value.height !== null && typeof value.height !== "number") {
    throw new Error("Invalid PIR root height");
  }
  if (typeof value.num_ranges !== "number") {
    throw new Error("Invalid PIR nullifier count");
  }
}

function assertSnapshotStatus(value: unknown): asserts value is SnapshotStatus {
  if (!isRecord(value)) throw new Error("Invalid PIR snapshot status");
  if (!["serving", "rebuilding", "error"].includes(String(value.phase))) {
    throw new Error("Invalid PIR snapshot phase");
  }
}

async function fetchReadOnlyNullifierJson<T>(
  path: "/root" | "/snapshot/status",
  validate: (value: unknown) => asserts value is T
): Promise<T> {
  return fetchNullifierJsonAtBase(getNullifierApiBase(), path, validate);
}

async function fetchNullifierJsonAtBase<T>(
  base: string,
  path: "/root" | "/snapshot/status",
  validate: (value: unknown) => asserts value is T
): Promise<T> {
  const fetchAndValidate = async (base: string): Promise<T> => {
    const data = await fetchJsonAtUrl<unknown>(nullifierURL(base, path));
    validate(data);
    return data;
  };

  return fetchAndValidate(base);
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
  status?: string | number;
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
  threshold?: number | string;
  // Per-round ceremony fields (populated when status = PENDING).
  ceremony_status?: string | number;
  ceremony_validators?: Array<{
    validator_address: string;
    pallas_pk: string;
    shamir_index?: number | string;
  }>;
  ceremony_dealer?: string;
  ceremony_phase_start?: string;
  ceremony_phase_timeout?: string;
  ceremony_log?: string[];
}

export interface CoordinatorAction {
  action_id?: number | string;
  payload?: {
    type_url?: string;
    value?: string;
  };
  proposer?: string;
  approvals?: string[];
  status?: string | number;
  created_at?: number | string;
  expires_at?: number | string;
  executed_at?: number | string;
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

export interface QueueSummaryBucket {
  start: number;
  end: number;
  submitted: number;
  pending_future: number;
  overdue_pending: number;
  processing: number;
  failed: number;
  total: number;
}

export interface QueueSummaryResponse {
  round_id: string;
  bucket_seconds: number;
  created_at_time: number;
  vote_end_time: number;
  generated_at: number;
  last_minute_start: number;
  buckets: QueueSummaryBucket[];
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
  time: string;
  timeMs: number;
}

function parseBlockInfo(data: {
  block?: { header?: { chain_id?: string; height?: string; time?: string } };
}): LatestBlockInfo {
  const time = data.block?.header?.time ?? "";
  const timeMs = time ? Date.parse(time) : 0;
  return {
    chainId: data.block?.header?.chain_id ?? "",
    height: parseInt(data.block?.header?.height ?? "0", 10),
    time,
    timeMs: Number.isFinite(timeMs) ? timeMs : 0,
  };
}

export async function getLatestBlock(): Promise<LatestBlockInfo> {
  const data = await fetchJson<{
    block?: { header?: { chain_id?: string; height?: string; time?: string } };
  }>("/cosmos/base/tendermint/v1beta1/blocks/latest");
  return parseBlockInfo(data);
}

export async function getBlock(height: number): Promise<LatestBlockInfo> {
  const data = await fetchJson<{
    block?: { header?: { chain_id?: string; height?: string; time?: string } };
  }>(`/cosmos/base/tendermint/v1beta1/blocks/${height}`);
  return parseBlockInfo(data);
}

export interface UpgradePlan {
  name: string;
  height: number;
  info: string;
}

export async function getCurrentUpgradePlan(): Promise<{ plan: UpgradePlan | null }> {
  const resp = await fetchJson<{
    plan?: { name?: string; height?: string | number; info?: string } | null;
  }>("/cosmos/upgrade/v1beta1/current_plan");
  const plan = resp.plan;
  if (!plan) return { plan: null };
  return {
    plan: {
      name: plan.name ?? "",
      height: typeof plan.height === "number" ? plan.height : parseInt(plan.height ?? "0", 10),
      info: plan.info ?? "",
    },
  };
}

export async function getVoteManagers(): Promise<{ vote_manager_addresses: string[]; threshold: number }> {
  const resp = await fetchJson<{ vote_manager_addresses?: string[]; threshold?: number | string }>(
    "/shielded-vote/v1/vote-managers"
  );
  return {
    vote_manager_addresses: resp.vote_manager_addresses ?? [],
    threshold: typeof resp.threshold === "number" ? resp.threshold : parseInt(resp.threshold ?? "1", 10) || 1,
  };
}

export async function getPendingCoordinatorActions(): Promise<{ actions: CoordinatorAction[] }> {
  const resp = await fetchJson<{ actions?: CoordinatorAction[] }>("/shielded-vote/v1/coordinator-actions");
  return { actions: resp.actions ?? [] };
}

export async function getCoordinatorAction(actionID: number): Promise<{ action?: CoordinatorAction }> {
  return fetchJson<{ action?: CoordinatorAction }>(
    `/shielded-vote/v1/coordinator-actions/${encodeURIComponent(String(actionID))}`
  );
}

export interface EndorserEntry {
  endorser_id: string;
  address: string;
}

export async function getEndorsers(): Promise<{ endorsers: EndorserEntry[] }> {
  const resp = await fetchJson<{ endorsers?: EndorserEntry[] }>("/shielded-vote/v1/endorsers");
  return { endorsers: resp.endorsers ?? [] };
}

export async function getEndorsedRounds(
  endorserId: string
): Promise<{ vote_round_ids: string[] }> {
  const resp = await fetchJson<{ vote_round_ids?: string[] }>(
    `/shielded-vote/v1/endorsed-rounds/${encodeURIComponent(endorserId)}`
  );
  return { vote_round_ids: resp.vote_round_ids ?? [] };
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
  const pir = await fetchReadOnlyNullifierJson<{ height: number | null; num_ranges: number }>(
    "/root",
    assertPirRootResponse
  );
  return {
    latest_height: pir.height,
    nullifier_count: pir.num_ranges,
  };
}

export async function listRounds(): Promise<{ rounds: ChainRound[] | null }> {
  return fetchJson<{ rounds: ChainRound[] | null }>("/shielded-vote/v1/rounds");
}

export function isActiveRoundStatus(status: unknown): boolean {
  const normalized = String(status ?? "").trim().toLowerCase();
  return normalized === "1" || normalized === "active" || normalized === "session_status_active";
}

function optionalRoundNumber(value: string | number | undefined): number | null {
  if (value === undefined || value === "") return null;
  const parsed = typeof value === "number" ? value : Number(value);
  return Number.isFinite(parsed) ? parsed : null;
}

function compareRoundsNewestFirst(a: ChainRound, b: ChainRound): number {
  const aHeight = optionalRoundNumber(a.created_at_height) ?? Number.NEGATIVE_INFINITY;
  const bHeight = optionalRoundNumber(b.created_at_height) ?? Number.NEGATIVE_INFINITY;
  if (aHeight !== bHeight) return bHeight - aHeight;

  const aEnd = optionalRoundNumber(a.vote_end_time) ?? Number.NEGATIVE_INFINITY;
  const bEnd = optionalRoundNumber(b.vote_end_time) ?? Number.NEGATIVE_INFINITY;
  return bEnd - aEnd;
}

export function getActiveRoundsFromList(rounds: ChainRound[] | null | undefined): ChainRound[] {
  return [...(rounds ?? [])]
    .filter((round) => isActiveRoundStatus(round.status))
    .sort(compareRoundsNewestFirst);
}

export function getPrimaryActiveRoundFromList(rounds: ChainRound[] | null | undefined): ChainRound | null {
  return getActiveRoundsFromList(rounds)[0] ?? null;
}

export async function getActiveRounds(): Promise<{ rounds: ChainRound[] }> {
  const resp = await listRounds();
  return { rounds: getActiveRoundsFromList(resp.rounds) };
}

export async function getPrimaryActiveRound(): Promise<{ round: ChainRound | null }> {
  const resp = await listRounds();
  return { round: getPrimaryActiveRoundFromList(resp.rounds) };
}

export interface AttestRoundEntryResponse {
  canonical_payload_b64: string;
  signed_payload_hash: string;
  auth_version: number;
}

export async function attestRoundEntry(input: {
  round_id: string;
  ea_pk: string;
  auth_version: 1;
}): Promise<AttestRoundEntryResponse> {
  return fetchJson<AttestRoundEntryResponse>("/api/sign-config-entry", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  });
}

export interface ConfigRoundSignature {
  key_id: string;
  alg: "ed25519";
  sig: string;
}

export interface ConfigRoundEntry {
  auth_version: 1;
  ea_pk: string;
  signatures: ConfigRoundSignature[];
}

export interface ConfigPRAuth {
  signer_address: string;
  payload: string;
  signature: string;
  pub_key: string;
}

export interface CreateConfigPRResponse {
  html_url: string;
  branch: string;
  commit_sha?: string;
  merged_existing_signature: boolean;
}

export async function createConfigPr(input: {
  round_id: string;
  entry: ConfigRoundEntry;
  signed_payload_hash: string;
  title?: string;
  auth: ConfigPRAuth;
}): Promise<CreateConfigPRResponse> {
  return fetchJson<CreateConfigPRResponse>("/api/config-prs", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify(input),
  });
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

export async function getQueueSummaryFromServer(
  serverUrl: string,
  roundIdHex: string
): Promise<QueueSummaryResponse> {
  const base = serverUrl.replace(/\/+$/, "");
  return fetchJsonAtUrl<QueueSummaryResponse>(
    `${base}/shielded-vote/v1/queue-summary/${encodeURIComponent(roundIdHex)}`
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
  return fetchReadOnlyNullifierJson<SnapshotStatus>("/snapshot/status", assertSnapshotStatus);
}

export async function getLocalSnapshotStatus(): Promise<SnapshotStatus> {
  return fetchNullifierJsonAtBase<SnapshotStatus>(LOCAL_PIR_URL, "/snapshot/status", assertSnapshotStatus);
}

export async function prepareSnapshot(height: number): Promise<{ status: string; target_height: number }> {
  return fetchJsonAtUrl<{ status: string; target_height: number }>(`${LOCAL_PIR_URL}/snapshot/prepare`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ height }),
  });
}

// -- UI runtime config --

export type UIMode = "dev" | "prod";

export interface UIConfig {
  mode: UIMode;
  dev_pir_controls: boolean;
  /**
   * Bucket origin this svoted's PIR siblings fetch snapshots from
   * (no trailing slash). Resolved server-side from SVOTE_PRECOMPUTED_BASE_URL
   * with a production-bucket default. Compose with {@link PIR_SNAPSHOTS_PATH}.
   *
   * Optional in the type so an older svoted that doesn't yet expose it
   * leaves the UI rendering an "unknown bucket" fallback rather than crashing.
   */
  precomputed_base_url?: string;
}

/**
 * Fetch the runtime UI config resolved by svoted from its environment.
 * Returns prod-safe defaults if the endpoint is unreachable so an older
 * svoted (or a misconfigured proxy) cannot accidentally expose dev controls.
 */
export async function getUIConfig(): Promise<UIConfig> {
  try {
    return await fetchJson<UIConfig>("/api/ui-config");
  } catch {
    return { mode: "prod", dev_pir_controls: false };
  }
}

// -- Published snapshot manifest (DigitalOcean Spaces) --

export interface PublishedSnapshotFile {
  size: number;
  sha256: string;
}

export interface PublishedSnapshotManifest {
  schema_version: number;
  height: number;
  created_at: string;
  nf_server_sha256?: string;
  publisher?: { git_ref?: string; git_sha?: string };
  files: Record<string, PublishedSnapshotFile>;
}

export const REQUIRED_PUBLISHED_SNAPSHOT_FILES = [
  "tier0.bin",
  "tier1.bin",
  "tier2.bin",
  "pir_root.json",
] as const;

export type PublishedSnapshotValidationStatus =
  | "valid"
  | "missing"
  | "invalid"
  | "error";

export interface PublishedSnapshotValidationResult {
  status: PublishedSnapshotValidationStatus;
  height: number;
  manifestUrl: string;
  manifest?: PublishedSnapshotManifest;
  issues?: string[];
  message?: string;
}

function canonicalPublishedSnapshotBase(precomputedBase: string): string {
  const base = precomputedBase.replace(/\/+$/, "");
  try {
    const url = new URL(base);
    if (url.hostname === "vote.fra1.digitaloceanspaces.com") {
      url.hostname = "vote.fra1.cdn.digitaloceanspaces.com";
      return url.toString().replace(/\/+$/, "");
    }
  } catch {
    // Leave non-URL bases unchanged; fetch will surface a useful error.
  }
  return base;
}

export function getPublishedSnapshotManifestUrl(
  precomputedBase: string,
  height: number
): string {
  const base = canonicalPublishedSnapshotBase(precomputedBase);
  return `${base}${PIR_SNAPSHOTS_PATH}/${height}/manifest.json`;
}

function getPublishedSnapshotFetchUrl(
  precomputedBase: string,
  height: number
): string {
  if (import.meta.env.DEV) {
    return `${PIR_SNAPSHOTS_PATH.replace(/^\/snapshots/, "/precomputed-snapshots/snapshots")}/${height}/manifest.json`;
  }
  return getPublishedSnapshotManifestUrl(precomputedBase, height);
}

export function validatePublishedSnapshotManifestShape(
  manifest: unknown,
  expectedHeight: number
): string[] {
  const issues: string[] = [];
  if (!isRecord(manifest)) {
    return ["manifest must be an object"];
  }
  if (manifest.schema_version !== 1) {
    issues.push(`schema_version must be 1, got ${String(manifest.schema_version)}`);
  }
  if (manifest.height !== expectedHeight) {
    issues.push(`manifest height ${manifest.height} does not match requested height ${expectedHeight}`);
  }
  if (!manifest.files || typeof manifest.files !== "object") {
    issues.push("manifest files must be an object");
    return issues;
  }
  for (const name of REQUIRED_PUBLISHED_SNAPSHOT_FILES) {
    const file = (manifest.files as Record<string, unknown>)[name];
    if (!isRecord(file)) {
      issues.push(`missing required file ${name}`);
      continue;
    }
    if (typeof file.size !== "number" || !Number.isFinite(file.size) || file.size <= 0) {
      issues.push(`${name} has invalid size`);
    }
    if (typeof file.sha256 !== "string" || !/^[a-f0-9]{64}$/i.test(file.sha256)) {
      issues.push(`${name} has invalid sha256`);
    }
  }
  return issues;
}

/**
 * Fetch the manifest.json for a pre-computed PIR snapshot at the given height.
 * The manifest is uploaded last by the publisher CI, so its presence implies
 * a complete snapshot directory.
 *
 * `precomputedBase` is the bucket-level base URL exposed by svoted via
 * /api/ui-config (it's a per-deployment service config, not a wallet-facing
 * one). The PIR-specific subpath is appended here so callers don't have to
 * hard-code it.
 */
export async function getPublishedSnapshotManifest(
  precomputedBase: string,
  height: number
): Promise<PublishedSnapshotManifest> {
  const url = getPublishedSnapshotFetchUrl(precomputedBase, height);
  const resp = await fetch(url, { cache: "no-cache" });
  if (!resp.ok) {
    throw new Error(`HTTP ${resp.status} fetching ${getPublishedSnapshotManifestUrl(precomputedBase, height)}`);
  }
  return resp.json();
}

export async function validatePublishedSnapshotManifest(
  precomputedBase: string,
  height: number
): Promise<PublishedSnapshotValidationResult> {
  const manifestUrl = getPublishedSnapshotManifestUrl(precomputedBase, height);
  const fetchUrl = getPublishedSnapshotFetchUrl(precomputedBase, height);
  try {
    const resp = await fetch(fetchUrl, { cache: "no-cache" });
    if (resp.status === 404) {
      return {
        status: "missing",
        height,
        manifestUrl,
        message: "No manifest.json exists for this snapshot height.",
      };
    }
    if (!resp.ok) {
      return {
        status: "error",
        height,
        manifestUrl,
        message: `HTTP ${resp.status} fetching ${manifestUrl}`,
      };
    }
    const manifest = await resp.json() as unknown;
    const issues = validatePublishedSnapshotManifestShape(manifest, height);
    if (issues.length > 0) {
      return { status: "invalid", height, manifestUrl, issues };
    }
    return {
      status: "valid",
      height,
      manifestUrl,
      manifest: manifest as PublishedSnapshotManifest,
    };
  } catch (err) {
    return {
      status: "error",
      height,
      manifestUrl,
      message: err instanceof Error ? err.message : "Failed to validate manifest",
    };
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
  pir_endpoints: ServiceEntry[];
}

/**
 * Subpath under the service-level precomputed_base_url where PIR snapshots
 * live. The base itself is per-deployment (svoted exposes it via
 * /api/ui-config); the path is a fleet-wide convention.
 */
export const PIR_SNAPSHOTS_PATH = "/snapshots";

/** Row from GET /api/pending-validators (SQLite-backed join queue). */
export interface PendingValidatorPublic {
  operator_address: string;
  url: string;
  moniker: string;
  requested_at: number;
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
 * Fetch pending validator join requests from svoted (SQLite-backed).
 */
export async function getPendingValidators(): Promise<PendingValidatorPublic[]> {
  try {
    return await fetchJson<PendingValidatorPublic[]>("/api/pending-validators");
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

// submitSession was removed: MsgCreateVotingSession is now proposed as a
// coordinator action signed client-side. See cosmosTx.ts.
