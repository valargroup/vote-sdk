import { useCallback, useEffect, useMemo, useState } from "react";
import {
  AlertCircle,
  AlertTriangle,
  CheckCircle2,
  Loader2,
  RefreshCw,
} from "lucide-react";
import * as chainApi from "../api/chain";
import type { ServiceEntry, VoteServerHealth } from "../api/chain";

function formatUnix(ts: number): string {
  if (!ts) return "never";
  return new Date(ts * 1000).toLocaleString();
}

function formatAgo(ts: number): string {
  if (!ts) return "never";
  const seconds = Math.max(0, Math.floor(Date.now() / 1000) - ts);
  if (seconds < 60) return `${seconds}s ago`;
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 48) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  return `${days}d ago`;
}

export function VoteServerHealthBadge({ health }: { health?: VoteServerHealth }) {
  if (!health || health.state === "unknown") {
    return (
      <span
        className="text-[8px] bg-surface-3 text-text-muted px-1.5 py-0.5 rounded-full shrink-0"
        title="Vote server health has not been checked yet"
      >
        Unknown
      </span>
    );
  }
  if (health.state === "up") {
    return (
      <span
        className="text-[8px] bg-success/15 text-success px-1.5 py-0.5 rounded-full shrink-0"
        title={`Last successful ping ${formatAgo(health.last_success_at)}`}
      >
        Up
      </span>
    );
  }
  return (
    <span
      className="text-[8px] bg-danger/15 text-danger px-1.5 py-0.5 rounded-full shrink-0"
      title={`${health.error || "Latest probe failed"}; last success ${formatAgo(health.last_success_at)}`}
    >
      Down
    </span>
  );
}

function statusCell(health: VoteServerHealth | undefined) {
  if (!health || health.state === "unknown") {
    return (
      <span className="inline-flex items-center justify-center gap-1 text-text-muted">
        <AlertCircle size={11} /> Unknown
      </span>
    );
  }
  if (health.state === "up") {
    return (
      <span className="inline-flex items-center justify-center gap-1 text-success">
        <CheckCircle2 size={11} /> Up
      </span>
    );
  }
  return (
    <span className="inline-flex items-center justify-center gap-1 text-danger">
      <AlertTriangle size={11} /> Down
    </span>
  );
}

export function VoteServerFleetStatus({ servers }: { servers: ServiceEntry[] }) {
  const [healthRows, setHealthRows] = useState<VoteServerHealth[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);

  const refresh = useCallback(async (silent = false) => {
    if (silent) {
      setRefreshing(true);
    } else {
      setLoading(true);
    }
    try {
      setHealthRows(await chainApi.getVoteServerHealth());
    } finally {
      if (silent) {
        setRefreshing(false);
      } else {
        setLoading(false);
      }
    }
  }, []);

  useEffect(() => {
    void refresh(false);
    const id = window.setInterval(() => void refresh(true), 15000);
    return () => window.clearInterval(id);
  }, [refresh]);

  const healthByURL = useMemo(() => {
    return new Map(healthRows.map((row) => [row.url, row]));
  }, [healthRows]);

  if (servers.length === 0) {
    return (
      <p className="text-[10px] text-text-muted">
        No vote servers configured in voting-config.
      </p>
    );
  }

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <h3 className="text-[11px] font-semibold text-text-primary">
          Vote server status{" "}
          <span className="text-text-muted font-normal">
            ({servers.length} server{servers.length === 1 ? "" : "s"})
          </span>
        </h3>
        <button
          type="button"
          onClick={() => void refresh(true)}
          disabled={loading || refreshing}
          className="p-1 hover:bg-surface-3 rounded text-text-muted hover:text-text-secondary cursor-pointer disabled:opacity-50"
          title="Refresh vote server health"
        >
          <RefreshCw
            size={12}
            className={loading || refreshing ? "animate-spin" : ""}
          />
        </button>
      </div>

      {loading && healthRows.length === 0 ? (
        <div className="flex items-center justify-center py-6">
          <Loader2 size={16} className="text-text-muted animate-spin" />
        </div>
      ) : (
        <div className="overflow-x-auto -mx-2">
          <table className="w-full text-[11px]">
            <thead>
              <tr className="text-text-muted text-[10px] uppercase tracking-wider border-b border-border-subtle">
                <th className="text-left font-medium py-1.5 px-2">Server</th>
                <th className="text-right font-medium py-1.5 px-2">Height</th>
                <th className="text-right font-medium py-1.5 px-2">Latency</th>
                <th className="text-left font-medium py-1.5 px-2">Last success</th>
                <th className="text-left font-medium py-1.5 px-2">Last check</th>
                <th className="text-center font-medium py-1.5 px-2">Status</th>
              </tr>
            </thead>
            <tbody>
              {servers.map((server) => {
                const health = healthByURL.get(server.url);
                return (
                  <tr
                    key={server.url}
                    className="border-b border-border-subtle/50 last:border-b-0"
                  >
                    <td className="py-2 px-2 min-w-[180px]">
                      <div className="font-medium text-text-primary">
                        {server.label || "—"}
                      </div>
                      <div className="font-mono text-[10px] text-text-muted break-all">
                        {server.url}
                      </div>
                    </td>
                    <td className="py-2 px-2 text-right font-mono text-text-secondary">
                      {health?.height != null ? health.height.toLocaleString() : "—"}
                    </td>
                    <td className="py-2 px-2 text-right font-mono text-text-secondary">
                      {health && health.last_checked_at ? `${health.latency_ms} ms` : "—"}
                    </td>
                    <td
                      className="py-2 px-2 text-text-secondary"
                      title={formatUnix(health?.last_success_at ?? 0)}
                    >
                      {formatAgo(health?.last_success_at ?? 0)}
                    </td>
                    <td
                      className="py-2 px-2 text-text-secondary"
                      title={formatUnix(health?.last_checked_at ?? 0)}
                    >
                      {formatAgo(health?.last_checked_at ?? 0)}
                    </td>
                    <td className="py-2 px-2 text-center">
                      <div>{statusCell(health)}</div>
                      {health?.state === "down" && health.error && (
                        <div className="mt-1 text-[10px] text-danger max-w-[220px] mx-auto break-words">
                          {health.error}
                        </div>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
