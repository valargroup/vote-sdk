import { useState, useEffect, useCallback, useRef } from "react";
import { RefreshCw, AlertTriangle, CheckCircle2, Loader2, Database } from "lucide-react";
import * as chainApi from "../api/chain";
import type { SnapshotStatus } from "../api/chain";

const NU5_ACTIVATION = 1_687_104;

export function SnapshotSettingsPage() {
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

  // Initial load
  useEffect(() => {
    fetchStatus();
    fetchActiveRound();
  }, [fetchStatus, fetchActiveRound]);

  // Poll during rebuild
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
      // Start polling
      fetchStatus();
    } catch (err) {
      setRebuildError(err instanceof Error ? err.message : "Failed to start rebuild");
      setRebuilding(false);
    }
  }, [targetHeight, fetchStatus]);

  const isServing = status?.phase === "serving";
  const isRebuildingPhase = status?.phase === "rebuilding";
  const isError = status?.phase === "error";

  // Inline validation for target height
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
    <div className="flex-1 overflow-y-auto">
      <div className="max-w-2xl mx-auto p-8">
        <div className="flex items-center gap-3 mb-6">
          <Database size={20} className="text-accent" />
          <h1 className="text-lg font-semibold text-text-primary">Snapshot Settings</h1>
        </div>

        {/* Current Status Card */}
        <div className="bg-surface-1 border border-border rounded-xl p-5 mb-6">
          <div className="flex items-center justify-between mb-3">
            <h2 className="text-sm font-medium text-text-primary">Current Status</h2>
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

        {/* Change Height Section */}
        <div className="bg-surface-1 border border-border rounded-xl p-5">
          <h2 className="text-sm font-medium text-text-primary mb-3">Change Snapshot Height</h2>

          {activeRound && (
            <div className="flex items-start gap-2 mb-4 px-3 py-2.5 bg-danger/10 border border-danger/30 rounded-lg">
              <AlertTriangle size={14} className="text-danger shrink-0 mt-0.5" />
              <div>
                <p className="text-xs text-danger font-semibold">Active voting round detected</p>
                <p className="text-[10px] text-danger/80 mt-0.5">
                  Rebuilding the snapshot will make the PIR server unavailable — voters will be unable
                  to cast votes during this time (~5–10 minutes). Proceed with caution.
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
      </div>
    </div>
  );
}
