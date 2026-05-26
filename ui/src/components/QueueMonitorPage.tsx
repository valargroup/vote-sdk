import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  AlertCircle,
  AlertTriangle,
  Activity,
  ChevronDown,
  Clock,
  Loader2,
  RefreshCw,
  ZoomIn,
  ZoomOut,
} from "lucide-react";
import * as chainApi from "../api/chain";
import type { ServiceEntry } from "../api/chain";
import {
  QUEUE_STATE_META,
  aggregateQueueBuckets,
  detectQueueBacklogSignals,
  isQueueSummaryStale,
  queueAggregateMaxBucketTotal,
  queueNextBucketRefreshAt,
  queueStateCount,
  queueSummaryDomain,
  queueSummaryTotals,
  splitQueueResults,
  type AggregatedQueueBucket,
  type QueueBacklogSignal,
  type QueueServerOK,
  type QueueServerResult,
  type QueueStateKey,
} from "../utils/queueSummary";

// Server colors live on a cool/muted palette so they don't collide with the
// saturated state colors in QUEUE_STATE_META (green / blue / amber / purple /
// red). Server identity and state identity stay on different hue families,
// which keeps the histogram readable when bars are stacked by server.
const SERVER_COLORS = [
  "#6fa9b3",
  "#8786c2",
  "#b787ad",
  "#5fa890",
  "#a787c2",
  "#c2a787",
];
const BACKLOG_SIGNAL_STROKE = "#e35d8a";
const LAST_MINUTE_FILL = "#2a2f3a";
const LAST_MINUTE_BORDER = "#7a8090";
const AUTO_REFRESH_INTERVAL_MS = 30_000;
const METADATA_REFRESH_INTERVAL_MS = 30_000;
const QUEUE_SUMMARY_TIMEOUT_MS = 12_000;
const HISTOGRAM_BASE_WIDTH = 1080;
const HISTOGRAM_MIN_WIDTH = 900;
const HISTOGRAM_MAX_ZOOM = 4;
const HISTOGRAM_ZOOM_STEP = 1;
const HISTOGRAM_TICK_SPACING_PX = 180;

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

function formatBucketRange(startSeconds: number, endSeconds: number): string {
  const startDate = new Date(startSeconds * 1000);
  const endDate = new Date(endSeconds * 1000);
  const sameDay = startDate.toDateString() === endDate.toDateString();
  if (sameDay) {
    const endTime = endDate.toLocaleTimeString([], {
      hour: "2-digit",
      minute: "2-digit",
    });
    return `${formatTime(startSeconds)} – ${endTime}`;
  }
  return `${formatTime(startSeconds)} – ${formatTime(endSeconds)}`;
}

function formatCountdown(targetSeconds: number, nowSeconds: number): string {
  const diff = targetSeconds - nowSeconds;
  if (!Number.isFinite(diff)) return "";
  const abs = Math.abs(diff);
  const days = Math.floor(abs / 86400);
  const hours = Math.floor((abs % 86400) / 3600);
  const minutes = Math.floor((abs % 3600) / 60);
  const parts: string[] = [];
  if (days > 0) parts.push(`${days}d`);
  if (hours > 0 || days > 0) parts.push(`${hours}h`);
  parts.push(`${minutes}m`);
  const text = parts.join(" ");
  return diff >= 0 ? `in ${text}` : `${text} ago`;
}

