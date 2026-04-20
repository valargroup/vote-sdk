// Side-by-side status card for every PIR endpoint declared in the
// published voting-config. Intended to stand in for the
// `curl /root | jq '.height, .root25, .root29'` loop that
// `shielded-vote-book/operations/snapshot-bumps.md` step 5 used to
// run by hand — the table renders one row per endpoint, fetches
// /root in parallel, and visibly marks divergence across the
// successful rows so an operator can confirm convergence without
// dropping into a terminal.
//
// Addresses vote-sdk#119. Paired with the runbook update in
// shielded-vote-book#9.

import { useState, useEffect, useCallback } from "react";
import {
  RefreshCw,
  CheckCircle2,
  AlertCircle,
  AlertTriangle,
  Copy,
  Check,
  Loader2,
} from "lucide-react";
import type { ServiceEntry } from "../api/chain";

// The shape nf-server's /root handler actually returns. `height` can
// be null if the replica has no snapshot loaded yet (fresh bootstrap
// fell through, or it's still warming up). All hex fields are hex
// strings without the 0x prefix.
interface PirRootResponse {
  height: number | null;
  num_ranges: number;
  root25?: string;
  root29?: string;
  pir_depth?: number;
}

type PirEndpointStatus =
  | { state: "loading" }
  | { state: "ok"; data: PirRootResponse }
  | { state: "error"; error: string };

// Browsers happily render a 64-char hex string, but three of them in a
// row next to each other on /settings eats a lot of horizontal space
// and makes it harder to eyeball "do these two rows match". Truncate
// for display, keep the full value reachable via the tooltip / copy
// button.
function truncateHex(s: string, head = 6, tail = 4): string {
  if (s.length <= head + tail + 1) return s;
  return `${s.slice(0, head)}…${s.slice(-tail)}`;
}

function CopyHex({ value }: { value?: string }) {
  const [copied, setCopied] = useState(false);
  if (!value) return <span className="text-text-muted">—</span>;

  const onClick = () => {
    navigator.clipboard.writeText(value).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  };

  return (
    <button
      onClick={onClick}
      title={`${value} (click to copy)`}
      className="inline-flex items-center gap-1 font-mono text-[11px] text-text-primary hover:text-accent cursor-pointer transition-colors"
    >
      <span>{truncateHex(value)}</span>
      {copied ? (
        <Check size={10} className="text-success" />
      ) : (
        <Copy size={10} className="text-text-muted" />
      )}
    </button>
  );
}

