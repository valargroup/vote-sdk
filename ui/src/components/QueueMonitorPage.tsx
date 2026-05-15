import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  AlertCircle,
  AlertTriangle,
  Activity,
  BarChart3,
  Loader2,
  RefreshCw,
  Server,
} from "lucide-react";
import * as chainApi from "../api/chain";
import type { QueueSummaryBucket, ServiceEntry } from "../api/chain";
import {
  QUEUE_STATE_META,
  QUEUE_STATE_ORDER,
  alignQueueBuckets,
  detectQueueBacklogSignals,
  isQueueSummaryStale,
  queueSummaryDomain,
  queueSummaryMaxBucketTotal,
  queueSummaryTotals,
  splitQueueResults,
  type QueueBacklogSignal,
  type QueueServerOK,
  type QueueServerResult,
} from "../utils/queueSummary";

const SERVER_COLORS = ["#4a9a4a", "#3b82f6", "#c4943a", "#a855f7", "#14b8a6", "#ec4899"];

function base64ToHex(b64: string): string {
  try {
    return Array.from(atob(b64), (c) => c.charCodeAt(0).toString(16).padStart(2, "0")).join("");
  } catch {
    return "";
  }
}

function isRoundIdHex(value: string): boolean {
  return /^[0-9a-fA-F]{64}$/.test(value.trim());
}

function roundLabel(round: chainApi.ChainRound): string {
  const roundId = base64ToHex(round.vote_round_id ?? "");
  const title = round.title || round.description || "Untitled round";
  const status = String(round.status ?? "unknown").replace(/^SESSION_STATUS_/, "").toLowerCase();
  return `${title} (${status}) - ${roundId.slice(0, 12)}`;
}

function defaultRoundId(rounds: chainApi.ChainRound[]): string {
  const active = chainApi.getPrimaryActiveRoundFromList(rounds);
  const candidate = active ?? [...rounds].sort((a, b) => {
    const ah = Number(a.created_at_height ?? 0);
    const bh = Number(b.created_at_height ?? 0);
    if (ah !== bh) return bh - ah;
    return Number(b.vote_end_time ?? 0) - Number(a.vote_end_time ?? 0);
  })[0];
  return candidate?.vote_round_id ? base64ToHex(candidate.vote_round_id) : "";
}

