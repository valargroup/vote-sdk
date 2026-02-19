import { useState, useCallback } from "react";
import { Settings2, X, Clock, RefreshCw } from "lucide-react";
import type { VotingRound, RoundSettings } from "../types";
import {
  useChainInfo,
  estimateTimestamp,
} from "../store/rpc";
import { getNullifierStatus } from "../api/chain";

interface RoundEditorProps {
  round: VotingRound;
  onUpdateName: (name: string) => void;
  onUpdateSettings: (patch: Partial<RoundSettings>) => void;
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

export function RoundEditor({ round, onUpdateName, onUpdateSettings }: RoundEditorProps) {
  const [showCustom, setShowCustom] = useState(false);
  const [imtLoading, setImtLoading] = useState(false);
  const [imtError, setImtError] = useState<string | null>(null);
  const endTime = round.settings.endTime;
  const hasEndTime = endTime.length > 0;

  const chain = useChainInfo();

  const handleUseImtHeight = useCallback(async () => {
    setImtLoading(true);
    setImtError(null);
    try {
      const status = await getNullifierStatus();
      const height = status.latest_height;
      if (height == null) throw new Error("IMT height unavailable");
      onUpdateSettings({ snapshotHeight: String(height) });
    } catch (err) {
      setImtError(err instanceof Error ? err.message : "Failed to fetch");
    } finally {
      setImtLoading(false);
    }
  }, [onUpdateSettings]);

  const snapshotHeight = parseInt(round.settings.snapshotHeight, 10);
  const isValidHeight = !isNaN(snapshotHeight) && snapshotHeight > 0;

  // Estimated timestamp for the snapshot height
  const estimatedDate =
    isValidHeight && chain.latestHeight && chain.latestTimestamp
      ? estimateTimestamp(snapshotHeight, chain.latestHeight, chain.latestTimestamp)
      : null;

  return (
    <div className="flex flex-col h-full">
      <div className="px-4 py-3 border-b border-border-subtle flex items-center gap-2">
        <Settings2 size={14} className="text-text-muted" />
        <h3 className="text-xs font-semibold text-text-primary">
          Round Settings
        </h3>
      </div>

      <div className="flex-1 overflow-y-auto p-4 space-y-4">
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
            className="w-full px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary placeholder:text-text-muted focus:outline-none focus:border-accent/50"
          />
        </div>

        {/* Snapshot height */}
        <div>
          <div className="flex items-center justify-between mb-1">
            <label className="text-[11px] text-text-secondary">
              Snapshot height
            </label>
            <div className="flex items-center gap-2">
              <button
                onClick={handleUseImtHeight}
                disabled={imtLoading}
                className="text-[10px] text-accent hover:text-accent-glow disabled:opacity-50 cursor-pointer flex items-center gap-0.5"
                title="Set to current IMT anchor height from the helper service"
              >
                <RefreshCw size={10} className={imtLoading ? "animate-spin" : ""} />
                Use IMT height
              </button>
              {chain.latestHeight && (
                <span className="text-[10px] text-text-muted flex items-center gap-1">
                  tip: {chain.latestHeight.toLocaleString()}
                  <button
                    onClick={chain.refresh}
                    className="p-0.5 hover:text-text-secondary cursor-pointer"
                    title="Refresh"
                  >
                    <RefreshCw size={10} className={chain.loading ? "animate-spin" : ""} />
                  </button>
                </span>
              )}
            </div>
          </div>
          <input
            type="text"
            inputMode="numeric"
            value={round.settings.snapshotHeight}
            onChange={(e) => {
              const val = e.target.value.replace(/[^0-9]/g, "");
              onUpdateSettings({ snapshotHeight: val });
            }}
            placeholder="e.g. 2800000"
            className="w-full px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary placeholder:text-text-muted focus:outline-none focus:border-accent/50 font-mono"
          />

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

          {/* Chain error */}
          {chain.error && (
            <p className="text-[10px] text-danger mt-1">
              RPC error: {chain.error}
            </p>
          )}

          {/* IMT error */}
          {imtError && (
            <p className="text-[10px] text-danger mt-1">
              IMT error: {imtError}
            </p>
          )}

          <p className="text-[10px] text-text-muted mt-1">
            The block height at which balances are captured for vote weighting.
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
            </div>
          ) : (
            <p className="text-[11px] text-text-muted italic mb-2">
              No end time set
            </p>
          )}

          {/* Preset buttons */}
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

          {/* Custom picker */}
          {showCustom && (
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
            className="w-full px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary placeholder:text-text-muted focus:outline-none focus:border-accent/50 resize-none"
          />
        </div>
      </div>
    </div>
  );
}
