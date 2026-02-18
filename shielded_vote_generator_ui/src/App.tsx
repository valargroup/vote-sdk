import { useState, useCallback, useRef, useEffect } from "react";
import { Sidebar } from "./components/Sidebar";
import { TopBar } from "./components/TopBar";
import { ProposalList } from "./components/ProposalList";
import { ProposalEditor } from "./components/ProposalEditor";
import { JsonView } from "./components/JsonView";
import { RoundEditor } from "./components/RoundEditor";
import { RoundsList } from "./components/RoundsList";
import { useStore } from "./store/useStore";
import { Shield, Plus, FileText, Settings, Settings2, RefreshCw, CheckCircle2, AlertCircle, X, Loader2, Server } from "lucide-react";
import type { Proposal, RoundSettings, RoundStatus, VotingRound } from "./types";
import {
  LIGHTWALLETD_ENDPOINTS,
  getStoredRpc,
  setStoredRpc,
  useChainInfo,
} from "./store/rpc";
import * as chainApi from "./api/chain";

type Section = "about" | "rounds" | "builder" | "json" | "downloads" | "preview" | "settings" | "chain-rounds";

function App() {
  const store = useStore();
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

    setPublishStatus("publishing");
    try {
      const vm = await chainApi.getVoteManager();
      if (!vm.address) {
        setPublishError("No VoteManager address set on the chain. Set one in Settings first.");
        setPublishStatus("error");
        return;
      }

      const result = await chainApi.submitSession({
        creator: vm.address,
        snapshot_height: round.settings.snapshotHeight
          ? parseInt(round.settings.snapshotHeight, 10)
          : 0,
        vote_end_time: round.settings.endTime
          ? Math.floor(new Date(round.settings.endTime).getTime() / 1000)
          : Math.floor(Date.now() / 1000) + 86400 * 7, // default: 7 days from now
        description: round.settings.description || round.name,
        proposals: round.proposals.map((p, i) => ({
          id: i + 1,
          title: p.title,
          description: p.description,
        })),
      });

      if (result.code !== 0) {
        setPublishError(result.log || `Transaction failed with code ${result.code}`);
        setPublishStatus("error");
      } else {
        setPublishResult(result.tx_hash);
        setPublishStatus("ok");
        store.setRoundStatus(publishModal, "published");
        store.updateRound(publishModal, { chainTxHash: result.tx_hash });
      }
    } catch (err) {
      setPublishError(err instanceof Error ? err.message : String(err));
      setPublishStatus("error");
    }
  }, [publishModal, store]);

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

        {/* Settings */}
        {section === "settings" && <SettingsPage />}

        {/* Publish modal */}
        {publishModal && (
          <PublishModal
            round={store.rounds.find((r) => r.id === publishModal)!}
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

function SettingsPage() {
  const [rpcUrl, setRpcUrl] = useState(getStoredRpc);
  const chain = useChainInfo();
  const isCustom = !LIGHTWALLETD_ENDPOINTS.some((e) => e.url === rpcUrl);

  // Voting chain state
  const [chainUrl, setChainUrlLocal] = useState(chainApi.getChainUrl);
  const [connStatus, setConnStatus] = useState<"idle" | "testing" | "ok" | "error">("idle");
  const [connError, setConnError] = useState("");
  const [ceremony, setCeremony] = useState<chainApi.CeremonyState | null>(null);
  const [voteManager, setVoteManager] = useState<string>("");
  const [vmCreator, setVmCreator] = useState("");
  const [vmNewAddr, setVmNewAddr] = useState("");
  const [vmStatus, setVmStatus] = useState<"idle" | "sending" | "ok" | "error">("idle");
  const [vmError, setVmError] = useState("");

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
    setConnStatus("testing");
    setConnError("");
    try {
      const state = await chainApi.testConnection();
      setCeremony(state);
      const vm = await chainApi.getVoteManager();
      setVoteManager(vm.address || "");
      setConnStatus("ok");
    } catch (err) {
      setConnError(err instanceof Error ? err.message : String(err));
      setConnStatus("error");
    }
  };

  const handleSetVoteManager = async () => {
    setVmStatus("sending");
    setVmError("");
    try {
      const result = await chainApi.setVoteManager(vmCreator, vmNewAddr);
      if (result.code !== 0) {
        setVmError(result.log || `tx failed with code ${result.code}`);
        setVmStatus("error");
      } else {
        setVmStatus("ok");
        setVoteManager(vmNewAddr);
        setVmCreator("");
        setVmNewAddr("");
      }
    } catch (err) {
      setVmError(err instanceof Error ? err.message : String(err));
      setVmStatus("error");
    }
  };

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
            value="1,000 blocks"
          />
          <SettingsStubRow
            label="Block time"
            value="~75 seconds"
          />
        </div>

        {/* Voting chain */}
        <h2 className="text-xs font-semibold text-text-primary mb-3">
          Voting chain
        </h2>
        <div className="bg-surface-1 border border-border-subtle rounded-xl p-5 space-y-4 mb-6">
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

          {connStatus === "ok" && (
            <div className="flex items-center gap-1.5 text-[11px] text-success">
              <CheckCircle2 size={12} /> Connected
            </div>
          )}
          {connStatus === "error" && (
            <div className="flex items-center gap-1.5 text-[11px] text-danger">
              <AlertCircle size={12} /> {connError}
            </div>
          )}

          {/* Ceremony status (shown when connected) */}
          {connStatus === "ok" && ceremony?.ceremony && (
            <>
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
            </>
          )}

          {/* Vote manager (shown when connected) */}
          {connStatus === "ok" && (
            <div className="border-t border-border-subtle pt-3 space-y-3">
              <div className="flex items-center justify-between">
                <span className="text-xs text-text-secondary">VoteManager</span>
                <span className="text-[11px] font-mono text-text-primary">
                  {voteManager || <span className="text-text-muted italic">not set</span>}
                </span>
              </div>

              <details className="group">
                <summary className="text-[11px] text-accent cursor-pointer hover:text-accent-glow">
                  Set VoteManager address
                </summary>
                <div className="mt-2 space-y-2">
                  <input
                    type="text"
                    value={vmCreator}
                    onChange={(e) => setVmCreator(e.target.value)}
                    placeholder="Creator address (validator operator)"
                    className="w-full px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary placeholder:text-text-muted focus:outline-none focus:border-accent/50 font-mono"
                  />
                  <input
                    type="text"
                    value={vmNewAddr}
                    onChange={(e) => setVmNewAddr(e.target.value)}
                    placeholder="New VoteManager address"
                    className="w-full px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary placeholder:text-text-muted focus:outline-none focus:border-accent/50 font-mono"
                  />
                  <button
                    onClick={handleSetVoteManager}
                    disabled={!vmCreator || !vmNewAddr || vmStatus === "sending"}
                    className="px-3 py-1.5 bg-accent/90 hover:bg-accent text-surface-0 rounded-lg text-[11px] font-semibold transition-colors cursor-pointer disabled:opacity-50"
                  >
                    {vmStatus === "sending" ? "Sending..." : "Set VoteManager"}
                  </button>
                  {vmStatus === "ok" && (
                    <p className="text-[11px] text-success">VoteManager updated.</p>
                  )}
                  {vmStatus === "error" && (
                    <p className="text-[11px] text-danger">{vmError}</p>
                  )}
                </div>
              </details>
            </div>
          )}

          {connStatus === "idle" && (
            <p className="text-[10px] text-text-muted">
              Enter the Zally chain API URL and click Test to connect.
            </p>
          )}
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
  status,
  result,
  error,
  onConfirm,
  onClose,
}: {
  round: VotingRound;
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
