import { useState } from "react";
import {
  AlertCircle,
  AlertTriangle,
  ExternalLink,
  Loader2,
  PackageCheck,
} from "lucide-react";
import { useWallet } from "../hooks/useWallet";
import { useDetectedChainId } from "../hooks/useDetectedChainId";
import { useUIConfig } from "../store/uiConfigContext";
import { deriveEd25519FromKeplr } from "../api/votingKey";
import { createPIRUpdateProposal, createPIRUpdatePR } from "../api/chain";
import {
  signPIRProposal,
  validatePIRProposal,
  type PIRProposal,
} from "../utils/pirUpdate";
import {
  resolvePIRUpdateScope,
  PIR_SCOPE_LABELS,
} from "../utils/pirUpdateScope";
import { CopyButton } from "./CopyButton";

const INPUT_CLASS =
  "w-full px-3 py-2 rounded-lg bg-surface-2 border border-border text-xs text-text-primary placeholder:text-text-muted focus:outline-none focus:border-accent/50 font-mono";

const PRIMARY_BUTTON_CLASS =
  "px-3 py-2 bg-accent/90 hover:bg-accent text-surface-0 rounded-lg text-[11px] font-semibold transition-colors cursor-pointer disabled:opacity-50 disabled:cursor-default inline-flex items-center gap-1.5";

