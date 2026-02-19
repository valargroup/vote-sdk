import { useState, useCallback, useRef, useEffect } from "react";
import { Sidebar } from "./components/Sidebar";
import { TopBar } from "./components/TopBar";
import { ProposalList } from "./components/ProposalList";
import { ProposalEditor } from "./components/ProposalEditor";
import { JsonView } from "./components/JsonView";
import { RoundEditor } from "./components/RoundEditor";
import { RoundsList } from "./components/RoundsList";
import { useStore } from "./store/useStore";
import { Shield, Plus, FileText, Settings, Settings2, RefreshCw, CheckCircle2, AlertCircle, X, Loader2, Server, Database, Eye, EyeOff, Wallet, Unplug, BarChart3, Copy, Check } from "lucide-react";
import type { Proposal, RoundSettings, RoundStatus, VotingRound } from "./types";
import {
  LIGHTWALLETD_ENDPOINTS,
  getStoredRpc,
  setStoredRpc,
  useChainInfo,
  estimateTimestamp,
} from "./store/rpc";
import * as chainApi from "./api/chain";
import * as cosmosTx from "./api/cosmosTx";
import { useWallet } from "./hooks/useWallet";
import type { UseWallet } from "./hooks/useWallet";

type Section = "about" | "rounds" | "builder" | "json" | "downloads" | "preview" | "settings" | "chain-rounds" | "vote-status";

function App() {
  const store = useStore();
  const wallet = useWallet();
  const [section, setSection] = useState<Section>("about");
  const [filter, setFilter] = useState<RoundStatus | "all">("all");
  const importRef = useRef<HTMLInputElement>(null);
  const [publishModal, setPublishModal] = useState<string | null>(null); // round id
  const [publishStatus, setPublishStatus] = useState<"idle" | "publishing" | "ok" | "error">("idle");
  const [publishResult, setPublishResult] = useState<string>("");
  const [publishError, setPublishError] = useState("");

  const handleSelectRound = useCallback(
    (id: string) => {
      store.setActiveRoundId(id);
      store.setActiveProposalId(null);
      setSection("builder");
    },
    [store]
  );

  const handleCreateRound = useCallback(() => {
    store.createRound();
    setSection("builder");
  }, [store]);

  const handleImportJson = useCallback(() => {
    importRef.current?.click();
  }, []);

  const handleFileImport = useCallback(
    (e: React.ChangeEvent<HTMLInputElement>) => {
      const file = e.target.files?.[0];
      if (!file) return;
      const reader = new FileReader();
      reader.onload = (ev) => {
        try {
          const data = JSON.parse(ev.target?.result as string);
          const roundData = data.round ?? data;
          const round = store.createRound(roundData.name ?? "Imported Round");
          if (roundData.proposals) {
            store.updateRound(round.id, { proposals: roundData.proposals });
          }
          if (roundData.settings) {
            store.updateRound(round.id, { settings: roundData.settings });
          }
          setSection("builder");
        } catch {
          alert("Invalid JSON file");
        }
      };
      reader.readAsText(file);
      e.target.value = "";
    },
    [store]
  );

  const handlePublish = useCallback(
    (roundId: string) => {
      setPublishModal(roundId);
      setPublishStatus("idle");
      setPublishResult("");
      setPublishError("");
    },
    [store]
  );

  const handlePublishConfirm = useCallback(async () => {
    if (!publishModal) return;
    const round = store.rounds.find((r) => r.id === publishModal);
    if (!round) return;

    if (!wallet.signer) {
      setPublishStatus("error");
      setPublishError(
        "No wallet connected. Go to Settings → Wallet to connect Keplr or enter a dev key."
      );
      return;
    }

    const snapshotHeight = parseInt(round.settings.snapshotHeight, 10) || 0;
    if (snapshotHeight === 0) {
      setPublishStatus("error");
      setPublishError("Snapshot height must be set to a non-zero value in Round Settings.");
      return;
    }

    const voteEndTime = round.settings.endTime
      ? Math.floor(new Date(round.settings.endTime).getTime() / 1000)
      : Math.floor(Date.now() / 1000) + 7 * 24 * 3600;

    const proposals = round.proposals.map((p, i) => {
      const options = p.options.map((opt, j) => ({ index: j, label: opt.label }));
      if (p.allowAbstain) {
        options.push({ index: options.length, label: "Abstain" });
      }
      return { id: i + 1, title: p.title, description: p.description, options };
    });

    setPublishStatus("publishing");
    setPublishError("");
    try {
      const base = chainApi.getApiBase();
      const result = await cosmosTx.createVotingSession(base, wallet.signer, {
        snapshotHeight,
        voteEndTime,
        proposals,
        nullifierApiBase: nullifierApiBase(),
        description: round.settings.description || round.name,
      });
      if (result.code !== 0) {
        setPublishError(result.log || `Transaction failed with code ${result.code}`);
        setPublishStatus("error");
      } else {
        setPublishResult(result.tx_hash);
        setPublishStatus("ok");
        store.setRoundStatus(publishModal, "published");
      }
    } catch (err) {
      setPublishError(err instanceof Error ? err.message : String(err));
      setPublishStatus("error");
    }
  }, [publishModal, store, wallet.signer]);

  const handleNavigate = useCallback(
    (s: string) => {
      setSection(s as Section);
    },
    []
  );

  const sampleRound = store.rounds.find((r) => r.name.includes("(SAMPLE)"));

  return (
    <div className="flex h-screen overflow-hidden bg-surface-0">
      <input
        ref={importRef}
        type="file"
        accept=".json"
        className="hidden"
        onChange={handleFileImport}
      />

      <Sidebar
        rounds={store.rounds}
        activeRoundId={store.activeRoundId}
        activeFilter={filter}
        onFilterChange={setFilter}
        onSelectRound={handleSelectRound}
        onCreateRound={handleCreateRound}
        onImportJson={handleImportJson}
        onNavigate={handleNavigate}
        currentSection={section}
      />

      <main className="flex-1 flex flex-col overflow-hidden">
        {/* About page */}
        {section === "about" && (
          <AboutPage
            onCreateRound={handleCreateRound}
            onOpenSample={
              sampleRound
                ? () => handleSelectRound(sampleRound.id)
                : undefined
            }
          />
        )}

        {/* Rounds list */}
        {section === "rounds" && (
          <RoundsList
            rounds={store.rounds}
            activeFilter={filter}
            onFilterChange={setFilter}
            onSelectRound={handleSelectRound}
            onDuplicate={(id) => store.duplicateRound(id)}
            onArchive={(id) => store.setRoundStatus(id, "archived")}
          />
        )}

        {/* Builder with round selected */}
        {section === "builder" && store.activeRound && (
          <>
            <TopBar
              round={store.activeRound}
              saveState={store.saveState}
              onUpdateName={(name) =>
                store.updateRound(store.activeRound!.id, { name })
              }
              onPublish={() => handlePublish(store.activeRound!.id)}
              onPreview={() => setSection("preview")}
              onDuplicate={() => store.duplicateRound(store.activeRound!.id)}
              onArchive={() =>
                store.setRoundStatus(store.activeRound!.id, "archived")
              }
              onDelete={() => {
                store.deleteRound(store.activeRound!.id);
                setSection("rounds");
              }}
              onNavigate={handleNavigate}
            />
            <div className="flex flex-1 overflow-hidden">
              <div className="w-[380px] min-w-[320px] border-r border-border bg-surface-0 overflow-hidden">
                <ProposalList
                  proposals={store.activeRound.proposals}
                  activeProposalId={store.activeProposalId}
                  onSelectProposal={(id) => store.setActiveProposalId(id)}
                  onAddProposal={() =>
                    store.addProposal(store.activeRound!.id)
                  }
                  onDuplicateProposal={(id) =>
                    store.duplicateProposal(store.activeRound!.id, id)
                  }
                  onDeleteProposal={(id) =>
                    store.deleteProposal(store.activeRound!.id, id)
                  }
                  onReorder={(from, to) =>
                    store.reorderProposals(store.activeRound!.id, from, to)
                  }
                />
              </div>
              <RightPanel
                round={store.activeRound}
                activeProposal={store.activeProposal}
                onUpdateRoundName={(name) =>
                  store.updateRound(store.activeRound!.id, { name })
                }
                onUpdateRoundSettings={(patch) =>
                  store.updateRound(store.activeRound!.id, {
                    settings: { ...store.activeRound!.settings, ...patch },
                  })
                }
                onUpdateProposal={(patch) =>
                  store.updateProposal(
                    store.activeRound!.id,
                    store.activeProposal!.id,
                    patch
                  )
                }
                onDeleteProposal={() =>
                  store.deleteProposal(
                    store.activeRound!.id,
                    store.activeProposal!.id
                  )
                }
              />
            </div>
          </>
        )}

        {/* Builder with no round */}
        {section === "builder" && !store.activeRound && (
          <div className="flex items-center justify-center h-full">
            <div className="text-center">
              <p className="text-xs text-text-muted mb-3">
                No round selected
              </p>
              <button
                onClick={handleCreateRound}
                className="px-4 py-2 bg-accent/90 hover:bg-accent text-surface-0 rounded-lg text-xs font-semibold transition-colors cursor-pointer"
              >
                Create a new round
              </button>
            </div>
          </div>
        )}

        {/* JSON view */}
        {section === "json" && store.activeRound && (
          <JsonView round={store.activeRound} />
        )}
        {section === "json" && !store.activeRound && (
          <div className="flex items-center justify-center h-full">
            <p className="text-xs text-text-muted">
              Select a round first to view its JSON.
            </p>
          </div>
        )}

        {/* Preview */}
        {section === "preview" && store.activeRound && (
          <PreviewView round={store.activeRound} onBack={() => setSection("builder")} />
        )}

        {/* Downloads stub */}
        {section === "downloads" && (
          <div className="flex items-center justify-center h-full">
            <p className="text-xs text-text-muted">
              Download history will appear here.
            </p>
          </div>
        )}

        {/* On-chain rounds */}
        {section === "chain-rounds" && <ChainRoundsView />}

        {/* Vote status */}
        {section === "vote-status" && <VoteStatusView />}

        {/* Settings */}
        {section === "settings" && <SettingsPage wallet={wallet} />}

        {/* Publish modal */}
        {publishModal && (
          <PublishModal
            round={store.rounds.find((r) => r.id === publishModal)!}
            signerAddress={wallet.address}
            status={publishStatus}
            result={publishResult}
            error={publishError}
            onConfirm={handlePublishConfirm}
            onClose={() => setPublishModal(null)}
          />
        )}
      </main>
    </div>
  );
}

