import type { QueueSummaryBucket, QueueSummaryResponse, ServiceEntry } from "../api/chain";

export type QueueStateKey =
  | "submitted"
  | "observed_on_chain"
  | "pending_future"
  | "overdue_pending"
  | "processing"
  | "failed"
  | "missed_deadline";

export const QUEUE_STATE_ORDER: QueueStateKey[] = [
  "failed",
  "missed_deadline",
  "processing",
  "overdue_pending",
  "pending_future",
  "observed_on_chain",
  "submitted",
];

export const QUEUE_STATE_META: Record<QueueStateKey, { label: string; color: string }> = {
  submitted: { label: "Submitted", color: "#4a9a4a" },
  observed_on_chain: { label: "Observed", color: "#65a30d" },
  pending_future: { label: "Future", color: "#3b82f6" },
  overdue_pending: { label: "Overdue", color: "#c4943a" },
  processing: { label: "Processing", color: "#a855f7" },
  failed: { label: "Failed", color: "#c44a4a" },
  missed_deadline: { label: "Missed", color: "#ef4444" },
};

export interface QueueServerOK {
  state: "ok";
  server: ServiceEntry;
  summary: QueueSummaryResponse;
}

export interface QueueServerError {
  state: "error";
  server: ServiceEntry;
  error: string;
}

export type QueueServerResult = QueueServerOK | QueueServerError;

export interface AlignedQueueBucket {
  start: number;
  end: number;
  byServer: Record<string, QueueSummaryBucket | null>;
}

export interface AggregatedQueueBucket extends QueueSummaryBucket {
  byServer: Record<string, QueueSummaryBucket | null>;
}

export interface QueueBacklogSignal {
  serverUrl: string;
  label: string;
  bucketStart: number;
  bucketEnd: number;
  backlogDelta: number;
}

export function queueBucketBacklog(bucket: Pick<QueueSummaryBucket, "overdue_pending" | "processing">): number {
  return bucket.overdue_pending + bucket.processing;
}

export function queueStateCount(bucket: QueueSummaryBucket, state: QueueStateKey): number {
  return bucket[state] ?? 0;
}

export function queueSummaryTotals(summary: QueueSummaryResponse): Record<QueueStateKey | "total", number> {
  return summary.buckets.reduce(
    (acc, bucket) => {
      acc.submitted += bucket.submitted;
      acc.observed_on_chain += bucket.observed_on_chain ?? 0;
      acc.pending_future += bucket.pending_future;
      acc.overdue_pending += bucket.overdue_pending;
      acc.processing += bucket.processing;
      acc.failed += bucket.failed;
      acc.missed_deadline += bucket.missed_deadline ?? 0;
      acc.total += bucket.total;
      return acc;
    },
    {
      submitted: 0,
      observed_on_chain: 0,
      pending_future: 0,
      overdue_pending: 0,
      processing: 0,
      failed: 0,
      missed_deadline: 0,
      total: 0,
    }
  );
}

export function queueSummaryMaxBucketTotal(summaries: QueueSummaryResponse[]): number {
  return Math.max(1, ...summaries.flatMap((summary) => summary.buckets.map((bucket) => bucket.total)));
}

export function queueAggregateMaxBucketTotal(buckets: Pick<QueueSummaryBucket, "total">[]): number {
  return Math.max(1, ...buckets.map((bucket) => bucket.total));
}

export function queueSingleShareWindowStart(start: number, end: number): number | null {
  if (!Number.isFinite(start) || !Number.isFinite(end) || end <= start) return null;

  const duration = end - start;
  const maxWindow = 6 * 60 * 60;
  const window = Math.max(1, Math.floor(Math.min(duration * 0.4, maxWindow)));

  return end - window;
}

export function queueSummaryDomain(summaries: QueueSummaryResponse[]): {
  start: number;
  end: number;
  lastMinuteStart: number | null;
} | null {
  if (summaries.length === 0) return null;
  const start = Math.min(...summaries.map((summary) => summary.created_at_time));
  const end = Math.max(...summaries.map((summary) => summary.vote_end_time));
  const lastMinuteStarts = summaries
    .map((summary) => summary.last_minute_start)
    .filter((value) => Number.isFinite(value) && value > 0);
  return {
    start,
    end,
    lastMinuteStart: queueSingleShareWindowStart(start, end) ??
      (lastMinuteStarts.length > 0 ? Math.min(...lastMinuteStarts) : null),
  };
}

function bucketKey(start: number, end: number): string {
  return `${start}:${end}`;
}