export function PIRUpdatePage() {
  const wallet = useWallet();
  const detectedChainId = useDetectedChainId();
  const { zcashNetwork } = useUIConfig();

  // The operator never picks the environment. svoted derives the scope itself
  // and echoes it on every proposal; this resolves it independently from the
  // chain id only so the page can name its environment up front.
  const scopeState = resolvePIRUpdateScope(wallet.chainId || detectedChainId);
  const scope = scopeState.status === "ready" ? scopeState.scope : null;

  const [tag, setTag] = useState("");
  const [height, setHeight] = useState("");
  const [proposal, setProposal] = useState<PIRProposal | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [pr, setPR] = useState("");

  function resetResults() {
    setProposal(null);
    setPR("");
  }

  async function prepare() {
    if (!scope) return;
    setBusy(true);
    setError("");
    resetResults();
    try {
      const parsedHeight = Number(height);
      if (
        !Number.isSafeInteger(parsedHeight) ||
        parsedHeight <= 0 ||
        parsedHeight % 10 !== 0 ||
        !/^v[0-9A-Za-z.+-]{1,127}$/.test(tag)
      ) {
        throw new Error(
          "Enter an explicit release tag and a positive snapshot height divisible by 10."
        );
      }
      const result = await createPIRUpdateProposal<PIRProposal>({
        schema_version: 1,
        binary_tag: tag,
        snapshot_height: parsedHeight,
      });
      await validatePIRProposal(result, scope, tag, parsedHeight);
      setProposal(result);
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  }

  async function sign() {
    if (!proposal || !scope) return;
    setBusy(true);
    setError("");
    try {
      await validatePIRProposal(proposal, scope, tag, Number(height));
      if (!wallet.address || wallet.source !== "keplr" || !wallet.chainId) {
        throw new Error("Connect the coordinator Keplr wallet first.");
      }
      // Each environment pins its own coordinator key, derived on that
      // environment's own chain, so the connected chain is always the right
      // one to derive against.
      const key = await deriveEd25519FromKeplr(
        wallet.address,
        wallet.chainId,
        wallet.signKeplrPayload
      );
      const attestations = await signPIRProposal(proposal, key);
      const result = await createPIRUpdatePR({
        base_sha: proposal.base_sha,
        config: proposal.config,
        attestations,
      });
      setPR(result.html_url);
    } catch (e) {
      setError(String(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="flex-1 overflow-y-auto">
      <div className="mx-auto max-w-3xl px-6 py-12 space-y-6">
        <div className="flex items-center gap-3">
          <div className="flex h-10 w-10 items-center justify-center rounded-xl bg-accent/15">
            <PackageCheck size={22} className="text-accent" />
          </div>
          <div>
            <h1 className="text-lg font-bold text-text-primary">
              Authorize PIR update
            </h1>
            <p className="text-[11px] text-text-muted">
              Select a published binary and snapshot. Your coordinator signature
              authorizes enrolled PIR hosts to install them once the pull
              request is merged.
            </p>
          </div>
        </div>

        {scopeState.status === "loading" && (
          <div className="flex items-center gap-2 rounded-xl border border-border-subtle bg-surface-1 p-5 text-[11px] text-text-muted">
            <Loader2 size={12} className="animate-spin" />
            Detecting the chain this dashboard is connected to…
          </div>
        )}

        {scopeState.status === "unknown" && (
          <div className="flex items-start gap-2 rounded-lg border border-warning/30 bg-warning/10 px-3 py-2.5">
            <AlertTriangle size={14} className="text-warning shrink-0 mt-0.5" />
            <div>
              <p className="text-xs font-semibold text-warning">
                Unrecognised chain — PIR updates are unavailable
              </p>
              <p className="mt-0.5 text-[10px] text-text-muted">
                This dashboard reports chain{" "}
                <code className="font-mono">{scopeState.chainId}</code>, which
                maps to neither the production nor the staging PIR environment.
                The environment selects which pinned coordinator key may
                authorize an update, so it is never assumed.
              </p>
            </div>
          </div>
        )}

        {scopeState.status === "ready" && (
          <>
            <section className="bg-surface-1 border border-border-subtle rounded-xl p-5 space-y-4">
              <div className="flex items-center justify-between gap-3">
                <h2 className="text-xs font-semibold text-text-primary">
                  1 · Select the update target
                </h2>
                <span className="inline-flex items-center gap-1 rounded-full border border-accent/30 bg-accent/10 px-1.5 py-0.5 text-[9px] font-semibold text-accent">
                  {PIR_SCOPE_LABELS[scopeState.scope]} · {scopeState.chainId}
                  {zcashNetwork && ` · zcash ${zcashNetwork}`}
                </span>
              </div>

              <div className="space-y-3">
                <div>
                  <label
                    className="text-[11px] text-text-secondary"
                    htmlFor="pir-binary-tag"
                  >
                    Binary release tag
                  </label>
                  <input
                    id="pir-binary-tag"
                    disabled={busy}
                    value={tag}
                    placeholder="v…"
                    onChange={(e) => {
                      setTag(e.target.value);
                      resetResults();
                    }}
                    className={`${INPUT_CLASS} mt-1`}
                  />
                </div>

                <div>
                  <label
                    className="text-[11px] text-text-secondary"
                    htmlFor="pir-snapshot-height"
                  >
                    Published snapshot height
                  </label>
                  <input
                    id="pir-snapshot-height"
                    disabled={busy}
                    value={height}
                    inputMode="numeric"
                    placeholder="must be divisible by 10"
                    onChange={(e) => {
                      setHeight(e.target.value);
                      resetResults();
                    }}
                    className={`${INPUT_CLASS} mt-1`}
                  />
                </div>
              </div>

              <button
                type="button"
                disabled={busy}
                onClick={() => void prepare()}
                className={PRIMARY_BUTTON_CLASS}
              >
                {busy && <Loader2 size={12} className="animate-spin" />}
                {busy ? "Working…" : "Review update"}
              </button>
            </section>

            {proposal && (
              <section className="bg-surface-1 border border-border-subtle rounded-xl p-5 space-y-4">
                <h2 className="text-xs font-semibold text-text-primary">
                  2 · Review and sign
                </h2>

                {proposal.current_config && (
                  <div>
                    <p className="text-[10px] text-text-muted">
                      Current configuration
                    </p>
                    <pre className="mt-1 overflow-x-auto rounded-lg bg-surface-2 p-3 text-[10px] text-text-secondary">
                      {proposal.current_config}
                    </pre>
                  </div>
                )}

                <div>
                  <p className="text-[10px] text-text-muted">
                    Proposed configuration
                  </p>
                  <pre className="mt-1 overflow-x-auto rounded-lg bg-surface-2 p-3 text-[10px] text-text-primary">
                    {proposal.config}
                  </pre>
                </div>

                <div className="flex items-center gap-2">
                  <span className="text-[10px] text-text-muted">Base commit</span>
                  <code className="font-mono text-[10px] text-text-primary truncate">
                    {proposal.base_sha}
                  </code>
                  <CopyButton value={proposal.base_sha} label="Copy" />
                </div>

                <details className="group">
                  <summary className="cursor-pointer select-none text-[10px] text-text-muted hover:text-text-secondary">
                    Authenticated artifact hashes
                  </summary>
                  <div className="mt-2 space-y-1">
                    {Object.entries(proposal.payload).map(([name, digest]) => (
                      <div
                        key={name}
                        className="flex items-baseline justify-between gap-3 text-[10px] font-mono"
                      >
                        <span className="shrink-0 text-text-primary">{name}</span>
                        <span className="truncate text-text-muted">{digest}</span>
                      </div>
                    ))}
                  </div>
                </details>

                <div className="flex items-start gap-2 rounded-lg border border-warning/30 bg-warning/10 px-3 py-2.5">
                  <AlertTriangle
                    size={14}
                    className="text-warning shrink-0 mt-0.5"
                  />
                  <div>
                    <p className="text-xs font-semibold text-warning">
                      Confirm this snapshot suits the active voting round
                    </p>
                    <p className="mt-0.5 text-[10px] text-text-muted">
                      Previously signed updates stay valid and can select older
                      binaries or snapshots, so signing an older target permits
                      a downgrade. There is no expiry or replay counter.
                    </p>
                  </div>
                </div>

                <button
                  type="button"
                  disabled={busy || !!pr}
                  onClick={() => void sign()}
                  className={PRIMARY_BUTTON_CLASS}
                >
                  {busy && <Loader2 size={12} className="animate-spin" />}
                  Sign and create pull request
                </button>
              </section>
            )}
          </>
        )}

        {error && (
          <div
            role="alert"
            className="flex items-start gap-2 rounded-lg border border-danger/30 bg-danger/10 p-3"
          >
            <AlertCircle size={14} className="text-danger shrink-0 mt-0.5" />
            <p className="whitespace-pre-wrap text-[11px] text-danger">{error}</p>
          </div>
        )}

        {pr && (
          <a
            href={pr}
            target="_blank"
            rel="noreferrer"
            className="inline-flex items-center gap-1.5 rounded-lg border border-success/30 bg-success/10 px-3 py-2 text-[11px] font-semibold text-success transition-colors hover:bg-success/20"
          >
            <ExternalLink size={12} />
            Review signed update pull request
          </a>
        )}
      </div>
    </div>
  );
}