/* ── Right panel (round settings / proposal editor) ──────────── */

function RightPanel({
  round,
  activeProposal,
  onUpdateRoundName,
  onUpdateRoundSettings,
  onUpdateProposal,
  onDeleteProposal,
}: {
  round: VotingRound;
  activeProposal: Proposal | null;
  onUpdateRoundName: (name: string) => void;
  onUpdateRoundSettings: (patch: Partial<RoundSettings>) => void;
  onUpdateProposal: (patch: Partial<Proposal>) => void;
  onDeleteProposal: () => void;
}) {
  const [tab, setTab] = useState<"proposal" | "round">(
    activeProposal ? "proposal" : "round"
  );

  // If a proposal gets selected, switch to proposal tab
  const effectiveTab = activeProposal ? tab : "round";

  return (
    <div className="flex-1 bg-surface-0 overflow-hidden flex flex-col">
      {/* Tab bar */}
      <div className="flex border-b border-border-subtle bg-surface-0 shrink-0">
        <button
          onClick={() => setTab("round")}
          className={`flex items-center gap-1.5 px-4 py-2 text-[11px] transition-colors cursor-pointer border-b-2 ${
            effectiveTab === "round"
              ? "border-accent text-accent-glow"
              : "border-transparent text-text-muted hover:text-text-secondary"
          }`}
        >
          <Settings2 size={12} />
          Round
        </button>
        <button
          onClick={() => setTab("proposal")}
          disabled={!activeProposal}
          className={`flex items-center gap-1.5 px-4 py-2 text-[11px] transition-colors cursor-pointer border-b-2 disabled:opacity-30 disabled:cursor-not-allowed ${
            effectiveTab === "proposal"
              ? "border-accent text-accent-glow"
              : "border-transparent text-text-muted hover:text-text-secondary"
          }`}
        >
          Proposal
        </button>
      </div>

      {/* Panel content */}
      <div className="flex-1 overflow-hidden">
        {effectiveTab === "round" && (
          <RoundEditor
            round={round}
            onUpdateName={onUpdateRoundName}
            onUpdateSettings={onUpdateRoundSettings}
          />
        )}
        {effectiveTab === "proposal" && activeProposal && (
          <ProposalEditor
            key={activeProposal.id}
            proposal={activeProposal}
            onUpdate={onUpdateProposal}
            onDelete={onDeleteProposal}
          />
        )}
      </div>
    </div>
  );
}

/* ── About page ──────────────────────────────────────────────── */

function AboutPage({
  onCreateRound,
  onOpenSample,
}: {
  onCreateRound: () => void;
  onOpenSample?: () => void;
}) {
  return (
    <div className="flex-1 overflow-y-auto">
      <div className="max-w-xl mx-auto px-6 py-12">
        {/* Hero */}
        <div className="flex items-center gap-3 mb-6">
          <div className="w-10 h-10 rounded-xl bg-accent/15 flex items-center justify-center">
            <Shield size={22} className="text-accent" />
          </div>
          <div>
            <h1 className="text-lg font-bold text-text-primary">
              Shielded Vote Creator
            </h1>
            <p className="text-[11px] text-text-muted">
              Build private voting rounds for the shielded vote chain
            </p>
          </div>
        </div>

        {/* Description */}
        <div className="bg-surface-1 border border-border-subtle rounded-xl p-5 mb-6">
          <p className="text-xs text-text-secondary leading-relaxed">
            This tool lets you build proposals for new shielded voting rounds.
            Define your proposals, configure options, preview the ballot, and
            export the round as JSON. Eventually, you'll be able to submit
            rounds directly to the vote chain from here.
          </p>
        </div>

        {/* Getting started */}
        <h2 className="text-xs font-semibold text-text-primary mb-3">
          Getting started
        </h2>
        <div className="space-y-3 mb-8">
          {onOpenSample && (
            <button
              onClick={onOpenSample}
              className="w-full flex items-start gap-3 bg-surface-1 border border-border-subtle hover:border-accent/30 rounded-xl p-4 text-left transition-colors cursor-pointer group"
            >
              <div className="w-8 h-8 rounded-lg bg-accent/10 flex items-center justify-center shrink-0 mt-0.5">
                <FileText size={16} className="text-accent" />
              </div>
              <div>
                <p className="text-xs font-semibold text-text-primary group-hover:text-accent-glow transition-colors">
                  Open the sample round
                </p>
                <p className="text-[11px] text-text-muted mt-0.5">
                  Explore a pre-loaded draft based on NU7 Sentiment Polling
                  with 11 proposals to see how the builder works.
                </p>
              </div>
            </button>
          )}

          <button
            onClick={onCreateRound}
            className="w-full flex items-start gap-3 bg-surface-1 border border-border-subtle hover:border-accent/30 rounded-xl p-4 text-left transition-colors cursor-pointer group"
          >
            <div className="w-8 h-8 rounded-lg bg-accent/10 flex items-center justify-center shrink-0 mt-0.5">
              <Plus size={16} className="text-accent" />
            </div>
            <div>
              <p className="text-xs font-semibold text-text-primary group-hover:text-accent-glow transition-colors">
                Create a new round
              </p>
              <p className="text-[11px] text-text-muted mt-0.5">
                Start from scratch. Add proposals, configure options, and
                export when you're ready.
              </p>
            </div>
          </button>
        </div>

        {/* How it works */}
        <h2 className="text-xs font-semibold text-text-primary mb-3">
          How it works
        </h2>
        <div className="bg-surface-1 border border-border-subtle rounded-xl p-5 space-y-3 mb-8">
          <Step n={1} text="Create a voting round and add one or more proposals with Support/Oppose or multi-choice options." />
          <Step n={2} text="Preview the ballot as voters will see it, and validate for completeness." />
          <Step n={3} text="Export the round as JSON or submit it to the shielded vote chain (coming soon)." />
        </div>

        {/* Footer note */}
        <p className="text-[10px] text-text-muted text-center">
          All data is stored locally in your browser. Nothing is sent to a
          server until you choose to publish.
        </p>
      </div>
    </div>
  );
}