function shortUrl(url: string): string {
  return url.replace(/^https?:\/\//, "").replace(/\/+$/, "");
}

function serverLabel(server: ServiceEntry): string {
  return server.label || shortUrl(server.url);
}

function sameServiceEntries(a: ServiceEntry[], b: ServiceEntry[]): boolean {
  return a.length === b.length && a.every((entry, idx) => {
    const other = b[idx];
    return (
      other !== undefined &&
      entry.url === other.url &&
      entry.label === other.label &&
      entry.operator_address === other.operator_address
    );
  });
}

function sameStrings(a: string[], b: string[]): boolean {
  return a.length === b.length && a.every((value, idx) => value === b[idx]);
}

// Bottom-to-top stacking order follows the share lifecycle:
// submitted (already delivered) → future (window not open yet) → processing
// (helper has started work) → overdue (window passed without delivery) →
// missed/failed terminal states. Alarm states climb toward the top.
const STATE_STACK_ORDER: QueueStateKey[] = [
  "submitted",
  "observed_on_chain",
  "pending_future",
  "processing",
  "overdue_pending",
  "missed_deadline",
  "failed",
];

async function fetchQueueSummaries(
  servers: ServiceEntry[],
  roundIdHex: string
): Promise<QueueServerResult[]> {
  return Promise.all(
    servers.map(async (server): Promise<QueueServerResult> => {
      const controller = new AbortController();
      let timedOut = false;
      const timeout = window.setTimeout(() => {
        timedOut = true;
        controller.abort();
      }, QUEUE_SUMMARY_TIMEOUT_MS);
      try {
        const summary = await chainApi.getQueueSummaryFromServer(server.url, roundIdHex, {
          signal: controller.signal,
        });
        return { state: "ok", server, summary };
      } catch (err) {
        return {
          state: "error",
          server,
          error:
            timedOut
              ? `Timed out after ${Math.round(QUEUE_SUMMARY_TIMEOUT_MS / 1000)}s`
              : err instanceof Error
                ? err.message
                : String(err),
        };
      } finally {
        window.clearTimeout(timeout);
      }
    })
  );
}

type BucketHover = {
  bucket: AggregatedQueueBucket;
  x: number;
  y: number;
  containerWidth: number;
  containerHeight: number;
};

function BucketTooltip({
  bucket,
  results,
}: {
  bucket: AggregatedQueueBucket;
  results: QueueServerOK[];
}) {
  return (
    <>
      <div className="mb-2 flex items-baseline justify-between gap-3 border-b border-border-subtle/60 pb-1.5">
        <span className="text-[10px] font-semibold text-text-primary">
          {formatBucketRange(bucket.start, bucket.end)}
        </span>
        <span className="whitespace-nowrap font-mono text-[10px] text-text-muted">
          total {bucket.total.toLocaleString()}
        </span>
      </div>
      <div className="space-y-2">
        {[...STATE_STACK_ORDER]
          .reverse()
          .filter((state) => queueStateCount(bucket, state) > 0)
          .map((state) => {
            const value = queueStateCount(bucket, state);
            const contribs = results
              .map((result) => {
                const sb = bucket.byServer[result.server.url];
                const count = sb ? queueStateCount(sb, state) : 0;
                return count > 0 ? { label: serverLabel(result.server), count } : null;
              })
              .filter((c): c is { label: string; count: number } => c !== null);
            return (
              <div key={state}>
                <div className="flex items-baseline gap-2 text-[10px]">
                  <span
                    aria-hidden
                    className="h-2 w-2 shrink-0 translate-y-px rounded-sm"
                    style={{ backgroundColor: QUEUE_STATE_META[state].color }}
                  />
                  <span className="font-semibold text-text-secondary">
                    {QUEUE_STATE_META[state].label}
                  </span>
                  <span className="ml-auto font-mono text-text-primary">
                    {value.toLocaleString()}
                  </span>
                </div>
                {contribs.length > 0 && (
                  <div className="mt-1 ml-3.5 space-y-0.5 text-[10px]">
                    {contribs.map((c) => (
                      <div key={c.label} className="flex items-baseline gap-2">
                        <span className="truncate text-text-muted">{c.label}</span>
                        <span className="ml-auto font-mono text-text-secondary">
                          {c.count.toLocaleString()}
                        </span>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            );
          })}
      </div>
    </>
  );
}

function BucketHoverOverlay({
  hover,
  results,
}: {
  hover: BucketHover;
  results: QueueServerOK[];
}) {
  const tooltipMaxWidth = 260;
  // Rough estimate; the tooltip's actual height varies with content density.
  // We only use it to decide whether to flip the panel above the cursor,
  // and being a little off just means a small gap or overlap — never broken.
  const tooltipEstimatedHeight = 220;
  const flipLeft =
    hover.containerWidth > 0 &&
    hover.x + tooltipMaxWidth + 24 > hover.containerWidth;
  const flipUp =
    hover.containerHeight > 0 &&
    hover.y + tooltipEstimatedHeight + 24 > hover.containerHeight;
  const transformParts: string[] = [];
  if (flipLeft) transformParts.push("translateX(-100%)");
  if (flipUp) transformParts.push("translateY(-100%)");
  return (
    <div
      role="tooltip"
      className="pointer-events-none absolute z-10 rounded-lg border border-border-subtle bg-surface-3/95 px-3 py-2 shadow-xl backdrop-blur-sm"
      style={{
        left: hover.x + (flipLeft ? -12 : 12),
        top: hover.y + (flipUp ? -12 : 12),
        transform: transformParts.length > 0 ? transformParts.join(" ") : undefined,
        minWidth: "200px",
        maxWidth: `${tooltipMaxWidth}px`,
      }}
    >
      <BucketTooltip bucket={hover.bucket} results={results} />
    </div>
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
  const containerRef = useRef<HTMLDivElement>(null);
  const scrollerRef = useRef<HTMLDivElement>(null);
  const [hover, setHover] = useState<BucketHover | null>(null);
  const [zoomLevel, setZoomLevel] = useState(1);
  const summaries = results.map((result) => result.summary);
  const domain = queueSummaryDomain(summaries);
  const buckets = aggregateQueueBuckets(results);
  // Keep ~15% headroom above the tallest bar so chrome (the "now" label, the
  // last-minute window title) never collides with a bar segment.
  const maxTotal = Math.max(
    1,
    Math.ceil(queueAggregateMaxBucketTotal(buckets) * 1.15)
  );
  const signalKeys = new Set(
    backlogSignals.map((signal) => `${signal.bucketStart}:${signal.bucketEnd}`)
  );

  if (!domain || domain.end <= domain.start) return null;

  const width = Math.round(HISTOGRAM_BASE_WIDTH * zoomLevel);
  const height = 340;
  // Y-axis labels render in a sticky HTML column outside the SVG, so the SVG
  // itself only needs a small left margin for visual breathing room.
  const margin = { top: 20, right: 36, bottom: 46, left: 8 };
  const plotW = width - margin.left - margin.right;
  const plotH = height - margin.top - margin.bottom;
  const duration = domain.end - domain.start;
  const xForTime = (seconds: number) => margin.left + ((seconds - domain.start) / duration) * plotW;
  const yForValue = (value: number) => margin.top + plotH - (value / maxTotal) * plotH;

  const lastMinuteStart =
    domain.lastMinuteStart && domain.lastMinuteStart > domain.start && domain.lastMinuteStart < domain.end
      ? domain.lastMinuteStart
      : null;
  const lastMinuteX = lastMinuteStart ? xForTime(lastMinuteStart) : null;
  const lastMinuteWidth = lastMinuteStart
    ? Math.max(1, xForTime(domain.end) - xForTime(lastMinuteStart))
    : 0;

  const tickCount = Math.min(
    32,
    Math.max(2, Math.ceil(plotW / HISTOGRAM_TICK_SPACING_PX))
  );
  const ticks = Array.from({ length: tickCount }, (_, i) => {
    const ratio = tickCount === 1 ? 0 : i / (tickCount - 1);
    return Math.round(domain.start + duration * ratio);
  });

  const clampZoom = (value: number) =>
    Math.min(HISTOGRAM_MAX_ZOOM, Math.max(1, Math.round(value)));

  const updateZoom = (nextZoom: number) => {
    const next = clampZoom(nextZoom);
    if (next === zoomLevel) return;
    const scroller = scrollerRef.current;
    const centerRatio =
      scroller && scroller.scrollWidth > 0
        ? (scroller.scrollLeft + scroller.clientWidth / 2) / scroller.scrollWidth
        : null;

    setHover(null);
    setZoomLevel(next);

    if (scroller && centerRatio !== null) {
      window.requestAnimationFrame(() => {
        const targetLeft = centerRatio * scroller.scrollWidth - scroller.clientWidth / 2;
        scroller.scrollLeft = Math.max(0, targetLeft);
      });
    }
  };

  return (
    <div className="rounded-xl border border-border-subtle bg-surface-1 p-4">
      <div className="mb-3 flex flex-wrap items-start justify-between gap-3">
        <div>
          <h2 className="text-sm font-semibold text-text-primary">Shares by submit time</h2>
          <p className="mt-1.5 text-[10px] text-text-muted">
            Stacked by state. Hover any bucket for the per-helper breakdown.
            {lastMinuteStart && (
              <> Last minute begins {formatTime(lastMinuteStart)}.</>
            )}
          </p>
        </div>
        <div className="flex items-center gap-1 rounded-lg border border-border-subtle bg-surface-2 p-0.5">
          <button
            type="button"
            aria-label="Zoom out"
            title="Zoom out"
            onClick={() => updateZoom(zoomLevel - HISTOGRAM_ZOOM_STEP)}
            disabled={zoomLevel <= 1}
            className="inline-flex h-7 w-7 items-center justify-center rounded-md text-text-secondary transition-colors hover:bg-surface-3 hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-35 disabled:hover:bg-transparent"
          >
            <ZoomOut size={14} />
          </button>
          <button
            type="button"
            aria-label="Zoom in"
            title="Zoom in"
            onClick={() => updateZoom(zoomLevel + HISTOGRAM_ZOOM_STEP)}
            disabled={zoomLevel >= HISTOGRAM_MAX_ZOOM}
            className="inline-flex h-7 w-7 items-center justify-center rounded-md text-text-secondary transition-colors hover:bg-surface-3 hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-35 disabled:hover:bg-transparent"
          >
            <ZoomIn size={14} />
          </button>
        </div>
      </div>

      <div
        ref={containerRef}
        className="relative flex gap-2"
        onMouseLeave={() => setHover(null)}
      >
        <div className="relative w-11 shrink-0">
          {[
            { ratio: margin.top / height, value: maxTotal },
            { ratio: (margin.top + plotH / 2) / height, value: Math.round(maxTotal / 2) },
            { ratio: (margin.top + plotH) / height, value: 0 },
          ].map(({ ratio, value }) => (
            <span
              key={ratio}
              className="absolute right-0 -translate-y-1/2 font-mono text-[10px] text-text-muted"
              style={{ top: `${ratio * 100}%` }}
            >
              {value.toLocaleString()}
            </span>
          ))}
        </div>
        <div ref={scrollerRef} className="min-w-0 flex-1 overflow-x-auto">
        <svg
          viewBox={`0 0 ${width} ${height}`}
          className="block max-w-none"
          style={{
            width: `${zoomLevel * 100}%`,
            minWidth: `${Math.round(HISTOGRAM_MIN_WIDTH * zoomLevel)}px`,
          }}
        >
          <rect x={margin.left} y={margin.top} width={plotW} height={plotH} fill="#141414" rx={6} />

          {[0, 0.5, 1].map((ratio) => {
            const y = margin.top + plotH * ratio;
            return (
              <line
                key={ratio}
                x1={margin.left}
                x2={margin.left + plotW}
                y1={y}
                y2={y}
                stroke="#2a2a2a"
              />
            );
          })}

          {lastMinuteX !== null && (
            <g>
              <rect
                x={lastMinuteX}
                y={margin.top}
                width={lastMinuteWidth}
                height={plotH}
                fill={LAST_MINUTE_FILL}
                opacity={0.55}
              />
              <line
                x1={lastMinuteX}
                x2={lastMinuteX}
                y1={margin.top}
                y2={margin.top + plotH}
                stroke={LAST_MINUTE_BORDER}
                strokeDasharray="3 3"
                opacity={0.85}
              />
              <text
                x={lastMinuteX + 5}
                y={margin.top - 6}
                className="fill-text-secondary text-[10px]"
              >
                Last minute window
              </text>
            </g>
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
                opacity={0.85}
              />
              <text
                x={xForTime(nowSeconds) + 5}
                y={margin.top + 12}
                className="fill-text-secondary text-[10px]"
              >
                now
              </text>
            </g>
          )}

          {buckets.map((bucket) => {
            if (bucket.total <= 0) return null;
            // Bars span their full bucket window so adjacent buckets touch,
            // giving the chart a continuous-timeline feel. The "now" line
            // then cuts cleanly through whichever bucket contains it
            // instead of falling into a gap.
            const x = xForTime(bucket.start);
            const barW = Math.max(1, xForTime(bucket.end) - x);
            let y = margin.top + plotH;
            const signal = signalKeys.has(`${bucket.start}:${bucket.end}`);

            const segments = STATE_STACK_ORDER.map((state) => {
              const value = queueStateCount(bucket, state);
              if (value <= 0) return null;
              const h = Math.max(1, margin.top + plotH - yForValue(value));
              y -= h;
              return (
                <rect
                  key={`${bucket.start}:${state}`}
                  x={x}
                  y={y}
                  width={barW}
                  height={h}
                  fill={QUEUE_STATE_META[state].color}
                  stroke="rgba(0,0,0,0.35)"
                  strokeWidth={1}
                  vectorEffect="non-scaling-stroke"
                  pointerEvents="none"
                />
              );
            });

            const handleHover = (event: React.MouseEvent) => {
              const node = containerRef.current;
              if (!node) return;
              const rect = node.getBoundingClientRect();
              setHover({
                bucket,
                x: event.clientX - rect.left,
                y: event.clientY - rect.top,
                containerWidth: node.clientWidth,
                containerHeight: node.clientHeight,
              });
            };
            const isHovered =
              hover?.bucket.start === bucket.start &&
              hover?.bucket.end === bucket.end;

            return (
              <g key={`${bucket.start}:${bucket.end}`}>
                <rect
                  x={x}
                  y={margin.top}
                  width={barW}
                  height={plotH}
                  fill="transparent"
                  pointerEvents="all"
                  onMouseEnter={handleHover}
                  onMouseMove={handleHover}
                />
                {isHovered && (
                  <rect
                    x={x}
                    y={margin.top}
                    width={barW}
                    height={plotH}
                    fill="rgba(255,255,255,0.07)"
                    pointerEvents="none"
                  />
                )}
                {segments}
                {signal && (
                  <rect
                    x={x - 1.5}
                    y={y - 1.5}
                    width={barW + 3}
                    height={margin.top + plotH - y + 3}
                    fill="none"
                    stroke={BACKLOG_SIGNAL_STROKE}
                    strokeWidth={2}
                    pointerEvents="none"
                  />
                )}
              </g>
            );
          })}

          {ticks.map((tick, idx) => {
            const isFirst = idx === 0;
            const isLast = idx === ticks.length - 1;
            const anchor = isFirst ? "start" : isLast ? "end" : "middle";
            return (
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
                  textAnchor={anchor}
                  className="fill-text-muted text-[10px]"
                >
                  {formatTime(tick)}
                </text>
              </g>
            );
          })}
        </svg>
        </div>
        {hover && <BucketHoverOverlay hover={hover} results={results} />}
      </div>

      <div className="mt-3 flex flex-wrap items-center justify-end gap-x-3 gap-y-1 border-t border-border-subtle pt-3 text-[10px] text-text-secondary">
        {STATE_STACK_ORDER.map((state) => (
          <span key={state} className="inline-flex items-center gap-1.5">
            <span
              aria-hidden
              className="h-2 w-2 rounded-sm"
              style={{ backgroundColor: QUEUE_STATE_META[state].color }}
            />
            {QUEUE_STATE_META[state].label}
          </span>
        ))}
        {backlogSignals.length > 0 && (
          <span className="inline-flex items-center gap-1.5">
            <span
              aria-hidden
              className="h-2 w-2 rounded-sm border-2"
              style={{ borderColor: BACKLOG_SIGNAL_STROKE }}
            />
            Backlog growing
          </span>
        )}
      </div>
    </div>
  );
}

function ServerChip({
  server,
  color,
  selected,
  onClick,
}: {
  server: ServiceEntry;
  color: string;
  selected: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      title={server.url}
      className={`flex min-w-0 items-center gap-2 rounded-lg border px-2.5 py-1.5 text-[11px] transition-colors ${
        selected
          ? "border-border-subtle bg-surface-1 text-text-primary"
          : "border-border-subtle/40 bg-surface-2 text-text-muted hover:bg-surface-1"
      }`}
    >
      <span
        aria-hidden
        className="h-2.5 w-2.5 shrink-0 rounded-full"
        style={{
          backgroundColor: selected ? color : "transparent",
          boxShadow: selected ? "none" : `inset 0 0 0 1.5px ${color}`,
        }}
      />
      <span className="truncate font-semibold">{serverLabel(server)}</span>
      <span className="truncate text-[10px] text-text-muted">{shortUrl(server.url)}</span>
    </button>
  );
}

function ServerStatusRow({
  result,
  color,
  nowSeconds,
}: {
  result: QueueServerResult;
  color: string;
  nowSeconds: number;
}) {
  const label = serverLabel(result.server);

  if (result.state === "error") {
    return (
      <div className="flex flex-wrap items-center gap-3 px-4 py-2 text-[11px]">
        <span className="h-2.5 w-2.5 shrink-0 rounded-full bg-danger" />
        <span className="min-w-0 flex-1">
          <span className="block truncate font-semibold text-text-primary">{label}</span>
          <span className="block truncate text-[10px] text-text-muted">
            {shortUrl(result.server.url)}
          </span>
        </span>
        <span className="flex items-center gap-1.5 text-danger">
          <AlertCircle size={12} className="shrink-0" />
          <span className="break-all">{result.error}</span>
        </span>
      </div>
    );
  }

  const totals = queueSummaryTotals(result.summary);
  const stale = isQueueSummaryStale(result.summary, nowSeconds);

  return (
    <div className="flex flex-wrap items-center gap-3 px-4 py-2 text-[11px]">
      <span
        aria-hidden
        className="h-2.5 w-2.5 shrink-0 rounded-full"
        style={{ backgroundColor: color }}
      />
      <span className="min-w-0 flex-1">
        <span className="block truncate font-semibold text-text-primary">{label}</span>
        <span className="block truncate text-[10px] text-text-muted">
          {shortUrl(result.server.url)}
        </span>
      </span>
      <span className="flex flex-wrap items-center gap-3 font-mono text-text-secondary">
        <span>
          <span className="mr-1 font-sans text-text-muted">total</span>
          {totals.total.toLocaleString()}
        </span>
        <span>
          <span className="mr-1 font-sans text-text-muted">submitted</span>
          {totals.submitted.toLocaleString()}
        </span>
        <span>
          <span className="mr-1 font-sans text-text-muted">observed</span>
          {totals.observed_on_chain.toLocaleString()}
        </span>
        <span>
          <span className="mr-1 font-sans text-text-muted">future</span>
          {totals.pending_future.toLocaleString()}
        </span>
        <span>
          <span className="mr-1 font-sans text-text-muted">processing</span>
          {totals.processing.toLocaleString()}
        </span>
        <span className={totals.overdue_pending > 0 ? "text-warning" : ""}>
          <span className="mr-1 font-sans text-text-muted">overdue</span>
          {totals.overdue_pending.toLocaleString()}
        </span>
        <span className={totals.failed > 0 ? "text-danger" : ""}>
          <span className="mr-1 font-sans text-text-muted">failed</span>
          {totals.failed.toLocaleString()}
        </span>
        <span className={totals.missed_deadline > 0 ? "text-danger" : ""}>
          <span className="mr-1 font-sans text-text-muted">missed</span>
          {totals.missed_deadline.toLocaleString()}
        </span>
        {stale && (
          <span className="rounded-full bg-warning/10 px-2 py-0.5 font-sans text-[10px] text-warning">
            stale
          </span>
        )}
      </span>
    </div>
  );
}

export function QueueMonitorPage() {
  const [rounds, setRounds] = useState<chainApi.ChainRound[]>([]);
  const [voteServers, setVoteServers] = useState<ServiceEntry[]>([]);
  const [selectedServerUrls, setSelectedServerUrls] = useState<string[]>([]);
  const [selectedRoundId, setSelectedRoundId] = useState("");
  const [results, setResults] = useState<QueueServerResult[]>([]);
  const [backlogSignals, setBacklogSignals] = useState<QueueBacklogSignal[]>([]);
  const [wallClockSeconds, setWallClockSeconds] = useState(() =>
    Math.floor(Date.now() / 1000)
  );
  const [loading, setLoading] = useState(false);
  const [metadataLoading, setMetadataLoading] = useState(true);
  const [error, setError] = useState("");
  const previousByRoundRef = useRef<Record<string, QueueServerOK[]>>({});
  const summaryRefreshIdRef = useRef(0);
  const metadataRefreshIdRef = useRef(0);
  const metadataForegroundInFlightRef = useRef(false);

  const invalidateSummaries = useCallback(() => {
    summaryRefreshIdRef.current += 1;
    setResults([]);
    setBacklogSignals([]);
    setLoading(false);
  }, []);

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

  const selectedServerUrlSet = useMemo(() => new Set(selectedServerUrls), [selectedServerUrls]);
  const selectedVoteServers = useMemo(
    () => voteServers.filter((server) => selectedServerUrlSet.has(server.url)),
    [selectedServerUrlSet, voteServers]
  );
  const allServersSelected =
    voteServers.length > 0 && selectedVoteServers.length === voteServers.length;

  const serverColorMap = useMemo(() => {
    const map = new Map<string, string>();
    voteServers.forEach((server, idx) => {
      map.set(server.url, SERVER_COLORS[idx % SERVER_COLORS.length]);
    });
    return map;
  }, [voteServers]);
  const serverColor = useCallback(
    (url: string) => serverColorMap.get(url) ?? "#7a7a7a",
    [serverColorMap]
  );

  const selectAllServers = useCallback(() => {
    invalidateSummaries();
    setSelectedServerUrls(voteServers.map((server) => server.url));
  }, [invalidateSummaries, voteServers]);

  const clearSelectedServers = useCallback(() => {
    invalidateSummaries();
    setSelectedServerUrls([]);
  }, [invalidateSummaries]);

  const toggleServer = useCallback((url: string) => {
    invalidateSummaries();
    setSelectedServerUrls((current) =>
      current.includes(url) ? current.filter((item) => item !== url) : [...current, url]
    );
  }, [invalidateSummaries]);

  const refreshMetadata = useCallback(async (options?: { background?: boolean }) => {
    const background = options?.background ?? false;
    if (background && metadataForegroundInFlightRef.current) return;
    const refreshId = ++metadataRefreshIdRef.current;
    if (!background) {
      metadataForegroundInFlightRef.current = true;
      setMetadataLoading(true);
      setError("");
    }
    try {
      const [roundResp, config] = await Promise.all([
        chainApi.listRounds(),
        chainApi.getVotingConfig(),
      ]);
      if (refreshId !== metadataRefreshIdRef.current) return;
      const loadedRounds = roundResp.rounds ?? [];
      const loadedServers = config?.vote_servers ?? [];
      setRounds(loadedRounds);
      setVoteServers((current) =>
        sameServiceEntries(current, loadedServers) ? current : loadedServers
      );
      setSelectedRoundId((current) => {
        if (isRoundIdHex(current)) return current;
        return defaultRoundId(loadedRounds);
      });
    } catch (err) {
      if (refreshId !== metadataRefreshIdRef.current || background) return;
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      if (!background) {
        metadataForegroundInFlightRef.current = false;
        setMetadataLoading(false);
      }
    }
  }, []);

  const refreshSummaries = useCallback(async () => {
    const refreshId = ++summaryRefreshIdRef.current;
    const roundIdHex = selectedRoundId.trim().toLowerCase();
    if (!isRoundIdHex(roundIdHex) || selectedVoteServers.length === 0) {
      setResults([]);
      setBacklogSignals([]);
      setLoading(false);
      return;
    }

    setLoading(true);
    setError("");
    try {
      const nextResults = await fetchQueueSummaries(selectedVoteServers, roundIdHex);
      if (refreshId !== summaryRefreshIdRef.current) return;
      const split = splitQueueResults(nextResults);
      const previous = previousByRoundRef.current[roundIdHex] ?? [];
      const fetchedAt = Math.floor(Date.now() / 1000);
      const nextSignals = detectQueueBacklogSignals(previous, split.ok, fetchedAt);
      previousByRoundRef.current[roundIdHex] = split.ok;
      setResults(nextResults);
      setBacklogSignals(nextSignals);
    } catch (err) {
      if (refreshId !== summaryRefreshIdRef.current) return;
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      if (refreshId === summaryRefreshIdRef.current) {
        setLoading(false);
      }
    }
  }, [selectedRoundId, selectedVoteServers]);

  const handleRefresh = useCallback(() => {
    void refreshSummaries();
    void refreshMetadata();
  }, [refreshMetadata, refreshSummaries]);

  useEffect(() => {
    void refreshMetadata();
  }, [refreshMetadata]);

  useEffect(() => {
    const interval = window.setInterval(() => {
      if (typeof document !== "undefined" && document.visibilityState !== "visible") return;
      void refreshMetadata({ background: true });
    }, METADATA_REFRESH_INTERVAL_MS);
    return () => window.clearInterval(interval);
  }, [refreshMetadata]);

  useEffect(() => {
    setSelectedServerUrls((current) => {
      const urls = voteServers.map((server) => server.url);
      if (urls.length === 0) {
        if (current.length === 0) return current;
        invalidateSummaries();
        return [];
      }
      if (current.length === 0) {
        invalidateSummaries();
        return urls;
      }
      const knownUrls = new Set(urls);
      const kept = current.filter((url) => knownUrls.has(url));
      const next = kept.length > 0 ? kept : urls;
      if (sameStrings(current, next)) return current;
      invalidateSummaries();
      return next;
    });
  }, [invalidateSummaries, voteServers]);

  useEffect(() => {
    void refreshSummaries();
  }, [refreshSummaries]);

  useEffect(() => {
    const interval = setInterval(() => {
      setWallClockSeconds(Math.floor(Date.now() / 1000));
    }, 1000);
    return () => clearInterval(interval);
  }, []);

  const split = useMemo(() => splitQueueResults(results), [results]);
  const aggregateBuckets = useMemo(() => aggregateQueueBuckets(split.ok), [split.ok]);
  const nextBucketRefreshAt = queueNextBucketRefreshAt(split.ok, wallClockSeconds);
  const selectedRound = rounds.find(
    (round) => base64ToHex(round.vote_round_id ?? "") === selectedRoundId
  );
  const totalServers = voteServers.length;
  const voteEndSeconds = selectedRound ? Number(selectedRound.vote_end_time ?? 0) : 0;
  const roundEnded = voteEndSeconds > 0 && wallClockSeconds > voteEndSeconds;
  const autoRefreshActive =
    !metadataLoading &&
    !roundEnded &&
    isRoundIdHex(selectedRoundId) &&
    selectedVoteServers.length > 0;

  useEffect(() => {
    if (typeof document === "undefined") return;
    const handleVisibilityChange = () => {
      if (document.visibilityState !== "visible") return;
      setWallClockSeconds(Math.floor(Date.now() / 1000));
      void refreshMetadata({ background: true });
      if (autoRefreshActive) {
        void refreshSummaries();
      }
    };
    document.addEventListener("visibilitychange", handleVisibilityChange);
    return () => document.removeEventListener("visibilitychange", handleVisibilityChange);
  }, [autoRefreshActive, refreshMetadata, refreshSummaries]);

  useEffect(() => {
    if (!autoRefreshActive) return;
    const interval = setInterval(() => {
      if (typeof document !== "undefined" && document.visibilityState !== "visible") return;
      void refreshSummaries();
    }, AUTO_REFRESH_INTERVAL_MS);
    return () => clearInterval(interval);
  }, [autoRefreshActive, refreshSummaries]);

  useEffect(() => {
    if (!autoRefreshActive || nextBucketRefreshAt === null) return;
    const nowSeconds = Math.floor(Date.now() / 1000);
    const secondsUntilRefresh = nextBucketRefreshAt - nowSeconds;
    const delayMs =
      nextBucketRefreshAt <= nowSeconds
        ? 250
        : Math.max(1_000, secondsUntilRefresh * 1_000 + 1_000);
    const timeout = window.setTimeout(() => {
      if (typeof document !== "undefined" && document.visibilityState !== "visible") return;
      void refreshSummaries();
    }, delayMs);
    return () => window.clearTimeout(timeout);
  }, [autoRefreshActive, nextBucketRefreshAt, refreshSummaries]);

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
            {autoRefreshActive && (
              <span
                className="inline-flex items-center gap-1.5 pr-1 text-[10px] text-text-muted"
                title="Auto-refreshing every 30s while this tab is visible"
              >
                <span
                  aria-hidden
                  className="h-1.5 w-1.5 animate-pulse rounded-full bg-accent"
                />
                Live
              </span>
            )}
            <button
              type="button"
              onClick={handleRefresh}
              disabled={metadataLoading || loading}
              title="Refresh queue summaries immediately and reload the rounds list and helper config"
              className="inline-flex items-center gap-2 rounded-lg bg-accent/90 px-3 py-2 text-[11px] font-semibold text-surface-0 transition-colors hover:bg-accent disabled:opacity-50"
            >
              <RefreshCw size={13} className={metadataLoading || loading ? "animate-spin" : ""} />
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
          <div className="grid gap-3 lg:grid-cols-[minmax(260px,1fr)_33rem]">
            <label className="block min-w-0">
              <span className="mb-1 block text-[10px] uppercase tracking-wider text-text-muted">
                Round
              </span>
              <select
                value={
                  roundOptions.some((round) => round.id === selectedRoundId)
                    ? selectedRoundId
                    : ""
                }
                onChange={(event) => {
                  invalidateSummaries();
                  setSelectedRoundId(event.target.value);
                }}
                disabled={metadataLoading || roundOptions.length === 0}
                className="w-full truncate rounded-lg border border-border-subtle bg-surface-2 py-2 pl-3 pr-8 text-xs text-text-primary outline-none [color-scheme:dark] disabled:opacity-50"
              >
                {metadataLoading && <option value="">Loading rounds...</option>}
                {!metadataLoading && roundOptions.length === 0 && (
                  <option value="">No chain rounds found</option>
                )}
                {!metadataLoading &&
                  roundOptions.map((round) => (
                    <option key={round.id} value={round.id}>
                      {round.label}
                    </option>
                  ))}
              </select>
            </label>

            <label className="block">
              <span className="mb-1 block text-[10px] uppercase tracking-wider text-text-muted">
                Round ID
              </span>
              <input
                value={selectedRoundId}
                onChange={(event) => {
                  invalidateSummaries();
                  setSelectedRoundId(event.target.value.trim());
                }}
                placeholder="64 character round id"
                className="w-full rounded-lg border border-border-subtle bg-surface-2 px-3 py-2 font-mono text-xs text-text-primary outline-none placeholder:text-text-muted"
              />
            </label>
          </div>
          {selectedRound && voteEndSeconds > 0 && (
            <p className="mt-2 text-[11px] text-text-muted">
              {selectedRound.title || selectedRound.description || "Selected round"} — voting{" "}
              {roundEnded ? "ended" : "ends"} {formatTime(voteEndSeconds)}
              <span
                className={`ml-1 ${roundEnded ? "text-text-muted" : "text-text-secondary"}`}
              >
                ({formatCountdown(voteEndSeconds, wallClockSeconds)})
              </span>
            </p>
          )}
          {voteServers.length > 0 && !roundEnded && (
            <div className="mt-3 rounded-lg border border-border-subtle bg-surface-2 p-3">
              <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
                <span className="text-[10px] uppercase tracking-wider text-text-muted">
                  Servers
                </span>
                <div className="flex items-center gap-2 text-[10px]">
                  <span className="text-text-muted">
                    {selectedVoteServers.length}/{voteServers.length} selected
                  </span>
                  <button
                    type="button"
                    onClick={selectAllServers}
                    disabled={allServersSelected}
                    className="rounded-md bg-surface-3 px-2 py-1 font-semibold text-text-secondary transition-colors hover:bg-surface-1 disabled:opacity-45"
                  >
                    All
                  </button>
                  <button
                    type="button"
                    onClick={clearSelectedServers}
                    disabled={selectedVoteServers.length === 0}
                    className="rounded-md bg-surface-3 px-2 py-1 font-semibold text-text-secondary transition-colors hover:bg-surface-1 disabled:opacity-45"
                  >
                    None
                  </button>
                </div>
              </div>
              <div className="flex flex-wrap gap-2">
                {voteServers.map((server) => (
                  <ServerChip
                    key={server.url}
                    server={server}
                    color={serverColor(server.url)}
                    selected={selectedServerUrlSet.has(server.url)}
                    onClick={() => toggleServer(server.url)}
                  />
                ))}
              </div>
            </div>
          )}
        </section>

        {roundEnded && (
          <div className="mb-5 flex items-start gap-3 rounded-xl border border-border-subtle bg-surface-2 p-5">
            <Clock size={20} className="mt-0.5 shrink-0 text-text-secondary" />
            <div>
              <p className="text-sm font-semibold text-text-primary">
                Round ended {formatCountdown(voteEndSeconds, wallClockSeconds)}
              </p>
              <p className="mt-1 text-[11px] text-text-muted">
                Helper servers purge share data after a round closes, so there's nothing to
                display for this round. Pick a different round above to view its queue activity.
              </p>
            </div>
          </div>
        )}

        {backlogSignals.length > 0 && !roundEnded && (
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
        ) : roundEnded ? null : selectedVoteServers.length === 0 ? (
          <div className="rounded-xl border border-border-subtle bg-surface-1 p-6 text-center text-xs text-text-muted">
            Select at least one vote server.
          </div>
        ) : (
          <div className="space-y-5">
            {split.ok.length > 0 ? (
              <QueueHistogram
                results={split.ok}
                backlogSignals={roundEnded ? [] : backlogSignals}
                nowSeconds={wallClockSeconds}
              />
            ) : (
              <div className="rounded-xl border border-border-subtle bg-surface-1 p-6 text-center text-xs text-text-muted">
                {loading ? (
                  <span className="inline-flex items-center gap-2">
                    <Loader2 size={14} className="animate-spin" />
                    Loading queue summaries…
                  </span>
                ) : roundEnded ? (
                  "Share data has been purged from helpers since this round closed."
                ) : (
                  "No queue summaries available for this round."
                )}
              </div>
            )}

            <section className="rounded-xl border border-border-subtle bg-surface-1">
              <header className="flex flex-wrap items-center justify-between gap-2 border-b border-border-subtle px-4 py-2.5">
                <h2 className="text-sm font-semibold text-text-primary">Server status</h2>
                <span className="text-[10px] text-text-muted">
                  {split.ok.length} reporting · {split.unavailable.length} unavailable
                </span>
              </header>
              <div className="divide-y divide-border-subtle/60">
                {results.length === 0 && loading ? (
                  selectedVoteServers.map((server) => (
                    <div
                      key={server.url}
                      className="flex items-center gap-2 px-4 py-2 text-[11px] text-text-muted"
                    >
                      <Loader2 size={12} className="animate-spin" />
                      {serverLabel(server)}
                    </div>
                  ))
                ) : (
                  results.map((result) => (
                    <ServerStatusRow
                      key={result.server.url}
                      result={result}
                      color={serverColor(result.server.url)}
                      nowSeconds={wallClockSeconds}
                    />
                  ))
                )}
              </div>
            </section>

            {aggregateBuckets.length > 0 && (
              <details className="group rounded-xl border border-border-subtle bg-surface-1">
                <summary className="flex cursor-pointer list-none items-center justify-between gap-3 px-4 py-2.5 [&::-webkit-details-marker]:hidden">
                  <span className="text-sm font-semibold text-text-primary">Bucket detail</span>
                  <ChevronDown
                    size={14}
                    aria-hidden
                    className="text-text-muted transition-transform duration-150 group-open:rotate-180"
                  />
                </summary>
                <div className="max-h-80 overflow-auto border-t border-border-subtle">
                  <table className="w-full text-[11px]">
                    <thead className="sticky top-0 bg-surface-1 text-[10px] uppercase tracking-wider text-text-muted">
                      <tr className="border-b border-border-subtle">
                        <th className="px-3 py-2 text-left font-medium">Bucket</th>
                        <th className="px-2 py-2 text-right font-medium">Total</th>
                        <th className="px-2 py-2 text-right font-medium">
                          {QUEUE_STATE_META.submitted.label}
                        </th>
                        <th className="px-2 py-2 text-right font-medium">
                          {QUEUE_STATE_META.observed_on_chain.label}
                        </th>
                        <th className="px-2 py-2 text-right font-medium">
                          {QUEUE_STATE_META.pending_future.label}
                        </th>
                        <th className="px-2 py-2 text-right font-medium">
                          {QUEUE_STATE_META.processing.label}
                        </th>
                        <th className="px-2 py-2 text-right font-medium">
                          {QUEUE_STATE_META.overdue_pending.label}
                        </th>
                        <th className="px-2 py-2 text-right font-medium">
                          {QUEUE_STATE_META.failed.label}
                        </th>
                        <th className="px-2 py-2 text-right font-medium">
                          {QUEUE_STATE_META.missed_deadline.label}
                        </th>
                      </tr>
                    </thead>
                    <tbody>
                      {aggregateBuckets.map((bucket) => {
                        const backlog = bucket.overdue_pending + bucket.processing;
                        return (
                          <tr
                            key={`${bucket.start}:${bucket.end}`}
                            className={`border-b border-border-subtle/40 ${
                              backlog > 0 ? "bg-warning/10" : ""
                            }`}
                          >
                            <td className="whitespace-nowrap px-3 py-1.5 text-text-muted">
                              {formatTime(bucket.start)} – {formatTime(bucket.end)}
                            </td>
                            <td className="px-2 py-1.5 text-right font-mono text-text-primary">
                              {bucket.total.toLocaleString()}
                            </td>
                            <td className="px-2 py-1.5 text-right font-mono text-text-secondary">
                              {bucket.submitted.toLocaleString()}
                            </td>
                            <td className="px-2 py-1.5 text-right font-mono text-text-secondary">
                              {(bucket.observed_on_chain ?? 0).toLocaleString()}
                            </td>
                            <td className="px-2 py-1.5 text-right font-mono text-text-secondary">
                              {bucket.pending_future.toLocaleString()}
                            </td>
                            <td className="px-2 py-1.5 text-right font-mono text-text-secondary">
                              {bucket.processing.toLocaleString()}
                            </td>
                            <td className="px-2 py-1.5 text-right font-mono text-text-secondary">
                              {bucket.overdue_pending.toLocaleString()}
                            </td>
                            <td className="px-2 py-1.5 text-right font-mono text-text-secondary">
                              {bucket.failed.toLocaleString()}
                            </td>
                            <td className="px-2 py-1.5 text-right font-mono text-text-secondary">
                              {(bucket.missed_deadline ?? 0).toLocaleString()}
                            </td>
                          </tr>
                        );
                      })}
                    </tbody>
                  </table>
                </div>
              </details>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