function formatTime(seconds: number): string {
  return new Date(seconds * 1000).toLocaleString([], {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

function shortUrl(url: string): string {
  return url.replace(/^https?:\/\//, "").replace(/\/+$/, "");
}

function serverLabel(server: ServiceEntry): string {
  return server.label || shortUrl(server.url);
}

async function fetchQueueSummaries(
  servers: ServiceEntry[],
  roundIdHex: string
): Promise<QueueServerResult[]> {
  return Promise.all(
    servers.map(async (server): Promise<QueueServerResult> => {
      try {
        const summary = await chainApi.getQueueSummaryFromServer(server.url, roundIdHex);
        return { state: "ok", server, summary };
      } catch (err) {
        return {
          state: "error",
          server,
          error: err instanceof Error ? err.message : String(err),
        };
      }
    })
  );
}

function QueueHistogram({
  results,
  backlogSignals,
  nowSeconds,
}: {
  results: QueueServerOK[];
  backlogSignals: QueueBacklogSignal[];
  nowSeconds: number;
}) {
  const summaries = results.map((result) => result.summary);
  const domain = queueSummaryDomain(summaries);
  const aligned = alignQueueBuckets(results);
  const maxTotal = queueSummaryMaxBucketTotal(summaries);
  const signalKeys = new Set(
    backlogSignals.map((signal) => `${signal.serverUrl}:${signal.bucketStart}:${signal.bucketEnd}`)
  );

  if (!domain || domain.end <= domain.start) return null;

  const width = 1080;
  const height = 340;
  const margin = { top: 20, right: 24, bottom: 46, left: 42 };
  const plotW = width - margin.left - margin.right;
  const plotH = height - margin.top - margin.bottom;
  const duration = domain.end - domain.start;
  const xForTime = (seconds: number) => margin.left + ((seconds - domain.start) / duration) * plotW;
  const yForValue = (value: number) => margin.top + plotH - (value / maxTotal) * plotH;
  const serverSlot = (bucket: QueueSummaryBucket) => {
    const bucketW = Math.max(2, xForTime(bucket.end) - xForTime(bucket.start));
    return Math.max(2, bucketW / Math.max(results.length, 1));
  };

  const tickCount = Math.min(6, Math.max(2, aligned.length));
  const ticks = Array.from({ length: tickCount }, (_, i) => {
    const ratio = tickCount === 1 ? 0 : i / (tickCount - 1);
    return Math.round(domain.start + duration * ratio);
  });

  return (
    <div className="rounded-xl border border-border-subtle bg-surface-1 p-4">
      <div className="mb-3 flex items-center justify-between gap-3">
        <div>
          <h2 className="text-sm font-semibold text-text-primary">Share queue histogram</h2>
          <p className="text-[10px] text-text-muted">
            {formatTime(domain.start)} to {formatTime(domain.end)}
          </p>
        </div>
        <div className="flex flex-wrap justify-end gap-2">
          {QUEUE_STATE_ORDER.slice().reverse().map((state) => (
            <span key={state} className="inline-flex items-center gap-1.5 text-[10px] text-text-secondary">
              <span
                className="h-2 w-2 rounded-sm"
                style={{ backgroundColor: QUEUE_STATE_META[state].color }}
              />
              {QUEUE_STATE_META[state].label}
            </span>
          ))}
        </div>
      </div>

      <div className="overflow-x-auto">
        <svg viewBox={`0 0 ${width} ${height}`} className="min-w-[900px] w-full">
          <rect x={margin.left} y={margin.top} width={plotW} height={plotH} fill="#141414" rx={6} />
          {[0, 0.5, 1].map((ratio) => {
            const y = margin.top + plotH * ratio;
            const value = Math.round(maxTotal * (1 - ratio));
            return (
              <g key={ratio}>
                <line x1={margin.left} x2={margin.left + plotW} y1={y} y2={y} stroke="#2a2a2a" />
                <text x={margin.left - 8} y={y + 3} textAnchor="end" className="fill-text-muted text-[10px]">
                  {value}
                </text>
              </g>
            );
          })}

          {domain.lastMinuteStart && domain.lastMinuteStart < domain.end && (
            <rect
              x={xForTime(domain.lastMinuteStart)}
              y={margin.top}
              width={Math.max(1, xForTime(domain.end) - xForTime(domain.lastMinuteStart))}
              height={plotH}
              fill="#c4943a"
              opacity={0.08}
            />
          )}

          {nowSeconds > domain.start && nowSeconds < domain.end && (
            <g>
              <line
                x1={xForTime(nowSeconds)}
                x2={xForTime(nowSeconds)}
                y1={margin.top}
                y2={margin.top + plotH}
                stroke="#e8e0d4"
                strokeDasharray="4 4"
                opacity={0.75}
              />
              <text x={xForTime(nowSeconds) + 5} y={margin.top + 12} className="fill-text-secondary text-[10px]">
                now
              </text>
            </g>
          )}

          {aligned.flatMap((window) =>
            results.flatMap((result, serverIndex) => {
              const bucket = window.byServer[result.server.url];
              if (!bucket || bucket.total <= 0) return [];
              const slot = serverSlot(bucket);
              const barW = Math.max(1, Math.min(14, slot - 1));
              const x = xForTime(bucket.start) + serverIndex * slot + Math.max(0, slot - barW) / 2;
              let y = margin.top + plotH;
              const signal = signalKeys.has(`${result.server.url}:${bucket.start}:${bucket.end}`);

              const segments = QUEUE_STATE_ORDER.map((state) => {
                const value = bucket[state];
                if (value <= 0) return null;
                const h = Math.max(1, margin.top + plotH - yForValue(value));
                y -= h;
                return (
                  <rect
                    key={`${result.server.url}:${bucket.start}:${state}`}
                    x={x}
                    y={y}
                    width={barW}
                    height={h}
                    fill={QUEUE_STATE_META[state].color}
                  />
                );
              });

              return [
                <g key={`${result.server.url}:${bucket.start}:${bucket.end}`}>
                  {segments}
                  <rect
                    x={x - 0.5}
                    y={y - 0.5}
                    width={barW + 1}
                    height={margin.top + plotH - y + 1}
                    fill="none"
                    stroke={signal ? "#c4943a" : SERVER_COLORS[serverIndex % SERVER_COLORS.length]}
                    strokeWidth={signal ? 2 : 1}
                    opacity={signal ? 1 : 0.75}
                  />
                  <title>
                    {serverLabel(result.server)} - {formatTime(bucket.start)} - total {bucket.total}
                  </title>
                </g>,
              ];
            })
          )}

          {ticks.map((tick) => (
            <g key={tick}>
              <line
                x1={xForTime(tick)}
                x2={xForTime(tick)}
                y1={margin.top + plotH}
                y2={margin.top + plotH + 5}
                stroke="#6a6050"
              />
              <text
                x={xForTime(tick)}
                y={margin.top + plotH + 22}
                textAnchor="middle"
                className="fill-text-muted text-[10px]"
              >
                {formatTime(tick)}
              </text>
            </g>
          ))}
        </svg>
      </div>
    </div>
  );
}

function ServerSummaryCard({
  result,
  index,
  nowSeconds,
}: {
  result: QueueServerResult;
  index: number;
  nowSeconds: number;
}) {
  const label = serverLabel(result.server);
  if (result.state === "error") {
    return (
      <div className="rounded-lg border border-danger/30 bg-danger/10 p-3">
        <div className="flex items-center gap-2">
          <AlertCircle size={14} className="text-danger" />
          <h3 className="text-xs font-semibold text-text-primary">{label}</h3>
        </div>
        <p className="mt-1 text-[10px] text-text-muted break-all">{shortUrl(result.server.url)}</p>
        <p className="mt-2 text-[11px] text-danger">{result.error}</p>
      </div>
    );
  }

  const totals = queueSummaryTotals(result.summary);
  const stale = isQueueSummaryStale(result.summary, nowSeconds);
  return (
    <div className="rounded-lg border border-border-subtle bg-surface-1 p-3">
      <div className="flex items-center justify-between gap-2">
        <div className="flex min-w-0 items-center gap-2">
          <span
            className="h-2.5 w-2.5 shrink-0 rounded-full"
            style={{ backgroundColor: SERVER_COLORS[index % SERVER_COLORS.length] }}
          />
          <h3 className="truncate text-xs font-semibold text-text-primary">{label}</h3>
        </div>
        {stale && <span className="rounded-full bg-warning/10 px-2 py-0.5 text-[10px] text-warning">stale</span>}
      </div>
      <p className="mt-1 truncate text-[10px] text-text-muted" title={result.server.url}>
        {shortUrl(result.server.url)}
      </p>
      <div className="mt-3 grid grid-cols-3 gap-2">
        <Metric label="total" value={totals.total} />
        <Metric label="submitted" value={totals.submitted} />
        <Metric label="overdue" value={totals.overdue_pending + totals.processing} />
      </div>
    </div>
  );
}

function Metric({ label, value }: { label: string; value: number }) {
  return (
    <div className="rounded-md bg-surface-2 px-2 py-1.5">
      <p className="text-[9px] uppercase tracking-wider text-text-muted">{label}</p>
      <p className="mt-0.5 text-xs font-semibold text-text-primary">{value.toLocaleString()}</p>
    </div>
  );
}

export function QueueMonitorPage() {
  const [rounds, setRounds] = useState<chainApi.ChainRound[]>([]);
  const [voteServers, setVoteServers] = useState<ServiceEntry[]>([]);
  const [selectedRoundId, setSelectedRoundId] = useState("");
  const [results, setResults] = useState<QueueServerResult[]>([]);
  const [backlogSignals, setBacklogSignals] = useState<QueueBacklogSignal[]>([]);
  const [nowSeconds, setNowSeconds] = useState(0);
  const [loading, setLoading] = useState(false);
  const [metadataLoading, setMetadataLoading] = useState(true);
  const [error, setError] = useState("");
  const previousByRoundRef = useRef<Record<string, QueueServerOK[]>>({});

  const roundOptions = useMemo(
    () =>
      [...rounds]
        .sort((a, b) => Number(b.created_at_height ?? 0) - Number(a.created_at_height ?? 0))
        .map((round) => ({
          id: base64ToHex(round.vote_round_id ?? ""),
          label: roundLabel(round),
        }))
        .filter((round) => isRoundIdHex(round.id)),
    [rounds]
  );

  const refreshMetadata = useCallback(async () => {
    setMetadataLoading(true);
    setError("");
    try {
      const [roundResp, config] = await Promise.all([
        chainApi.listRounds(),
        chainApi.getVotingConfig(),
      ]);
      const loadedRounds = roundResp.rounds ?? [];
      const loadedServers = config?.vote_servers ?? [];
      setRounds(loadedRounds);
      setVoteServers(loadedServers);
      setSelectedRoundId((current) => {
        if (isRoundIdHex(current)) return current;
        return defaultRoundId(loadedRounds);
      });
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setMetadataLoading(false);
    }
  }, []);

  const refreshSummaries = useCallback(async () => {
    const roundIdHex = selectedRoundId.trim().toLowerCase();
    if (!isRoundIdHex(roundIdHex) || voteServers.length === 0) {
      setResults([]);
      setBacklogSignals([]);
      return;
    }

    setLoading(true);
    setError("");
    try {
      const nextResults = await fetchQueueSummaries(voteServers, roundIdHex);
      const split = splitQueueResults(nextResults);
      const previous = previousByRoundRef.current[roundIdHex] ?? [];
      const fetchedAt = Math.floor(Date.now() / 1000);
      const nextSignals = detectQueueBacklogSignals(
        previous,
        split.ok,
        fetchedAt
      );
      setNowSeconds(fetchedAt);
      previousByRoundRef.current[roundIdHex] = split.ok;
      setResults(nextResults);
      setBacklogSignals(nextSignals);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }, [selectedRoundId, voteServers]);

  useEffect(() => {
    void refreshMetadata();
  }, [refreshMetadata]);

  useEffect(() => {
    void refreshSummaries();
  }, [refreshSummaries]);

  const split = splitQueueResults(results);
  const selectedRound = rounds.find((round) => base64ToHex(round.vote_round_id ?? "") === selectedRoundId);
  const totalServers = voteServers.length;

  return (
    <div className="flex-1 overflow-y-auto">
      <div className="mx-auto max-w-7xl px-6 py-8">
        <div className="mb-6 flex flex-wrap items-center justify-between gap-4">
          <div className="flex items-center gap-3">
            <div className="flex h-10 w-10 items-center justify-center rounded-xl bg-surface-3">
              <Activity size={22} className="text-text-secondary" />
            </div>
            <div>
              <h1 className="text-lg font-bold text-text-primary">Share queues</h1>
              <p className="text-[11px] text-text-muted">
                {totalServers} vote server{totalServers === 1 ? "" : "s"} from voting-config
              </p>
            </div>
          </div>
          <div className="flex items-center gap-2">
            <button
              type="button"
              onClick={() => void refreshMetadata()}
              disabled={metadataLoading || loading}
              className="inline-flex items-center gap-2 rounded-lg bg-surface-2 px-3 py-2 text-[11px] font-semibold text-text-secondary transition-colors hover:bg-surface-3 disabled:opacity-50"
            >
              <Server size={13} />
              Reload config
            </button>
            <button
              type="button"
              onClick={() => void refreshSummaries()}
              disabled={metadataLoading || loading || !isRoundIdHex(selectedRoundId)}
              className="inline-flex items-center gap-2 rounded-lg bg-accent/90 px-3 py-2 text-[11px] font-semibold text-surface-0 transition-colors hover:bg-accent disabled:opacity-50"
            >
              <RefreshCw size={13} className={loading ? "animate-spin" : ""} />
              Refresh
            </button>
          </div>
        </div>

        {error && (
          <div className="mb-5 flex items-start gap-2 rounded-lg border border-danger/30 bg-danger/10 p-3">
            <AlertCircle size={14} className="mt-0.5 shrink-0 text-danger" />
            <p className="text-[11px] text-danger">{error}</p>
          </div>
        )}

        <section className="mb-5 rounded-xl border border-border-subtle bg-surface-1 p-4">
          <div className="grid gap-3 lg:grid-cols-[minmax(260px,420px)_minmax(320px,1fr)]">
            <label className="block">
              <span className="mb-1 block text-[10px] uppercase tracking-wider text-text-muted">Round</span>
              <select
                value={roundOptions.some((round) => round.id === selectedRoundId) ? selectedRoundId : ""}
                onChange={(event) => setSelectedRoundId(event.target.value)}
                disabled={metadataLoading || roundOptions.length === 0}
                className="w-full rounded-lg border border-border-subtle bg-surface-2 px-3 py-2 text-xs text-text-primary outline-none [color-scheme:dark] disabled:opacity-50"
              >
                {metadataLoading && <option value="">Loading rounds...</option>}
                {!metadataLoading && roundOptions.length === 0 && <option value="">No chain rounds found</option>}
                {!metadataLoading &&
                  roundOptions.map((round) => (
                    <option key={round.id} value={round.id}>
                      {round.label}
                    </option>
                  ))}
              </select>
            </label>

            <label className="block">
              <span className="mb-1 block text-[10px] uppercase tracking-wider text-text-muted">Round ID</span>
              <input
                value={selectedRoundId}
                onChange={(event) => setSelectedRoundId(event.target.value.trim())}
                placeholder="64 character round id"
                className="w-full rounded-lg border border-border-subtle bg-surface-2 px-3 py-2 font-mono text-xs text-text-primary outline-none placeholder:text-text-muted"
              />
            </label>
          </div>
          {selectedRound && (
            <p className="mt-2 text-[11px] text-text-muted">
              {selectedRound.title || selectedRound.description || "Selected round"} - voting ends{" "}
              {formatTime(Number(selectedRound.vote_end_time ?? 0))}
            </p>
          )}
        </section>

        {backlogSignals.length > 0 && (
          <div className="mb-5 rounded-lg border border-warning/30 bg-warning/10 p-3">
            <div className="flex items-start gap-2">
              <AlertTriangle size={15} className="mt-0.5 shrink-0 text-warning" />
              <div>
                <p className="text-[11px] font-semibold text-warning">Backlog growth detected</p>
                <p className="mt-0.5 text-[10px] text-text-secondary">
                  {backlogSignals
                    .slice(0, 3)
                    .map(
                      (signal) =>
                        `${signal.label} +${signal.backlogDelta} in ${formatTime(signal.bucketStart)}`
                    )
                    .join("; ")}
                </p>
              </div>
            </div>
          </div>
        )}

        {metadataLoading ? (
          <div className="flex items-center justify-center rounded-xl border border-border-subtle bg-surface-1 py-16">
            <Loader2 size={18} className="animate-spin text-text-muted" />
          </div>
        ) : voteServers.length === 0 ? (
          <div className="rounded-xl border border-border-subtle bg-surface-1 p-6 text-center text-xs text-text-muted">
            No vote servers found in voting-config.
          </div>
        ) : (
          <div className="space-y-5">
            <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
              {results.length === 0 && loading
                ? voteServers.map((server) => (
                    <div key={server.url} className="rounded-lg border border-border-subtle bg-surface-1 p-3">
                      <div className="flex items-center gap-2 text-xs text-text-muted">
                        <Loader2 size={14} className="animate-spin" />
                        {serverLabel(server)}
                      </div>
                    </div>
                  ))
                : results.map((result, index) => (
                    <ServerSummaryCard
                      key={result.server.url}
                      result={result}
                      index={index}
                      nowSeconds={nowSeconds}
                    />
                  ))}
            </div>

            {split.ok.length > 0 ? (
              <QueueHistogram results={split.ok} backlogSignals={backlogSignals} nowSeconds={nowSeconds} />
            ) : (
              <div className="rounded-xl border border-border-subtle bg-surface-1 p-6 text-center text-xs text-text-muted">
                No queue summaries available for this round.
              </div>
            )}

            {split.unavailable.length > 0 && (
              <section className="rounded-xl border border-border-subtle bg-surface-1 p-4">
                <div className="mb-3 flex items-center gap-2">
                  <AlertCircle size={15} className="text-warning" />
                  <h2 className="text-sm font-semibold text-text-primary">Unavailable servers</h2>
                </div>
                <div className="grid gap-2 md:grid-cols-2">
                  {split.unavailable.map((result) => (
                    <div key={result.server.url} className="rounded-lg bg-surface-2 p-3">
                      <p className="text-xs font-semibold text-text-primary">{serverLabel(result.server)}</p>
                      <p className="mt-1 break-all text-[10px] text-text-muted">{result.server.url}</p>
                      <p className="mt-1 text-[11px] text-warning">{result.error}</p>
                    </div>
                  ))}
                </div>
              </section>
            )}

            <section className="rounded-xl border border-border-subtle bg-surface-1 p-4">
              <div className="mb-3 flex items-center gap-2">
                <BarChart3 size={15} className="text-text-secondary" />
                <h2 className="text-sm font-semibold text-text-primary">Aligned buckets</h2>
              </div>
              <div className="max-h-80 overflow-auto">
                <table className="w-full text-[11px]">
                  <thead className="sticky top-0 bg-surface-1 text-[10px] uppercase tracking-wider text-text-muted">
                    <tr className="border-b border-border-subtle">
                      <th className="px-2 py-2 text-left font-medium">Bucket</th>
                      {split.ok.map((result) => (
                        <th key={result.server.url} className="px-2 py-2 text-right font-medium">
                          {serverLabel(result.server)}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {alignQueueBuckets(split.ok).map((bucket) => (
                      <tr key={`${bucket.start}:${bucket.end}`} className="border-b border-border-subtle/60">
                        <td className="whitespace-nowrap px-2 py-1.5 text-text-muted">
                          {formatTime(bucket.start)}
                        </td>
                        {split.ok.map((result) => {
                          const item = bucket.byServer[result.server.url];
                          return (
                            <td key={result.server.url} className="px-2 py-1.5 text-right font-mono text-text-secondary">
                              {item ? item.total.toLocaleString() : "-"}
                            </td>
                          );
                        })}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            </section>
          </div>
        )}
      </div>
    </div>
  );
}
