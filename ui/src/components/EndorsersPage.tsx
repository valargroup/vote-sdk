import { useCallback, useEffect, useState } from "react";
import { CheckCircle2, Loader2, Plus, RefreshCw, ShieldCheck, Trash2 } from "lucide-react";
import * as chainApi from "../api/chain";
import * as cosmosTx from "../api/cosmosTx";
import type { UseWallet } from "../hooks/useWallet";

interface EndorsersPageProps {
  wallet: UseWallet;
}

function base64ToHex(value: string): string {
  const binary = atob(value);
  return Array.from(binary, (ch) => ch.charCodeAt(0).toString(16).padStart(2, "0")).join("");
}

function shortHex(value: string): string {
  return value.length <= 16 ? value : `${value.slice(0, 8)}...${value.slice(-8)}`;
}

export function EndorsersPage({ wallet }: EndorsersPageProps) {
  const [endorsers, setEndorsers] = useState<chainApi.EndorserEntry[]>([]);
  const [rounds, setRounds] = useState<chainApi.ChainRound[]>([]);
  const [selectedEndorserID, setSelectedEndorserID] = useState("");
  const [endorsedRoundIDs, setEndorsedRoundIDs] = useState<Set<string>>(new Set());
  const [endorsedRoundIDsByEndorser, setEndorsedRoundIDsByEndorser] = useState<Record<string, string[]>>({});
  const [newEndorserID, setNewEndorserID] = useState("zodl");
  const [newAddress, setNewAddress] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [message, setMessage] = useState<string | null>(null);

  const refreshEndorsedRounds = useCallback(async (endorserID: string) => {
    if (!endorserID) {
      setEndorsedRoundIDs(new Set());
      return;
    }
    const resp = await chainApi.getEndorsedRounds(endorserID);
    const roundIDs = resp.vote_round_ids.map(base64ToHex);
    setEndorsedRoundIDs(new Set(roundIDs));
    setEndorsedRoundIDsByEndorser((current) => ({ ...current, [endorserID]: roundIDs }));
  }, []);

  const refresh = useCallback(async () => {
    setError(null);
    const [endorsersResp, roundsResp] = await Promise.all([
      chainApi.getEndorsers(),
      chainApi.listRounds(),
    ]);
    setEndorsers(endorsersResp.endorsers);
    setRounds(roundsResp.rounds ?? []);
    const allEndorsements = await Promise.all(
      endorsersResp.endorsers.map(async (endorser) => {
        const resp = await chainApi.getEndorsedRounds(endorser.endorser_id);
        return [endorser.endorser_id, resp.vote_round_ids.map(base64ToHex)] as const;
      })
    );
    const byEndorser = Object.fromEntries(allEndorsements);
    setEndorsedRoundIDsByEndorser(byEndorser);
    const controlled = endorsersResp.endorsers.find((e) => wallet.address && e.address === wallet.address);
    const selectedStillConfigured = endorsersResp.endorsers.some((e) => e.endorser_id === selectedEndorserID);
    const nextSelected =
      (selectedStillConfigured ? selectedEndorserID : "") ||
      controlled?.endorser_id ||
      endorsersResp.endorsers[0]?.endorser_id ||
      "";
    setSelectedEndorserID(nextSelected);
    setEndorsedRoundIDs(new Set(nextSelected ? byEndorser[nextSelected] ?? [] : []));
  }, [selectedEndorserID, wallet.address]);

  useEffect(() => {
    refresh().catch((err) => setError(err instanceof Error ? err.message : String(err)));
  }, [refresh]);

  const submitSetEndorser = useCallback(
    async (endorserID: string, address: string) => {
      if (!wallet.signer) throw new Error("Connect a wallet first");
      setBusy(`set:${endorserID}`);
      setError(null);
      setMessage(null);
      try {
        const result = await cosmosTx.setEndorser(chainApi.getApiBase(), wallet.signer, endorserID, address);
        if (result.code !== 0) throw new Error(result.log || `Transaction failed with code ${result.code}`);
        setMessage(address ? `Updated endorser ${endorserID}` : `Cleared endorser ${endorserID}`);
        await refresh();
      } finally {
        setBusy(null);
      }
    },
    [refresh, wallet.signer],
  );

  const submitEndorseRound = useCallback(
    async (endorserID: string, roundIDHex: string) => {
      if (!wallet.signer) throw new Error("Connect a wallet first");
      setBusy(`endorse:${roundIDHex}`);
      setError(null);
      setMessage(null);
      try {
        const result = await cosmosTx.endorseRound(chainApi.getApiBase(), wallet.signer, endorserID, roundIDHex);
        if (result.code !== 0) throw new Error(result.log || `Transaction failed with code ${result.code}`);
        setMessage(`Endorsed ${shortHex(roundIDHex)} as ${endorserID}`);
        await refreshEndorsedRounds(endorserID);
      } finally {
        setBusy(null);
      }
    },
    [refreshEndorsedRounds, wallet.signer],
  );

  const submitClearRoundEndorsement = useCallback(
    async (endorserID: string, roundIDHex: string) => {
      if (!wallet.signer) throw new Error("Connect a wallet first");
      setBusy(`clear:${roundIDHex}`);
      setError(null);
      setMessage(null);
      try {
        const result = await cosmosTx.clearRoundEndorsement(chainApi.getApiBase(), wallet.signer, endorserID, roundIDHex);
        if (result.code !== 0) throw new Error(result.log || `Transaction failed with code ${result.code}`);
        setMessage(`Cleared endorsement for ${shortHex(roundIDHex)} as ${endorserID}`);
        await refreshEndorsedRounds(endorserID);
      } finally {
        setBusy(null);
      }
    },
    [refreshEndorsedRounds, wallet.signer],
  );

  const selectedEndorser = endorsers.find((e) => e.endorser_id === selectedEndorserID);
  const canEndorse = Boolean(wallet.address && selectedEndorser?.address === wallet.address && wallet.signer);

  return (
    <div className="flex-1 overflow-y-auto p-6">
      <div className="max-w-5xl mx-auto space-y-6">
        <div className="flex items-start justify-between gap-4">
          <div>
            <h1 className="text-lg font-semibold text-text-primary flex items-center gap-2">
              <ShieldCheck size={20} className="text-accent" />
              Endorsements
            </h1>
            <p className="text-xs text-text-muted mt-1">
              Configure vote-manager controlled endorser mappings and endorse existing on-chain rounds.
            </p>
          </div>
          <button
            onClick={() => refresh().catch((err) => setError(err instanceof Error ? err.message : String(err)))}
            className="flex items-center gap-2 px-3 py-2 rounded-lg border border-border text-xs text-text-secondary hover:bg-surface-2 cursor-pointer"
          >
            <RefreshCw size={14} />
            Refresh
          </button>
        </div>

        {error && <div className="p-3 rounded-lg bg-danger/10 text-danger text-xs">{error}</div>}
        {message && <div className="p-3 rounded-lg bg-success/10 text-success text-xs">{message}</div>}

        <section className="bg-surface-1 border border-border rounded-xl p-4">
          <h2 className="text-sm font-semibold text-text-primary mb-3">Add or rotate mapping</h2>
          <div className="grid grid-cols-1 md:grid-cols-[180px_1fr_auto] gap-3">
            <input
              value={newEndorserID}
              onChange={(e) => setNewEndorserID(e.target.value)}
              placeholder="endorser id"
              className="px-3 py-2 rounded-lg bg-surface-2 border border-border text-xs text-text-primary"
            />
            <input
              value={newAddress}
              onChange={(e) => setNewAddress(e.target.value)}
              placeholder="sv1..."
              className="px-3 py-2 rounded-lg bg-surface-2 border border-border text-xs text-text-primary"
            />
            <button
              disabled={!wallet.signer || !newEndorserID || !newAddress || busy !== null}
              onClick={() =>
                submitSetEndorser(newEndorserID, newAddress).catch((err) =>
                  setError(err instanceof Error ? err.message : String(err)),
                )
              }
              className="flex items-center justify-center gap-2 px-4 py-2 bg-accent/90 hover:bg-accent disabled:opacity-50 text-surface-0 rounded-lg text-xs font-semibold cursor-pointer"
            >
              {busy === `set:${newEndorserID}` ? <Loader2 size={14} className="animate-spin" /> : <Plus size={14} />}
              Save mapping
            </button>
          </div>
        </section>

        <section className="bg-surface-1 border border-border rounded-xl overflow-hidden">
          <div className="px-4 py-3 border-b border-border-subtle">
            <h2 className="text-sm font-semibold text-text-primary">Configured endorsements</h2>
          </div>
          {endorsers.length === 0 ? (
            <p className="p-4 text-xs text-text-muted">No endorsement mappings configured yet.</p>
          ) : (
            <div className="divide-y divide-border-subtle">
              {endorsers.map((endorser) => {
                const roundIDs = endorsedRoundIDsByEndorser[endorser.endorser_id] ?? [];
                return (
                  <div key={endorser.endorser_id} className="p-4 flex items-start justify-between gap-3">
                    <div className="min-w-0 space-y-2">
                      <div>
                        <div className="text-sm font-medium text-text-primary">{endorser.endorser_id}</div>
                        <div className="text-xs text-text-muted break-all">{endorser.address}</div>
                      </div>
                      <div className="rounded-lg border border-border-subtle bg-surface-2 px-3 py-2">
                        <div className="flex items-center justify-between gap-3">
                          <p className="text-[10px] uppercase tracking-wider text-text-muted">Round endorsements</p>
                          <span className="text-[10px] text-text-muted">
                            {roundIDs.length} round{roundIDs.length === 1 ? "" : "s"}
                          </span>
                        </div>
                        {roundIDs.length > 0 ? (
                          <div className="mt-2 flex flex-wrap gap-1.5">
                            {roundIDs.map((roundID) => (
                              <span
                                key={roundID}
                                title={roundID}
                                className="rounded-full bg-surface-3 px-2 py-0.5 font-mono text-[10px] text-text-secondary"
                              >
                                {shortHex(roundID)}
                              </span>
                            ))}
                          </div>
                        ) : (
                          <p className="mt-1 text-xs text-text-muted">No Endorsements.</p>
                        )}
                      </div>
                    </div>
                    <button
                      disabled={!wallet.signer || busy !== null}
                      onClick={() =>
                        submitSetEndorser(endorser.endorser_id, "").catch((err) =>
                          setError(err instanceof Error ? err.message : String(err)),
                        )
                      }
                      className="flex items-center gap-2 px-3 py-2 rounded-lg border border-border text-xs text-danger hover:bg-danger/10 disabled:opacity-50 cursor-pointer"
                    >
                      <Trash2 size={14} />
                      Clear
                    </button>
                  </div>
                );
              })}
            </div>
          )}
        </section>

        <section className="bg-surface-1 border border-border rounded-xl overflow-hidden">
          <div className="px-4 py-3 border-b border-border-subtle flex items-center justify-between gap-3">
            <div>
              <h2 className="text-sm font-semibold text-text-primary">Endorse rounds</h2>
              <p className="text-xs text-text-muted mt-0.5">
                Select an endorser id. The connected wallet must match its mapped address to endorse.
              </p>
            </div>
            <select
              value={selectedEndorserID}
              onChange={(e) => {
                const id = e.target.value;
                setSelectedEndorserID(id);
                refreshEndorsedRounds(id).catch((err) => setError(err instanceof Error ? err.message : String(err)));
              }}
              className="px-3 py-2 rounded-lg bg-surface-2 border border-border text-xs text-text-primary"
            >
              <option value="">Select endorser</option>
              {endorsers.map((endorser) => (
                <option key={endorser.endorser_id} value={endorser.endorser_id}>
                  {endorser.endorser_id}
                </option>
              ))}
            </select>
          </div>

          {rounds.length === 0 ? (
            <p className="p-4 text-xs text-text-muted">No on-chain rounds found.</p>
          ) : (
            <div className="divide-y divide-border-subtle">
              {rounds.map((round) => {
                const roundIDHex = round.vote_round_id ? base64ToHex(round.vote_round_id) : "";
                const endorsed = roundIDHex ? endorsedRoundIDs.has(roundIDHex) : false;
                return (
                  <div key={round.vote_round_id ?? round.title} className="p-4 flex items-center justify-between gap-3">
                    <div className="min-w-0">
                      <div className="text-sm font-medium text-text-primary">
                        {round.title || round.description || shortHex(roundIDHex)}
                      </div>
                      <div className="text-xs text-text-muted font-mono">{shortHex(roundIDHex)}</div>
                    </div>
                    {endorsed ? (
                      <div className="flex items-center gap-2">
                        <span className="flex items-center gap-1.5 text-xs text-success">
                          <CheckCircle2 size={14} />
                          Endorsed
                        </span>
                        {canEndorse && (
                          <button
                            disabled={busy !== null}
                            onClick={() =>
                              submitClearRoundEndorsement(selectedEndorserID, roundIDHex).catch((err) =>
                                setError(err instanceof Error ? err.message : String(err)),
                              )
                            }
                            className="flex items-center gap-1 rounded-md px-2 py-1 text-[10px] text-text-muted hover:bg-surface-2 hover:text-danger disabled:opacity-50 cursor-pointer"
                            title="Clear this endorsement"
                          >
                            <Trash2 size={11} />
                            {busy === `clear:${roundIDHex}` ? "Clearing..." : "Clear"}
                          </button>
                        )}
                      </div>
                    ) : (
                      <button
                        disabled={!canEndorse || !roundIDHex || busy !== null}
                        onClick={() =>
                          submitEndorseRound(selectedEndorserID, roundIDHex).catch((err) =>
                            setError(err instanceof Error ? err.message : String(err)),
                          )
                        }
                        className="px-3 py-2 rounded-lg bg-accent/90 hover:bg-accent disabled:opacity-50 text-surface-0 text-xs font-semibold cursor-pointer"
                      >
                        {busy === `endorse:${roundIDHex}` ? "Endorsing..." : "Endorse"}
                      </button>
                    )}
                  </div>
                );
              })}
            </div>
          )}
        </section>
      </div>
    </div>
  );
}

