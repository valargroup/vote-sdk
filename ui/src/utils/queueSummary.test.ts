import { describe, expect, it } from "vitest";
import type { QueueSummaryResponse, ServiceEntry } from "../api/chain";
import {
  aggregateQueueBuckets,
  alignQueueBuckets,
  detectQueueBacklogSignals,
  isQueueSummaryStale,
  queueAggregateMaxBucketTotal,
  queueNextBucketRefreshAt,
  queueSingleShareWindowStart,
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

  it("aggregates aligned bucket windows across selected servers", () => {
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

    const buckets = aggregateQueueBuckets([a, b]);
    expect(buckets.map((bucket) => bucket.total)).toEqual([6, 16, 1]);
    expect(buckets[1].submitted).toBe(11);
    expect(buckets[1].pending_future).toBe(4);
    expect(queueAggregateMaxBucketTotal(buckets)).toBe(16);
  });

  it("summarizes totals and chart scale", () => {
    const s = summary();
    expect(queueSummaryTotals(s)).toEqual({
      submitted: 3,
      observed_on_chain: 0,
      pending_future: 4,
      overdue_pending: 2,
      processing: 3,
      failed: 1,
      missed_deadline: 0,
      total: 13,
    });
    expect(queueSummaryMaxBucketTotal([s])).toBe(7);
  });

  it("includes closeout states in totals and aggregates", () => {
    const s = summary({
      buckets: [
        {
          start: 1000,
          end: 1060,
          submitted: 1,
          observed_on_chain: 2,
          pending_future: 0,
          overdue_pending: 0,
          processing: 0,
          failed: 1,
          missed_deadline: 3,
          total: 7,
        },
      ],
    });
    const result: QueueServerOK = { state: "ok", server: serverA, summary: s };
    const [bucket] = aggregateQueueBuckets([result]);

    expect(queueSummaryTotals(s)).toEqual({
      submitted: 1,
      observed_on_chain: 2,
      pending_future: 0,
      overdue_pending: 0,
      processing: 0,
      failed: 1,
      missed_deadline: 3,
      total: 7,
    });
    expect(bucket.observed_on_chain).toBe(2);
    expect(bucket.missed_deadline).toBe(3);
  });

  it("uses the capped single-share window policy", () => {
    expect(queueSingleShareWindowStart(1000, 1600)).toBe(1360);
    expect(queueSingleShareWindowStart(1000, 4600)).toBe(3160);
    expect(queueSingleShareWindowStart(1000, 8200)).toBe(5320);
    expect(queueSingleShareWindowStart(1000, 1000 + 7 * 24 * 3600)).toBe(1000 + 7 * 24 * 3600 - 6 * 3600);
    expect(queueSingleShareWindowStart(1000, 1000)).toBeNull();
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

  it("finds the next bucket boundary refresh time", () => {
    const result: QueueServerOK = {
      state: "ok",
      server: serverA,
      summary: summary({
        generated_at: 1115,
        buckets: [
          {
            start: 1000,
            end: 1060,
            submitted: 1,
            pending_future: 0,
            overdue_pending: 0,
            processing: 0,
            failed: 0,
            total: 1,
          },
          {
            start: 1060,
            end: 1120,
            submitted: 0,
            pending_future: 0,
            overdue_pending: 0,
            processing: 1,
            failed: 0,
            total: 1,
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

    expect(queueNextBucketRefreshAt([result], 1116)).toBe(1120);
    expect(queueNextBucketRefreshAt([result], 1121)).toBe(1121);
    expect(
      queueNextBucketRefreshAt(
        [{ ...result, summary: { ...result.summary, generated_at: 1121 } }],
        1121
      )
    ).toBe(1180);
    expect(queueNextBucketRefreshAt([], 1121)).toBeNull();
  });
});