export function alignQueueBuckets(results: QueueServerOK[]): AlignedQueueBucket[] {
  const windows = new Map<string, { start: number; end: number }>();
  for (const result of results) {
    for (const bucket of result.summary.buckets) {
      windows.set(bucketKey(bucket.start, bucket.end), {
        start: bucket.start,
        end: bucket.end,
      });
    }
  }

  return [...windows.values()]
    .sort((a, b) => a.start - b.start || a.end - b.end)
    .map((window) => {
      const byServer: Record<string, QueueSummaryBucket | null> = {};
      for (const result of results) {
        byServer[result.server.url] =
          result.summary.buckets.find(
            (bucket) => bucket.start === window.start && bucket.end === window.end
          ) ?? null;
      }
      return { ...window, byServer };
    });
}

export function aggregateQueueBuckets(results: QueueServerOK[]): AggregatedQueueBucket[] {
  return alignQueueBuckets(results).map((window) => {
    const bucket: AggregatedQueueBucket = {
      start: window.start,
      end: window.end,
      submitted: 0,
      observed_on_chain: 0,
      pending_future: 0,
      overdue_pending: 0,
      processing: 0,
      failed: 0,
      missed_deadline: 0,
      total: 0,
      byServer: window.byServer,
    };

    for (const result of results) {
      const serverBucket = window.byServer[result.server.url];
      if (!serverBucket) continue;
      bucket.submitted += serverBucket.submitted;
      bucket.observed_on_chain =
        queueStateCount(bucket, "observed_on_chain") + queueStateCount(serverBucket, "observed_on_chain");
      bucket.pending_future += serverBucket.pending_future;
      bucket.overdue_pending += serverBucket.overdue_pending;
      bucket.processing += serverBucket.processing;
      bucket.failed += serverBucket.failed;
      bucket.missed_deadline =
        queueStateCount(bucket, "missed_deadline") + queueStateCount(serverBucket, "missed_deadline");
      bucket.total += serverBucket.total;
    }

    return bucket;
  });
}

export function isQueueSummaryStale(summary: QueueSummaryResponse, nowSeconds: number): boolean {
  const maxAge = Math.max(300, summary.bucket_seconds * 2);
  return summary.generated_at > 0 && nowSeconds - summary.generated_at > maxAge;
}

export function queueNextBucketRefreshAt(
  results: QueueServerOK[],
  nowSeconds: number
): number | null {
  if (results.length === 0 || !Number.isFinite(nowSeconds)) return null;

  const generatedAt = Math.max(
    ...results
      .map((result) => result.summary.generated_at)
      .filter((value) => Number.isFinite(value) && value > 0)
  );
  const latestGeneratedAt = Number.isFinite(generatedAt) ? generatedAt : nowSeconds;
  const boundaries = new Set<number>();

  for (const result of results) {
    for (const bucket of result.summary.buckets) {
      if (Number.isFinite(bucket.start)) boundaries.add(bucket.start);
      if (Number.isFinite(bucket.end)) boundaries.add(bucket.end);
    }
  }

  const missedBoundary = [...boundaries].some(
    (boundary) => boundary > latestGeneratedAt && boundary <= nowSeconds
  );
  if (missedBoundary) return nowSeconds;

  const futureBoundaries = [...boundaries].filter((boundary) => boundary > nowSeconds);
  if (futureBoundaries.length === 0) return null;
  return Math.min(...futureBoundaries);
}

export function detectQueueBacklogSignals(
  previous: QueueServerOK[],
  current: QueueServerOK[],
  nowSeconds: number
): QueueBacklogSignal[] {
  const previousByServer = new Map(previous.map((result) => [result.server.url, result]));
  const signals: QueueBacklogSignal[] = [];

  for (const currentResult of current) {
    const previousResult = previousByServer.get(currentResult.server.url);
    if (!previousResult) continue;

    const previousBuckets = new Map(
      previousResult.summary.buckets.map((bucket) => [bucketKey(bucket.start, bucket.end), bucket])
    );
    for (const bucket of currentResult.summary.buckets) {
      if (bucket.end > nowSeconds) continue;
      const previousBucket = previousBuckets.get(bucketKey(bucket.start, bucket.end));
      if (!previousBucket) continue;

      const backlogDelta = queueBucketBacklog(bucket) - queueBucketBacklog(previousBucket);
      const submittedDelta = bucket.submitted - previousBucket.submitted;
      if (backlogDelta > 0 && submittedDelta <= 0) {
        signals.push({
          serverUrl: currentResult.server.url,
          label: currentResult.server.label || currentResult.server.url,
          bucketStart: bucket.start,
          bucketEnd: bucket.end,
          backlogDelta,
        });
      }
    }
  }

  return signals;
}

export function splitQueueResults(results: QueueServerResult[]): {
  ok: QueueServerOK[];
  unavailable: QueueServerError[];
} {
  return {
    ok: results.filter((result): result is QueueServerOK => result.state === "ok"),
    unavailable: results.filter((result): result is QueueServerError => result.state === "error"),
  };
}
