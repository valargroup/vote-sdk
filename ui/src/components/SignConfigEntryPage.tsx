import { useEffect, useMemo, useState } from "react";
import {
  AlertCircle,
  Check,
  Copy,
  KeyRound,
  RefreshCw,
  ShieldCheck,
} from "lucide-react";
import * as chainApi from "../api/chain";
import * as votingKey from "../api/votingKey";

interface RoundOption {
  roundIdHex: string;
  eaPK: string;
  title: string;
  status: string;
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

export function SignConfigEntryPage() {
  const [keyInfo, setKeyInfo] = useState<votingKey.VotingKeyInfo | null>(null);
  const [signerId, setSignerId] = useState("");
  const [keyMaterialB64, setKeyMaterialB64] = useState("");
  const [importNotice, setImportNotice] = useState("");
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
  const [verifyRoundId, setVerifyRoundId] = useState("");
  const [verifyEaPK, setVerifyEaPK] = useState("");
  const [verifySignature, setVerifySignature] = useState("");
  const [verifying, setVerifying] = useState(false);
  const [verifyResult, setVerifyResult] = useState<chainApi.VerifyConfigEntryResponse | null>(null);
  const [verifyError, setVerifyError] = useState("");

  const selectedRoundKey = useMemo(() => `${roundId}|${eaPK}`, [roundId, eaPK]);
  const canSignRound = /^[0-9a-f]{64}$/.test(roundId) && validateEaPK(eaPK);
  const canVerify =
    /^[0-9a-f]{64}$/.test(verifyRoundId) &&
    validateEaPK(verifyEaPK) &&
    verifySignature.trim().length > 0;

  const loadRounds = async () => {
    setLoadingRounds(true);
    setRoundError("");
    try {
      const resp = await chainApi.listRounds();
      const options = (resp.rounds ?? [])
        .map((round): RoundOption | null => {
          const roundIdHex = normalizeRoundId(round.vote_round_id);
          if (!roundIdHex || !round.ea_pk) return null;
          return {
            roundIdHex,
            eaPK: round.ea_pk,
            title: round.title || round.description || roundIdHex,
            status: String(round.status ?? "unknown"),
          };
        })
        .filter((round): round is RoundOption => round !== null);
      setRounds(options);
      if (options.length > 0 && !roundId) {
        setRoundId(options[0].roundIdHex);
        setEaPK(options[0].eaPK);
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
    setHash("");
    setPayloadNotice("");
    setSnippet("");
    setError("");
  }, [selectedRoundKey]);

  const createSignedJSON = async (key: votingKey.VotingKeyInfo) => {
    if (!canSignRound) {
      setError("Pick a round with a 64-character hex round_id and base64 32-byte ea_pk first.");
      return;
    }
    setSigning(true);
    setError("");
    setPayloadNotice("");
    try {
      let response: chainApi.SignConfigEntryResponse;
      try {
        response = await chainApi.signConfigEntry({
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
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSigning(false);
    }
  };

  const handleGenerate = async () => {
    setError("");
    setImportNotice("");
    try {
      const generated = votingKey.generateKeypair(signerId);
      setKeyInfo(generated);
      try {
        await navigator.clipboard.writeText(generated.seedB64);
        setImportNotice("Generated a new key and copied its seed once. It will not be shown again. Write it down.");
      } catch {
        setImportNotice("Generated a new key, but clipboard copy failed. Generate again to retry copying the seed.");
      }
      await createSignedJSON(generated);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  };

  const handleImport = async () => {
    setError("");
    try {
      const imported = votingKey.importKeyMaterial(keyMaterialB64, signerId);
      setKeyInfo(imported);
      setImportNotice(
        imported.importedAs === "seed"
          ? "Imported 32-byte seed."
          : "Imported 64-byte Ed25519 private key."
      );
      await createSignedJSON(imported);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    }
  };

  const handleSelectRound = (value: string) => {
    const selected = rounds.find((round) => round.roundIdHex === value);
    if (!selected) return;
    setRoundId(selected.roundIdHex);
    setEaPK(selected.eaPK);
  };

  const handleVerify = async () => {
    setVerifying(true);
    setVerifyError("");
    setVerifyResult(null);
    try {
      const resp = await chainApi.verifyConfigEntry({
        round_id: verifyRoundId,
        ea_pk: verifyEaPK,
        signature: verifySignature.trim(),
        auth_version: 1,
      });
      setVerifyResult(resp);
    } catch (err) {
      setVerifyError(err instanceof Error ? err.message : String(err));
    } finally {
      setVerifying(false);
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
                Sign config entry
              </h1>
            </div>
            <p className="text-[11px] text-text-muted max-w-2xl">
              Create a signed `rounds` entry for `voting-config-v2.json`.
              This page uses a browser-resident Ed25519 key and does not use
              Keplr.
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
            <KeyRound size={14} className="text-text-muted" />
            <h2 className="text-xs font-semibold text-text-primary">
              Browser signing key
            </h2>
          </div>

          <div className="rounded-lg border border-warning/30 bg-warning/10 p-3">
            <p className="text-[11px] text-warning font-semibold">
              Temporary operator tooling
            </p>
            <p className="text-[10px] text-text-secondary mt-1">
              Key material is not stored. It is kept only in page memory while
              creating the snippet. When generating in-browser, the seed is
              copied to the clipboard once and is not shown again.
            </p>
          </div>

          <div className="grid md:grid-cols-[1fr_auto] gap-3 items-end">
            <div>
              <label className="block text-[11px] text-text-secondary mb-1">
                signer_id / key_id
              </label>
              <input
                value={signerId}
                onChange={(e) => setSignerId(e.target.value)}
                placeholder="valar-2026-q2"
                className="w-full px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary placeholder:text-text-muted focus:outline-none focus:border-accent/50 font-mono"
              />
            </div>
            <button
              onClick={handleGenerate}
              disabled={!signerId.trim() || !canSignRound || signing}
              className="px-3 py-2 bg-surface-3 hover:bg-surface-2 text-text-secondary rounded-lg text-[11px] font-semibold transition-colors cursor-pointer disabled:opacity-50"
            >
              {signing ? "Signing..." : "Generate and sign"}
            </button>
          </div>

          <div className="grid md:grid-cols-[1fr_auto] gap-3 items-end">
            <div>
              <label className="block text-[11px] text-text-secondary mb-1">
                Import seed or private key
              </label>
              <input
                value={keyMaterialB64}
                onChange={(e) => setKeyMaterialB64(e.target.value)}
                placeholder="base64 32-byte seed or 64-byte Ed25519 private key"
                spellCheck={false}
                autoComplete="off"
                data-1p-ignore
                data-lpignore="true"
                className="w-full px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary placeholder:text-text-muted focus:outline-none focus:border-accent/50 font-mono"
              />
            </div>
            <button
              onClick={handleImport}
              disabled={!signerId.trim() || !keyMaterialB64.trim() || !canSignRound || signing}
              className="px-3 py-2 bg-accent/90 hover:bg-accent text-surface-0 rounded-lg text-[11px] font-semibold transition-colors cursor-pointer disabled:opacity-50"
            >
              {signing ? "Signing..." : "Import and sign"}
            </button>
          </div>

          {importNotice && (
            <p className="text-[10px] text-success">{importNotice}</p>
          )}

          {keyInfo && (
            <div className="bg-surface-2 rounded-lg px-3 py-2 space-y-1">
              <p className="text-[10px] text-text-muted">Current public key (memory only)</p>
              <p className="text-[11px] text-text-primary font-mono break-all">
                {keyInfo.publicKeyB64}
              </p>
              <CopyButton value={keyInfo.publicKeyB64} label="Copy public key" />
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
                  {round.title} ({round.status}) — {round.roundIdHex.slice(0, 12)}...
                </option>
              ))}
            </select>
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

          <p className="text-[10px] text-text-muted">
            Import or generate a key above to create the signed JSON for this round.
          </p>
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

        {(snippet || hash) && (
          <section className="bg-surface-1 border border-border-subtle rounded-xl p-5 space-y-4">
            {hash && (
              <div>
                <div className="flex items-center justify-between mb-1.5">
                  <h2 className="text-xs font-semibold text-text-primary">
                    signed_payload_hash
                  </h2>
                  <CopyButton value={hash} label="Copy hash" />
                </div>
                <p className="text-[11px] text-text-primary font-mono break-all bg-surface-2 rounded-lg px-3 py-2">
                  {hash}
                </p>
              </div>
            )}

            {snippet && (
              <div>
                <div className="flex items-center justify-between mb-1.5">
                  <h2 className="text-xs font-semibold text-text-primary">
                    JSON snippet
                  </h2>
                  <CopyButton value={snippet} label="Copy JSON" />
                </div>
                <pre className="text-[11px] text-text-primary font-mono whitespace-pre-wrap break-all bg-surface-2 rounded-lg px-3 py-2 overflow-x-auto">
                  {snippet}
                </pre>
              </div>
            )}
          </section>
        )}

        <section className="bg-surface-1 border border-border-subtle rounded-xl p-5 space-y-4">
          <div>
            <h2 className="text-xs font-semibold text-text-primary">
              Verify signature
            </h2>
            <p className="text-[10px] text-text-muted mt-1">
              Check a signature against the admin server&apos;s trusted keys.
            </p>
          </div>

          <div>
            <label className="block text-[11px] text-text-secondary mb-1">
              round_id
            </label>
            <input
              value={verifyRoundId}
              onChange={(e) => setVerifyRoundId(e.target.value.trim())}
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
              value={verifyEaPK}
              onChange={(e) => setVerifyEaPK(e.target.value.trim())}
              placeholder="base64 32-byte EA public key"
              spellCheck={false}
              className="w-full px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary placeholder:text-text-muted focus:outline-none focus:border-accent/50 font-mono"
            />
          </div>

          <div>
            <label className="block text-[11px] text-text-secondary mb-1">
              signature
            </label>
            <input
              value={verifySignature}
              onChange={(e) => setVerifySignature(e.target.value)}
              placeholder="base64 64-byte Ed25519 signature"
              spellCheck={false}
              className="w-full px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary placeholder:text-text-muted focus:outline-none focus:border-accent/50 font-mono"
            />
          </div>

          <button
            onClick={handleVerify}
            disabled={!canVerify || verifying}
            className="px-3 py-2 bg-accent/90 hover:bg-accent text-surface-0 rounded-lg text-[11px] font-semibold transition-colors cursor-pointer disabled:opacity-50"
          >
            {verifying ? "Verifying..." : "Verify signature"}
          </button>

          {verifyError && (
            <div className="flex items-start gap-2 bg-danger/10 border border-danger/30 rounded-lg p-3">
              <AlertCircle size={14} className="text-danger mt-0.5 shrink-0" />
              <p className="text-[11px] text-danger">{verifyError}</p>
            </div>
          )}

          {verifyResult && (
            <div
              className={`rounded-lg border p-3 ${
                verifyResult.ok
                  ? "bg-success/10 border-success/30"
                  : "bg-danger/10 border-danger/30"
              }`}
            >
              <p
                className={`text-[11px] font-semibold ${
                  verifyResult.ok ? "text-success" : "text-danger"
                }`}
              >
                {verifyResult.ok
                  ? `Valid signature${verifyResult.key_id ? ` from ${verifyResult.key_id}` : ""}`
                  : "Invalid signature"}
              </p>
              {verifyResult.error && (
                <p className="text-[10px] text-text-secondary mt-1">
                  {verifyResult.error}
                </p>
              )}
              <p className="text-[10px] text-text-muted mt-2">signed_payload_hash</p>
              <p className="text-[11px] text-text-primary font-mono break-all">
                {verifyResult.signed_payload_hash}
              </p>
            </div>
          )}
        </section>
      </div>
    </div>
  );
}