// Parallel /root fetch across every endpoint, with a per-endpoint
// ok/error/loading state so one replica being down doesn't hide the
// rest. Re-fetches when the set of endpoint URLs changes.
function usePirFleetStatus(endpoints: ServiceEntry[]) {
  const [statuses, setStatuses] = useState<Record<string, PirEndpointStatus>>(
    {}
  );
  const [refreshing, setRefreshing] = useState(false);

  // Dep key: stable join of URLs so the effect doesn't re-fire on every
  // parent render just because `endpoints` is a fresh array reference.
  const key = endpoints.map((e) => e.url).join("|");

  const refresh = useCallback(() => {
    setRefreshing(true);
    const initial: Record<string, PirEndpointStatus> = {};
    for (const ep of endpoints) initial[ep.url] = { state: "loading" };
    setStatuses(initial);

    Promise.all(
      endpoints.map(async (ep): Promise<[string, PirEndpointStatus]> => {
        try {
          const res = await fetch(`${ep.url}/root`);
          if (!res.ok) throw new Error(`HTTP ${res.status}`);
          const data = (await res.json()) as PirRootResponse;
          return [ep.url, { state: "ok", data }];
        } catch (err) {
          return [
            ep.url,
            {
              state: "error",
              error: err instanceof Error ? err.message : String(err),
            },
          ];
        }
      })
    ).then((results) => {
      setStatuses(Object.fromEntries(results));
      setRefreshing(false);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);

  useEffect(() => {
    refresh();
  }, [refresh]);

  return { statuses, refreshing, refresh };
}

interface Props {
  endpoints: ServiceEntry[];
  /** URL of the endpoint the wallet / page is configured to use, so we
   * can highlight "this is the row driving your queries". */
  selectedUrl?: string;
  /** snapshot_height from the published voting-config. When provided,
   * renders a warning row if any successful replica is at a different
   * height (which can happen legitimately for a few minutes during a
   * bump while one replica is still bootstrapping). */
  expectedHeight?: number;
}

export function PirFleetStatus({
  endpoints,
  selectedUrl,
  expectedHeight,
}: Props) {
  const { statuses, refreshing, refresh } = usePirFleetStatus(endpoints);

  if (endpoints.length === 0) {
    return (
      <p className="text-[10px] text-text-muted">
        No PIR endpoints configured in voting-config.
      </p>
    );
  }

  // Divergence across successful rows is the thing we're actually here
  // to check. An error row can't tell us anything, so it's not part of
  // the comparison — ops can still see that the error happened in the
  // status column.
  const okRows = endpoints
    .map((ep) => ({ ep, status: statuses[ep.url] }))
    .filter(
      (
        r
      ): r is {
        ep: ServiceEntry;
        status: { state: "ok"; data: PirRootResponse };
      } => r.status?.state === "ok"
    );

  const heights = new Set(okRows.map((r) => r.status.data.height));
  const root25s = new Set(okRows.map((r) => r.status.data.root25 ?? ""));
  const root29s = new Set(okRows.map((r) => r.status.data.root29 ?? ""));
  const heightDiverges = okRows.length > 1 && heights.size > 1;
  const root25Diverges = okRows.length > 1 && root25s.size > 1;
  const root29Diverges = okRows.length > 1 && root29s.size > 1;
  const anyDivergence = heightDiverges || root25Diverges || root29Diverges;

  const heightMismatchesExpected =
    expectedHeight !== undefined &&
    expectedHeight > 0 &&
    okRows.some((r) => r.status.data.height !== expectedHeight);

  const divergingFields = [
    heightDiverges && "height",
    root25Diverges && "root25",
    root29Diverges && "root29",
  ]
    .filter(Boolean)
    .join(" / ");

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <h3 className="text-[11px] font-semibold text-text-primary">
          Fleet status{" "}
          <span className="text-text-muted font-normal">
            ({endpoints.length} endpoint{endpoints.length === 1 ? "" : "s"})
          </span>
        </h3>
        <button
          onClick={refresh}
          disabled={refreshing}
          className="p-1 hover:bg-surface-3 rounded text-text-muted hover:text-text-secondary cursor-pointer disabled:opacity-50"
          title="Refresh all endpoints"
        >
          <RefreshCw
            size={12}
            className={refreshing ? "animate-spin" : ""}
          />
        </button>
      </div>

      {anyDivergence && (
        <div className="flex items-start gap-2 p-2.5 rounded-lg bg-danger/10 border border-danger/30">
          <AlertTriangle size={14} className="text-danger shrink-0 mt-0.5" />
          <div className="text-[11px] text-danger">
            Replicas disagree on {divergingFields}. See the{" "}
            <a
              href="https://valargroup.gitbook.io/shielded-vote-docs/operations/snapshot-bumps"
              target="_blank"
              rel="noreferrer"
              className="underline hover:text-danger/80"
            >
              snapshot-bumps runbook
            </a>
            .
          </div>
        </div>
      )}

      {heightMismatchesExpected && !anyDivergence && (
        <div className="flex items-start gap-2 p-2.5 rounded-lg bg-warning/10 border border-warning/30">
          <AlertCircle size={14} className="text-warning shrink-0 mt-0.5" />
          <div className="text-[11px] text-warning">
            One or more replicas are at a different height than the
            published voting-config (
            <span className="font-mono">
              {expectedHeight?.toLocaleString()}
            </span>
            ) — bootstrap may still be in progress.
          </div>
        </div>
      )}

      <div className="overflow-x-auto -mx-2">
        <table className="w-full text-[11px]">
          <thead>
            <tr className="text-text-muted text-[10px] uppercase tracking-wider border-b border-border-subtle">
              <th className="text-left font-medium py-1.5 px-2">Endpoint</th>
              <th className="text-right font-medium py-1.5 px-2">Height</th>
              <th className="text-left font-medium py-1.5 px-2">root25</th>
              <th className="text-left font-medium py-1.5 px-2">root29</th>
              <th className="text-right font-medium py-1.5 px-2">
                Nullifiers
              </th>
              <th className="text-center font-medium py-1.5 px-2">Status</th>
            </tr>
          </thead>
          <tbody>
            {endpoints.map((ep) => {
              const status = statuses[ep.url];
              const isSelected = selectedUrl && ep.url === selectedUrl;
              return (
                <tr
                  key={ep.url}
                  className={`border-b border-border-subtle/40 ${
                    isSelected ? "bg-accent/5" : ""
                  }`}
                >
                  <td className="py-1.5 px-2">
                    <div className="flex flex-col">
                      <span className="text-text-primary font-medium">
                        {ep.label}
                        {isSelected && (
                          <span className="ml-1.5 text-[9px] text-accent font-normal uppercase tracking-wider">
                            selected
                          </span>
                        )}
                      </span>
                      <span className="text-text-muted text-[10px] font-mono break-all">
                        {ep.url}
                      </span>
                    </div>
                  </td>
                  {status?.state === "ok" ? (
                    <>
                      <td
                        className={`text-right font-mono py-1.5 px-2 ${
                          heightDiverges ? "text-danger" : "text-text-primary"
                        }`}
                      >
                        {status.data.height != null
                          ? status.data.height.toLocaleString()
                          : "—"}
                      </td>
                      <td
                        className={`py-1.5 px-2 ${
                          root25Diverges ? "text-danger" : ""
                        }`}
                      >
                        <CopyHex value={status.data.root25} />
                      </td>
                      <td
                        className={`py-1.5 px-2 ${
                          root29Diverges ? "text-danger" : ""
                        }`}
                      >
                        <CopyHex value={status.data.root29} />
                      </td>
                      <td className="text-right font-mono text-text-primary py-1.5 px-2">
                        {status.data.num_ranges.toLocaleString()}
                      </td>
                      <td className="text-center py-1.5 px-2">
                        <span className="inline-flex items-center gap-1 text-success">
                          <CheckCircle2 size={10} />
                          <span className="text-[10px]">ok</span>
                        </span>
                      </td>
                    </>
                  ) : status?.state === "error" ? (
                    <>
                      <td className="py-1.5 px-2" colSpan={4}>
                        <span
                          className="text-danger text-[10px] font-mono"
                          title={status.error}
                        >
                          {status.error}
                        </span>
                      </td>
                      <td className="text-center py-1.5 px-2">
                        <span className="inline-flex items-center gap-1 text-danger">
                          <AlertCircle size={10} />
                          <span className="text-[10px]">error</span>
                        </span>
                      </td>
                    </>
                  ) : (
                    <>
                      <td
                        className="py-1.5 px-2 text-text-muted text-[10px]"
                        colSpan={4}
                      >
                        <Loader2 size={10} className="animate-spin inline" />
                      </td>
                      <td className="text-center py-1.5 px-2">
                        <span className="inline-flex items-center gap-1 text-text-muted">
                          <span className="text-[10px]">loading</span>
                        </span>
                      </td>
                    </>
                  )}
                </tr>
              );
            })}
          </tbody>
        </table>
      </div>
    </div>
  );
}