function Step({ n, text }: { n: number; text: string }) {
  return (
    <div className="flex items-start gap-3">
      <span className="text-[10px] font-bold text-accent bg-accent/10 rounded-full w-5 h-5 flex items-center justify-center shrink-0 mt-0.5">
        {n}
      </span>
      <p className="text-[11px] text-text-secondary leading-relaxed">{text}</p>
    </div>
  );
}

/* ── Settings page ───────────────────────────────────────────── */

const CEREMONY_STATUS_NAMES: Record<number, string> = {
  0: "unspecified",
  1: "registering",
  2: "dealt",
  3: "confirmed",
};

interface NullifierStatus {
  latest_height: number | null;
  nullifier_count: number;
}

const NULLIFIER_BASE_URL = import.meta.env.VITE_NULLIFIER_URL || "";

function nullifierApiBase(): string {
  // In dev mode, always use the Vite proxy path — VITE_NULLIFIER_URL is only
  // the proxy *target* (server-side), not a browser-reachable URL.
  if (typeof window !== "undefined" && window.location.port === "5173") {
    return "/nullifier";
  }
  return NULLIFIER_BASE_URL || "/nullifier";
}

function useNullifierStatus() {
  const [data, setData] = useState<NullifierStatus | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(() => {
    setLoading(true);
    setError(null);
    fetch(`${nullifierApiBase()}/status`)
      .then((res) => {
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        return res.json();
      })
      .then((json: NullifierStatus) => {
        setData(json);
        setLoading(false);
      })
      .catch((err) => {
        setError(err instanceof Error ? err.message : String(err));
        setLoading(false);
      });
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh]);

  return { data, loading, error, refresh };
}

