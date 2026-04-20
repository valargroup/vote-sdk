import { useState, useEffect, useCallback, useRef } from "react";
import {
  RefreshCw,
  AlertTriangle,
  CheckCircle2,
  Loader2,
  Database,
  Cloud,
  Wrench,
} from "lucide-react";
import * as chainApi from "../api/chain";
import type { SnapshotStatus, PublishedSnapshotManifest } from "../api/chain";
import { useUIConfig } from "../store/uiConfigContext";

const NU5_ACTIVATION = 1_687_104;

function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  if (n < 1024 * 1024) return `${(n / 1024).toFixed(1)} KiB`;
  if (n < 1024 * 1024 * 1024) return `${(n / 1024 / 1024).toFixed(1)} MiB`;
  return `${(n / 1024 / 1024 / 1024).toFixed(2)} GiB`;
}

function formatTimestamp(iso: string): string {
  try {
    const d = new Date(iso);
    if (isNaN(d.getTime())) return iso;
    return d.toLocaleString(undefined, {
      year: "numeric",
      month: "short",
      day: "numeric",
      hour: "2-digit",
      minute: "2-digit",
      timeZoneName: "short",
    });
  } catch {
    return iso;
  }
}

// Card showing the published snapshot manifest from the configured CDN base.
// Always rendered (prod + dev). Independent of whether the local PIR server
// is up — this is the operator's view of "what's the canonical published
// snapshot the entire fleet should converge on?".
//
// Two sources are combined:
//   * snapshot_height comes from the published wallet-facing voting-config —
//     it's what wallets/iOS will validate the chain round against.
//   * precomputed_base_url comes from THIS svoted's /api/ui-config — it's a
//     deployment-level concern (staging svoted points at a staging bucket)
//     rather than a wallet-facing one.
function PublishedSnapshotCard() {
  const {
    publishedConfig,
    publishedConfigLoaded,
    refreshPublishedConfig,
    precomputedBaseURL,
  } = useUIConfig();
  const [manifest, setManifest] = useState<PublishedSnapshotManifest | null>(
    null
  );
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);

  const precomputedBase = precomputedBaseURL ?? null;
  const height = publishedConfig?.snapshot_height ?? null;

  const fetchManifest = useCallback(async () => {
    if (!precomputedBase || height == null) return;
    setLoading(true);
    setError(null);
    try {
      const m = await chainApi.getPublishedSnapshotManifest(precomputedBase, height);
      setManifest(m);
    } catch (err) {
      setError(err instanceof Error ? err.message : "Failed to fetch manifest");
      setManifest(null);
    } finally {
      setLoading(false);
    }
  }, [precomputedBase, height]);

  useEffect(() => {
    fetchManifest();
  }, [fetchManifest]);

  const totalBytes = manifest
    ? Object.values(manifest.files).reduce((sum, f) => sum + f.size, 0)
    : 0;

  return (
    <div className="bg-surface-1 border border-border rounded-xl p-5 mb-6">
      <div className="flex items-center justify-between mb-3">
        <div className="flex items-center gap-2">
          <Cloud size={14} className="text-accent" />
          <h2 className="text-sm font-medium text-text-primary">
            Published snapshot
          </h2>
        </div>
        <button
          onClick={async () => {
            await refreshPublishedConfig();
            await fetchManifest();
          }}
          className="p-1 text-text-muted hover:text-text-secondary cursor-pointer"
          title="Refresh"
        >
          <RefreshCw size={14} className={loading ? "animate-spin" : ""} />
        </button>
      </div>

      {!publishedConfigLoaded && (
        <p className="text-xs text-text-muted">Loading published config…</p>
      )}

      {publishedConfigLoaded && (!precomputedBase || height == null) && (
        <div className="flex items-start gap-2 px-3 py-2.5 bg-warning/10 border border-warning/30 rounded-lg">
          <AlertTriangle size={14} className="text-warning shrink-0 mt-0.5" />
          <div>
            <p className="text-xs text-warning font-semibold">
              No published snapshot declared
            </p>
            <p className="text-[10px] text-text-muted mt-0.5">
              {height == null && (
                <>
                  The voting-config is missing{" "}
                  <code className="font-mono">snapshot_height</code>.{" "}
                </>
              )}
              {!precomputedBase && (
                <>
                  This svoted has no{" "}
                  <code className="font-mono">SVOTE_PRECOMPUTED_BASE_URL</code>{" "}
                  resolved.{" "}
                </>
              )}
              PIR servers cannot bootstrap from CDN until both are set.
            </p>
          </div>
        </div>
      )}

      {precomputedBase && height != null && (
        <div className="space-y-3">
          <div className="grid grid-cols-2 gap-x-4 gap-y-2">
            <div>
              <p className="text-[10px] text-text-muted">Height</p>
              <p className="text-xs text-text-primary font-mono">
                {height.toLocaleString()}
              </p>
            </div>
            <div>
              <p className="text-[10px] text-text-muted">CDN base</p>
              <p
                className="text-xs text-text-primary font-mono truncate"
                title={`${precomputedBase}${chainApi.PIR_SNAPSHOTS_PATH}`}
              >
                {precomputedBase}
                <span className="text-text-muted">{chainApi.PIR_SNAPSHOTS_PATH}</span>
              </p>
            </div>
            {manifest && (
              <>
                <div>
                  <p className="text-[10px] text-text-muted">Created</p>
                  <p className="text-xs text-text-primary">
                    {formatTimestamp(manifest.created_at)}
                  </p>
                </div>
                <div>
                  <p className="text-[10px] text-text-muted">Total size</p>
                  <p className="text-xs text-text-primary font-mono">
                    {formatBytes(totalBytes)}
                  </p>
                </div>
                {manifest.publisher?.git_sha && (
                  <div className="col-span-2">
                    <p className="text-[10px] text-text-muted">Publisher</p>
                    <p
                      className="text-xs text-text-primary font-mono truncate"
                      title={`${manifest.publisher.git_ref ?? ""} @ ${manifest.publisher.git_sha}`}
                    >
                      {manifest.publisher.git_ref ?? "?"} @{" "}
                      {manifest.publisher.git_sha.slice(0, 12)}
                    </p>
                  </div>
                )}
              </>
            )}
          </div>

          {manifest && (
            <details className="group">
              <summary className="text-[10px] text-text-muted cursor-pointer hover:text-text-secondary select-none">
                Files (sha256)
              </summary>
              <div className="mt-2 space-y-1">
                {Object.entries(manifest.files).map(([name, f]) => (
                  <div
                    key={name}
                    className="flex items-baseline justify-between gap-3 text-[10px] font-mono"
                  >
                    <span className="text-text-primary shrink-0">{name}</span>
                    <span className="text-text-muted truncate" title={f.sha256}>
                      {f.sha256.slice(0, 12)}…
                    </span>
                    <span className="text-text-muted shrink-0">
                      {formatBytes(f.size)}
                    </span>
                  </div>
                ))}
              </div>
            </details>
          )}

          {manifest && (
            <div className="flex items-center gap-2 px-3 py-2 bg-success/10 border border-success/30 rounded-lg">
              <CheckCircle2 size={14} className="text-success shrink-0" />
              <p className="text-xs text-success">
                Manifest reachable. PIR servers will bootstrap from this on next
                start.
              </p>
            </div>
          )}

          {error && (
            <div className="flex items-start gap-2 px-3 py-2 bg-danger/10 border border-danger/30 rounded-lg">
              <AlertTriangle size={14} className="text-danger shrink-0 mt-0.5" />
              <div>
                <p className="text-xs text-danger font-semibold">
                  Manifest unreachable
                </p>
                <p className="text-[10px] text-danger/80 mt-0.5">{error}</p>
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  );
}

// Card with the live PIR server status. Always shown, but the rebuild
// controls inside it are gated on uiMode === 'dev' so a misconfigured prod
// cannot trigger an in-process rebuild.
function PIRStatusCard({ devControls }: { devControls: boolean }) {
  const [status, setStatus] = useState<SnapshotStatus | null>(null);
  const [statusError, setStatusError] = useState<string | null>(null);
  const [targetHeight, setTargetHeight] = useState("");
  const [activeRound, setActiveRound] = useState<boolean>(false);
  const [rebuilding, setRebuilding] = useState(false);
  const [rebuildError, setRebuildError] = useState<string | null>(null);
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const fetchStatus = useCallback(async () => {
    try {
      const s = await chainApi.getSnapshotStatus();
      setStatus(s);
      setStatusError(null);
      return s;
    } catch (err) {
      setStatusError(err instanceof Error ? err.message : "Failed to fetch status");
      return null;
    }
  }, []);

  const fetchActiveRound = useCallback(async () => {
    try {
      const resp = await chainApi.getActiveRound();
      setActiveRound(resp.round != null);
    } catch {
      setActiveRound(false);
    }
  }, []);

  useEffect(() => {
    const init = async () => {
      await fetchStatus();
      await fetchActiveRound();
    };
    init();
  }, [fetchStatus, fetchActiveRound]);

  useEffect(() => {
    if (status?.phase === "rebuilding") {
      if (!pollRef.current) {
        pollRef.current = setInterval(async () => {
          const s = await fetchStatus();
          if (s && s.phase !== "rebuilding") {
            if (pollRef.current) clearInterval(pollRef.current);
            pollRef.current = null;
            setRebuilding(false);
          }
        }, 3000);
      }
    } else {
      if (pollRef.current) {
        clearInterval(pollRef.current);
        pollRef.current = null;
      }
    }
    return () => {
      if (pollRef.current) {
        clearInterval(pollRef.current);
        pollRef.current = null;
      }
    };
  }, [status?.phase, fetchStatus]);

  const handleRebuild = useCallback(async () => {
    const height = parseInt(targetHeight, 10);
    if (isNaN(height) || height < NU5_ACTIVATION) {
      setRebuildError(`Height must be >= ${NU5_ACTIVATION.toLocaleString()} (NU5 activation)`);
      return;
    }
    if (height % 10 !== 0) {
      setRebuildError("Height must be a multiple of 10");
      return;
    }
    setRebuilding(true);
    setRebuildError(null);
    try {
      await chainApi.prepareSnapshot(height);
      fetchStatus();
    } catch (err) {
      setRebuildError(err instanceof Error ? err.message : "Failed to start rebuild");
      setRebuilding(false);
    }
  }, [targetHeight, fetchStatus]);

  const isServing = status?.phase === "serving";
  const isRebuildingPhase = status?.phase === "rebuilding";
  const isError = status?.phase === "error";

  const parsedHeight = parseInt(targetHeight, 10);
  const heightValid = !isNaN(parsedHeight) && parsedHeight >= NU5_ACTIVATION && parsedHeight % 10 === 0;
  const heightHint = targetHeight.length > 0 && !isNaN(parsedHeight)
    ? parsedHeight < NU5_ACTIVATION
      ? `Must be ≥ ${NU5_ACTIVATION.toLocaleString()}`
      : parsedHeight % 10 !== 0
        ? "Must be a multiple of 10"
        : null
    : null;

  return (
    <>
      {/* Current Status Card */}
      <div className="bg-surface-1 border border-border rounded-xl p-5 mb-6">
        <div className="flex items-center justify-between mb-3">
          <h2 className="text-sm font-medium text-text-primary">
            Local PIR server status
          </h2>
          <button
            onClick={fetchStatus}
            className="p-1 text-text-muted hover:text-text-secondary cursor-pointer"
            title="Refresh"
          >
            <RefreshCw size={14} className={isRebuildingPhase ? "animate-spin" : ""} />
          </button>
        </div>

        {statusError && (
          <div className="flex items-center gap-2 px-3 py-2 bg-danger/10 border border-danger/30 rounded-lg mb-3">
            <AlertTriangle size={14} className="text-danger shrink-0" />
            <p className="text-xs text-danger">{statusError}</p>
          </div>
        )}

        {status && (
          <div className="space-y-2">
            <div className="flex items-center gap-2">
              <span className="text-xs text-text-muted w-24">Phase:</span>
              <span className={`text-xs font-medium ${
                isServing ? "text-success" : isRebuildingPhase ? "text-accent" : "text-danger"
              }`}>
                {isServing && <><CheckCircle2 size={12} className="inline mr-1" />Serving</>}
                {isRebuildingPhase && <><Loader2 size={12} className="inline mr-1 animate-spin" />Rebuilding</>}
                {isError && <><AlertTriangle size={12} className="inline mr-1" />Error</>}
              </span>
            </div>
            <div className="flex items-center gap-2">
              <span className="text-xs text-text-muted w-24">Height:</span>
              <span className="text-xs text-text-primary font-mono">
                {status.height != null ? status.height.toLocaleString() : "—"}
              </span>
            </div>
            <div className="flex items-center gap-2">
              <span className="text-xs text-text-muted w-24">Ranges:</span>
              <span className="text-xs text-text-primary font-mono">
                {status.num_ranges != null ? status.num_ranges.toLocaleString() : "—"}
              </span>
            </div>

            {isRebuildingPhase && (
              <div className="mt-2 px-3 py-3 bg-accent/10 border border-accent/30 rounded-lg space-y-2">
                <div className="flex items-center gap-2">
                  <Loader2 size={14} className="text-accent animate-spin shrink-0" />
                  <div className="flex-1 min-w-0">
                    <p className="text-xs text-text-primary">
                      Rebuilding to height {status.target_height?.toLocaleString()}
                    </p>
                    <p className="text-[10px] text-text-muted mt-0.5">
                      {status.progress || "starting..."}
                    </p>
                  </div>
                  {status.progress_pct != null && (
                    <span className="text-xs text-accent font-mono shrink-0">
                      {status.progress_pct}%
                    </span>
                  )}
                </div>
                {status.progress_pct != null && (
                  <div className="w-full h-1.5 bg-surface-3 rounded-full overflow-hidden">
                    <div
                      className="h-full bg-accent rounded-full transition-all duration-500"
                      style={{ width: `${status.progress_pct}%` }}
                    />
                  </div>
                )}
                <p className="text-[10px] text-text-muted">
                  This typically takes 5–10 minutes.
                </p>
              </div>
            )}

            {isError && status.message && (
              <div className="flex items-start gap-2 mt-2 px-3 py-2 bg-danger/10 border border-danger/30 rounded-lg">
                <AlertTriangle size={14} className="text-danger shrink-0 mt-0.5" />
                <p className="text-xs text-danger">{status.message}</p>
              </div>
            )}
          </div>
        )}
      </div>

      {/* Dev-only rebuild controls */}
      {devControls && (
        <div className="bg-surface-1 border border-border rounded-xl p-5">
          <div className="flex items-center justify-between mb-3">
            <div className="flex items-center gap-2">
              <Wrench size={14} className="text-warning" />
              <h2 className="text-sm font-medium text-text-primary">
                Local PIR (dev) — rebuild snapshot
              </h2>
            </div>
            <span className="text-[10px] uppercase tracking-wide font-semibold text-warning bg-warning/10 border border-warning/30 px-2 py-0.5 rounded">
              dev only
            </span>
          </div>

          <p className="text-[10px] text-text-muted mb-3">
            Rebuilds this svoted's in-process PIR data on disk. Production
            servers should never use this — they bootstrap from the published
            snapshot above. Hidden when{" "}
            <code className="font-mono">SVOTE_UI_MODE</code> ≠ <code>dev</code>.
          </p>

          {activeRound && (
            <div className="flex items-start gap-2 mb-4 px-3 py-2.5 bg-danger/10 border border-danger/30 rounded-lg">
              <AlertTriangle size={14} className="text-danger shrink-0 mt-0.5" />
              <div>
                <p className="text-xs text-danger font-semibold">Active voting round detected</p>
                <p className="text-[10px] text-danger/80 mt-0.5">
                  Rebuilding at a different height will <strong>permanently break</strong> the active round —
                  the new tree root won't match the root committed on-chain, so all delegation proofs will
                  fail. To recover you must rebuild at the original snapshot height ({status?.height?.toLocaleString()}).
                  The PIR server will also be unavailable during the rebuild (~5–10 minutes).
                </p>
              </div>
            </div>
          )}

          <div className="space-y-3">
            <div>
              <div className="flex items-center justify-between mb-1">
                <label className="text-[11px] text-text-secondary">
                  Target height
                </label>
                {status?.zcash_tip && (
                  <span className="text-[10px] text-text-muted flex items-center gap-1">
                    Zcash tip: <span className="font-mono">{status.zcash_tip.toLocaleString()}</span>
                    <button
                      onClick={fetchStatus}
                      className="p-0.5 hover:text-text-secondary cursor-pointer"
                      title="Refresh"
                    >
                      <RefreshCw size={10} />
                    </button>
                  </span>
                )}
              </div>
              <div className="flex gap-2">
                <input
                  type="text"
                  inputMode="numeric"
                  value={targetHeight}
                  onChange={(e) => {
                    setTargetHeight(e.target.value.replace(/[^0-9]/g, ""));
                    setRebuildError(null);
                  }}
                  placeholder={`e.g. ${status?.height ? status.height.toLocaleString() : "2800000"}`}
                  disabled={isRebuildingPhase || rebuilding}
                  className="flex-1 px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary placeholder:text-text-muted focus:outline-none focus:border-accent/50 font-mono disabled:opacity-50"
                />
                <button
                  onClick={handleRebuild}
                  disabled={isRebuildingPhase || rebuilding || !heightValid}
                  className="px-4 py-2 bg-accent/90 hover:bg-accent text-surface-0 rounded-lg text-xs font-semibold transition-colors cursor-pointer disabled:opacity-50 disabled:cursor-default flex items-center gap-1.5"
                >
                  {(isRebuildingPhase || rebuilding) && (
                    <Loader2 size={12} className="animate-spin" />
                  )}
                  Rebuild
                </button>
              </div>

              {heightHint && (
                <p className="text-[10px] text-danger mt-1">{heightHint}</p>
              )}
            </div>

            {rebuildError && (
              <div className="flex items-center gap-2 px-3 py-2 bg-danger/10 border border-danger/30 rounded-lg">
                <AlertTriangle size={14} className="text-danger shrink-0" />
                <p className="text-xs text-danger">{rebuildError}</p>
              </div>
            )}

            <p className="text-[10px] text-text-muted">
              Must be ≥ {NU5_ACTIVATION.toLocaleString()} (NU5 activation) and a multiple of 10.
              If the target is below the current sync point, no re-ingestion is needed.
              If above, new blocks will be ingested first.
            </p>
          </div>
        </div>
      )}
    </>
  );
}

export function SnapshotSettingsPage() {
  const { devPIRControls, uiConfigLoaded } = useUIConfig();

  return (
    <div className="flex-1 overflow-y-auto">
      <div className="max-w-2xl mx-auto p-8">
        <div className="flex items-center gap-3 mb-6">
          <Database size={20} className="text-accent" />
          <h1 className="text-lg font-semibold text-text-primary">Snapshot Settings</h1>
        </div>

        <PublishedSnapshotCard />
        <PIRStatusCard devControls={uiConfigLoaded && devPIRControls} />
      </div>
    </div>
  );
}
