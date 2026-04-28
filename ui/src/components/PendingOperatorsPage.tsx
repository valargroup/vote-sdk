import { useCallback, useEffect, useState } from "react";
import { Loader2, RefreshCw, Users, AlertCircle, ExternalLink } from "lucide-react";
import * as chainApi from "../api/chain";
import * as cosmosTx from "../api/cosmosTx";
import type { UseWallet } from "../hooks/useWallet";

function formatUnix(ts: number): string {
  if (!ts) return "—";
  return new Date(ts * 1000).toLocaleString();
}

export function PendingOperatorsPage({ wallet }: { wallet: UseWallet }) {
  const [rows, setRows] = useState<chainApi.PendingValidatorPublic[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [approvingAddr, setApprovingAddr] = useState<string | null>(null);
  const [approvedLocal, setApprovedLocal] = useState<Record<string, boolean>>({});
  const [resultMsg, setResultMsg] = useState<{ addr: string; ok: boolean; msg: string } | null>(null);

  const load = useCallback(async (silent = false) => {
    if (!silent) setLoading(true);
    setError("");
    try {
      const list = await chainApi.getPendingValidators();
      setRows(list);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      if (!silent) setLoading(false);
    }
  }, []);

  useEffect(() => {
    void load(false);
    const id = setInterval(() => void load(true), 5000);
    return () => clearInterval(id);
  }, [load]);

  const handleApprove = async (operatorAddress: string) => {
    if (!wallet.signer) return;
    setApprovingAddr(operatorAddress);
    setResultMsg(null);
    try {
      const base = chainApi.getApiBase();
      const res = await cosmosTx.fundValidatorJoin(base, wallet.signer, operatorAddress);
      if (res.code === 0) {
        setApprovedLocal((prev) => ({ ...prev, [operatorAddress]: true }));
        setResultMsg({
          addr: operatorAddress,
          ok: true,
          msg: `Approved (tx ${res.tx_hash.slice(0, 12)}...). Row clears when the operator bonds.`,
        });
      } else {
        setResultMsg({
          addr: operatorAddress,
          ok: false,
          msg: res.log || `tx failed (code ${res.code})`,
        });
      }
      await load(true);
    } catch (e) {
      setResultMsg({
        addr: operatorAddress,
        ok: false,
        msg: e instanceof Error ? e.message : String(e),
      });
    } finally {
      setApprovingAddr(null);
    }
  };

  return (
    <div className="flex-1 overflow-y-auto">
      <div className="max-w-5xl mx-auto px-6 py-12">
        <div className="flex items-center justify-between mb-6">
          <div className="flex items-center gap-3">
            <div className="w-10 h-10 rounded-xl bg-accent/15 flex items-center justify-center">
              <Users size={22} className="text-accent" />
            </div>
            <div>
              <h1 className="text-lg font-bold text-text-primary">Validator join queue</h1>
              <p className="text-[11px] text-text-muted">
                Operators who ran <code className="text-[10px]">join.sh</code> and are waiting for manual approval.
                Approving an operator sends their join stake; their node bonds and exits the queue automatically.
              </p>
            </div>
          </div>
          <button
            type="button"
            onClick={() => void load(false)}
            className="p-2 hover:bg-surface-3 rounded-lg text-text-muted hover:text-text-secondary cursor-pointer"
            title="Refresh"
          >
            <RefreshCw size={14} className={loading ? "animate-spin" : ""} />
          </button>
        </div>

        <div className="mb-6 p-4 rounded-xl border border-border-subtle bg-surface-1 text-[11px] text-text-secondary space-y-2">
          <p>
            After bonding, operators add their public URL to{" "}
            <code className="text-[10px] text-text-primary">vote_servers</code> via a manual PR on{" "}
            <a
              className="text-accent inline-flex items-center gap-0.5 hover:underline"
              href="https://github.com/valargroup/token-holder-voting-config"
              target="_blank"
              rel="noreferrer"
            >
              token-holder-voting-config
              <ExternalLink size={10} />
            </a>
            .
          </p>
        </div>

        {!wallet.address && (
          <div className="flex items-center gap-2 bg-warning/10 border border-warning/30 rounded-lg p-3 mb-4">
            <AlertCircle size={14} className="text-warning shrink-0" />
            <p className="text-[11px] text-text-secondary">Connect a vote-manager wallet to approve operators.</p>
          </div>
        )}

        {error && (
          <div className="flex items-center gap-2 bg-danger/10 border border-danger/30 rounded-lg p-3 mb-4">
            <AlertCircle size={14} className="text-danger shrink-0" />
            <p className="text-[11px] text-danger">{error}</p>
          </div>
        )}

        {loading && rows.length === 0 && (
          <div className="flex items-center justify-center py-16">
            <Loader2 size={22} className="text-text-muted animate-spin" />
          </div>
        )}

        {!loading && rows.length === 0 && !error && (
          <p className="text-xs text-text-muted text-center py-12">No pending join requests.</p>
        )}

        {rows.length > 0 && (
          <div className="overflow-x-auto rounded-xl border border-border-subtle">
            <table className="w-full text-left text-[11px]">
              <thead className="bg-surface-2 text-text-muted uppercase tracking-wider">
                <tr>
                  <th className="sticky left-0 z-20 bg-surface-2 px-3 py-2 font-medium border-r border-border-subtle">Manual Approval</th>
                  <th className="px-3 py-2 font-medium">Moniker</th>
                  <th className="px-3 py-2 font-medium">Operator</th>
                  <th className="px-3 py-2 font-medium">URL</th>
                  <th className="px-3 py-2 font-medium">First seen</th>
                  <th className="px-3 py-2 font-medium">Last seen</th>
                  <th className="px-3 py-2 font-medium">Expires</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((r) => (
                  <tr key={r.operator_address} className="group border-t border-border-subtle hover:bg-surface-2/50">
                    <td className="sticky left-0 z-10 bg-surface-0 group-hover:bg-surface-2 px-3 py-2 border-r border-border-subtle whitespace-nowrap">
                      {approvedLocal[r.operator_address] ? (
                        <span className="px-2 py-1 rounded-md bg-success/15 text-success text-[10px] font-semibold">
                          Approved
                        </span>
                      ) : (
                        <button
                          type="button"
                          disabled={!wallet.signer || approvingAddr === r.operator_address}
                          onClick={() => void handleApprove(r.operator_address)}
                          className="px-2 py-1 rounded-md bg-accent/90 hover:bg-accent text-surface-0 text-[10px] font-semibold disabled:opacity-40 cursor-pointer"
                          title={`Approve with ${cosmosTx.VALIDATOR_JOIN_FUND_USVOTE} usvote`}
                        >
                          {approvingAddr === r.operator_address ? (
                            <span className="inline-flex items-center gap-1">
                              <Loader2 size={10} className="animate-spin" /> Approving…
                            </span>
                          ) : (
                            "Approve"
                          )}
                        </button>
                      )}
                    </td>
                    <td className="px-3 py-2 font-semibold text-text-primary">{r.moniker}</td>
                    <td className="px-3 py-2 font-mono text-text-muted truncate max-w-[140px]" title={r.operator_address}>
                      {r.operator_address}
                    </td>
                    <td className="px-3 py-2 text-text-secondary truncate max-w-[180px]" title={r.url}>
                      {r.url}
                    </td>
                    <td className="px-3 py-2 text-text-muted whitespace-nowrap">{formatUnix(r.first_seen_at)}</td>
                    <td className="px-3 py-2 text-text-muted whitespace-nowrap">{formatUnix(r.last_seen_at)}</td>
                    <td className="px-3 py-2 text-text-muted whitespace-nowrap">{formatUnix(r.expires_at)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}

        {resultMsg && (
          <p
            className={`mt-4 text-[11px] ${resultMsg.ok ? "text-success" : "text-danger"}`}
          >
            {resultMsg.msg}
          </p>
        )}
      </div>
    </div>
  );
}
