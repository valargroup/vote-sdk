import { useState, useCallback, useEffect, useRef } from "react";
import { X, Clock, AlertTriangle, ExternalLink, Loader2 } from "lucide-react";
import type { VotingRound, RoundSettings } from "../types";
import {
  useChainInfo,
  estimateTimestamp,
} from "../store/rpc";
import {
  getActiveRound,
  getSnapshotStatus,
  validatePublishedSnapshotManifest,
} from "../api/chain";
import type { PublishedSnapshotValidationResult } from "../api/chain";
import { useUIConfig } from "../store/uiConfigContext";

interface RoundEditorProps {
  round: VotingRound;
  onUpdateName: (name: string) => void;
  onUpdateSettings: (patch: Partial<RoundSettings>) => void;
  onNavigate?: (section: string) => void;
  isReadonly?: boolean;
}

type DurationPreset =
  | { label: string; minutes: number; days?: undefined }
  | { label: string; days: number; minutes?: undefined };

const DURATION_PRESETS: DurationPreset[] = [
  { label: "10 min", minutes: 10 },
  { label: "1 week", days: 7 },
  { label: "2 weeks", days: 14 },
  { label: "1 month", days: 30 },
  { label: "3 months", days: 90 },
];

function addMinutes(minutes: number): string {
  const d = new Date();
  d.setMinutes(d.getMinutes() + minutes, 0, 0);
  return d.toISOString();
}

function addDays(days: number): string {
  const d = new Date();
  d.setDate(d.getDate() + days);
  d.setHours(23, 59, 0, 0);
  return d.toISOString();
}

function formatEndTime(iso: string): string {
  try {
    const d = new Date(iso);
    if (isNaN(d.getTime())) return "";
    return d.toLocaleDateString("en-US", {
      weekday: "short",
      month: "short",
      day: "numeric",
      year: "numeric",
      hour: "numeric",
      minute: "2-digit",
    });
  } catch {
    return "";
  }
}

function timeUntil(iso: string): string {
  try {
    const d = new Date(iso);
    if (isNaN(d.getTime())) return "";
    const diff = d.getTime() - Date.now();
    if (diff <= 0) return "Already ended";
    const days = Math.floor(diff / 86400000);
    const hrs = Math.floor((diff % 86400000) / 3600000);
    if (days > 0) return `${days}d ${hrs}h from now`;
    const mins = Math.floor((diff % 3600000) / 60000);
    if (hrs > 0) return `${hrs}h ${mins}m from now`;
    return `${mins}m from now`;
  } catch {
    return "";
  }
}

function toLocalInput(iso: string): string {
  try {
    const d = new Date(iso);
    if (isNaN(d.getTime())) return "";
    const pad = (n: number) => String(n).padStart(2, "0");
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
  } catch {
    return "";
  }
}

function fromLocalInput(val: string): string {
  try {
    const d = new Date(val);
    if (isNaN(d.getTime())) return "";
    return d.toISOString();
  } catch {
    return "";
  }
}

function formatTimestamp(d: Date): string {
  return d.toLocaleDateString("en-US", {
    weekday: "short",
    month: "short",
    day: "numeric",
    year: "numeric",
    hour: "numeric",
    minute: "2-digit",
  });
}

