import type { QueueSummaryBucket, QueueSummaryResponse, ServiceEntry } from "../api/chain";

export type QueueStateKey =
  | "submitted"
  | "pending_future"
  | "overdue_pending"
  | "processing"
  | "failed";

export const QUEUE_STATE_ORDER: QueueStateKey[] = [
  "failed",
  "processing",
  "overdue_pending",
  "pending_future",
  "submitted",
];

export const QUEUE_STATE_META: Record<QueueStateKey, { label: string; color: string }> = {
  submitted: { label: "Submitted", color: "#4a9a4a" },
  pending_future: { label: "Future", color: "#3b82f6" },
  overdue_pending: { label: "Overdue", color: "#c4943a" },
  processing: { label: "Processing", color: "#a855f7" },
  failed: { label: "Failed", color: "#c44a4a" },
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

export function queueSummaryTotals(summary: QueueSummaryResponse): Record<QueueStateKey | "total", number> {
  return summary.buckets.reduce(
    (acc, bucket) => {
      acc.submitted += bucket.submitted;
      acc.pending_future += bucket.pending_future;
      acc.overdue_pending += bucket.overdue_pending;
      acc.processing += bucket.processing;
      acc.failed += bucket.failed;
      acc.total += bucket.total;
      return acc;
    },
    {
      submitted: 0,
      pending_future: 0,
      overdue_pending: 0,
      processing: 0,
      failed: 0,
      total: 0,
    }
  );
}

export function queueSummaryMaxBucketTotal(summaries: QueueSummaryResponse[]): number {
  return Math.max(1, ...summaries.flatMap((summary) => summary.buckets.map((bucket) => bucket.total)));
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
    lastMinuteStart: lastMinuteStarts.length > 0 ? Math.min(...lastMinuteStarts) : null,
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

export function isQueueSummaryStale(summary: QueueSummaryResponse, nowSeconds: number): boolean {
  const maxAge = Math.max(300, summary.bucket_seconds * 2);
  return summary.generated_at > 0 && nowSeconds - summary.generated_at > maxAge;
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
