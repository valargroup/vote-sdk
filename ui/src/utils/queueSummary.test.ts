import { describe, expect, it } from "vitest";
import type { QueueSummaryResponse, ServiceEntry } from "../api/chain";
import {
  alignQueueBuckets,
  detectQueueBacklogSignals,
  isQueueSummaryStale,
  queueSummaryMaxBucketTotal,
  queueSummaryTotals,
  splitQueueResults,
  type QueueServerOK,
  type QueueServerResult,
} from "./queueSummary";

const serverA: ServiceEntry = { url: "https://a.example", label: "a" };
const serverB: ServiceEntry = { url: "https://b.example", label: "b" };

function summary(patch: Partial<QueueSummaryResponse> = {}): QueueSummaryResponse {
  return {
    round_id: "ab".repeat(32),
    bucket_seconds: 60,
    created_at_time: 1000,
    vote_end_time: 1180,
    generated_at: 1120,
    last_minute_start: 1120,
    buckets: [
      {
        start: 1000,
        end: 1060,
        submitted: 1,
        pending_future: 0,
        overdue_pending: 2,
        processing: 3,
        failed: 0,
        total: 6,
      },
      {
        start: 1060,
        end: 1120,
        submitted: 2,
        pending_future: 4,
        overdue_pending: 0,
        processing: 0,
        failed: 1,
        total: 7,
      },
    ],
    ...patch,
  };
}

describe("queue summary helpers", () => {
  it("aligns bucket windows across servers", () => {
    const a: QueueServerOK = { state: "ok", server: serverA, summary: summary() };
    const b: QueueServerOK = {
      state: "ok",
      server: serverB,
      summary: summary({
        buckets: [
          {
            start: 1060,
            end: 1120,
            submitted: 9,
            pending_future: 0,
            overdue_pending: 0,
            processing: 0,
            failed: 0,
            total: 9,
          },
          {
            start: 1120,
            end: 1180,
            submitted: 0,
            pending_future: 1,
            overdue_pending: 0,
            processing: 0,
            failed: 0,
            total: 1,
          },
        ],
      }),
    };

    const aligned = alignQueueBuckets([a, b]);
    expect(aligned.map((bucket) => [bucket.start, bucket.end])).toEqual([
      [1000, 1060],
      [1060, 1120],
      [1120, 1180],
    ]);
    expect(aligned[0].byServer[serverA.url]?.total).toBe(6);
    expect(aligned[0].byServer[serverB.url]).toBeNull();
    expect(aligned[2].byServer[serverA.url]).toBeNull();
    expect(aligned[2].byServer[serverB.url]?.pending_future).toBe(1);
  });

  it("summarizes totals and chart scale", () => {
    const s = summary();
    expect(queueSummaryTotals(s)).toEqual({
      submitted: 3,
      pending_future: 4,
      overdue_pending: 2,
      processing: 3,
      failed: 1,
      total: 13,
    });
    expect(queueSummaryMaxBucketTotal([s])).toBe(7);
  });

  it("separates unavailable servers", () => {
    const results: QueueServerResult[] = [
      { state: "ok", server: serverA, summary: summary() },
      { state: "error", server: serverB, error: "HTTP 404" },
    ];
    const split = splitQueueResults(results);
    expect(split.ok).toHaveLength(1);
    expect(split.unavailable).toHaveLength(1);
    expect(split.unavailable[0].server.url).toBe(serverB.url);
  });

  it("flags backlog growth in old buckets when submitted does not increase", () => {
    const previous: QueueServerOK[] = [
      {
        state: "ok",
        server: serverA,
        summary: summary({
          buckets: [
            {
              start: 1000,
              end: 1060,
              submitted: 3,
              pending_future: 0,
              overdue_pending: 1,
              processing: 1,
              failed: 0,
              total: 5,
            },
          ],
        }),
      },
    ];
    const current: QueueServerOK[] = [
      {
        state: "ok",
        server: serverA,
        summary: summary({
          buckets: [
            {
              start: 1000,
              end: 1060,
              submitted: 3,
              pending_future: 0,
              overdue_pending: 4,
              processing: 2,
              failed: 0,
              total: 9,
            },
          ],
        }),
      },
    ];

    expect(detectQueueBacklogSignals(previous, current, 1200)).toEqual([
      {
        serverUrl: serverA.url,
        label: "a",
        bucketStart: 1000,
        bucketEnd: 1060,
        backlogDelta: 4,
      },
    ]);
  });

  it("does not flag fresh or stale-free summaries incorrectly", () => {
    expect(isQueueSummaryStale(summary({ generated_at: 1000, bucket_seconds: 60 }), 1201)).toBe(false);
    expect(isQueueSummaryStale(summary({ generated_at: 1000, bucket_seconds: 60 }), 1401)).toBe(true);
  });
});