function SettingsPage({ wallet }: { wallet: UseWallet }) {
  const [rpcUrl, setRpcUrl] = useState(getStoredRpc);
  const chain = useChainInfo();
  const nullifier = useNullifierStatus();
  const isCustom = !LIGHTWALLETD_ENDPOINTS.some((e) => e.url === rpcUrl);

  // Voting chain state
  const [chainUrl, setChainUrlLocal] = useState(chainApi.getChainUrl);
  const [connStatus, setConnStatus] = useState<"idle" | "testing" | "ok" | "error">("idle");
  const [connError, setConnError] = useState("");
  const [ceremony, setCeremony] = useState<chainApi.CeremonyState | null>(null);
  const [latestBlock, setLatestBlock] = useState<chainApi.LatestBlockInfo | null>(null);
  const [helperStatus, setHelperStatus] = useState<chainApi.HelperStatus | null>(null);
  const [voteManager, setVoteManagerAddr] = useState<string>(
    () => localStorage.getItem("zally-vm-address") ?? ""
  );
  const [chainDetailsOpen, setChainDetailsOpen] = useState(false);

  // Dev private key connection (collapsible section)
  const [devKey, setDevKey] = useState("");
  const [devKeyVisible, setDevKeyVisible] = useState(false);

  // Set VoteManager flow
  const [vmNewAddr, setVmNewAddr] = useState("");
  const [vmStatus, setVmStatus] = useState<"idle" | "sending" | "ok" | "error">("idle");
  const [vmError, setVmError] = useState("");
  const [vmTxHash, setVmTxHash] = useState("");

  const handleRpcChange = (url: string) => {
    setRpcUrl(url);
    setStoredRpc(url);
  };

  const handleChainUrlChange = (url: string) => {
    setChainUrlLocal(url);
    chainApi.setChainUrl(url);
    setConnStatus("idle");
  };

  const handleTestConnection = async () => {
    // Ensure the displayed URL is persisted so apiBase() uses it.
    chainApi.setChainUrl(chainUrl);
    setConnStatus("testing");
    setConnError("");
    try {
      // Query the standard Cosmos SDK blocks endpoint first — this is the
      // most reliable way to confirm the chain is reachable.
      const block = await chainApi.getLatestBlock();
      setLatestBlock(block);

      const [state, vm, helper] = await Promise.all([
        chainApi.testConnection(),
        chainApi.getVoteManager(),
        chainApi.getHelperStatus().catch(() => null),
      ]);
      setCeremony(state);
      setVoteManagerAddr(vm.address || "");
      setHelperStatus(helper);
      setConnStatus("ok");
    } catch (err) {
      setConnError(err instanceof Error ? err.message : String(err));
      setConnStatus("error");
    }
  };

  const handleConnectDev = async () => {
    await wallet.connectDev(devKey);
  };

  const handleSetVoteManager = async () => {
    if (!wallet.signer) return;
    setVmStatus("sending");
    setVmError("");
    setVmTxHash("");
    try {
      const base = chainApi.getApiBase();
      const result = await cosmosTx.setVoteManager(base, wallet.signer, vmNewAddr);
      if (result.code !== 0) {
        setVmError(result.log || `tx failed with code ${result.code}`);
        setVmStatus("error");
      } else {
        setVmTxHash(result.tx_hash);
        setVmStatus("ok");
        setVoteManagerAddr(vmNewAddr);
      }
    } catch (err) {
      setVmError(err instanceof Error ? err.message : String(err));
      setVmStatus("error");
    }
  };

  // Auto-test voting chain connection on mount.
  useEffect(() => {
    handleTestConnection();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const ceremonyPhase = CEREMONY_STATUS_NAMES[Number(ceremony?.ceremony?.status)] ?? String(ceremony?.ceremony?.status ?? "unknown");

  return (
    <div className="flex-1 overflow-y-auto">
      <div className="max-w-xl mx-auto px-6 py-12">
        <div className="flex items-center gap-3 mb-6">
          <div className="w-10 h-10 rounded-xl bg-surface-3 flex items-center justify-center">
            <Settings size={22} className="text-text-secondary" />
          </div>
          <div>
            <h1 className="text-lg font-bold text-text-primary">Settings</h1>
            <p className="text-[11px] text-text-muted">
              Configuration for chain connectivity and defaults
            </p>
          </div>
        </div>

        {/* Lightwalletd RPC */}
        <h2 className="text-xs font-semibold text-text-primary mb-3">
          Lightwalletd RPC
        </h2>
        <div className="bg-surface-1 border border-border-subtle rounded-xl p-5 space-y-4 mb-6">
          <div>
            <label className="block text-[11px] text-text-secondary mb-1.5">
              Endpoint
            </label>
            <select
              value={isCustom ? "__custom__" : rpcUrl}
              onChange={(e) => {
                if (e.target.value === "__custom__") return;
                handleRpcChange(e.target.value);
              }}
              className="w-full px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary focus:outline-none focus:border-accent/50 cursor-pointer [color-scheme:dark]"
            >
              {LIGHTWALLETD_ENDPOINTS.map((ep) => (
                <option key={ep.url} value={ep.url}>
                  {ep.label} ({ep.region})
                </option>
              ))}
              <option value="__custom__">Custom URL...</option>
            </select>
          </div>

          {isCustom && (
            <div>
              <label className="block text-[11px] text-text-secondary mb-1">
                Custom URL
              </label>
              <input
                type="text"
                value={rpcUrl}
                onChange={(e) => handleRpcChange(e.target.value)}
                placeholder="https://your-lightwalletd:443"
                className="w-full px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary placeholder:text-text-muted focus:outline-none focus:border-accent/50 font-mono"
              />
            </div>
          )}

          <p className="text-[10px] text-text-muted">
            Used for future direct chain submission. Block height data is
            currently fetched from Blockchair.
          </p>
        </div>

        {/* Zcash mainnet status */}
        <h2 className="text-xs font-semibold text-text-primary mb-3">
          Zcash mainnet
        </h2>
        <div className="bg-surface-1 border border-border-subtle rounded-xl p-5 space-y-4 mb-6">
          <div className="flex items-center justify-between">
            <span className="text-xs text-text-secondary">Latest block</span>
            <div className="flex items-center gap-2">
              {chain.loading ? (
                <RefreshCw size={12} className="text-text-muted animate-spin" />
              ) : chain.error ? (
                <span className="text-[11px] text-danger">{chain.error}</span>
              ) : chain.latestHeight ? (
                <span className="text-[11px] text-text-primary font-mono flex items-center gap-1">
                  <CheckCircle2 size={10} className="text-success" />
                  {chain.latestHeight.toLocaleString()}
                </span>
              ) : null}
              <button
                onClick={chain.refresh}
                className="p-1 hover:bg-surface-3 rounded text-text-muted hover:text-text-secondary cursor-pointer"
                title="Refresh"
              >
                <RefreshCw size={12} />
              </button>
            </div>
          </div>
          <SettingsStubRow
            label="Anchor interval"
            value="10 blocks"
          />
          <SettingsStubRow
            label="Block time"
            value="~75 seconds"
          />
        </div>

        {/* Nullifier service */}
        <h2 className="text-xs font-semibold text-text-primary mb-3">
          Nullifier service
        </h2>
        <div className="bg-surface-1 border border-border-subtle rounded-xl p-5 space-y-4 mb-6">
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-2">
              <Database size={14} className="text-text-secondary" />
              <span className="text-xs text-text-secondary">Status</span>
            </div>
            <div className="flex items-center gap-2">
              {nullifier.loading ? (
                <RefreshCw size={12} className="text-text-muted animate-spin" />
              ) : nullifier.error ? (
                <span className="text-[11px] text-danger">{nullifier.error}</span>
              ) : nullifier.data ? (
                <span className="text-[11px] text-success flex items-center gap-1">
                  <CheckCircle2 size={10} /> Connected
                </span>
              ) : null}
              <button
                onClick={nullifier.refresh}
                className="p-1 hover:bg-surface-3 rounded text-text-muted hover:text-text-secondary cursor-pointer"
                title="Refresh"
              >
                <RefreshCw size={12} />
              </button>
            </div>
          </div>
          {nullifier.data && (
            <>
              <SettingsStubRow
                label="Latest ingested height"
                value={
                  nullifier.data.latest_height != null
                    ? nullifier.data.latest_height.toLocaleString()
                    : "—"
                }
              />
              <SettingsStubRow
                label="Nullifier count"
                value={nullifier.data.nullifier_count.toLocaleString()}
              />
            </>
          )}
          {!nullifier.data && !nullifier.loading && !nullifier.error && (
            <p className="text-[10px] text-text-muted">
              Fetching nullifier service status...
            </p>
          )}
        </div>

        {/* Wallet */}
        <h2 className="text-xs font-semibold text-text-primary mb-3">
          Wallet
        </h2>
        <div className="bg-surface-1 border border-border-subtle rounded-xl p-5 space-y-4 mb-6">
          {wallet.address ? (
            <div className="space-y-3">
              <div className="flex items-center justify-between">
                <div className="flex items-center gap-2">
                  <Wallet size={14} className="text-success" />
                  <span className="text-xs text-text-secondary">Connected</span>
                  <span className="text-[10px] text-text-muted">
                    ({wallet.source === "keplr" ? "Keplr" : "dev key"})
                  </span>
                </div>
                <button
                  onClick={wallet.disconnect}
                  className="flex items-center gap-1 px-2 py-1 text-[10px] text-text-muted hover:text-danger hover:bg-danger/10 rounded transition-colors cursor-pointer"
                >
                  <Unplug size={10} /> Disconnect
                </button>
              </div>
              <div className="bg-surface-2 rounded-lg px-3 py-2">
                <p className="text-[10px] text-text-muted mb-0.5">Address</p>
                <p className="text-[11px] text-text-primary font-mono break-all">
                  {wallet.address}
                </p>
              </div>
            </div>
          ) : (
            <div className="space-y-3">
              <button
                onClick={wallet.connect}
                disabled={wallet.connecting}
                className="w-full flex items-center justify-center gap-2 px-4 py-2.5 bg-accent/90 hover:bg-accent text-surface-0 rounded-lg text-xs font-semibold transition-colors cursor-pointer disabled:opacity-50"
              >
                {wallet.connecting ? (
                  <><Loader2 size={14} className="animate-spin" /> Connecting...</>
                ) : (
                  <><Wallet size={14} /> Connect Keplr</>
                )}
              </button>

              {wallet.error && (
                <div className="flex items-start gap-1.5 text-[11px] text-danger">
                  <AlertCircle size={12} className="mt-0.5 shrink-0" />
                  <span>{wallet.error}</span>
                </div>
              )}

              <details className="group">
                <summary className="text-[11px] text-text-muted cursor-pointer hover:text-text-secondary">
                  Developer: connect with private key
                </summary>
                <div className="mt-2 space-y-2">
                  <div className="relative">
                    <input
                      type={devKeyVisible ? "text" : "password"}
                      value={devKey}
                      onChange={(e) => setDevKey(e.target.value.trim())}
                      placeholder="64-character hex private key"
                      spellCheck={false}
                      autoComplete="off"
                      className="w-full px-3 py-2 pr-9 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary placeholder:text-text-muted focus:outline-none focus:border-accent/50 font-mono"
                    />
                    <button
                      type="button"
                      onClick={() => setDevKeyVisible((v) => !v)}
                      className="absolute right-2 top-1/2 -translate-y-1/2 p-0.5 text-text-muted hover:text-text-secondary cursor-pointer"
                      title={devKeyVisible ? "Hide" : "Show"}
                    >
                      {devKeyVisible ? <EyeOff size={14} /> : <Eye size={14} />}
                    </button>
                  </div>
                  {devKey.length > 0 && devKey.length !== 64 && (
                    <p className="text-[10px] text-warning">
                      Key must be exactly 64 hex characters ({devKey.length}/64)
                    </p>
                  )}
                  <button
                    onClick={handleConnectDev}
                    disabled={devKey.length !== 64 || wallet.connecting}
                    className="px-3 py-1.5 bg-surface-3 hover:bg-surface-2 text-text-secondary rounded-lg text-[11px] font-semibold transition-colors cursor-pointer disabled:opacity-50"
                  >
                    Connect
                  </button>
                </div>
              </details>
            </div>
          )}
        </div>

        {/* Voting chain */}
        <h2 className="text-xs font-semibold text-text-primary mb-3">
          Voting chain
        </h2>
        <div className="bg-surface-1 border border-border-subtle rounded-xl p-5 space-y-4 mb-6">
          {/* Compact status line — always visible */}
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-2">
              <Server size={14} className="text-text-secondary" />
              <span className="text-xs text-text-secondary">Status</span>
            </div>
            <div className="flex items-center gap-2">
              {connStatus === "testing" && (
                <span className="text-[11px] text-text-muted flex items-center gap-1">
                  <RefreshCw size={10} className="animate-spin" /> Connecting...
                </span>
              )}
              {connStatus === "ok" && (
                <span className="text-[11px] text-success flex items-center gap-1">
                  <CheckCircle2 size={10} /> Connected
                  {latestBlock && (
                    <span className="text-text-muted ml-1">
                      (height {latestBlock.height.toLocaleString()})
                    </span>
                  )}
                </span>
              )}
              {connStatus === "error" && (
                <span className="text-[11px] text-danger flex items-center gap-1">
                  <AlertCircle size={10} /> Disconnected
                </span>
              )}
              {connStatus === "idle" && (
                <span className="text-[11px] text-text-muted italic">not tested</span>
              )}
              <button
                onClick={handleTestConnection}
                disabled={connStatus === "testing"}
                className="p-1 hover:bg-surface-3 rounded text-text-muted hover:text-text-secondary cursor-pointer disabled:opacity-50"
                title="Refresh connection"
              >
                <RefreshCw size={12} />
              </button>
            </div>
          </div>

          {/* Error detail */}
          {connStatus === "error" && (
            <div className="flex items-start gap-1.5 text-[11px] text-danger bg-danger/10 border border-danger/30 rounded-lg p-2.5">
              <AlertCircle size={12} className="mt-0.5 shrink-0" />
              <span>{connError}</span>
            </div>
          )}

          {/* Expandable details */}
          <details
            open={chainDetailsOpen}
            onToggle={(e) => setChainDetailsOpen((e.target as HTMLDetailsElement).open)}
          >
            <summary className="text-[11px] text-text-muted cursor-pointer hover:text-text-secondary select-none">
              {chainDetailsOpen ? "Hide details" : "Show details"}
            </summary>
            <div className="mt-3 space-y-4">
              {/* Chain API URL */}
              <div>
                <label className="block text-[11px] text-text-secondary mb-1">
                  Chain API URL
                </label>
                <div className="flex gap-2">
                  <input
                    type="text"
                    value={chainUrl}
                    onChange={(e) => handleChainUrlChange(e.target.value)}
                    placeholder="http://localhost:1318"
                    className="flex-1 px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary placeholder:text-text-muted focus:outline-none focus:border-accent/50 font-mono"
                  />
                  <button
                    onClick={handleTestConnection}
                    disabled={connStatus === "testing"}
                    className="px-3 py-2 bg-accent/90 hover:bg-accent text-surface-0 rounded-lg text-[11px] font-semibold transition-colors cursor-pointer disabled:opacity-50"
                  >
                    {connStatus === "testing" ? (
                      <RefreshCw size={12} className="animate-spin" />
                    ) : (
                      "Test"
                    )}
                  </button>
                </div>
              </div>

              {/* Connection info */}
              {connStatus === "ok" && latestBlock && (
                <div className="space-y-2">
                  <SettingsStubRow label="Chain ID" value={latestBlock.chainId} />
                  <SettingsStubRow label="Latest height" value={latestBlock.height.toLocaleString()} />
                </div>
              )}

              {/* Ceremony status */}
              {connStatus === "ok" && ceremony?.ceremony && (
                <div className="border-t border-border-subtle pt-3 space-y-2">
                  <SettingsStubRow label="Ceremony phase" value={ceremonyPhase} />
                  {ceremony.ceremony.ea_pk && (
                    <SettingsStubRow
                      label="EA public key"
                      value={ceremony.ceremony.ea_pk.slice(0, 16) + "..."}
                    />
                  )}
                  <SettingsStubRow
                    label="Validators"
                    value={String(ceremony.ceremony.validators?.length ?? 0)}
                  />
                </div>
              )}

              {/* Vote manager */}
              {connStatus === "ok" && (
                <div className="border-t border-border-subtle pt-3 space-y-3">
                  <div className="flex items-center justify-between">
                    <span className="text-xs text-text-secondary">VoteManager</span>
                    <span className="text-[11px] font-mono text-text-primary">
                      {voteManager || <span className="text-text-muted italic">not set</span>}
                    </span>
                  </div>

                  {wallet.signer && (
                    <details className="group">
                      <summary className="text-[11px] text-accent cursor-pointer hover:text-accent-glow">
                        Set VoteManager address
                      </summary>
                      <div className="mt-3 space-y-3">
                        <div className="bg-surface-2 rounded-lg px-3 py-2">
                          <p className="text-[10px] text-text-muted mb-0.5">Signing as</p>
                          <p className="text-[11px] text-text-primary font-mono break-all">
                            {wallet.address}
                          </p>
                        </div>
                        <div>
                          <label className="block text-[11px] text-text-secondary mb-1">
                            New VoteManager address
                          </label>
                          <input
                            type="text"
                            value={vmNewAddr}
                            onChange={(e) => setVmNewAddr(e.target.value)}
                            placeholder="zvote1..."
                            className="w-full px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary placeholder:text-text-muted focus:outline-none focus:border-accent/50 font-mono"
                          />
                        </div>
                        <button
                          onClick={handleSetVoteManager}
                          disabled={!vmNewAddr || vmStatus === "sending"}
                          className="px-3 py-1.5 bg-accent/90 hover:bg-accent text-surface-0 rounded-lg text-[11px] font-semibold transition-colors cursor-pointer disabled:opacity-50"
                        >
                          {vmStatus === "sending" ? (
                            <span className="flex items-center gap-1.5">
                              <Loader2 size={12} className="animate-spin" /> Signing & broadcasting...
                            </span>
                          ) : (
                            "Sign & broadcast on-chain"
                          )}
                        </button>
                        {vmStatus === "ok" && (
                          <div className="bg-success/10 border border-success/30 rounded-lg p-2.5">
                            <p className="text-[11px] text-success font-semibold">
                              VoteManager updated
                            </p>
                            {vmTxHash && (
                              <p className="text-[10px] text-text-secondary font-mono mt-0.5 break-all">
                                TX: {vmTxHash}
                              </p>
                            )}
                          </div>
                        )}
                        {vmStatus === "error" && (
                          <div className="bg-danger/10 border border-danger/30 rounded-lg p-2.5">
                            <p className="text-[11px] text-danger">{vmError}</p>
                          </div>
                        )}
                      </div>
                    </details>
                  )}
                  {!wallet.signer && connStatus === "ok" && (
                    <p className="text-[10px] text-text-muted">
                      Connect a wallet above to sign VoteManager transactions.
                    </p>
                  )}
                </div>
              )}

              {/* Helper server status */}
              {connStatus === "ok" && helperStatus && (
                <div className="border-t border-border-subtle pt-3 space-y-2">
                  <div className="flex items-center justify-between">
                    <span className="text-xs text-text-secondary">Helper server</span>
                    <span className="text-[11px] text-success flex items-center gap-1">
                      <CheckCircle2 size={10} /> {helperStatus.status}
                    </span>
                  </div>
                  {helperStatus.tree && (
                    <>
                      <SettingsStubRow
                        label="Commitment leaves"
                        value={helperStatus.tree.leaf_count.toLocaleString()}
                      />
                      <SettingsStubRow
                        label="Anchor height"
                        value={helperStatus.tree.anchor_height.toLocaleString()}
                      />
                    </>
                  )}
                  {Object.keys(helperStatus.queues).length > 0 && (
                    <>
                      {Object.entries(helperStatus.queues).map(([roundId, q]) => (
                        <div key={roundId} className="bg-surface-2 rounded-lg px-3 py-2 space-y-1">
                          <p className="text-[10px] text-text-muted font-mono truncate">
                            {roundId.slice(0, 16)}...
                          </p>
                          <div className="flex gap-3 text-[10px]">
                            <span className="text-text-secondary">
                              {q.pending} pending
                            </span>
                            <span className="text-success">
                              {q.submitted} submitted
                            </span>
                            {q.failed > 0 && (
                              <span className="text-danger">
                                {q.failed} failed
                              </span>
                            )}
                          </div>
                        </div>
                      ))}
                    </>
                  )}
                  {Object.keys(helperStatus.queues).length === 0 && (
                    <p className="text-[10px] text-text-muted">No shares in queue</p>
                  )}
                </div>
              )}
              {connStatus === "ok" && !helperStatus && (
                <div className="border-t border-border-subtle pt-3">
                  <div className="flex items-center justify-between">
                    <span className="text-xs text-text-secondary">Helper server</span>
                    <span className="text-[11px] text-text-muted italic">disabled</span>
                  </div>
                </div>
              )}
            </div>
          </details>
        </div>
      </div>
    </div>
  );
}

function SettingsStubRow({
  label,
  value,
}: {
  label: string;
  value: string;
}) {
  return (
    <div className="flex items-center justify-between">
      <span className="text-xs text-text-secondary">{label}</span>
      <span className="text-[11px] text-text-muted">{value}</span>
    </div>
  );
}

/* ── Publish modal ───────────────────────────────────────────── */

function PublishModal({
  round,
  signerAddress,
  status,
  result,
  error,
  onConfirm,
  onClose,
}: {
  round: VotingRound;
  signerAddress: string | null;
  status: "idle" | "publishing" | "ok" | "error";
  result: string;
  error: string;
  onConfirm: () => void;
  onClose: () => void;
}) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60">
      <div className="bg-surface-1 border border-border rounded-xl shadow-xl max-w-md w-full mx-4">
        <div className="flex items-center justify-between px-5 py-4 border-b border-border-subtle">
          <h3 className="text-sm font-semibold text-text-primary">
            Publish to chain
          </h3>
          <button
            onClick={onClose}
            className="p-1 hover:bg-surface-3 rounded text-text-muted cursor-pointer"
          >
            <X size={14} />
          </button>
        </div>

        <div className="px-5 py-4 space-y-3">
          <div className="space-y-2">
            <SettingsStubRow label="Round" value={round.name} />
            <SettingsStubRow
              label="Proposals"
              value={String(round.proposals.length)}
            />
            <SettingsStubRow
              label="Snapshot height"
              value={round.settings.snapshotHeight || "0 (stub)"}
            />
            <SettingsStubRow
              label="End time"
              value={
                round.settings.endTime
                  ? new Date(round.settings.endTime).toLocaleString()
                  : "7 days from now (default)"
              }
            />
            <SettingsStubRow
              label="Signer"
              value={
                signerAddress
                  ? `${signerAddress.slice(0, 12)}...${signerAddress.slice(-6)}`
                  : "No wallet connected"
              }
            />
          </div>

          {status === "ok" && (
            <div className="bg-success/10 border border-success/30 rounded-lg p-3">
              <p className="text-[11px] text-success font-semibold mb-1">
                Published successfully
              </p>
              <p className="text-[10px] text-text-secondary font-mono break-all">
                TX: {result}
              </p>
            </div>
          )}

          {status === "error" && (
            <div className="bg-danger/10 border border-danger/30 rounded-lg p-3">
              <p className="text-[11px] text-danger">{error}</p>
            </div>
          )}
        </div>

        <div className="flex justify-end gap-2 px-5 py-3 border-t border-border-subtle">
          <button
            onClick={onClose}
            className="px-3 py-1.5 text-[11px] text-text-secondary hover:text-text-primary hover:bg-surface-2 rounded-md transition-colors cursor-pointer"
          >
            {status === "ok" ? "Done" : "Cancel"}
          </button>
          {status !== "ok" && (
            <button
              onClick={onConfirm}
              disabled={status === "publishing"}
              className="flex items-center gap-1.5 px-3 py-1.5 bg-accent/90 hover:bg-accent text-surface-0 rounded-md text-[11px] font-semibold transition-colors cursor-pointer disabled:opacity-50"
            >
              {status === "publishing" ? (
                <>
                  <Loader2 size={12} className="animate-spin" /> Publishing...
                </>
              ) : (
                "Publish to chain"
              )}
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

/* ── On-chain rounds view ────────────────────────────────────── */

const STATUS_MAP: Record<string | number, { label: string; color: string }> = {
  SESSION_STATUS_ACTIVE: { label: "Active", color: "bg-success/20 text-success" },
  SESSION_STATUS_TALLYING: { label: "Tallying", color: "bg-warning/20 text-warning" },
  SESSION_STATUS_FINALIZED: { label: "Finalized", color: "bg-accent-dim/40 text-accent-glow" },
  1: { label: "Active", color: "bg-success/20 text-success" },
  2: { label: "Tallying", color: "bg-warning/20 text-warning" },
  3: { label: "Finalized", color: "bg-accent-dim/40 text-accent-glow" },
};

function ChainRoundsView() {
  const [rounds, setRounds] = useState<chainApi.ChainRound[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [expandedId, setExpandedId] = useState<string | null>(null);
  const [tallyResults, setTallyResults] = useState<Record<string, chainApi.TallyResult[]>>({});

  const fetchRounds = async () => {
    setLoading(true);
    setError("");
    try {
      const resp = await chainApi.listRounds();
      setRounds(resp.rounds ?? []);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    fetchRounds();
  }, []);

  const handleExpand = async (roundIdB64: string) => {
    if (expandedId === roundIdB64) {
      setExpandedId(null);
      return;
    }
    setExpandedId(roundIdB64);

    // Fetch tally results if finalized and not yet loaded.
    const round = rounds.find((r) => r.vote_round_id === roundIdB64);
    const isFinalized = Number(round?.status) === 3 || round?.status === "SESSION_STATUS_FINALIZED";
    if (isFinalized && !tallyResults[roundIdB64]) {
      try {
        const hex = base64ToHex(roundIdB64);
        const resp = await chainApi.getTallyResults(hex);
        if (resp.results) {
          setTallyResults((prev) => ({ ...prev, [roundIdB64]: resp.results! }));
        }
      } catch {
        // Ignore tally fetch errors silently
      }
    }
  };

  return (
    <div className="flex-1 overflow-y-auto">
      <div className="max-w-2xl mx-auto px-6 py-12">
        <div className="flex items-center justify-between mb-6">
          <div className="flex items-center gap-3">
            <div className="w-10 h-10 rounded-xl bg-accent/15 flex items-center justify-center">
              <Server size={22} className="text-accent" />
            </div>
            <div>
              <h1 className="text-lg font-bold text-text-primary">
                On-chain rounds
              </h1>
              <p className="text-[11px] text-text-muted">
                Voting rounds on the Zally chain
              </p>
            </div>
          </div>
          <button
            onClick={fetchRounds}
            className="p-2 hover:bg-surface-3 rounded-lg text-text-muted hover:text-text-secondary cursor-pointer"
            title="Refresh"
          >
            <RefreshCw size={14} className={loading ? "animate-spin" : ""} />
          </button>
        </div>

        {error && (
          <div className="flex items-center gap-2 bg-danger/10 border border-danger/30 rounded-lg p-3 mb-4">
            <AlertCircle size={14} className="text-danger shrink-0" />
            <p className="text-[11px] text-danger">{error}</p>
          </div>
        )}

        {!loading && !error && rounds.length === 0 && (
          <div className="text-center py-12">
            <p className="text-xs text-text-muted">
              No voting rounds found on the chain.
            </p>
          </div>
        )}

        <div className="space-y-3">
          {rounds.map((round) => {
            const roundId = round.vote_round_id ?? "";
            const statusInfo = STATUS_MAP[round.status ?? ""] ?? {
              label: String(round.status ?? "Unknown"),
              color: "bg-surface-3 text-text-muted",
            };
            const isExpanded = expandedId === roundId;

            return (
              <div
                key={roundId}
                className="bg-surface-1 border border-border-subtle rounded-xl overflow-hidden"
              >
                <button
                  onClick={() => handleExpand(roundId)}
                  className="w-full flex items-center justify-between px-5 py-4 text-left cursor-pointer hover:bg-surface-2/50 transition-colors"
                >
                  <div className="min-w-0">
                    <p className="text-xs font-semibold text-text-primary truncate">
                      {round.description || `Round ${roundId.slice(0, 12)}...`}
                    </p>
                    <p className="text-[10px] text-text-muted mt-0.5">
                      {round.proposals?.length ?? 0} proposal
                      {(round.proposals?.length ?? 0) !== 1 ? "s" : ""}
                      {round.creator && (
                        <span className="ml-2">
                          by {round.creator.slice(0, 12)}...
                        </span>
                      )}
                    </p>
                  </div>
                  <span
                    className={`text-[9px] px-2 py-0.5 rounded-full shrink-0 ml-3 ${statusInfo.color}`}
                  >
                    {statusInfo.label}
                  </span>
                </button>

                {isExpanded && (
                  <div className="px-5 pb-4 border-t border-border-subtle pt-3 space-y-3">
                    <div className="space-y-1.5">
                      <SettingsStubRow
                        label="Round ID"
                        value={roundId.length > 24 ? roundId.slice(0, 24) + "..." : roundId}
                      />
                      {round.snapshot_height && (
                        <SettingsStubRow
                          label="Snapshot height"
                          value={round.snapshot_height}
                        />
                      )}
                      {round.vote_end_time && round.vote_end_time !== "0" && (
                        <SettingsStubRow
                          label="Vote end time"
                          value={new Date(
                            parseInt(round.vote_end_time) * 1000
                          ).toLocaleString()}
                        />
                      )}
                    </div>

                    {round.proposals && round.proposals.length > 0 && (
                      <div>
                        <p className="text-[10px] uppercase tracking-wider text-text-muted mb-1.5">
                          Proposals
                        </p>
                        <div className="space-y-1.5">
                          {round.proposals.map((p) => (
                            <div
                              key={p.id}
                              className="flex items-start gap-2 px-3 py-2 bg-surface-2 rounded-lg"
                            >
                              <span className="text-[10px] font-bold text-text-muted bg-surface-3 rounded px-1.5 py-0.5 shrink-0">
                                {String(p.id).padStart(2, "0")}
                              </span>
                              <div className="min-w-0">
                                <p className="text-xs text-text-primary">
                                  {p.title}
                                </p>
                                {p.description && (
                                  <p className="text-[10px] text-text-muted mt-0.5 line-clamp-2">
                                    {p.description}
                                  </p>
                                )}
                              </div>
                            </div>
                          ))}
                        </div>
                      </div>
                    )}

                    {/* Tally results */}
                    {tallyResults[roundId] &&
                      tallyResults[roundId].length > 0 && (
                        <div>
                          <p className="text-[10px] uppercase tracking-wider text-text-muted mb-1.5">
                            Tally results
                          </p>
                          <div className="space-y-1">
                            {tallyResults[roundId].map((tr, i) => (
                              <div
                                key={i}
                                className="flex items-center justify-between px-3 py-1.5 bg-surface-2 rounded-lg text-[11px]"
                              >
                                <span className="text-text-secondary">
                                  Proposal {tr.proposal_id}, Decision{" "}
                                  {tr.vote_decision}
                                </span>
                                <span className="text-text-primary font-mono">
                                  {tr.total_value ?? 0}
                                </span>
                              </div>
                            ))}
                          </div>
                        </div>
                      )}
                  </div>
                )}
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}

function base64ToHex(b64: string): string {
  const bytes = atob(b64);
  return Array.from(bytes, (c) =>
    c.charCodeAt(0).toString(16).padStart(2, "0")
  ).join("");
}

/* ── Copyable field helper ────────────────────────────────────── */

function CopyableField({ label, value, mono = true }: { label: string; value: string; mono?: boolean }) {
  const [copied, setCopied] = useState(false);

  const handleCopy = () => {
    navigator.clipboard.writeText(value).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  };

  return (
    <div className="flex items-center justify-between gap-3">
      <span className="text-[11px] text-text-secondary shrink-0">{label}</span>
      <div className="flex items-center gap-1.5 min-w-0">
        <span className={`text-[11px] text-text-primary truncate ${mono ? "font-mono" : ""}`}>
          {value}
        </span>
        <button
          onClick={handleCopy}
          className="p-0.5 rounded hover:bg-surface-3 text-text-muted hover:text-text-secondary cursor-pointer shrink-0 transition-colors"
          title="Copy to clipboard"
        >
          {copied ? <Check size={11} className="text-success" /> : <Copy size={11} />}
        </button>
      </div>
    </div>
  );
}

/* ── Vote status view ────────────────────────────────────────── */

function VoteStatusView() {
  const [rounds, setRounds] = useState<chainApi.ChainRound[]>([]);
  const [summaries, setSummaries] = useState<Record<string, chainApi.VoteSummaryResponse>>({});
  const [summaryErrors, setSummaryErrors] = useState<Record<string, string>>({});
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const zcashChain = useChainInfo();

  const fetchAll = async () => {
    setLoading(true);
    setError("");
    setSummaryErrors({});
    try {
      const resp = await chainApi.listRounds();
      const allRounds = resp.rounds ?? [];
      setRounds(allRounds);

      // Fetch vote summary for each round in parallel.
      const entries = await Promise.all(
        allRounds.map(async (r) => {
          const id = r.vote_round_id ?? "";
          if (!id) return null;
          try {
            const hex = base64ToHex(id);
            const summary = await chainApi.getVoteSummary(hex);
            return { id, summary, error: null };
          } catch (err) {
            const msg = err instanceof Error ? err.message : String(err);
            console.warn(`VoteSummary failed for ${id.slice(0, 12)}:`, msg);
            return { id, summary: null, error: msg };
          }
        })
      );
      const map: Record<string, chainApi.VoteSummaryResponse> = {};
      const errs: Record<string, string> = {};
      for (const entry of entries) {
        if (!entry) continue;
        if (entry.summary) map[entry.id] = entry.summary;
        if (entry.error) errs[entry.id] = entry.error;
      }
      setSummaries(map);
      setSummaryErrors(errs);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    fetchAll();
  }, []);

  return (
    <div className="flex-1 overflow-y-auto">
      <div className="max-w-2xl mx-auto px-6 py-12">
        <div className="flex items-center justify-between mb-6">
          <div className="flex items-center gap-3">
            <div className="w-10 h-10 rounded-xl bg-accent/15 flex items-center justify-center">
              <BarChart3 size={22} className="text-accent" />
            </div>
            <div>
              <h1 className="text-lg font-bold text-text-primary">
                Vote status
              </h1>
              <p className="text-[11px] text-text-muted">
                Live proposal results from the Zally chain
              </p>
            </div>
          </div>
          <button
            onClick={fetchAll}
            className="p-2 hover:bg-surface-3 rounded-lg text-text-muted hover:text-text-secondary cursor-pointer"
            title="Refresh"
          >
            <RefreshCw size={14} className={loading ? "animate-spin" : ""} />
          </button>
        </div>

        {error && (
          <div className="flex items-center gap-2 bg-danger/10 border border-danger/30 rounded-lg p-3 mb-4">
            <AlertCircle size={14} className="text-danger shrink-0" />
            <p className="text-[11px] text-danger">{error}</p>
          </div>
        )}

        {!loading && !error && rounds.length === 0 && (
          <div className="text-center py-12">
            <p className="text-xs text-text-muted">
              No voting rounds found on the chain.
            </p>
          </div>
        )}

        <div className="space-y-6">
          {rounds.map((round) => {
            const roundId = round.vote_round_id ?? "";
            const summary = summaries[roundId];
            const statusKey = summary?.status ?? round.status ?? "";
            const isFinalized =
              Number(statusKey) === 3 ||
              statusKey === "SESSION_STATUS_FINALIZED";
            const isActive =
              Number(statusKey) === 1 ||
              statusKey === "SESSION_STATUS_ACTIVE";
            const statusInfo = STATUS_MAP[statusKey] ?? {
              label: String(statusKey || "Unknown"),
              color: "bg-surface-3 text-text-muted",
            };

            const endTimeRaw = summary?.vote_end_time ?? round.vote_end_time;
            const endTimeSec = typeof endTimeRaw === "number" ? endTimeRaw : parseInt(String(endTimeRaw ?? "0"), 10);
            const endDate =
              endTimeSec > 0
                ? new Date(endTimeSec * 1000)
                : null;
            const isExpired = endDate ? endDate.getTime() < Date.now() : false;

            const roundIdHex = base64ToHex(roundId);
            const snapshotHeight = Number(round.snapshot_height ?? 0);
            const snapshotTime =
              snapshotHeight > 0 && zcashChain.latestHeight && zcashChain.latestTimestamp
                ? estimateTimestamp(snapshotHeight, zcashChain.latestHeight, zcashChain.latestTimestamp)
                : null;

            return (
              <div
                key={roundId}
                className="bg-surface-1 border border-border-subtle rounded-xl overflow-hidden"
              >
                {/* Round header */}
                <div className="px-5 py-4 flex items-center justify-between">
                  <div className="min-w-0">
                    <div className="flex items-center gap-2">
                      <h2 className="text-sm font-semibold text-text-primary truncate">
                        {summary?.description ||
                          round.description ||
                          `Round ${roundIdHex.slice(0, 16)}...`}
                      </h2>
                      <span
                        className={`text-[9px] px-2 py-0.5 rounded-full shrink-0 ${statusInfo.color}`}
                      >
                        {statusInfo.label}
                      </span>
                    </div>
                    {endDate && (
                      <p className="text-[10px] text-text-muted mt-0.5">
                        {isFinalized
                          ? `Ended ${endDate.toLocaleDateString()}`
                          : isExpired
                            ? `Voting ended ${endDate.toLocaleDateString()} (tallying)`
                            : `Voting until ${endDate.toLocaleString()}`}
                      </p>
                    )}
                  </div>
                  {isActive && !isExpired && (
                    <span className="relative flex h-2.5 w-2.5 shrink-0 ml-3">
                      <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-success opacity-75" />
                      <span className="relative inline-flex rounded-full h-2.5 w-2.5 bg-success" />
                    </span>
                  )}
                </div>

                {/* Round info */}
                <div className="px-5 pb-3 space-y-1.5">
                  <CopyableField
                    label="Round ID"
                    value={roundIdHex}
                  />
                  {snapshotHeight > 0 && (
                    <CopyableField
                      label="Snapshot height"
                      value={
                        snapshotTime
                          ? `${snapshotHeight.toLocaleString()} (~${snapshotTime.toLocaleDateString()})`
                          : snapshotHeight.toLocaleString()
                      }
                    />
                  )}
                  {endDate && (
                    <CopyableField
                      label="Vote end time"
                      value={endDate.toLocaleString()}
                      mono={false}
                    />
                  )}
                </div>

                {/* Proposals */}
                {summary?.proposals && summary.proposals.length > 0 && (
                  <div className="px-5 pb-4 space-y-3">
                    {summary.proposals.map((prop) => (
                      <div
                        key={prop.id}
                        className="bg-surface-2 rounded-lg p-3"
                      >
                        <div className="flex items-center gap-2 mb-2">
                          <span className="text-[10px] font-bold text-text-muted bg-surface-3 rounded px-1.5 py-0.5">
                            {String(prop.id ?? 0).padStart(2, "0")}
                          </span>
                          <span className="text-xs font-semibold text-text-primary">
                            {prop.title || "Untitled"}
                          </span>
                        </div>

                        {/* Option bars */}
                        <div className="space-y-1.5">
                          {(prop.options ?? []).map((opt) => {
                            const count = Number(opt.ballot_count ?? 0);
                            const total = isFinalized
                              ? Number(opt.total_value ?? 0)
                              : null;

                            // Compute bar width relative to max in this proposal.
                            const allCounts = (prop.options ?? []).map((o) =>
                              isFinalized
                                ? Number(o.total_value ?? 0)
                                : Number(o.ballot_count ?? 0)
                            );
                            const maxCount = Math.max(1, ...allCounts);
                            const barValue =
                              isFinalized && total !== null ? total : count;
                            const pct = (barValue / maxCount) * 100;

                            return (
                              <div key={opt.index} className="space-y-0.5">
                                <div className="flex items-center justify-between">
                                  <span className="text-[11px] text-text-secondary">
                                    {opt.label ?? `Option ${opt.index}`}
                                  </span>
                                  <span className="text-[11px] font-mono text-text-primary">
                                    {isFinalized && total !== null
                                      ? total.toLocaleString()
                                      : `${count} ballot${count !== 1 ? "s" : ""}`}
                                  </span>
                                </div>
                                <div className="h-1.5 bg-surface-3 rounded-full overflow-hidden">
                                  <div
                                    className={`h-full rounded-full transition-all duration-500 ${
                                      isFinalized
                                        ? "bg-accent"
                                        : "bg-accent/60"
                                    }`}
                                    style={{
                                      width: `${Math.max(barValue > 0 ? 2 : 0, pct)}%`,
                                    }}
                                  />
                                </div>
                              </div>
                            );
                          })}
                        </div>
                      </div>
                    ))}
                  </div>
                )}

                {/* Fallback: show basic proposal list from round data when summary unavailable */}
                {!summary && !loading && round.proposals && round.proposals.length > 0 && (
                  <div className="px-5 pb-4 space-y-3">
                    {summaryErrors[roundId] && (
                      <p className="text-[10px] text-warning">
                        Summary unavailable: {summaryErrors[roundId]}
                      </p>
                    )}
                    {round.proposals.map((p) => (
                      <div
                        key={p.id}
                        className="bg-surface-2 rounded-lg p-3"
                      >
                        <div className="flex items-center gap-2">
                          <span className="text-[10px] font-bold text-text-muted bg-surface-3 rounded px-1.5 py-0.5">
                            {String(p.id).padStart(2, "0")}
                          </span>
                          <span className="text-xs text-text-primary">
                            {p.title || "Untitled"}
                          </span>
                        </div>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            );
          })}
        </div>
      </div>
    </div>
  );
}

/* ── Preview page ────────────────────────────────────────────── */

function PreviewView({ round, onBack }: { round: VotingRound; onBack: () => void }) {
  return (
    <div className="flex flex-col h-full">
      <div className="px-6 py-4 border-b border-border flex items-center justify-between">
        <div>
          <h2 className="text-sm font-semibold text-text-primary">
            Preview: {round.name}
          </h2>
          <p className="text-[11px] text-text-muted mt-0.5">
            Read-only view as a voter would see it
          </p>
        </div>
        <button
          onClick={onBack}
          className="px-3 py-1.5 bg-surface-3 hover:bg-surface-2 text-text-secondary rounded-md text-[11px] transition-colors cursor-pointer"
        >
          Back to builder
        </button>
      </div>
      <div className="flex-1 overflow-y-auto p-6 max-w-2xl">
        {round.proposals.length === 0 ? (
          <p className="text-xs text-text-muted italic">No proposals yet.</p>
        ) : (
          <div className="space-y-6">
            {round.proposals.map((p, i) => (
              <div
                key={p.id}
                className="bg-surface-1 border border-border-subtle rounded-xl p-5"
              >
                <div className="flex items-center gap-2 mb-2">
                  <span className="text-[10px] font-bold text-text-muted bg-surface-2 rounded px-1.5 py-0.5">
                    {String(i + 1).padStart(2, "0")}
                  </span>
                  <h3 className="text-xs font-semibold text-text-primary">
                    {p.title || "Untitled"}
                  </h3>
                </div>
                {p.description && (
                  <p className="text-[11px] text-text-secondary mb-3 whitespace-pre-wrap">
                    {p.description}
                  </p>
                )}
                <div className="space-y-1.5">
                  {p.options.map((opt) => (
                    <div
                      key={opt.id}
                      className="flex items-center gap-2 px-3 py-2 bg-surface-2 rounded-lg border border-border-subtle hover:border-accent/30 transition-colors cursor-pointer"
                    >
                      <div className="w-3 h-3 rounded-full border-2 border-text-muted" />
                      <span className="text-xs text-text-primary">
                        {opt.label}
                      </span>
                    </div>
                  ))}
                  {p.allowAbstain && (
                    <div className="flex items-center gap-2 px-3 py-2 bg-surface-2 rounded-lg border border-border-subtle hover:border-accent/30 transition-colors cursor-pointer">
                      <div className="w-3 h-3 rounded-full border-2 border-text-muted" />
                      <span className="text-xs text-text-muted italic">
                        Abstain
                      </span>
                    </div>
                  )}
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}

export default App;
