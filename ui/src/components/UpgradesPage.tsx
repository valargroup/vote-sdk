import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  AlertCircle,
  CalendarClock,
  CheckCircle2,
  Loader2,
  RefreshCw,
  Rocket,
  XCircle,
} from "lucide-react";
import * as chainApi from "../api/chain";
import * as cosmosTx from "../api/cosmosTx";
import type { UseWallet } from "../hooks/useWallet";
import {
  estimateUpgradeHeight,
  sampleHeightForWindow,
  type UpgradeHeightEstimate,
} from "../utils/upgradeEstimate";
import {
  createScheduleUpgradeReview,
  fetchCosmovisorReleaseBinaries,
  ReleaseRequestGate,
  releaseBinariesMap,
  REQUIRED_UPGRADE_PLATFORMS,
  validateScheduleUpgradeReview,
  validateUpgradeInfoJson,
  type ReleaseBinary,
  type ScheduleUpgradeReview,
  type UpgradePlatform,
} from "../utils/upgradeRelease";

const DEFAULT_AVERAGING_WINDOW = 50;
const MAX_UPGRADE_INFO_BYTES = 4096;

type ReviewAction =
  | { kind: "schedule"; review: ScheduleUpgradeReview }
  | { kind: "cancel" };

function pad2(value: number): string {
  return String(value).padStart(2, "0");
}

function toLocalDateTimeInput(date: Date): string {
  return [
    date.getFullYear(),
    "-",
    pad2(date.getMonth() + 1),
    "-",
    pad2(date.getDate()),
    "T",
    pad2(date.getHours()),
    ":",
    pad2(date.getMinutes()),
    ":",
    pad2(date.getSeconds()),
  ].join("");
}

function defaultTargetTime(): string {
  return toLocalDateTimeInput(new Date(Date.now() + 2 * 60 * 1000));
}

function formatDateTime(valueMs?: number): string {
  if (!valueMs || !Number.isFinite(valueMs)) return "-";
  return new Date(valueMs).toLocaleString();
}

function formatBlockTime(block: chainApi.LatestBlockInfo | null): string {
  if (!block?.timeMs) return "-";
  return `${block.height.toLocaleString()} at ${new Date(block.timeMs).toLocaleString()}`;
}

function parseTargetTimeMs(value: string): number {
  const ms = Date.parse(value);
  return Number.isFinite(ms) ? ms : 0;
}

function compactHash(hash: string): string {
  if (!hash) return "";
  return `${hash.slice(0, 12)}...`;
}

function InfoRow({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="min-w-0">
      <p className="mb-1 text-[10px] uppercase tracking-wider text-text-muted">{label}</p>
      <div className="min-h-[18px] break-words text-[11px] text-text-secondary">{value}</div>
    </div>
  );
}

