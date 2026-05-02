import { useEffect, useMemo, useState } from "react";
import {
  AlertCircle,
  Check,
  Copy,
  ExternalLink,
  RefreshCw,
  ShieldCheck,
  Wallet,
} from "lucide-react";
import * as chainApi from "../api/chain";
import * as votingKey from "../api/votingKey";
import { useWallet } from "../hooks/useWallet";

interface RoundOption {
  roundIdHex: string;
  eaPK: string;
  title: string;
  status: string;
  createdAtHeight: number | null;
  voteEndTime: number | null;
  isActive: boolean;
}

interface DerivedPublicKeyInfo {
  signerId: string;
  publicKeyB64: string;
  createdAt: string;
  sourceAddress: string;
  chainId: string;
}

interface ConfigPRIntentPayload {
  action: "create_config_pr";
  round_id: string;
  signed_payload_hash: string;
  entry_sha256: string;
  timestamp: number;
}

function bytesToHex(bytes: Uint8Array): string {
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
}

function base64ToBytes(value: string): Uint8Array {
  const binary = atob(value);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

function bytesToBase64(bytes: Uint8Array): string {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

function arrayBufferFromBytes(bytes: Uint8Array): ArrayBuffer {
  return bytes.buffer.slice(
    bytes.byteOffset,
    bytes.byteOffset + bytes.byteLength
  ) as ArrayBuffer;
}

async function sha256Hex(bytes: Uint8Array): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", arrayBufferFromBytes(bytes));
  return bytesToHex(new Uint8Array(digest));
}

function normalizeRoundId(value: string | undefined): string | null {
  if (!value) return null;
  const trimmed = value.trim();
  if (/^[0-9a-f]{64}$/.test(trimmed)) return trimmed;
  try {
    const hex = bytesToHex(base64ToBytes(trimmed));
    return /^[0-9a-f]{64}$/.test(hex) ? hex : null;
  } catch {
    return null;
  }
}

function validateEaPK(value: string): boolean {
  try {
    return base64ToBytes(value.trim()).length === 32;
  } catch {
    return false;
  }
}

function optionalNumber(value: string | number | undefined): number | null {
  if (value === undefined || value === "") return null;
  const parsed = typeof value === "number" ? value : Number(value);
  return Number.isFinite(parsed) ? parsed : null;
}

function isActiveStatus(status: string): boolean {
  const normalized = status.trim().toLowerCase();
  return normalized === "1" || normalized === "active" || normalized === "session_status_active";
}

function getLatestRound(rounds: RoundOption[]): RoundOption | null {
  return rounds.reduce<RoundOption | null>((latest, round) => {
    if (!latest) return round;

    const roundHeight = round.createdAtHeight ?? Number.NEGATIVE_INFINITY;
    const latestHeight = latest.createdAtHeight ?? Number.NEGATIVE_INFINITY;
    if (roundHeight !== latestHeight) return roundHeight > latestHeight ? round : latest;

    const roundEndTime = round.voteEndTime ?? Number.NEGATIVE_INFINITY;
    const latestEndTime = latest.voteEndTime ?? Number.NEGATIVE_INFINITY;
    return roundEndTime > latestEndTime ? round : latest;
  }, null);
}

function getDefaultRound(
  rounds: RoundOption[],
  activeRoundIdHex: string | null
): RoundOption | null {
  if (activeRoundIdHex) {
    const activeRound = rounds.find((round) => round.roundIdHex === activeRoundIdHex);
    if (activeRound) return activeRound;
  }
  return rounds.find((round) => round.isActive) ?? getLatestRound(rounds);
}

function CopyButton({ value, label }: { value: string; label: string }) {
  const [copied, setCopied] = useState(false);

  const copy = async () => {
    await navigator.clipboard.writeText(value);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  };

  return (
    <button
      onClick={copy}
      disabled={!value}
      className="inline-flex items-center gap-1.5 px-2.5 py-1.5 bg-surface-3 hover:bg-surface-2 text-text-secondary hover:text-text-primary rounded-md text-[11px] transition-colors cursor-pointer disabled:opacity-50"
    >
      {copied ? <Check size={12} className="text-success" /> : <Copy size={12} />}
      {copied ? "Copied" : label}
    </button>
  );
}

export function AttestRoundEntryPage() {
  const wallet = useWallet();
  const [keyInfo, setKeyInfo] = useState<DerivedPublicKeyInfo | null>(null);
  const [derivingKey, setDerivingKey] = useState(false);
  const [deriveNotice, setDeriveNotice] = useState("");
  const [rounds, setRounds] = useState<RoundOption[]>([]);
  const [loadingRounds, setLoadingRounds] = useState(true);
  const [roundError, setRoundError] = useState("");
  const [roundId, setRoundId] = useState("");
  const [eaPK, setEaPK] = useState("");
  const [signing, setSigning] = useState(false);
  const [error, setError] = useState("");
  const [hash, setHash] = useState("");
  const [payloadNotice, setPayloadNotice] = useState("");
  const [snippet, setSnippet] = useState("");
  const [configPrStatus, setConfigPrStatus] = useState<"idle" | "creating" | "ok" | "error">("idle");
  const [configPrUrl, setConfigPrUrl] = useState("");
  const [configPrError, setConfigPrError] = useState("");

  const selectedRoundKey = useMemo(() => `${roundId}|${eaPK}`, [roundId, eaPK]);
  const latestRound = useMemo(() => getLatestRound(rounds), [rounds]);
  const selectedRound = useMemo(
    () => rounds.find((round) => round.roundIdHex === roundId) ?? null,
    [roundId, rounds]
  );
  const selectedRoundIsActive = selectedRound?.isActive ?? false;
  const selectedRoundIsLatest =
    !!selectedRound && latestRound?.roundIdHex === selectedRound.roundIdHex;
  const canSignRound = /^[0-9a-f]{64}$/.test(roundId) && validateEaPK(eaPK);

  const loadRounds = async () => {
    setLoadingRounds(true);
    setRoundError("");
    try {
      const [resp, activeResp] = await Promise.all([
        chainApi.listRounds(),
        chainApi.getActiveRound().catch(() => ({ round: null })),
      ]);
      const activeRoundIdHex = normalizeRoundId(activeResp.round?.vote_round_id);
      const options = (resp.rounds ?? [])
        .map((round): RoundOption | null => {
          const roundIdHex = normalizeRoundId(round.vote_round_id);
          if (!roundIdHex || !round.ea_pk) return null;
          return {
            roundIdHex,
            eaPK: round.ea_pk,
            title: round.title || round.description || roundIdHex,
            status: String(round.status ?? "unknown"),
            createdAtHeight: optionalNumber(round.created_at_height),
            voteEndTime: optionalNumber(round.vote_end_time),
            isActive: roundIdHex === activeRoundIdHex || isActiveStatus(String(round.status ?? "")),
          };
        })
        .filter((round): round is RoundOption => round !== null);
      setRounds(options);
      const currentSelectionStillExists = options.some((round) => round.roundIdHex === roundId);
      if (options.length > 0 && (!roundId || !currentSelectionStillExists)) {
        const defaultRound = getDefaultRound(options, activeRoundIdHex);
        if (defaultRound) {
          setRoundId(defaultRound.roundIdHex);
          setEaPK(defaultRound.eaPK);
        }
      }
    } catch (err) {
      setRoundError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoadingRounds(false);
    }
  };

  useEffect(() => {
    void loadRounds();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useEffect(() => {
    const clearDerivedPublicKey = () => {
      setKeyInfo(null);
      setDeriveNotice("");
    };
    window.addEventListener("keplr_keystorechange", clearDerivedPublicKey);
    return () => window.removeEventListener("keplr_keystorechange", clearDerivedPublicKey);
  }, []);

  useEffect(() => {
    setHash("");
    setPayloadNotice("");
    setSnippet("");
    setError("");
    setConfigPrStatus("idle");
    setConfigPrUrl("");
    setConfigPrError("");
  }, [selectedRoundKey]);

  const deriveEphemeralKey = async (): Promise<votingKey.KeplrDerivedVotingKeyInfo> => {
    if (!wallet.address || wallet.source !== "keplr" || !wallet.chainId) {
      throw new Error("Connect Keplr before deriving the signing key.");
    }
    return votingKey.deriveEd25519FromKeplr(
      wallet.address,
      wallet.chainId,
      wallet.signKeplrPayload
    );
  };

  const rememberPublicKey = (key: votingKey.KeplrDerivedVotingKeyInfo) => {
    setKeyInfo({
      signerId: key.signerId,
      publicKeyB64: key.publicKeyB64,
      createdAt: key.createdAt,
      sourceAddress: key.sourceAddress,
      chainId: key.chainId,
    });
  };

  const createSignedJSON = async (key: votingKey.VotingKeyInfo) => {
    if (!canSignRound) {
      setError("Pick a round with a 64-character hex round_id and base64 32-byte ea_pk first.");
      return;
    }
    setSigning(true);
    setError("");
    setPayloadNotice("");
    try {
      let response: chainApi.AttestRoundEntryResponse;
      try {
        response = await chainApi.attestRoundEntry({
          round_id: roundId,
          ea_pk: eaPK,
          auth_version: 1,
        });
      } catch {
        const payload = base64ToBytes(eaPK);
        response = {
          canonical_payload_b64: bytesToBase64(payload),
          signed_payload_hash: await sha256Hex(payload),
          auth_version: 1,
        };
        setPayloadNotice(
          "Remote /api/sign-config-entry did not return JSON, so this used the auth_version 1 local payload fallback."
        );
      }
      const sigB64 = await votingKey.signCanonicalPayload(
        response.canonical_payload_b64,
        key
      );
      const entry = {
        auth_version: response.auth_version,
        ea_pk: eaPK,
        signatures: [
          {
            key_id: key.signerId,
            alg: "ed25519",
            sig: sigB64,
          },
        ],
      };
      setHash(response.signed_payload_hash);
      setSnippet(JSON.stringify({ [roundId]: entry }, null, 2));
      setConfigPrStatus("idle");
      setConfigPrUrl("");
      setConfigPrError("");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSigning(false);
    }
  };

  const handleDeriveFromKeplr = async () => {
    setError("");
    setDeriveNotice("");
    if (!wallet.address || wallet.source !== "keplr" || !wallet.chainId) {
      setError("Connect Keplr before deriving the signing key.");
      return;
    }
    setDerivingKey(true);
    try {
      const derived = await deriveEphemeralKey();
      rememberPublicKey(derived);
      setDeriveNotice("Derived the Ed25519 public key from Keplr. Secret material was discarded.");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setDerivingKey(false);
    }
  };

  const handleSignWithKeplr = async () => {
    setError("");
    setDeriveNotice("");
    setSigning(true);
    try {
      const derived = await deriveEphemeralKey();
      rememberPublicKey(derived);
      await createSignedJSON(derived);
      setDeriveNotice("Signed with a freshly derived key. Secret material was discarded after signing.");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setSigning(false);
    }
  };

  const handleDisconnect = () => {
    setKeyInfo(null);
    setDeriveNotice("");
    wallet.disconnect();
  };

  const handleSelectRound = (value: string) => {
    const selected = rounds.find((round) => round.roundIdHex === value);
    if (!selected) return;
    setRoundId(selected.roundIdHex);
    setEaPK(selected.eaPK);
  };

  const handleCreateConfigPr = async () => {
    setConfigPrStatus("creating");
    setConfigPrError("");
    setConfigPrUrl("");
    try {
      if (!wallet.address) {
        throw new Error("Connect a vote-manager wallet before opening a config PR.");
      }
      const managers = await chainApi.getVoteManagers();
      const isVoteManager = (managers.vote_manager_addresses ?? []).some(
        (address) => address.toLowerCase() === wallet.address?.toLowerCase()
      );
      if (!isVoteManager) {
        throw new Error("Connected wallet is not in the current vote-manager set.");
      }

      const parsed = JSON.parse(snippet) as Record<string, chainApi.ConfigRoundEntry>;
      const entry = parsed[roundId];
      if (!entry) {
        throw new Error("Generated JSON snippet does not contain the selected round.");
      }
      const entryHash = await sha256Hex(new TextEncoder().encode(JSON.stringify(entry)));
      const intent: ConfigPRIntentPayload = {
        action: "create_config_pr",
        round_id: roundId,
        signed_payload_hash: hash,
        entry_sha256: entryHash,
        timestamp: Math.floor(Date.now() / 1000),
      };
      const payload = JSON.stringify(intent);
      const signature = await wallet.signPayload(payload);
      const selectedRound = rounds.find((round) => round.roundIdHex === roundId);
      const resp = await chainApi.createConfigPr({
        round_id: roundId,
        entry,
        signed_payload_hash: hash,
        title: selectedRound?.title,
        auth: {
          signer_address: wallet.address,
          payload,
          signature: signature.signature,
          pub_key: signature.pubKey,
        },
      });
      setConfigPrUrl(resp.html_url);
      setConfigPrStatus("ok");
    } catch (err) {
      setConfigPrError(err instanceof Error ? err.message : String(err));
      setConfigPrStatus("error");
    }
  };

  return (
    <div className="flex-1 overflow-y-auto">
      <div className="max-w-3xl mx-auto px-6 py-10 space-y-6">
        <div className="flex items-start justify-between gap-4">
          <div>
            <div className="flex items-center gap-2 mb-2">
              <ShieldCheck size={18} className="text-accent" />
              <h1 className="text-lg font-bold text-text-primary">
                Attest Round Entry
              </h1>
            </div>
            <p className="text-[11px] text-text-muted max-w-2xl">
              Create a signed <code>rounds</code> entry for{" "}
              <a
                href="https://github.com/valargroup/token-holder-voting-config/blob/main/dynamic-voting-config.json"
                target="_blank"
                rel="noreferrer"
                className="inline-flex items-center gap-1 text-accent hover:text-accent/80 underline-offset-2 hover:underline"
              >
                <code>dynamic-voting-config.json</code>
                <ExternalLink size={10} />
              </a>
              . This page generates a signed <code>rounds</code> entry for the selected round.
            </p>
          </div>
          <button
            onClick={loadRounds}
            disabled={loadingRounds}
            className="p-2 hover:bg-surface-3 rounded-lg text-text-muted hover:text-text-secondary cursor-pointer disabled:opacity-50"
            title="Refresh rounds"
          >
            <RefreshCw size={14} className={loadingRounds ? "animate-spin" : ""} />
          </button>
        </div>

        <section className="bg-surface-1 border border-border-subtle rounded-xl p-5 space-y-4">
          <div className="flex items-center gap-2">
            <Wallet size={14} className="text-text-muted" />
            <h2 className="text-xs font-semibold text-text-primary">
              Connect Keplr wallet
            </h2>
          </div>

          <div className="rounded-lg border border-accent/30 bg-accent/10 p-3">
            <p className="text-[11px] text-accent font-semibold">
              Connect Keplr to approve the round entry Election Authority public key by signing over it.
            </p>
            <p className="text-[10px] text-text-secondary mt-1">
              Derives the Ed25519 key in memory. The seed is not stored or shared with the server.
              The derived key is used to sign the round entry Election Authority public key.
              Upon successful signing, an option to open a pull request against the [token-holder-voting-config](https://github.com/valargroup/token-holder-voting-config) repository is provided.
            </p>
          </div>

          <div className="flex flex-col gap-3 rounded-lg bg-surface-2 px-3 py-3">
            <div className="flex flex-col gap-2 md:flex-row md:items-center md:justify-between">
              <div className="min-w-0">
                <p className="text-[10px] text-text-muted">Connected wallet</p>
                <p className="text-[11px] text-text-primary font-mono break-all">
                  {wallet.address
                    ? `${wallet.address}${wallet.chainId ? ` (${wallet.chainId})` : ""}`
                    : "No Keplr wallet connected"}
                </p>
                {wallet.error && (
                  <p className="mt-1 text-[10px] text-danger">{wallet.error}</p>
                )}
              </div>
              <div className="flex gap-2 shrink-0">
                {wallet.address ? (
                  <button
                    onClick={handleDisconnect}
                    className="px-3 py-2 bg-surface-3 hover:bg-surface-1 text-text-secondary rounded-lg text-[11px] font-semibold transition-colors cursor-pointer"
                  >
                    Disconnect
                  </button>
                ) : (
                  <button
                    onClick={wallet.connect}
                    disabled={wallet.connecting}
                    className="px-3 py-2 bg-accent/90 hover:bg-accent text-surface-0 rounded-lg text-[11px] font-semibold transition-colors cursor-pointer disabled:opacity-50"
                  >
                    {wallet.connecting ? "Connecting..." : "Connect Keplr"}
                  </button>
                )}
                <button
                  onClick={handleDeriveFromKeplr}
                  disabled={
                    !wallet.address ||
                    wallet.source !== "keplr" ||
                    !wallet.chainId ||
                    derivingKey
                  }
                  className="px-3 py-2 bg-surface-3 hover:bg-surface-1 text-text-secondary rounded-lg text-[11px] font-semibold transition-colors cursor-pointer disabled:opacity-50"
                >
                  {derivingKey ? "Deriving..." : "Show public key"}
                </button>
              </div>
            </div>
          </div>

          {deriveNotice && (
            <p className="text-[10px] text-success">{deriveNotice}</p>
          )}

          {keyInfo && (
            <div className="bg-surface-2 rounded-lg px-3 py-2 space-y-1">
              <p className="text-[10px] text-text-muted">
                Derived Ed25519 public key
              </p>
              <p className="text-[11px] text-text-primary font-mono break-all">
                {keyInfo.publicKeyB64}
              </p>
              <div className="flex flex-wrap items-center gap-2">
                <CopyButton value={keyInfo.publicKeyB64} label="Copy public key" />
                <CopyButton
                  value={JSON.stringify({
                    key_id: keyInfo.signerId,
                    alg: "ed25519",
                    pubkey: keyInfo.publicKeyB64,
                  })}
                  label="Copy trusted key"
                />
              </div>
              <p className="text-[10px] text-warning mt-2">
                Add this public key to{" "}
                <a
                  href="https://github.com/valargroup/token-holder-voting-config/blob/main/static-voting-config.json"
                  target="_blank"
                  rel="noreferrer"
                  className="inline-flex items-center gap-1 text-warning underline-offset-2 hover:underline"
                >
                  <code>static-voting-config.json</code>
                  <ExternalLink size={10} />
                </a>{" "}
                <code>trusted_keys</code> and ship that trust anchor before
                signatures from this key are accepted by wallets.
              </p>
            </div>
          )}
        </section>

        <section className="bg-surface-1 border border-border-subtle rounded-xl p-5 space-y-4">
          <h2 className="text-xs font-semibold text-text-primary">
            Round entry
          </h2>

          {roundError && (
            <div className="flex items-start gap-2 bg-danger/10 border border-danger/30 rounded-lg p-3">
              <AlertCircle size={14} className="text-danger mt-0.5 shrink-0" />
              <p className="text-[11px] text-danger">{roundError}</p>
            </div>
          )}

          <div>
            <label className="block text-[11px] text-text-secondary mb-1">
              Pick round from chain
            </label>
            <select
              value={rounds.some((round) => round.roundIdHex === roundId) ? roundId : ""}
              onChange={(e) => handleSelectRound(e.target.value)}
              disabled={loadingRounds || rounds.length === 0}
              className="w-full px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary focus:outline-none focus:border-accent/50 cursor-pointer disabled:opacity-50 [color-scheme:dark]"
            >
              {loadingRounds && <option value="">Loading rounds...</option>}
              {!loadingRounds && rounds.length === 0 && (
                <option value="">No rounds with EA keys found</option>
              )}
              {rounds.map((round) => (
                <option key={round.roundIdHex} value={round.roundIdHex}>
                  {round.isActive ? "[ACTIVE] " : ""}
                  {latestRound?.roundIdHex === round.roundIdHex ? "[LATEST] " : ""}
                  {round.title} ({round.status}) - {round.roundIdHex.slice(0, 12)}...
                </option>
              ))}
            </select>
            {selectedRound && (
              <div className="mt-2 flex flex-wrap items-center gap-2">
                {selectedRoundIsActive && (
                  <span className="rounded-full border border-success/30 bg-success/10 px-2 py-0.5 text-[10px] font-semibold text-success">
                    Active round
                  </span>
                )}
                {!selectedRoundIsActive && selectedRoundIsLatest && (
                  <span className="rounded-full border border-accent/30 bg-accent/10 px-2 py-0.5 text-[10px] font-semibold text-accent">
                    Latest round
                  </span>
                )}
                <span className="text-[10px] text-text-muted">
                  {selectedRound.title}
                </span>
              </div>
            )}
          </div>

          <div>
            <label className="block text-[11px] text-text-secondary mb-1">
              round_id
            </label>
            <input
              value={roundId}
              onChange={(e) => setRoundId(e.target.value.trim())}
              placeholder="64 lowercase hex characters"
              spellCheck={false}
              className="w-full px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary placeholder:text-text-muted focus:outline-none focus:border-accent/50 font-mono"
            />
          </div>

          <div>
            <label className="block text-[11px] text-text-secondary mb-1">
              ea_pk
            </label>
            <input
              value={eaPK}
              onChange={(e) => setEaPK(e.target.value.trim())}
              placeholder="base64 32-byte EA public key"
              spellCheck={false}
              className="w-full px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary placeholder:text-text-muted focus:outline-none focus:border-accent/50 font-mono"
            />
          </div>

          <div className="flex flex-col gap-2 md:flex-row md:items-center md:justify-between">
            <p className="text-[10px] text-text-muted">
              Signing asks Keplr again, derives the Ed25519 key in flight, and
              discards secret material after creating the JSON snippet.
            </p>
            <button
              onClick={handleSignWithKeplr}
              disabled={
                !wallet.address ||
                wallet.source !== "keplr" ||
                !wallet.chainId ||
                !canSignRound ||
                signing
              }
              className="px-3 py-2 bg-accent/90 hover:bg-accent text-surface-0 rounded-lg text-[11px] font-semibold transition-colors cursor-pointer disabled:opacity-50"
            >
              {signing ? "Signing..." : "Sign with derived key"}
            </button>
          </div>
        </section>

        {error && (
          <div className="flex items-start gap-2 bg-danger/10 border border-danger/30 rounded-lg p-3">
            <AlertCircle size={14} className="text-danger mt-0.5 shrink-0" />
            <p className="text-[11px] text-danger">{error}</p>
          </div>
        )}

        {payloadNotice && (
          <div className="flex items-start gap-2 bg-warning/10 border border-warning/30 rounded-lg p-3">
            <AlertCircle size={14} className="text-warning mt-0.5 shrink-0" />
            <p className="text-[11px] text-text-secondary">{payloadNotice}</p>
          </div>
        )}

        {snippet && (
          <section className="bg-surface-1 border border-border-subtle rounded-xl p-5 space-y-4">
            <div>
              <div className="flex items-center justify-between mb-1.5">
                <h2 className="text-xs font-semibold text-text-primary">
                  JSON snippet
                </h2>
                <div className="flex flex-wrap items-center gap-2">
                  <CopyButton value={snippet} label="Copy JSON" />
                  <button
                    onClick={handleCreateConfigPr}
                    disabled={
                      configPrStatus === "creating" ||
                      !wallet.address ||
                      !hash ||
                      !snippet ||
                      !canSignRound
                    }
                    className="inline-flex items-center gap-1.5 px-2.5 py-1.5 bg-accent/90 hover:bg-accent text-surface-0 rounded-md text-[11px] font-semibold transition-colors cursor-pointer disabled:opacity-50"
                  >
                    {configPrStatus === "creating" ? "Opening Pull Request..." : "Update via Pull Request"}
                  </button>
                  <a
                    href="https://github.com/valargroup/token-holder-voting-config/pull/36"
                    target="_blank"
                    rel="noreferrer"
                    className="inline-flex items-center gap-1.5 px-2.5 py-1.5 bg-surface-2 hover:bg-surface-3 text-text-secondary rounded-md text-[11px] font-semibold transition-colors ml-2"
                  >
                    <span>Sample Pull Request</span>
                    <ExternalLink size={12} />
                  </a>
                </div>
              </div>
              <pre className="text-[11px] text-text-primary font-mono whitespace-pre-wrap break-all bg-surface-2 rounded-lg px-3 py-2 overflow-x-auto">
                {snippet}
              </pre>
              {configPrError && (
                <div className="flex items-start gap-2 bg-danger/10 border border-danger/30 rounded-lg p-3 mt-3">
                  <AlertCircle size={14} className="text-danger mt-0.5 shrink-0" />
                  <p className="text-[11px] text-danger">{configPrError}</p>
                </div>
              )}
              {configPrUrl && (
                <a
                  href={configPrUrl}
                  target="_blank"
                  rel="noreferrer"
                  className="inline-flex items-center gap-1.5 mt-3 text-[11px] font-semibold text-accent hover:text-accent/80"
                >
                  Opened config PR
                  <ExternalLink size={12} />
                </a>
              )}
            </div>
          </section>
        )}

      </div>
    </div>
  );
}