export function RoundEditor({ round, onUpdateName, onUpdateSettings, onNavigate, isReadonly = false }: RoundEditorProps) {
  const [showCustom, setShowCustom] = useState(false);
  const [nhLoading, setNhLoading] = useState(false);
  const [nhError, setNhError] = useState<string | null>(null);
  const [pirRebuilding, setPirRebuilding] = useState(false);
  const [snapshotHeightEdited, setSnapshotHeightEdited] = useState(false);
  const [snapshotValidation, setSnapshotValidation] =
    useState<PublishedSnapshotValidationResult | null>(null);
  const [snapshotValidationLoading, setSnapshotValidationLoading] = useState(false);
  // PIR server's actually-served height; only used to warn when it disagrees
  // with the canonical published height.
  const [pirServedHeight, setPirServedHeight] = useState<number | null>(null);
  const endTime = round.settings.endTime;
  const hasEndTime = endTime.length > 0;

  const chain = useChainInfo();
  const { precomputedBaseURL } = useUIConfig();
  const [chainSnapshotHeight, setChainSnapshotHeight] = useState<number | null>(null);
  const [chainSnapshotLoaded, setChainSnapshotLoaded] = useState(false);

  // Ref to avoid stale closure — parent passes an inline arrow that changes
  // every render, but the callback logic should always use the latest version.
  const onUpdateSettingsRef = useRef(onUpdateSettings);
  useEffect(() => { onUpdateSettingsRef.current = onUpdateSettings; });
  const lastAutoSnapshotHeightRef = useRef<string>("");

  useEffect(() => {
    setSnapshotHeightEdited(false);
    setSnapshotValidation(null);
    lastAutoSnapshotHeightRef.current = "";
  }, [round.id]);

  // Fetch snapshot height from PIR server. Chain state is the source of truth
  // for active rounds, but PIR status remains useful while drafting the first
  // round and for surfacing replica/bootstrap mismatches.
  const fetchSnapshotHeight = useCallback(async (setLoading: boolean) => {
    if (setLoading) {
      setNhLoading(true);
      setNhError(null);
    }
    try {
      const status = await getSnapshotStatus();
      if (status.phase === "rebuilding") {
        setPirRebuilding(true);
        return false;
      }
      setPirRebuilding(false);
      setPirServedHeight(status.height);
      return true;
    } catch (err) {
      setNhError(err instanceof Error ? err.message : "Failed to fetch");
      return true; // stop polling on hard error
    } finally {
      if (setLoading) setNhLoading(false);
    }
  }, []);

  useEffect(() => {
    if (isReadonly) return;
    let cancelled = false;
    setChainSnapshotLoaded(false);
    getActiveRound()
      .then((resp) => {
        if (cancelled) return;
        const height = Number(resp.round?.snapshot_height ?? 0);
        setChainSnapshotHeight(height > 0 ? height : null);
      })
      .catch(() => {
        if (!cancelled) setChainSnapshotHeight(null);
      })
      .finally(() => {
        if (!cancelled) setChainSnapshotLoaded(true);
      });
    return () => {
      cancelled = true;
    };
  }, [round.id, isReadonly]);

  // Auto-populate the first draft value, then leave manual operator edits alone.
  useEffect(() => {
    if (isReadonly) return;
    if (!chainSnapshotLoaded) return;
    const nextAutoHeight =
      chainSnapshotHeight != null
        ? String(chainSnapshotHeight)
        : pirServedHeight != null
          ? String(pirServedHeight)
          : "";
    if (nextAutoHeight) {
      const current = round.settings.snapshotHeight;
      const canAutoFill =
        !snapshotHeightEdited &&
        (current === "" || current === lastAutoSnapshotHeightRef.current);
      if (canAutoFill) {
        lastAutoSnapshotHeightRef.current = nextAutoHeight;
        onUpdateSettingsRef.current({ snapshotHeight: nextAutoHeight });
      }
    } else if (round.settings.snapshotHeight === "") {
      // Surface "no source of truth" only once a fetch has completed.
      if (chainSnapshotLoaded && pirServedHeight === null && !pirRebuilding && !nhLoading) {
        setNhError((e) => e ?? "No active on-chain round and PIR server has no checkpoint");
      }
    }
  }, [chainSnapshotHeight, pirServedHeight, chainSnapshotLoaded, pirRebuilding, nhLoading, isReadonly, round.settings.snapshotHeight, snapshotHeightEdited]);

  // Auto-populate snapshot height from PIR server on mount and round switch.
  useEffect(() => {
    if (isReadonly) return;
    fetchSnapshotHeight(true);
  }, [round.id, isReadonly, fetchSnapshotHeight]);

  // Poll while PIR is rebuilding, auto-update when done.
  useEffect(() => {
    if (!pirRebuilding) return;
    const interval = setInterval(() => { fetchSnapshotHeight(false); }, 5000);
    return () => clearInterval(interval);
  }, [pirRebuilding, fetchSnapshotHeight]);

  const snapshotHeight = parseInt(round.settings.snapshotHeight, 10);
  const isValidHeight = !isNaN(snapshotHeight) && snapshotHeight > 0;
  useEffect(() => {
    if (isReadonly || !isValidHeight || !precomputedBaseURL) {
      setSnapshotValidation(null);
      setSnapshotValidationLoading(false);
      return;
    }
    let cancelled = false;
    setSnapshotValidationLoading(true);
    const timer = setTimeout(() => {
      validatePublishedSnapshotManifest(precomputedBaseURL, snapshotHeight)
        .then((result) => {
          if (!cancelled) setSnapshotValidation(result);
        })
        .catch((err) => {
          if (!cancelled) {
            setSnapshotValidation({
              status: "error",
              height: snapshotHeight,
              manifestUrl: "",
              message: err instanceof Error ? err.message : "Snapshot validation failed",
            });
          }
        })
        .finally(() => {
          if (!cancelled) setSnapshotValidationLoading(false);
        });
    }, 350);

    return () => {
      cancelled = true;
      clearTimeout(timer);
    };
  }, [isReadonly, isValidHeight, precomputedBaseURL, snapshotHeight]);

  // PIR is "behind" the active chain round — typically a deploy-in-progress.
  const pirMismatch =
    chainSnapshotHeight != null && pirServedHeight != null && pirServedHeight !== chainSnapshotHeight;

  // Estimated timestamp for the snapshot height
  const estimatedDate =
    isValidHeight && chain.latestHeight && chain.latestTimestamp
      ? estimateTimestamp(snapshotHeight, chain.latestHeight, chain.latestTimestamp)
      : null;

  return (
    <div className="space-y-4">
        {/* Round name */}
        <div>
          <label className="block text-[11px] text-text-secondary mb-1">
            Round name
          </label>
          <input
            type="text"
            value={round.name}
            onChange={(e) => onUpdateName(e.target.value)}
            placeholder="e.g. NU7 Sentiment Polling"
            readOnly={isReadonly}
            className={`w-full px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary placeholder:text-text-muted focus:outline-none focus:border-accent/50 ${isReadonly ? "opacity-60 cursor-default" : ""}`}
          />
        </div>

        {/* Snapshot height — defaults from chain/PIR, but operators can override. */}
        <div>
          <div className="flex items-center gap-2 mb-1">
            <label className="text-[11px] text-text-secondary">
              Snapshot height
            </label>
            {nhLoading && (
              <Loader2 size={10} className="text-accent animate-spin" />
            )}
            {snapshotValidationLoading && (
              <Loader2 size={10} className="text-accent animate-spin" />
            )}
          </div>
          <div className="relative">
            <input
              type="text"
              inputMode="numeric"
              value={round.settings.snapshotHeight}
              onChange={(e) => {
                setSnapshotHeightEdited(true);
                setNhError(null);
                setSnapshotValidation(null);
                onUpdateSettings({ snapshotHeight: e.target.value.replace(/[^0-9]/g, "") });
              }}
              readOnly={isReadonly}
              placeholder={lastAutoSnapshotHeightRef.current || "Enter snapshot height"}
              className={`w-full px-3 py-2 pr-24 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary placeholder:text-text-muted focus:outline-none focus:border-accent/50 font-mono ${isReadonly ? "opacity-60 cursor-default" : ""}`}
            />
            {onNavigate && (
              <button
                onClick={() => onNavigate("snapshot")}
                className="absolute right-2 top-1/2 -translate-y-1/2 text-[10px] text-accent hover:text-accent-glow cursor-pointer"
              >
                Change height
              </button>
            )}
          </div>

          {/* Estimated timestamp */}
          {estimatedDate && (
            <div className="flex items-center gap-2 mt-1.5 px-2.5 py-1.5 bg-surface-2 border border-border-subtle rounded-md">
              <Clock size={12} className="text-accent shrink-0" />
              <div className="min-w-0">
                <p className="text-[10px] text-text-primary">
                  {formatTimestamp(estimatedDate)}
                </p>
                <p className="text-[9px] text-text-muted">
                  estimated @ 75s/block from tip
                </p>
              </div>
            </div>
          )}

          {snapshotValidation?.status === "valid" && (
            <div className="flex items-start gap-2 mt-2 px-2.5 py-2 bg-success/10 border border-success/30 rounded-md">
              <div className="w-3 h-3 mt-0.5 rounded-full bg-success shrink-0" />
              <div className="min-w-0">
                <p className="text-[10px] text-success font-semibold leading-snug">
                  Published PIR snapshot exists
                </p>
                <p className="text-[10px] text-text-muted leading-snug mt-0.5 truncate" title={snapshotValidation.manifestUrl}>
                  {snapshotValidation.manifestUrl}
                </p>
              </div>
            </div>
          )}

          {snapshotValidation?.status === "missing" && (
            <div className="flex items-start gap-2 mt-2 px-2.5 py-2 bg-warning/10 border border-warning/40 rounded-md">
              <AlertTriangle size={12} className="text-warning shrink-0 mt-0.5" />
              <div className="min-w-0">
                <p className="text-[10px] text-warning font-semibold leading-snug">
                  No published PIR snapshot for this height
                </p>
                <p className="text-[10px] text-text-muted leading-snug mt-0.5">
                  Publishing will require force confirmation unless a manifest
                  appears at this height first.
                </p>
              </div>
            </div>
          )}

          {snapshotValidation?.status === "invalid" && (
            <div className="flex items-start gap-2 mt-2 px-2.5 py-2 bg-danger/10 border border-danger/40 rounded-md">
              <AlertTriangle size={12} className="text-danger shrink-0 mt-0.5" />
              <div className="min-w-0">
                <p className="text-[10px] text-danger font-semibold leading-snug">
                  Published PIR snapshot manifest is invalid
                </p>
                <p className="text-[10px] text-danger/80 leading-snug mt-0.5">
                  {(snapshotValidation.issues ?? []).join("; ")}
                </p>
              </div>
            </div>
          )}

          {snapshotValidation?.status === "error" && (
            <div className="flex items-start gap-2 mt-2 px-2.5 py-2 bg-danger/10 border border-danger/40 rounded-md">
              <AlertTriangle size={12} className="text-danger shrink-0 mt-0.5" />
              <div className="min-w-0">
                <p className="text-[10px] text-danger font-semibold leading-snug">
                  Could not validate published PIR snapshot
                </p>
                <p className="text-[10px] text-danger/80 leading-snug mt-0.5">
                  {snapshotValidation.message}
                </p>
              </div>
            </div>
          )}

          {/* PIR vs active round height mismatch */}
          {pirMismatch && !pirRebuilding && (
            <div className="flex items-start gap-2 mt-2 px-2.5 py-2 bg-warning/10 border border-warning/40 rounded-md">
              <AlertTriangle size={12} className="text-warning shrink-0 mt-0.5" />
              <div className="min-w-0">
                <p className="text-[10px] text-warning font-semibold leading-snug">
                  PIR server height differs from active round
                </p>
                <p className="text-[10px] text-text-muted leading-snug mt-0.5">
                  Chain wants <span className="font-mono">{chainSnapshotHeight!.toLocaleString()}</span>,
                  PIR is serving <span className="font-mono">{pirServedHeight!.toLocaleString()}</span>.
                  You can still publish a different height, but confirm the
                  published PIR snapshot exists before proposing.
                </p>
                {onNavigate && (
                  <button
                    onClick={() => onNavigate("snapshot")}
                    className="text-[10px] text-accent hover:text-accent-glow mt-1 flex items-center gap-0.5 cursor-pointer"
                  >
                    <ExternalLink size={10} />
                    Open Snapshot Settings
                  </button>
                )}
              </div>
            </div>
          )}

          {/* PIR server rebuilding */}
          {pirRebuilding && (
            <div className="flex items-start gap-2 mt-2 px-2.5 py-2 bg-accent/10 border border-accent/40 rounded-md">
              <Loader2 size={12} className="text-accent shrink-0 mt-0.5 animate-spin" />
              <div className="min-w-0">
                <p className="text-[10px] text-accent font-semibold leading-snug">
                  PIR server is rebuilding
                </p>
                <p className="text-[10px] text-text-muted leading-snug mt-0.5">
                  The snapshot is being regenerated. Wait for it to complete before publishing.
                </p>
                {onNavigate && (
                  <button
                    onClick={() => onNavigate("snapshot")}
                    className="text-[10px] text-accent hover:text-accent-glow mt-1 flex items-center gap-0.5 cursor-pointer"
                  >
                    <ExternalLink size={10} />
                    Go to Snapshot Settings
                  </button>
                )}
              </div>
            </div>
          )}

          {/* PIR server error */}
          {nhError && !pirRebuilding && (
            <div className="flex items-start gap-2 mt-2 px-2.5 py-2 bg-danger/10 border border-danger/40 rounded-md">
              <AlertTriangle size={12} className="text-danger shrink-0 mt-0.5" />
              <div className="min-w-0">
                <p className="text-[10px] text-danger font-semibold leading-snug">
                  PIR server unavailable
                </p>
                <p className="text-[10px] text-danger/80 leading-snug mt-0.5">
                  {nhError}
                </p>
                {onNavigate && (
                  <button
                    onClick={() => onNavigate("snapshot")}
                    className="text-[10px] text-accent hover:text-accent-glow mt-1 flex items-center gap-0.5 cursor-pointer"
                  >
                    <ExternalLink size={10} />
                    Go to Snapshot Settings
                  </button>
                )}
              </div>
            </div>
          )}

          {/* Chain error */}
          {chain.error && (
            <p className="text-[10px] text-danger mt-1">
              RPC error: {chain.error}
            </p>
          )}

          <p className="text-[10px] text-text-muted mt-1">
            Defaults from the active on-chain round, falling back to the PIR
            server. Edit this when you intentionally want a different balance
            snapshot height.
          </p>
        </div>

        {/* Voting end time */}
        <div>
          <label className="block text-[11px] text-text-secondary mb-1.5">
            Voting ends
          </label>

          {/* Current value display */}
          {hasEndTime ? (
            <div className="flex items-center gap-2 px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg mb-2">
              <Clock size={13} className="text-accent shrink-0" />
              <div className="flex-1 min-w-0">
                <p className="text-xs text-text-primary">
                  {formatEndTime(endTime)}
                </p>
                <p className="text-[10px] text-text-muted">
                  {timeUntil(endTime)}
                </p>
              </div>
              {!isReadonly && (
                <button
                  onClick={() => {
                    onUpdateSettings({ endTime: "" });
                    setShowCustom(false);
                  }}
                  className="p-0.5 text-text-muted hover:text-danger rounded cursor-pointer"
                  title="Clear"
                >
                  <X size={13} />
                </button>
              )}
            </div>
          ) : (
            <p className="text-[11px] text-text-muted italic mb-2">
              No end time set
            </p>
          )}

          {/* Preset buttons */}
          {!isReadonly && (
            <div className="flex flex-wrap gap-1.5 mb-2">
              {DURATION_PRESETS.map((preset) => (
                <button
                  key={preset.label}
                  onClick={() => {
                    const endTime =
                      preset.minutes !== undefined
                        ? addMinutes(preset.minutes)
                        : addDays(preset.days);
                    onUpdateSettings({ endTime });
                    setShowCustom(false);
                  }}
                  className="px-2.5 py-1 bg-surface-2 border border-border-subtle hover:border-accent/40 hover:text-accent-glow text-text-secondary rounded-md text-[11px] transition-colors cursor-pointer"
                >
                  {preset.label}
                </button>
              ))}
              <button
                onClick={() => setShowCustom(!showCustom)}
                className={`px-2.5 py-1 border rounded-md text-[11px] transition-colors cursor-pointer ${
                  showCustom
                    ? "bg-accent/10 border-accent/40 text-accent-glow"
                    : "bg-surface-2 border-border-subtle text-text-secondary hover:border-accent/40 hover:text-accent-glow"
                }`}
              >
                Custom...
              </button>
            </div>
          )}

          {/* Custom picker */}
          {!isReadonly && showCustom && (
            <div className="flex items-center gap-2">
              <input
                type="date"
                value={toLocalInput(endTime).split("T")[0] ?? ""}
                onChange={(e) => {
                  const time = toLocalInput(endTime).split("T")[1] ?? "23:59";
                  onUpdateSettings({ endTime: fromLocalInput(`${e.target.value}T${time}`) });
                }}
                className="flex-1 px-2.5 py-1.5 bg-surface-2 border border-border-subtle rounded-md text-xs text-text-primary focus:outline-none focus:border-accent/50 [color-scheme:dark]"
              />
              <input
                type="time"
                value={toLocalInput(endTime).split("T")[1] ?? ""}
                onChange={(e) => {
                  const date = toLocalInput(endTime).split("T")[0] || new Date().toISOString().split("T")[0];
                  onUpdateSettings({ endTime: fromLocalInput(`${date}T${e.target.value}`) });
                }}
                className="w-[100px] px-2.5 py-1.5 bg-surface-2 border border-border-subtle rounded-md text-xs text-text-primary focus:outline-none focus:border-accent/50 [color-scheme:dark]"
              />
            </div>
          )}

          <p className="text-[10px] text-text-muted mt-1.5">
            After this time, no more votes are accepted.
          </p>
        </div>

        {/* Round description */}
        <div>
          <label className="block text-[11px] text-text-secondary mb-1">
            Description
          </label>
          <textarea
            value={round.settings.description}
            onChange={(e) => onUpdateSettings({ description: e.target.value })}
            placeholder="Describe the purpose of this voting round..."
            rows={4}
            readOnly={isReadonly}
            className={`w-full px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary placeholder:text-text-muted focus:outline-none focus:border-accent/50 resize-none ${isReadonly ? "opacity-60 cursor-default" : ""}`}
          />
        </div>

        {/* Discussion URL */}
        <div>
          <label className="block text-[11px] text-text-secondary mb-1">
            Forum Discussion URL
          </label>
          <input
            type="url"
            value={round.settings.discussionURL}
            onChange={(e) => onUpdateSettings({ discussionURL: e.target.value })}
            placeholder="https://forum.zcashcommunity.com/..."
            readOnly={isReadonly}
            className={`w-full px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary placeholder:text-text-muted focus:outline-none focus:border-accent/50 ${isReadonly ? "opacity-60 cursor-default" : ""}`}
          />
          <p className="text-[10px] text-text-muted mt-1">
            Link to the forum thread for the overall vote. Shown in the iOS poll description.
          </p>
        </div>
    </div>
  );
}