export function UpgradesPage({ wallet }: { wallet: UseWallet }) {
  const [latestBlock, setLatestBlock] = useState<chainApi.LatestBlockInfo | null>(null);
  const [sampleBlock, setSampleBlock] = useState<chainApi.LatestBlockInfo | null>(null);
  const [currentPlan, setCurrentPlan] = useState<chainApi.UpgradePlan | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState("");

  const [averagingWindow, setAveragingWindow] = useState(DEFAULT_AVERAGING_WINDOW);
  const [targetTime, setTargetTime] = useState(defaultTargetTime);
  const [planName, setPlanName] = useState("");
  const [releaseTag, setReleaseTag] = useState("");
  const [notes, setNotes] = useState("");
  const [replaceExisting, setReplaceExisting] = useState(false);
  const [releaseBinaries, setReleaseBinaries] = useState<ReleaseBinary[]>([]);
  const [selectedBinaryPlatforms, setSelectedBinaryPlatforms] = useState<UpgradePlatform[]>([]);
  const [releaseBinariesLoading, setReleaseBinariesLoading] = useState(false);
  const [releaseBinariesError, setReleaseBinariesError] = useState("");
  const releaseRequestGate = useRef(new ReleaseRequestGate());

  const [reviewAction, setReviewAction] = useState<ReviewAction | null>(null);
  const [actionBusy, setActionBusy] = useState(false);
  const [actionError, setActionError] = useState("");
  const [resultMsg, setResultMsg] = useState("");

  const endpoint = chainApi.getChainUrl();

  const refresh = useCallback(async (silent = false) => {
    if (!silent) setLoading(true);
    setLoadError("");
    try {
      const [latest, planResp] = await Promise.all([
        chainApi.getLatestBlock(),
        chainApi.getCurrentUpgradePlan(),
      ]);
      if (latest.height <= 1) {
        throw new Error("latest height is too low to sample block speed");
      }
      const sampleHeight = sampleHeightForWindow(latest.height, averagingWindow);
      if (sampleHeight >= latest.height) {
        throw new Error("averaging window does not leave an older sample block");
      }
      const sampled = await chainApi.getBlock(sampleHeight);
      setLatestBlock(latest);
      setSampleBlock(sampled);
      setCurrentPlan(planResp.plan);
      if (!planResp.plan) setReplaceExisting(false);
    } catch (err) {
      setLoadError(err instanceof Error ? err.message : String(err));
    } finally {
      if (!silent) setLoading(false);
    }
  }, [averagingWindow]);

  useEffect(() => {
    void refresh(false);
  }, [refresh]);

  const estimateState = useMemo<{
    estimate: UpgradeHeightEstimate | null;
    error: string;
    targetTimeMs: number;
  }>(() => {
    const targetTimeMs = parseTargetTimeMs(targetTime);
    if (!targetTimeMs) {
      return { estimate: null, error: "Choose a valid target date and time", targetTimeMs: 0 };
    }
    if (!latestBlock || !sampleBlock) {
      return { estimate: null, error: "", targetTimeMs };
    }
    try {
      return {
        estimate: estimateUpgradeHeight({
          latestHeight: latestBlock.height,
          latestTimeMs: latestBlock.timeMs,
          sampleHeight: sampleBlock.height,
          sampleTimeMs: sampleBlock.timeMs,
          targetTimeMs,
        }),
        error: "",
        targetTimeMs,
      };
    } catch (err) {
      return {
        estimate: null,
        error: err instanceof Error ? err.message : String(err),
        targetTimeMs,
      };
    }
  }, [latestBlock, sampleBlock, targetTime]);

  const selectedBinaries = useMemo(
    () => releaseBinariesMap(releaseBinaries, selectedBinaryPlatforms),
    [releaseBinaries, selectedBinaryPlatforms],
  );

  const infoJson = useMemo(() => {
    const targetTimeMs = estimateState.targetTimeMs;
    const payload: Record<string, unknown> = {
      requested_time: targetTimeMs ? new Date(targetTimeMs).toISOString() : null,
      requested_local_time: targetTime,
    };
    if (releaseTag.trim()) payload.tag = releaseTag.trim();
    if (notes.trim()) payload.notes = notes.trim();
    if (Object.keys(selectedBinaries).length > 0) payload.binaries = selectedBinaries;
    if (estimateState.estimate) {
      payload.estimated_height = estimateState.estimate.targetHeight;
      payload.average_seconds_per_block = Number(estimateState.estimate.averageSecondsPerBlock.toFixed(3));
      payload.averaging_window_blocks = estimateState.estimate.sampledBlocks;
      payload.latest_height = latestBlock?.height ?? null;
    }
    return JSON.stringify(payload, null, 2);
  }, [estimateState, latestBlock?.height, notes, releaseTag, selectedBinaries, targetTime]);

  const infoBytes = useMemo(() => new TextEncoder().encode(infoJson).length, [infoJson]);
  const infoError = useMemo(() => {
    if (infoBytes > MAX_UPGRADE_INFO_BYTES) {
      return `Info JSON is ${infoBytes} bytes; maximum is ${MAX_UPGRADE_INFO_BYTES}`;
    }
    if (!infoJson.trim()) return "Info JSON is required";
    return validateUpgradeInfoJson(infoJson, releaseTag);
  }, [infoBytes, infoJson, releaseTag]);

  const updateReleaseTag = (value: string) => {
    releaseRequestGate.current.invalidate();
    setReleaseTag(value);
    setReleaseBinaries([]);
    setSelectedBinaryPlatforms([]);
    setReleaseBinariesLoading(false);
    setReleaseBinariesError("");
  };

  const loadReleaseBinaries = async () => {
    const request = releaseRequestGate.current.begin();
    const requestedTag = releaseTag.trim();
    setReleaseBinariesLoading(true);
    setReleaseBinariesError("");
    try {
      const binaries = await fetchCosmovisorReleaseBinaries(requestedTag);
      if (!releaseRequestGate.current.isCurrent(request)) return;
      setReleaseBinaries(binaries);
      setSelectedBinaryPlatforms([...REQUIRED_UPGRADE_PLATFORMS]);
    } catch (err) {
      if (!releaseRequestGate.current.isCurrent(request)) return;
      setReleaseBinaries([]);
      setSelectedBinaryPlatforms([]);
      setReleaseBinariesError(err instanceof Error ? err.message : String(err));
    } finally {
      if (releaseRequestGate.current.isCurrent(request)) {
        setReleaseBinariesLoading(false);
      }
    }
  };

  const toggleBinaryPlatform = (platform: UpgradePlatform) => {
    setSelectedBinaryPlatforms((current) =>
      current.includes(platform)
        ? current.filter((candidate) => candidate !== platform)
        : REQUIRED_UPGRADE_PLATFORMS.filter(
            (candidate) => candidate === platform || current.includes(candidate),
          ),
    );
  };

  const planNameError = planName.trim() ? "" : "Enter the handler name registered in the future binary";
  const pendingPlanRequiresReplace = Boolean(currentPlan && !replaceExisting);
  const scheduleDisabled = Boolean(
    loading ||
    loadError ||
    estimateState.error ||
    !estimateState.estimate ||
    planNameError ||
    infoError ||
    releaseBinariesLoading ||
    pendingPlanRequiresReplace ||
    !wallet.signer ||
    actionBusy,
  );

  const startScheduleReview = () => {
    setResultMsg("");
    setActionError("");
    if (scheduleDisabled || !estimateState.estimate) return;
    setReviewAction({
      kind: "schedule",
      review: createScheduleUpgradeReview({
        planName,
        height: estimateState.estimate.targetHeight,
        infoJson,
        releaseTag,
        replaceExisting,
        estimatedTimeMs: estimateState.estimate.estimatedTimeMs,
        requestedTimeMs: estimateState.targetTimeMs,
      }),
    });
  };

  const startCancelReview = () => {
    setResultMsg("");
    setActionError("");
    if (!currentPlan || !wallet.signer || actionBusy) return;
    setReviewAction({ kind: "cancel" });
  };

  const confirmReviewAction = async () => {
    if (!reviewAction || !wallet.signer) return;
    if (reviewAction.kind === "schedule") {
      const reviewError = validateScheduleUpgradeReview(
        reviewAction.review,
        MAX_UPGRADE_INFO_BYTES,
      );
      if (reviewError) {
        setActionError(reviewError);
        return;
      }
    }
    setActionBusy(true);
    setActionError("");
    try {
      const base = chainApi.getApiBase();
      const result =
        reviewAction.kind === "schedule"
          ? await cosmosTx.scheduleUpgrade(base, wallet.signer, reviewAction.review.payload)
          : await cosmosTx.cancelUpgrade(base, wallet.signer);
      if (result.code !== 0) {
        setActionError(result.log || `tx failed with code ${result.code}`);
        return;
      }
      setResultMsg(`Coordinator action proposed to ${reviewAction.kind === "schedule" ? "schedule" : "cancel"} upgrade (${compactHash(result.tx_hash)})`);
      setReviewAction(null);
      await refresh(true);
    } catch (err) {
      setActionError(err instanceof Error ? err.message : String(err));
    } finally {
      setActionBusy(false);
    }
  };

  const currentPlanBlocksAway =
    currentPlan && latestBlock ? currentPlan.height - latestBlock.height : null;
  const selectedHeight = estimateState.estimate?.targetHeight ?? 0;
  const primaryButtonText = currentPlan ? "Replace upgrade plan" : "Schedule upgrade";

  return (
    <div className="flex-1 overflow-y-auto">
      <div className="mx-auto max-w-6xl px-6 py-12">
        <div className="mb-6 flex items-center justify-between gap-4">
          <div className="flex items-center gap-3">
            <div className="flex h-10 w-10 items-center justify-center rounded-xl bg-accent/15">
              <Rocket size={22} className="text-accent" />
            </div>
            <div>
              <h1 className="text-lg font-bold text-text-primary">Upgrades</h1>
              <p className="text-[11px] text-text-muted">
                Schedule coordinated chain halts through the vote-manager upgrade message.
              </p>
            </div>
          </div>
          <button
            type="button"
            onClick={() => void refresh(false)}
            className="rounded-lg p-2 text-text-muted hover:bg-surface-3 hover:text-text-secondary"
            title="Refresh"
          >
            <RefreshCw size={14} className={loading ? "animate-spin" : ""} />
          </button>
        </div>

        {!wallet.address && (
          <div className="mb-4 flex items-center justify-between gap-3 rounded-lg border border-warning/30 bg-warning/10 p-3">
            <div className="flex items-center gap-2">
              <AlertCircle size={14} className="shrink-0 text-warning" />
              <p className="text-[11px] text-text-secondary">Connect a vote-manager wallet to broadcast upgrade transactions.</p>
            </div>
            <button
              type="button"
              onClick={() => void wallet.connect()}
              disabled={wallet.connecting}
              className="rounded-md bg-accent/90 px-3 py-1.5 text-[11px] font-semibold text-surface-0 hover:bg-accent disabled:opacity-50"
            >
              {wallet.connecting ? "Connecting..." : "Connect Keplr"}
            </button>
          </div>
        )}

        {wallet.error && (
          <div className="mb-4 flex items-center gap-2 rounded-lg border border-danger/30 bg-danger/10 p-3">
            <AlertCircle size={14} className="shrink-0 text-danger" />
            <p className="text-[11px] text-danger">{wallet.error}</p>
          </div>
        )}

        {loadError && (
          <div className="mb-4 flex items-center gap-2 rounded-lg border border-danger/30 bg-danger/10 p-3">
            <AlertCircle size={14} className="shrink-0 text-danger" />
            <p className="text-[11px] text-danger">{loadError}</p>
          </div>
        )}

        {resultMsg && (
          <div className="mb-4 flex items-center gap-2 rounded-lg border border-success/30 bg-success/10 p-3">
            <CheckCircle2 size={14} className="shrink-0 text-success" />
            <p className="text-[11px] text-success">{resultMsg}</p>
          </div>
        )}

        <div className="mb-6 grid gap-4 lg:grid-cols-2">
          <section className="min-w-0 rounded-xl border border-border-subtle bg-surface-1 p-5">
            <div className="mb-4 flex items-center justify-between">
              <h2 className="text-xs font-semibold text-text-primary">Chain status</h2>
              {loading ? <Loader2 size={14} className="animate-spin text-text-muted" /> : null}
            </div>
            <div className="grid gap-4 sm:grid-cols-2">
              <InfoRow label="Endpoint" value={<span className="font-mono">{endpoint}</span>} />
              <InfoRow label="Latest block" value={formatBlockTime(latestBlock)} />
              <InfoRow label="Sample block" value={formatBlockTime(sampleBlock)} />
              <InfoRow
                label="Average speed"
                value={
                  estimateState.estimate
                    ? `${estimateState.estimate.averageSecondsPerBlock.toFixed(3)} sec/block`
                    : "-"
                }
              />
            </div>
          </section>

          <section className="min-w-0 rounded-xl border border-border-subtle bg-surface-1 p-5">
            <div className="mb-4 flex items-center justify-between">
              <h2 className="text-xs font-semibold text-text-primary">Current plan</h2>
              {currentPlan ? (
                <span className="rounded-md bg-warning/15 px-2 py-1 text-[10px] font-semibold text-warning">
                  Pending
                </span>
              ) : (
                <span className="rounded-md bg-success/15 px-2 py-1 text-[10px] font-semibold text-success">
                  None
                </span>
              )}
            </div>
            {currentPlan ? (
              <div className="space-y-4">
                <div className="grid gap-4 sm:grid-cols-2">
                  <InfoRow label="Name" value={<span className="font-mono text-text-primary">{currentPlan.name}</span>} />
                  <InfoRow
                    label="Height"
                    value={`${currentPlan.height.toLocaleString()}${
                      currentPlanBlocksAway !== null ? ` (${currentPlanBlocksAway.toLocaleString()} blocks away)` : ""
                    }`}
                  />
                </div>
                {currentPlan.info ? (
                  <pre className="max-h-36 max-w-full overflow-auto whitespace-pre-wrap break-all rounded-lg border border-border-subtle bg-surface-2 p-3 text-[10px] text-text-secondary">
                    {currentPlan.info}
                  </pre>
                ) : null}
                <button
                  type="button"
                  onClick={startCancelReview}
                  disabled={!wallet.signer || actionBusy}
                  className="inline-flex items-center gap-1.5 rounded-md border border-danger/40 px-3 py-1.5 text-[11px] font-semibold text-danger hover:bg-danger/10 disabled:opacity-40"
                >
                  <XCircle size={12} /> Cancel plan
                </button>
              </div>
            ) : (
              <p className="text-[11px] text-text-muted">No upgrade plan is currently scheduled.</p>
            )}
          </section>
        </div>

        <section className="rounded-xl border border-border-subtle bg-surface-1 p-5">
          <div className="mb-5 flex items-center gap-2">
            <CalendarClock size={16} className="text-accent" />
            <h2 className="text-xs font-semibold text-text-primary">Schedule by date and time</h2>
          </div>

          <div className="grid gap-4 lg:grid-cols-[1fr_1fr]">
            <div className="space-y-4">
              <div>
                <label className="mb-1.5 block text-[11px] text-text-secondary">Handler name</label>
                <input
                  type="text"
                  value={planName}
                  onChange={(e) => setPlanName(e.target.value)}
                  placeholder="v1.2.3-upgrade"
                  className="w-full rounded-lg border border-border-subtle bg-surface-2 px-3 py-2 text-xs text-text-primary placeholder:text-text-muted focus:border-accent/50 focus:outline-none"
                />
                {planNameError && <p className="mt-1 text-[10px] text-warning">{planNameError}</p>}
              </div>

              <div className="grid gap-3 sm:grid-cols-2">
                <div>
                  <label className="mb-1.5 block text-[11px] text-text-secondary">Target time</label>
                  <input
                    type="datetime-local"
                    step={1}
                    value={targetTime}
                    onChange={(e) => setTargetTime(e.target.value)}
                    className="w-full rounded-lg border border-border-subtle bg-surface-2 px-3 py-2 text-xs text-text-primary focus:border-accent/50 focus:outline-none [color-scheme:dark]"
                  />
                </div>
                <div>
                  <label className="mb-1.5 block text-[11px] text-text-secondary">Averaging window</label>
                  <input
                    type="number"
                    min={1}
                    max={500}
                    step={1}
                    value={averagingWindow}
                    onChange={(e) => setAveragingWindow(Math.max(1, Number(e.target.value) || DEFAULT_AVERAGING_WINDOW))}
                    className="w-full rounded-lg border border-border-subtle bg-surface-2 px-3 py-2 text-xs text-text-primary focus:border-accent/50 focus:outline-none"
                  />
                </div>
              </div>

              <div className="grid gap-3 sm:grid-cols-2">
                <div>
                  <label className="mb-1.5 block text-[11px] text-text-secondary">Release tag</label>
                  <input
                    type="text"
                    value={releaseTag}
                    onChange={(e) => updateReleaseTag(e.target.value)}
                    placeholder="v1.2.3"
                    className="w-full rounded-lg border border-border-subtle bg-surface-2 px-3 py-2 text-xs text-text-primary placeholder:text-text-muted focus:border-accent/50 focus:outline-none"
                  />
                </div>
                <div>
                  <label className="mb-1.5 block text-[11px] text-text-secondary">Notes</label>
                  <input
                    type="text"
                    value={notes}
                    onChange={(e) => setNotes(e.target.value)}
                    placeholder="operator note"
                    className="w-full rounded-lg border border-border-subtle bg-surface-2 px-3 py-2 text-xs text-text-primary placeholder:text-text-muted focus:border-accent/50 focus:outline-none"
                  />
                </div>
              </div>

              <div className="rounded-lg border border-border-subtle bg-surface-2 p-3">
                <div className="flex items-start justify-between gap-3">
                  <div>
                    <p className="text-[11px] font-semibold text-text-secondary">Cosmovisor binaries</p>
                    <p className="mt-1 text-[10px] text-text-muted">
                      Load release archives and SHA-256 digests from GitHub. Both Linux platforms are required.
                    </p>
                  </div>
                  <button
                    type="button"
                    onClick={() => void loadReleaseBinaries()}
                    disabled={!releaseTag.trim() || releaseBinariesLoading}
                    className="inline-flex shrink-0 items-center gap-1.5 rounded-md border border-accent/40 px-2.5 py-1.5 text-[10px] font-semibold text-accent hover:bg-accent/10 disabled:opacity-40"
                  >
                    {releaseBinariesLoading ? <Loader2 size={11} className="animate-spin" /> : null}
                    Load from release
                  </button>
                </div>

                {releaseBinaries.length > 0 ? (
                  <div className="mt-3 space-y-2">
                    {releaseBinaries.map((binary) => (
                      <label
                        key={binary.platform}
                        className="flex gap-2 rounded-md border border-border-subtle bg-surface-1 px-2.5 py-2"
                      >
                        <input
                          type="checkbox"
                          checked={selectedBinaryPlatforms.includes(binary.platform)}
                          onChange={() => toggleBinaryPlatform(binary.platform)}
                          className="mt-0.5 h-3.5 w-3.5 shrink-0 accent-accent"
                        />
                        <span className="min-w-0">
                          <span className="block font-mono text-[10px] text-text-primary">{binary.platform}</span>
                          <span className="block truncate text-[9px] text-text-muted" title={binary.assetName}>
                            {binary.assetName}
                          </span>
                          <span className="block break-all font-mono text-[9px] text-text-muted">{binary.digest}</span>
                        </span>
                      </label>
                    ))}
                  </div>
                ) : null}

                {releaseBinariesError ? (
                  <p className="mt-2 text-[10px] text-danger">{releaseBinariesError}</p>
                ) : null}
              </div>

              {currentPlan && (
                <label className="flex items-center gap-2 rounded-lg border border-warning/30 bg-warning/10 px-3 py-2 text-[11px] text-text-secondary">
                  <input
                    type="checkbox"
                    checked={replaceExisting}
                    onChange={(e) => setReplaceExisting(e.target.checked)}
                    className="h-3.5 w-3.5 accent-accent"
                  />
                  Replace the pending plan named <span className="font-mono text-text-primary">{currentPlan.name}</span>
                </label>
              )}
            </div>

            <div className="space-y-4">
              <div className="grid gap-3 rounded-lg border border-border-subtle bg-surface-2 p-4 sm:grid-cols-2">
                <InfoRow
                  label="Selected height"
                  value={selectedHeight ? <span className="font-mono text-text-primary">{selectedHeight.toLocaleString()}</span> : "-"}
                />
                <InfoRow
                  label="Estimated time"
                  value={estimateState.estimate ? formatDateTime(estimateState.estimate.estimatedTimeMs) : "-"}
                />
                <InfoRow
                  label="Blocks from tip"
                  value={estimateState.estimate ? estimateState.estimate.blocksUntilTarget.toLocaleString() : "-"}
                />
                <InfoRow
                  label="Requested time"
                  value={estimateState.targetTimeMs ? formatDateTime(estimateState.targetTimeMs) : "-"}
                />
              </div>
              {estimateState.error && (
                <div className="flex items-center gap-2 rounded-lg border border-danger/30 bg-danger/10 p-3">
                  <AlertCircle size={14} className="shrink-0 text-danger" />
                  <p className="text-[11px] text-danger">{estimateState.error}</p>
                </div>
              )}

              <div>
                <label className="mb-1.5 block text-[11px] text-text-secondary">Info JSON</label>
                <textarea
                  value={infoJson}
                  readOnly
                  rows={9}
                  spellCheck={false}
                  className="w-full resize-y rounded-lg border border-border-subtle bg-surface-2 px-3 py-2 font-mono text-[11px] text-text-primary focus:outline-none"
                />
                <div className="mt-1 flex items-center justify-between text-[10px]">
                  <span className={infoError ? "text-danger" : "text-text-muted"}>
                    {infoError || "Valid JSON with checksum-pinned binaries"}
                  </span>
                  <span className="text-text-muted">{infoBytes}/{MAX_UPGRADE_INFO_BYTES} bytes</span>
                </div>
                <p className="mt-1 text-[9px] text-text-muted">
                  This exact read-only JSON is signed and regenerates from the fields and selected binaries above.
                </p>
              </div>
            </div>
          </div>

          {pendingPlanRequiresReplace && (
            <div className="mt-4 flex items-center gap-2 rounded-lg border border-warning/30 bg-warning/10 p-3">
              <AlertCircle size={14} className="shrink-0 text-warning" />
              <p className="text-[11px] text-text-secondary">Enable replace existing before scheduling over the current plan.</p>
            </div>
          )}

          <div className="mt-5 flex items-center justify-end gap-3">
            <button
              type="button"
              onClick={startScheduleReview}
              disabled={scheduleDisabled}
              className="inline-flex items-center gap-1.5 rounded-md bg-accent/90 px-3 py-2 text-[11px] font-semibold text-surface-0 hover:bg-accent disabled:opacity-40"
            >
              <Rocket size={12} />
              {primaryButtonText}
            </button>
          </div>
        </section>
      </div>

      {reviewAction && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 px-4">
          <div className="w-full max-w-2xl rounded-xl border border-border bg-surface-1 p-5 shadow-2xl">
            <div className="mb-4 flex items-start justify-between gap-4">
              <div>
                <h2 className="text-sm font-semibold text-text-primary">
                  {reviewAction.kind === "schedule"
                    ? reviewAction.review.payload.replaceExisting
                      ? "Replace upgrade plan"
                      : "Schedule upgrade"
                    : "Cancel upgrade plan"}
                </h2>
                <p className="mt-1 text-[11px] text-text-muted">Review the transaction before signing.</p>
              </div>
              <button
                type="button"
                onClick={() => setReviewAction(null)}
                disabled={actionBusy}
                className="rounded-md p-1 text-text-muted hover:bg-surface-2"
              >
                <XCircle size={16} />
              </button>
            </div>

            <div className="mb-4 grid gap-3 rounded-lg border border-border-subtle bg-surface-2 p-4 text-[11px]">
              <InfoRow label="Endpoint" value={<span className="font-mono">{endpoint}</span>} />
              <InfoRow label="Signer" value={<span className="font-mono">{wallet.address}</span>} />
              {reviewAction.kind === "schedule" ? (
                <>
                  <InfoRow label="Plan name" value={<span className="font-mono text-text-primary">{reviewAction.review.payload.name}</span>} />
                  <InfoRow label="Height" value={<span className="font-mono text-text-primary">{reviewAction.review.payload.height.toLocaleString()}</span>} />
                  <InfoRow label="Estimated time" value={formatDateTime(reviewAction.review.estimatedTimeMs)} />
                  <InfoRow label="Requested time" value={formatDateTime(reviewAction.review.requestedTimeMs)} />
                  <InfoRow label="Replace existing" value={reviewAction.review.payload.replaceExisting ? "Yes" : "No"} />
                </>
              ) : currentPlan ? (
                <>
                  <InfoRow label="Plan name" value={<span className="font-mono text-text-primary">{currentPlan.name}</span>} />
                  <InfoRow label="Height" value={<span className="font-mono text-text-primary">{currentPlan.height.toLocaleString()}</span>} />
                </>
              ) : null}
            </div>

            {reviewAction.kind === "schedule" ? (
              <div className="mb-4">
                <p className="mb-1.5 text-[10px] uppercase tracking-wider text-text-muted">Signed info JSON</p>
                <pre className="max-h-52 overflow-auto whitespace-pre-wrap break-all rounded-lg border border-border-subtle bg-surface-2 p-3 font-mono text-[10px] text-text-secondary">
                  {reviewAction.review.payload.info}
                </pre>
              </div>
            ) : null}

            {actionError && (
              <div className="mb-4 flex items-center gap-2 rounded-lg border border-danger/30 bg-danger/10 p-3">
                <AlertCircle size={14} className="shrink-0 text-danger" />
                <p className="text-[11px] text-danger">{actionError}</p>
              </div>
            )}

            <div className="flex justify-end gap-2">
              <button
                type="button"
                onClick={() => setReviewAction(null)}
                disabled={actionBusy}
                className="rounded-md border border-border-subtle px-3 py-2 text-[11px] text-text-secondary hover:bg-surface-2 disabled:opacity-40"
              >
                Back
              </button>
              <button
                type="button"
                onClick={() => void confirmReviewAction()}
                disabled={actionBusy}
                className={`inline-flex items-center gap-1.5 rounded-md px-3 py-2 text-[11px] font-semibold disabled:opacity-40 ${
                  reviewAction.kind === "cancel"
                    ? "bg-danger/90 text-white hover:bg-danger"
                    : "bg-accent/90 text-surface-0 hover:bg-accent"
                }`}
              >
                {actionBusy ? <Loader2 size={12} className="animate-spin" /> : null}
                {reviewAction.kind === "schedule" ? "Sign and broadcast" : "Cancel plan"}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
