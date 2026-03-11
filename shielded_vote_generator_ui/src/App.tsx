// Tailwind safelist for dynamically-constructed binary-vote classes:
// bg-success bg-success/10 bg-success/60 bg-danger bg-danger/10 bg-danger/60 text-success text-danger
import { useState, useCallback, useRef, useEffect } from "react";
import { Sidebar } from "./components/Sidebar";
import { TopBar } from "./components/TopBar";
import { ProposalEditor } from "./components/ProposalEditor";
import { JsonView } from "./components/JsonView";
import { RoundEditor } from "./components/RoundEditor";
import { SnapshotSettingsPage } from "./components/SnapshotSettingsPage";
import { RoundsList } from "./components/RoundsList";
import { useStore } from "./store/useStore";
import { Shield, Plus, FileText, Settings, Settings2, RefreshCw, CheckCircle2, AlertCircle, AlertTriangle, X, Loader2, Server, Database, Eye, EyeOff, Wallet, Unplug, BarChart3, Copy, Check, Users, ExternalLink, ShieldAlert, ShieldCheck, GripVertical, MoreHorizontal, Trash2, Lock, ChevronDown } from "lucide-react";
import type { Proposal, RoundSettings, RoundStatus, VotingRound } from "./types";
import {
  LIGHTWALLETD_ENDPOINTS,
  getStoredRpc,
  setStoredRpc,
  useChainInfo,
  estimateTimestamp,
} from "./store/rpc";
import { fromBech32 } from "@cosmjs/encoding";
import * as chainApi from "./api/chain";
import * as cosmosTx from "./api/cosmosTx";
import { useWallet, DEFAULT_DEV_KEY } from "./hooks/useWallet";
import type { UseWallet } from "./hooks/useWallet";

// Matches the iOS voteOptionColor palette in VotingComponents.swift.
// For 2-option proposals: green, red. For 3+: cycles through 8 colors.
const VOTE_OPTION_COLORS = [
  "#22c55e", // green
  "#ef4444", // red
  "#3b82f6", // blue
  "#a855f7", // purple
  "#f97316", // orange
  "#14b8a6", // teal
  "#ec4899", // pink
  "#6366f1", // indigo
];

function optionColor(index: number, total: number): string {
  if (total === 2) return index === 0 ? VOTE_OPTION_COLORS[0] : VOTE_OPTION_COLORS[1];
  return VOTE_OPTION_COLORS[index % VOTE_OPTION_COLORS.length];
}

type Section = "about" | "rounds" | "builder" | "json" | "downloads" | "preview" | "settings" | "vote-status" | "validators" | "snapshot";

const SECTION_PATHS: Record<Section, string> = {
  about: "/",
  rounds: "/rounds",
  builder: "/builder",
  json: "/json",
  downloads: "/downloads",
  preview: "/preview",
  settings: "/settings",
  "vote-status": "/vote-status",
  validators: "/validators",
  snapshot: "/snapshot",
};

const PATH_TO_SECTION: Record<string, Section> = Object.fromEntries(
  Object.entries(SECTION_PATHS).map(([s, p]) => [p, s as Section])
) as Record<string, Section>;

function sectionFromPath(): Section {
  return PATH_TO_SECTION[window.location.pathname] ?? "about";
}

function App() {
  const store = useStore();
  const wallet = useWallet();
  const [section, setSectionState] = useState<Section>(sectionFromPath);
  const [filter, setFilter] = useState<RoundStatus | "all">("all");
  const importRef = useRef<HTMLInputElement>(null);
  const [publishModal, setPublishModal] = useState<string | null>(null); // round id
  const [publishStatus, setPublishStatus] = useState<"idle" | "publishing" | "ok" | "error">("idle");
  const [publishResult, setPublishResult] = useState<string>("");
  const [publishError, setPublishError] = useState("");
  const [expectedRoundCount, setExpectedRoundCount] = useState<number | null>(null);

  // Sync section ↔ URL path, keeping nav instant (no full reload).
  const setSection = useCallback((s: Section) => {
    setSectionState(s);
    const path = SECTION_PATHS[s];
    if (window.location.pathname !== path) {
      window.history.pushState(null, "", path);
    }
  }, []);

  // Handle browser back/forward buttons.
  useEffect(() => {
    const onPopState = () => setSectionState(sectionFromPath());
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);

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

    // Snapshot height is auto-populated from the PIR server (read-only in the editor).
    // Verify it's still current before publishing.
    let snapshotHeight = parseInt(round.settings.snapshotHeight, 10) || 0;
    try {
      const nhStatus = await chainApi.getNullifierStatus();
      const nhHeight = nhStatus.latest_height;
      if (nhHeight != null) {
        snapshotHeight = nhHeight;
        if (String(nhHeight) !== round.settings.snapshotHeight) {
          store.updateRound(round.id, {
            settings: { ...round.settings, snapshotHeight: String(nhHeight) },
          });
        }
      }
    } catch {
      // PIR server may be rebuilding — check snapshot status
      try {
        const snapStatus = await chainApi.getSnapshotStatus();
        if (snapStatus.phase === "rebuilding") {
          setPublishStatus("error");
          setPublishError(
            "PIR server is currently rebuilding. Wait for it to complete, then try again. Go to Snapshot Settings to check progress."
          );
          return;
        }
      } catch {
        // ignore
      }
    }

    if (snapshotHeight === 0) {
      setPublishStatus("error");
      setPublishError("Snapshot height is not available. Check the PIR server status in Snapshot Settings.");
      return;
    }

    if (!round.settings.endTime) {
      setPublishStatus("error");
      setPublishError("Voting end time must be set in Round Settings.");
      return;
    }

    const voteEndTime = Math.floor(new Date(round.settings.endTime).getTime() / 1000);

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
        title: round.name,
      });
      if (result.code !== 0) {
        setPublishError(result.log || `Transaction failed with code ${result.code}`);
        setPublishStatus("error");
      } else {
        setPublishResult(result.tx_hash);
        setPublishStatus("ok");
        store.setRoundStatus(publishModal, "published");
        // Tell VoteStatusView to poll until a new round appears.
        try {
          const resp = await chainApi.listRounds();
          setExpectedRoundCount((resp.rounds ?? []).length + 1);
        } catch {
          setExpectedRoundCount(null);
        }
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
    [setSection]
  );

  const handleCreateSampleRound = useCallback(() => {
    store.createSampleRound();
    setSection("builder");
  }, [store]);

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
        onNavigate={handleNavigate}
        onDeleteRound={store.deleteRound}
        currentSection={section}
      />

      <main className="flex-1 flex flex-col overflow-hidden">
        {/* About page */}
        {section === "about" && (
          <AboutPage
            onCreateRound={handleCreateRound}
            onOpenSample={handleCreateSampleRound}
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
            onDelete={(id) => store.deleteRound(id)}
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
              onDelete={() => {
                store.deleteRound(store.activeRound!.id);
                setSection("rounds");
              }}
              onNavigate={handleNavigate}
              isReadonly={store.activeRound.status === "published"}
            />
            <BuilderView
              round={store.activeRound}
              expandedProposalId={store.activeProposalId}
              onExpandProposal={(id) => store.setActiveProposalId(id)}
              onUpdateRoundName={(name) =>
                store.updateRound(store.activeRound!.id, { name })
              }
              onUpdateRoundSettings={(patch) =>
                store.updateRound(store.activeRound!.id, {
                  settings: { ...store.activeRound!.settings, ...patch },
                })
              }
              onUpdateProposal={(proposalId, patch) =>
                store.updateProposal(store.activeRound!.id, proposalId, patch)
              }
              onAddProposal={() => store.addProposal(store.activeRound!.id)}
              onDuplicateProposal={(id) =>
                store.duplicateProposal(store.activeRound!.id, id)
              }
              onDeleteProposal={(id) =>
                store.deleteProposal(store.activeRound!.id, id)
              }
              onReorder={(from, to) =>
                store.reorderProposals(store.activeRound!.id, from, to)
              }
              onPublish={() => handlePublish(store.activeRound!.id)}
              onNavigate={handleNavigate}
              isReadonly={store.activeRound.status === "published"}
            />
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
          <JsonView round={store.activeRound} onBack={() => setSection("builder")} />
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

        {/* Validators */}
        {section === "validators" && <ValidatorsView wallet={wallet} />}

        {/* Vote status */}
        {section === "vote-status" && <VoteStatusView expectRoundCount={expectedRoundCount} />}

        {/* Snapshot settings */}
        {section === "snapshot" && <SnapshotSettingsPage />}

        {/* Settings */}
        {section === "settings" && <SettingsPage wallet={wallet} />}

        {/* Publish modal */}
        {publishModal && (
          <PublishModal
            round={store.rounds.find((r) => r.id === publishModal)!}
            wallet={wallet}
            status={publishStatus}
            result={publishResult}
            error={publishError}
            onConfirm={handlePublishConfirm}
            onClose={() => {
              const wasSuccess = publishStatus === "ok";
              setPublishModal(null);
              if (wasSuccess) setSection("vote-status");
            }}
          />
        )}
      </main>
    </div>
  );
}

/* ── Unified builder view (single scrollable column) ─────────── */

function isProposalValid(p: Proposal): boolean {
  return p.title.trim().length > 0 && p.options.length >= 2;
}

function BuilderView({
  round,
  expandedProposalId,
  onExpandProposal,
  onUpdateRoundName,
  onUpdateRoundSettings,
  onUpdateProposal,
  onAddProposal,
  onDuplicateProposal,
  onDeleteProposal,
  onReorder,
  onPublish,
  onNavigate,
  isReadonly = false,
}: {
  round: VotingRound;
  expandedProposalId: string | null;
  onExpandProposal: (id: string | null) => void;
  onUpdateRoundName: (name: string) => void;
  onUpdateRoundSettings: (patch: Partial<RoundSettings>) => void;
  onUpdateProposal: (proposalId: string, patch: Partial<Proposal>) => void;
  onAddProposal: () => void;
  onDuplicateProposal: (id: string) => void;
  onDeleteProposal: (id: string) => void;
  onReorder: (from: number, to: number) => void;
  onPublish: () => void;
  onNavigate?: (section: string) => void;
  isReadonly?: boolean;
}) {
  const [menuOpen, setMenuOpen] = useState<string | null>(null);
  const dragItem = useRef<number | null>(null);
  const dragOver = useRef<number | null>(null);

  const handleDragStart = (index: number) => {
    dragItem.current = index;
  };

  const handleDragEnter = (index: number) => {
    dragOver.current = index;
  };

  const handleDragEnd = () => {
    if (
      dragItem.current !== null &&
      dragOver.current !== null &&
      dragItem.current !== dragOver.current
    ) {
      onReorder(dragItem.current, dragOver.current);
    }
    dragItem.current = null;
    dragOver.current = null;
  };

  return (
    <div className="flex-1 overflow-y-auto">
      <div className="max-w-[720px] mx-auto px-6 py-6 space-y-6">
        {/* Round Settings */}
        <section className="bg-surface-1 border border-border-subtle rounded-xl p-5">
          <div className="flex items-center gap-2 mb-4">
            <Settings2 size={14} className="text-text-muted" />
            <h3 className="text-xs font-semibold text-text-primary">
              Round Settings
            </h3>
            {isReadonly && (
              <span className="ml-auto flex items-center gap-1 text-[10px] text-text-muted">
                <Lock size={10} /> Read-only
              </span>
            )}
          </div>
          <RoundEditor
            round={round}
            onUpdateName={onUpdateRoundName}
            onUpdateSettings={onUpdateRoundSettings}
            onNavigate={onNavigate}
            isReadonly={isReadonly}
          />
        </section>

        {/* Proposals header */}
        <div className="flex items-center justify-between">
          <h3 className="text-xs font-semibold text-text-primary">
            Proposals ({round.proposals.length})
          </h3>
        </div>

        {/* Proposal cards */}
        {round.proposals.length === 0 ? (
          <div className="flex flex-col items-center justify-center text-center px-6 py-12 bg-surface-1 border border-border-subtle rounded-xl">
            <div className="w-12 h-12 rounded-full bg-surface-3 flex items-center justify-center mb-3">
              <Plus size={20} className="text-text-muted" />
            </div>
            <p className="text-xs text-text-muted mb-3">
              {isReadonly ? "No proposals" : "Add your first proposal"}
            </p>
            {!isReadonly && (
              <button
                onClick={onAddProposal}
                className="flex items-center gap-1.5 px-3 py-1.5 bg-accent/90 hover:bg-accent text-surface-0 rounded-lg text-[11px] font-semibold transition-colors cursor-pointer"
              >
                <Plus size={12} />
                Add Support/Oppose proposal
              </button>
            )}
          </div>
        ) : (
          <div className="flex flex-col gap-2">
            {round.proposals.map((proposal, index) => {
              const isExpanded = expandedProposalId === proposal.id;
              const valid = isProposalValid(proposal);
              return (
                <div
                  key={proposal.id}
                  draggable={!isReadonly && !isExpanded}
                  onDragStart={
                    isReadonly ? undefined : () => handleDragStart(index)
                  }
                  onDragEnter={
                    isReadonly ? undefined : () => handleDragEnter(index)
                  }
                  onDragEnd={isReadonly ? undefined : handleDragEnd}
                  onDragOver={
                    isReadonly ? undefined : (e) => e.preventDefault()
                  }
                  className="bg-surface-1 border border-border-subtle rounded-xl overflow-hidden"
                >
                  {/* Card header (always visible) */}
                  <div
                    onClick={() =>
                      onExpandProposal(isExpanded ? null : proposal.id)
                    }
                    className="group flex items-center gap-2 px-3 py-2.5 cursor-pointer hover:bg-surface-2/50 transition-colors"
                  >
                    {!isReadonly && (
                      <GripVertical
                        size={14}
                        className="text-text-muted opacity-0 group-hover:opacity-100 transition-opacity cursor-grab shrink-0"
                      />
                    )}
                    <span className="text-[10px] font-bold text-text-muted bg-surface-2 rounded px-1.5 py-0.5 shrink-0">
                      {String(index + 1).padStart(2, "0")}
                    </span>
                    <span className="text-xs text-text-primary truncate flex-1 min-w-0">
                      {proposal.title || "Untitled proposal"}
                    </span>
                    <span className="text-[9px] text-text-muted shrink-0">
                      {proposal.type === "binary" ? "Binary" : "Multi-Choice"}
                    </span>
                    {valid ? (
                      <CheckCircle2 size={13} className="text-success shrink-0" />
                    ) : (
                      <AlertTriangle
                        size={13}
                        className="text-warning shrink-0"
                      />
                    )}
                    <ChevronDown
                      size={14}
                      className={`text-text-muted shrink-0 transition-transform ${
                        isExpanded ? "" : "-rotate-90"
                      }`}
                    />
                    {!isReadonly && (
                      <div className="relative shrink-0">
                        <button
                          onClick={(e) => {
                            e.stopPropagation();
                            setMenuOpen(
                              menuOpen === proposal.id ? null : proposal.id
                            );
                          }}
                          className="p-0.5 rounded hover:bg-surface-3 text-text-muted opacity-0 group-hover:opacity-100 transition-opacity cursor-pointer"
                        >
                          <MoreHorizontal size={14} />
                        </button>
                        {menuOpen === proposal.id && (
                          <div className="absolute right-0 top-6 z-10 bg-surface-2 border border-border rounded-lg shadow-lg py-1 min-w-[130px]">
                            <button
                              onClick={(e) => {
                                e.stopPropagation();
                                onDuplicateProposal(proposal.id);
                                setMenuOpen(null);
                              }}
                              className="w-full flex items-center gap-2 px-3 py-1.5 text-[11px] text-text-secondary hover:bg-surface-3 hover:text-text-primary cursor-pointer"
                            >
                              <Copy size={12} /> Duplicate
                            </button>
                            <button
                              onClick={(e) => {
                                e.stopPropagation();
                                onDeleteProposal(proposal.id);
                                setMenuOpen(null);
                              }}
                              className="w-full flex items-center gap-2 px-3 py-1.5 text-[11px] text-danger hover:bg-surface-3 cursor-pointer"
                            >
                              <Trash2 size={12} /> Delete
                            </button>
                          </div>
                        )}
                      </div>
                    )}
                  </div>

                  {/* Expanded: full proposal editor inline */}
                  {isExpanded && (
                    <div className="px-4 pb-4 pt-1 border-t border-border-subtle">
                      <ProposalEditor
                        key={proposal.id}
                        proposal={proposal}
                        onUpdate={(patch) =>
                          onUpdateProposal(proposal.id, patch)
                        }
                        readonly={isReadonly}
                      />
                    </div>
                  )}
                </div>
              );
            })}
          </div>
        )}

        {/* Bottom actions */}
        {round.proposals.length > 0 && (
          <div className="flex items-center gap-2">
            {!isReadonly && (
              <button
                onClick={onAddProposal}
                className="flex-1 flex items-center justify-center gap-1.5 py-2.5 border border-dashed border-border-subtle hover:border-accent/40 rounded-xl text-[11px] text-text-muted hover:text-accent-glow transition-colors cursor-pointer"
              >
                <Plus size={12} />
                Add Proposal
              </button>
            )}
            {(() => {
              const hasEndTime = round.settings.endTime.length > 0;
              const hasSnapshot = parseInt(round.settings.snapshotHeight, 10) > 0;
              const hasProposals = round.proposals.length > 0;
              const allValid = round.proposals.every(isProposalValid);
              const canPublish = hasEndTime && hasSnapshot && hasProposals && allValid;
              return (
                <button
                  onClick={onPublish}
                  disabled={!canPublish}
                  title={!canPublish ? [
                    !hasEndTime && "Set a voting end time",
                    !hasSnapshot && "Set a snapshot height",
                    !hasProposals && "Add at least one proposal",
                    hasProposals && !allValid && "Fix incomplete proposals",
                  ].filter(Boolean).join(", ") : undefined}
                  className={`flex-1 flex items-center justify-center gap-1.5 py-2.5 rounded-xl text-[11px] font-semibold transition-colors ${
                    canPublish
                      ? "bg-accent/90 hover:bg-accent text-surface-0 cursor-pointer"
                      : "bg-surface-3 text-text-muted cursor-not-allowed"
                  }`}
                >
                  Publish Round
                </button>
              );
            })()}
          </div>
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
  onOpenSample: () => void;
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
          <button
            onClick={onOpenSample}
            className="w-full flex items-start gap-3 bg-surface-1 border border-border-subtle hover:border-accent/30 rounded-xl p-4 text-left transition-colors cursor-pointer group"
          >
            <div className="w-8 h-8 rounded-lg bg-accent/10 flex items-center justify-center shrink-0 mt-0.5">
              <FileText size={16} className="text-accent" />
            </div>
            <div>
              <p className="text-xs font-semibold text-text-primary group-hover:text-accent-glow transition-colors">
                Start from a sample round
              </p>
              <p className="text-[11px] text-text-muted mt-0.5">
                Create a new draft pre-loaded with 3 sample NU7 proposals
                to see how the builder works.
              </p>
            </div>
          </button>

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

        {/* Resources */}
        <h2 className="text-xs font-semibold text-text-primary mb-3">
          Resources
        </h2>
        <div className="bg-surface-1 border border-border-subtle rounded-xl p-5 space-y-2 mb-8">
          <a
            href="https://valargroup.gitbook.io/shielded-vote-docs/chain/building-from-source"
            target="_blank"
            rel="noopener noreferrer"
            className="flex items-center gap-2 text-[11px] text-accent hover:underline"
          >
            <ExternalLink size={12} />
            Building from Source
          </a>
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
  if (!NULLIFIER_BASE_URL && import.meta.env.DEV) {
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
    fetch(`${nullifierApiBase()}/root`)
      .then((res) => {
        if (!res.ok) throw new Error(`HTTP ${res.status}`);
        return res.json();
      })
      .then((json: { height: number | null; num_ranges: number }) => {
        setData({ latest_height: json.height, nullifier_count: json.num_ranges });
        setLoading(false);
      })
      .catch((err) => {
        setError(err instanceof Error ? err.message : String(err));
        setLoading(false);
      });
  }, []);

  // eslint-disable-next-line react-hooks/set-state-in-effect -- initial fetch on mount
  useEffect(() => { refresh(); }, [refresh]);

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
    () => localStorage.getItem("sv-vm-address") ?? ""
  );
  const [chainDetailsOpen, setChainDetailsOpen] = useState(false);

  // Dev private key connection (collapsible section)
  const [devKey, setDevKey] = useState(DEFAULT_DEV_KEY);
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
                      type="text"
                      value={devKey}
                      onChange={(e) => setDevKey(e.target.value.trim())}
                      placeholder="64-character hex private key"
                      spellCheck={false}
                      autoComplete="off"
                      data-1p-ignore
                      data-lpignore="true"
                      style={devKeyVisible ? undefined : { WebkitTextSecurity: "disc" } as React.CSSProperties}
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
                            placeholder="sv1..."
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
  wallet,
  status,
  result,
  error,
  onConfirm,
  onClose,
}: {
  round: VotingRound;
  wallet: UseWallet;
  status: "idle" | "publishing" | "ok" | "error";
  result: string;
  error: string;
  onConfirm: () => void;
  onClose: () => void;
}) {
  const [devKey, setDevKey] = useState(DEFAULT_DEV_KEY);
  const [devKeyVisible, setDevKeyVisible] = useState(false);
  const walletConnected = !!wallet.address;

  const handleConnectDev = async () => {
    await wallet.connectDev(devKey);
    setDevKey("");
  };

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60">
      <div className="bg-surface-1 border border-border rounded-xl shadow-xl max-w-md w-full mx-4">
        <div className="flex items-center justify-between px-5 py-4 border-b border-border-subtle">
          <h3 className="text-sm font-semibold text-text-primary">
            {walletConnected ? "Publish to chain" : "Connect admin wallet"}
          </h3>
          <button
            onClick={onClose}
            className="p-1 hover:bg-surface-3 rounded text-text-muted cursor-pointer"
          >
            <X size={14} />
          </button>
        </div>

        <div className="px-5 py-4 space-y-3">
          {walletConnected ? (
            <>
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
                      : "10 min from now (default)"
                  }
                />
                <SettingsStubRow
                  label="Signer"
                  value={`${wallet.address!.slice(0, 12)}...${wallet.address!.slice(-6)}`}
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
            </>
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
                  Paste dev private key
                </summary>
                <div className="mt-2 space-y-2">
                  <div className="relative">
                    <input
                      type="text"
                      value={devKey}
                      onChange={(e) => setDevKey(e.target.value.trim())}
                      placeholder="64-character hex private key"
                      spellCheck={false}
                      autoComplete="off"
                      data-1p-ignore
                      data-lpignore="true"
                      style={devKeyVisible ? undefined : { WebkitTextSecurity: "disc" } as React.CSSProperties}
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

        <div className="flex justify-end gap-2 px-5 py-3 border-t border-border-subtle">
          <button
            onClick={onClose}
            className="px-3 py-1.5 text-[11px] text-text-secondary hover:text-text-primary hover:bg-surface-2 rounded-md transition-colors cursor-pointer"
          >
            {status === "ok" ? "Done" : "Cancel"}
          </button>
          {status !== "ok" && walletConnected && (
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

/* ── Validators view ─────────────────────────────────────────── */

const BOND_STATUS_LABELS: Record<string, { label: string; color: string }> = {
  BOND_STATUS_BONDED: { label: "Active", color: "bg-success/20 text-success" },
  BOND_STATUS_UNBONDING: { label: "Unbonding", color: "bg-warning/20 text-warning" },
  BOND_STATUS_UNBONDED: { label: "Inactive", color: "bg-surface-3 text-text-muted" },
};

function formatTokens(raw: string | undefined): string {
  if (!raw) return "0";
  // Cosmos SDK tokens are typically in micro denomination (1e6).
  // For display, show the integer with commas. Chains may vary in denomination
  // so we show the raw value formatted with locale separators.
  const n = BigInt(raw);
  return n.toLocaleString();
}

function ValidatorsView({ wallet }: { wallet: UseWallet }) {
  const [validators, setValidators] = useState<chainApi.Validator[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [ceremony, setCeremony] = useState<chainApi.CeremonyState | null>(null);
  const [pallasKeys, setPallasKeys] = useState<Set<string>>(new Set());
  const [showAddModal, setShowAddModal] = useState(false);
  const [unjailing, setUnjailing] = useState<string | null>(null); // operator_address being unjailed
  const [unjailResult, setUnjailResult] = useState<{ addr: string; ok: boolean; msg: string } | null>(null);

  // Pending validator registrations.
  const [pendingRegistrations, setPendingRegistrations] = useState<chainApi.PendingRegistration[]>([]);
  const [approvingAddr, setApprovingAddr] = useState<string | null>(null);
  const [rejectingAddr, setRejectingAddr] = useState<string | null>(null);
  const [approveAmounts, setApproveAmounts] = useState<Record<string, string>>({});
  const [approveResult, setApproveResult] = useState<{ addr: string; ok: boolean; msg: string } | null>(null);

  // Approved servers + pulse status.
  const [approvedServers, setApprovedServers] = useState<chainApi.ServiceEntry[]>([]);
  const [serverPulses, setServerPulses] = useState<chainApi.ServerPulses>({});
  const [removingServer, setRemovingServer] = useState<string | null>(null);
  const [removeResult, setRemoveResult] = useState<{ addr: string; ok: boolean; msg: string } | null>(null);

  // Edge Config network management state.
  const [votingConfig, setVotingConfig] = useState<chainApi.VotingConfig | null>(null);
  const [urlInputFor, setUrlInputFor] = useState<string | null>(null); // moniker being edited
  const [urlInput, setUrlInput] = useState("");
  const [includePir, setIncludePir] = useState(false);
  const [networkUpdating, setNetworkUpdating] = useState(false);
  const [networkResult, setNetworkResult] = useState<{ moniker: string; ok: boolean; msg: string } | null>(null);

  const handleUnjail = async (operatorAddress: string) => {
    if (!wallet.signer) return;
    setUnjailing(operatorAddress);
    setUnjailResult(null);
    try {
      const base = chainApi.getApiBase();
      const res = await cosmosTx.unjailValidator(base, wallet.signer, operatorAddress);
      if (res.code === 0) {
        setUnjailResult({ addr: operatorAddress, ok: true, msg: `Unjailed (tx ${res.tx_hash.slice(0, 12)}…)` });
        fetchValidators(); // refresh list
      } else {
        setUnjailResult({ addr: operatorAddress, ok: false, msg: res.log || `tx failed (code ${res.code})` });
      }
    } catch (err) {
      setUnjailResult({ addr: operatorAddress, ok: false, msg: err instanceof Error ? err.message : String(err) });
    } finally {
      setUnjailing(null);
    }
  };

  const fetchValidators = async (silent = false) => {
    if (!silent) setLoading(true);
    setError("");
    try {
      const [valResp, ceremonyResp, pallasResp, vcResp, pendingResp, approvedResp, pulsesResp] = await Promise.all([
        chainApi.getValidators(),
        chainApi.getCeremonyState().catch(() => null),
        chainApi.getPallasKeys().catch(() => ({ validators: [] })),
        chainApi.getVotingConfig().catch(() => null),
        chainApi.getPendingRegistrations().catch(() => []),
        chainApi.getApprovedServers().catch(() => []),
        chainApi.getServerPulses().catch(() => ({})),
      ]);
      setValidators(valResp.validators ?? []);
      setCeremony(ceremonyResp);
      setPallasKeys(new Set(pallasResp.validators.map((v) => v.validator_address)));
      setVotingConfig(vcResp);
      setPendingRegistrations(pendingResp);
      setApprovedServers(approvedResp);
      setServerPulses(pulsesResp);
    } catch (err) {
      if (!silent) setError(err instanceof Error ? err.message : String(err));
    } finally {
      if (!silent) setLoading(false);
    }
  };

  // Register or remove a validator's public URL in Edge Config.
  const updateNetwork = async (action: "add" | "remove", moniker: string, url?: string) => {
    if (!wallet.address) return;
    setNetworkUpdating(true);
    setNetworkResult(null);
    try {
      const current = votingConfig ?? { version: 1, vote_servers: [], pir_servers: [] };
      let updated: chainApi.VotingConfig;

      if (action === "add" && url) {
        const label = moniker;
        // Add to vote_servers if not already present.
        const existing = current.vote_servers.find((s) => s.url === url);
        if (!existing) {
          updated = {
            ...current,
            vote_servers: [...current.vote_servers, { url, label }],
            pir_servers: includePir
              ? [...current.pir_servers, { url, label }]
              : current.pir_servers,
          };
        } else {
          updated = current;
        }
      } else if (action === "remove") {
        // Remove all entries with this label from both server lists.
        updated = {
          ...current,
          vote_servers: current.vote_servers.filter((s) => s.label !== moniker),
          pir_servers: current.pir_servers.filter((s) => s.label !== moniker),
        };
      } else {
        return;
      }

      const payloadStr = JSON.stringify(updated);
      const sig = await wallet.signPayload(payloadStr);
      await chainApi.updateVotingConfig({
        payload: updated,
        signature: sig.signature,
        pubKey: sig.pubKey,
        signerAddress: wallet.address,
      });

      setVotingConfig(updated);
      setUrlInputFor(null);
      setUrlInput("");
      setIncludePir(false);
      setNetworkResult({ moniker, ok: true, msg: action === "add" ? "Registered" : "Removed" });
    } catch (err) {
      setNetworkResult({ moniker, ok: false, msg: err instanceof Error ? err.message : String(err) });
    } finally {
      setNetworkUpdating(false);
    }
  };

  // Approve a pending registration: sign approval, call edge function, then fund on-chain.
  const handleApproveRegistration = async (reg: chainApi.PendingRegistration) => {
    if (!wallet.address || !wallet.signer) return;
    setApprovingAddr(reg.operator_address);
    setApproveResult(null);
    try {
      // 1. Approve registration (moves URL to vote_servers).
      const approvePayload = { action: "approve" as const, operator_address: reg.operator_address };
      const payloadStr = JSON.stringify(approvePayload);
      const sig = await wallet.signPayload(payloadStr);
      await chainApi.approveRegistration({
        payload: approvePayload,
        signature: sig.signature,
        pubKey: sig.pubKey,
        signerAddress: wallet.address,
      });

      // 2. Fund the validator on-chain.
      const amount = approveAmounts[reg.operator_address] || "10000000";
      const base = chainApi.getApiBase();
      const fundRes = await cosmosTx.fundValidator(base, wallet.signer, reg.operator_address, amount);
      if (fundRes.code !== 0) {
        setApproveResult({
          addr: reg.operator_address,
          ok: false,
          msg: `Approved but funding failed: ${fundRes.log || `code ${fundRes.code}`}`,
        });
      } else {
        setApproveResult({
          addr: reg.operator_address,
          ok: true,
          msg: `Approved & funded (tx ${fundRes.tx_hash.slice(0, 12)}…)`,
        });
      }
      fetchValidators(); // refresh
    } catch (err) {
      setApproveResult({
        addr: reg.operator_address,
        ok: false,
        msg: err instanceof Error ? err.message : String(err),
      });
    } finally {
      setApprovingAddr(null);
    }
  };

  // Reject a pending registration: sign rejection, call edge function to remove from pending.
  const handleRejectRegistration = async (reg: chainApi.PendingRegistration) => {
    if (!wallet.address || !wallet.signer) return;
    setRejectingAddr(reg.operator_address);
    setApproveResult(null);
    try {
      const rejectPayload = { action: "reject" as const, operator_address: reg.operator_address };
      const payloadStr = JSON.stringify(rejectPayload);
      const sig = await wallet.signPayload(payloadStr);
      await chainApi.rejectRegistration({
        payload: rejectPayload,
        signature: sig.signature,
        pubKey: sig.pubKey,
        signerAddress: wallet.address,
      });
      setApproveResult({
        addr: reg.operator_address,
        ok: true,
        msg: "Registration rejected",
      });
      fetchValidators(); // refresh
    } catch (err) {
      setApproveResult({
        addr: reg.operator_address,
        ok: false,
        msg: err instanceof Error ? err.message : String(err),
      });
    } finally {
      setRejectingAddr(null);
    }
  };

  const handleRemoveApprovedServer = async (srv: chainApi.ServiceEntry) => {
    if (!wallet.address || !srv.operator_address) return;
    setRemovingServer(srv.operator_address);
    setRemoveResult(null);
    try {
      const removePayload = { action: "remove-approved" as const, operator_address: srv.operator_address };
      const payloadStr = JSON.stringify(removePayload);
      const sig = await wallet.signPayload(payloadStr);
      await chainApi.removeApprovedServer({
        payload: removePayload,
        signature: sig.signature,
        pubKey: sig.pubKey,
        signerAddress: wallet.address,
      });
      setApprovedServers((prev) => prev.filter((s) => s.operator_address !== srv.operator_address));
      setRemoveResult({ addr: srv.operator_address, ok: true, msg: "Removed" });
    } catch (err) {
      setRemoveResult({
        addr: srv.operator_address,
        ok: false,
        msg: err instanceof Error ? err.message : String(err),
      });
    } finally {
      setRemovingServer(null);
    }
  };

  useEffect(() => {
    fetchValidators();
    const id = setInterval(() => fetchValidators(true), 5000);
    return () => clearInterval(id);
  }, []);

  // Build a set of per-round ceremony participants for cross-referencing.
  const ceremonyValidators = new Set(
    ceremony?.ceremony?.validators?.map((v) => v.validator_address) ?? []
  );

  const sorted = [...validators].sort((a, b) => {
    const aName = (a.description?.moniker ?? "").toLowerCase();
    const bName = (b.description?.moniker ?? "").toLowerCase();
    return aName.localeCompare(bName);
  });

  // Compute total bonded power for percentage display.
  const totalPower = validators
    .filter((v) => v.status === "BOND_STATUS_BONDED")
    .reduce((sum, v) => sum + BigInt(v.tokens ?? "0"), BigInt(0));

  return (
    <div className="flex-1 overflow-y-auto">
      <div className="max-w-2xl mx-auto px-6 py-12">
        {/* Header */}
        <div className="flex items-center justify-between mb-6">
          <div className="flex items-center gap-3">
            <div className="w-10 h-10 rounded-xl bg-accent/15 flex items-center justify-center">
              <Users size={22} className="text-accent" />
            </div>
            <div>
              <h1 className="text-lg font-bold text-text-primary">
                Validators
              </h1>
              <p className="text-[11px] text-text-muted">
                Active validator set on the Shielded-Vote chain
              </p>
            </div>
          </div>
          <button
            onClick={() => fetchValidators()}
            className="p-2 hover:bg-surface-3 rounded-lg text-text-muted hover:text-text-secondary cursor-pointer"
            title="Refresh"
          >
            <RefreshCw size={14} className={loading ? "animate-spin" : ""} />
          </button>
        </div>

        {/* Pending validator registrations */}
        {pendingRegistrations.length > 0 && wallet.address && (
          <div className="mb-6">
            <div className="flex items-center gap-2 mb-3">
              <span className="text-[10px] text-text-muted uppercase tracking-wider">
                Pending registrations
              </span>
              <span className="text-[9px] bg-warning/20 text-warning px-1.5 py-0.5 rounded-full">
                {pendingRegistrations.length}
              </span>
            </div>
            <div className="space-y-2">
              {pendingRegistrations.map((reg) => {
                const submitted = Math.floor(Date.now() / 1000) - reg.timestamp;
                const hours = Math.floor(submitted / 3600);
                const days = Math.floor(hours / 24);
                const timeAgo = days > 0 ? `${days}d ago` : hours > 0 ? `${hours}h ago` : "just now";

                return (
                  <div
                    key={reg.operator_address}
                    className="bg-warning/5 border border-warning/20 rounded-xl p-4"
                  >
                    <div className="flex items-start justify-between gap-3">
                      <div className="min-w-0 flex-1">
                        <div className="flex items-center gap-2">
                          <span className="text-xs font-semibold text-text-primary truncate">
                            {reg.moniker}
                          </span>
                          <span className="text-[9px] px-2 py-0.5 rounded-full bg-warning/20 text-warning">
                            Pending
                          </span>
                          <span className="text-[9px] text-text-muted">{timeAgo}</span>
                        </div>
                        <p className="text-[10px] text-text-muted font-mono mt-1 truncate">
                          {reg.operator_address}
                        </p>
                        <div className="flex items-center gap-1 mt-1">
                          <Server size={9} className="text-text-muted" />
                          <span className="text-[10px] text-text-secondary truncate">{reg.url}</span>
                        </div>
                      </div>
                      <div className="shrink-0 flex flex-col items-end gap-1.5">
                        <div className="flex items-center gap-1.5">
                          <input
                            type="text"
                            value={approveAmounts[reg.operator_address] ?? "10000000"}
                            onChange={(e) =>
                              setApproveAmounts((prev) => ({
                                ...prev,
                                [reg.operator_address]: e.target.value,
                              }))
                            }
                            className="w-24 px-1.5 py-0.5 bg-surface-2 border border-border-subtle rounded text-[10px] text-text-primary text-right font-mono focus:outline-none focus:border-accent/50"
                            placeholder="amount"
                          />
                          <span className="text-[9px] text-text-muted">usvote</span>
                        </div>
                        <div className="flex items-center gap-1.5">
                          <button
                            className="flex items-center gap-1 px-2.5 py-1 rounded-md bg-accent/90 hover:bg-accent text-surface-0 text-[10px] font-semibold transition-colors cursor-pointer disabled:opacity-50"
                            disabled={approvingAddr === reg.operator_address || rejectingAddr === reg.operator_address}
                            onClick={() => handleApproveRegistration(reg)}
                          >
                            {approvingAddr === reg.operator_address ? (
                              <>
                                <Loader2 size={10} className="animate-spin" /> Approving…
                              </>
                            ) : (
                              "Approve & Fund"
                            )}
                          </button>
                          <button
                            className="flex items-center gap-1 px-2 py-1 rounded-md bg-danger/15 hover:bg-danger/25 text-danger text-[10px] font-semibold transition-colors cursor-pointer disabled:opacity-50"
                            disabled={approvingAddr === reg.operator_address || rejectingAddr === reg.operator_address}
                            onClick={() => handleRejectRegistration(reg)}
                          >
                            {rejectingAddr === reg.operator_address ? (
                              <>
                                <Loader2 size={10} className="animate-spin" /> Rejecting…
                              </>
                            ) : (
                              <>
                                <Trash2 size={10} /> Reject
                              </>
                            )}
                          </button>
                        </div>
                      </div>
                    </div>
                    {approveResult?.addr === reg.operator_address && (
                      <p className={`text-[10px] mt-2 ${approveResult.ok ? "text-success" : "text-danger"}`}>
                        {approveResult.msg}
                      </p>
                    )}
                  </div>
                );
              })}
            </div>
          </div>
        )}

        {/* Approved submission servers with pulse status */}
        {approvedServers.length > 0 && (
          <div className="mb-6">
            <div className="flex items-center gap-2 mb-2">
              <span className="text-[10px] text-text-muted uppercase tracking-wider">
                Approved Submission Servers
              </span>
              <span className="text-[9px] bg-accent/20 text-accent px-1.5 py-0.5 rounded-full">
                {approvedServers.length}
              </span>
            </div>
            <p className="text-[10px] text-text-secondary mb-3">
              Servers that ZODL and other client apps connect to for share submission and chain interaction. Admins can remove unreliable validators from this list without unbonding them.
            </p>
            <div className="space-y-1.5">
              {approvedServers.map((srv) => {
                const pulseTime = serverPulses[srv.url];
                const now = Math.floor(Date.now() / 1000);
                const isActive = pulseTime != null && now - pulseTime <= 21600;
                const lastSeen = pulseTime
                  ? `${Math.floor((now - pulseTime) / 60)}m ${(now - pulseTime) % 60}s ago`
                  : "never";

                return (
                  <div key={srv.url}>
                    <div className="flex items-center justify-between bg-surface-1 border border-border-subtle rounded-lg px-3 py-2">
                      <div className="min-w-0 flex-1">
                        <div className="flex items-center gap-2">
                          <span className={`w-1.5 h-1.5 rounded-full ${isActive ? "bg-success" : "bg-text-muted"}`} />
                          <span className="text-[11px] font-semibold text-text-primary truncate">
                            {srv.label}
                          </span>
                          <span className="text-[9px] text-text-muted font-mono truncate">
                            {srv.operator_address ? `${srv.operator_address.slice(0, 12)}…` : ""}
                          </span>
                        </div>
                        <p className="text-[10px] text-text-secondary truncate ml-3.5 mt-0.5">
                          {srv.url}
                        </p>
                      </div>
                      <div className="shrink-0 flex items-center gap-2 ml-3">
                        <div className="text-right">
                          <span className={`text-[9px] px-1.5 py-0.5 rounded-full font-semibold ${
                            isActive
                              ? "bg-success/15 text-success"
                              : "bg-text-muted/15 text-text-muted"
                          }`}>
                            {isActive ? "Active" : "Inactive"}
                          </span>
                          <p className="text-[9px] text-text-muted mt-0.5">
                            {pulseTime ? `Pulse: ${lastSeen}` : "No pulse"}
                          </p>
                        </div>
                        {wallet.address && srv.operator_address && (
                          <button
                            className="text-[9px] px-1.5 py-0.5 rounded bg-danger/15 text-danger hover:bg-danger/25 transition-colors cursor-pointer disabled:opacity-50"
                            disabled={removingServer === srv.operator_address}
                            onClick={() => handleRemoveApprovedServer(srv)}
                          >
                            {removingServer === srv.operator_address ? "…" : "Remove"}
                          </button>
                        )}
                      </div>
                    </div>
                    {removeResult && removeResult.addr === srv.operator_address && !removeResult.ok && (
                      <p className="text-[9px] text-danger mt-1 px-3">{removeResult.msg}</p>
                    )}
                  </div>
                );
              })}
            </div>
          </div>
        )}

        {error && (
          <div className="flex items-center gap-2 bg-danger/10 border border-danger/30 rounded-lg p-3 mb-4">
            <AlertCircle size={14} className="text-danger shrink-0" />
            <p className="text-[11px] text-danger">{error}</p>
          </div>
        )}

        {loading && (
          <div className="flex items-center justify-center py-12">
            <Loader2 size={20} className="text-text-muted animate-spin" />
          </div>
        )}

        {!loading && !error && validators.length === 0 && (
          <div className="text-center py-12">
            <p className="text-xs text-text-muted">
              No validators found on the chain.
            </p>
          </div>
        )}

        {/* Election Authority */}
        {!loading && !error && validators.length > 0 && (
          <div className="mb-4">
            <div className="flex items-center justify-between mb-2">
              <div className="flex items-center gap-2">
                <span className="text-[10px] text-text-muted uppercase tracking-wider">
                  Election Authority
                </span>
                <span className="text-[9px] bg-accent/20 text-accent px-1.5 py-0.5 rounded-full">
                  {validators.length}
                </span>
              </div>
              <button
                onClick={() => setShowAddModal(true)}
                className="flex items-center gap-1 px-2 py-0.5 bg-accent/90 hover:bg-accent text-surface-0 rounded text-[10px] font-semibold transition-colors cursor-pointer"
              >
                <Plus size={10} />
                Fund
              </button>
            </div>
            <p className="text-[10px] text-text-secondary mb-1">
              All bonded validators on-chain. A validator can be bonded and producing blocks but not listed as an approved submission server if it has been taken out of client rotation by an admin.
            </p>
            {pallasKeys.size > 0 && (
              <p className="text-[10px] text-text-secondary">
                {pallasKeys.size} validator{pallasKeys.size !== 1 ? "s have" : " has"} registered
                a Pallas key (<ShieldCheck size={10} className="text-accent inline" />) and {pallasKeys.size !== 1 ? "are" : "is"} eligible
                to participate in EA key ceremonies.
                {ceremonyValidators.size > 0 && <>{" "}Validators with <span className="text-[9px] px-1 py-0.5 rounded-full bg-accent/15 text-accent font-semibold">EA</span> are participating in the current round{"'"}s ceremony.</>}
              </p>
            )}
          </div>
        )}

        <div className="space-y-2">
          {sorted.map((val, i) => {
            const moniker = val.description?.moniker || "Unknown";
            const statusInfo = BOND_STATUS_LABELS[val.status ?? ""] ?? {
              label: val.status ?? "Unknown",
              color: "bg-surface-3 text-text-muted",
            };
            const tokens = val.tokens ?? "0";
            const powerPct =
              totalPower > BigInt(0) && val.status === "BOND_STATUS_BONDED"
                ? Number((BigInt(tokens) * BigInt(10000)) / totalPower) / 100
                : 0;
            const hasPallasKey = pallasKeys.has(val.operator_address ?? "");
            const isCeremonyParticipant = ceremonyValidators.has(val.operator_address ?? "");

            return (
              <div
                key={val.operator_address ?? i}
                className="bg-surface-1 border border-border-subtle rounded-xl p-4"
              >
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2">
                      <span className="text-xs font-semibold text-text-primary truncate">
                        {moniker}
                      </span>
                      {hasPallasKey && (
                        <span title="Pallas key registered">
                          <ShieldCheck size={12} className="text-accent shrink-0" />
                        </span>
                      )}
                      {isCeremonyParticipant && (
                        <span className="text-[8px] px-1.5 py-0.5 rounded-full bg-accent/15 text-accent font-semibold shrink-0" title="Participating in active round ceremony">
                          EA
                        </span>
                      )}
                      {val.jailed && (
                        <>
                          <span title="Jailed"><ShieldAlert size={12} className="text-danger shrink-0" /></span>
                          {wallet.address && (
                            <button
                              className="text-[9px] px-1.5 py-0.5 rounded bg-danger/20 text-danger hover:bg-danger/30 transition-colors disabled:opacity-50"
                              disabled={unjailing === val.operator_address}
                              onClick={() => handleUnjail(val.operator_address!)}
                              title="Signer must be this validator's operator"
                            >
                              {unjailing === val.operator_address ? "Unjailing…" : "Unjail"}
                            </button>
                          )}
                        </>
                      )}
                      <span className={`text-[9px] px-2 py-0.5 rounded-full shrink-0 ${statusInfo.color}`}>
                        {statusInfo.label}
                      </span>
                    </div>

                    {/* Operator address */}
                    <p className="text-[10px] text-text-muted font-mono mt-1 truncate">
                      {val.operator_address}
                    </p>

                    {/* Unjail result */}
                    {unjailResult && unjailResult.addr === val.operator_address && (
                      <p className={`text-[10px] mt-1 ${unjailResult.ok ? "text-green-400" : "text-danger"}`}>
                        {unjailResult.msg}
                      </p>
                    )}

                    {/* Description */}
                    {val.description?.details && (
                      <p className="text-[10px] text-text-secondary mt-1 line-clamp-2">
                        {val.description.details}
                      </p>
                    )}
                  </div>

                  {/* Stats column */}
                  <div className="shrink-0 text-right space-y-1">
                    <div>
                      <p className="text-[10px] text-text-muted">Voting power</p>
                      <p className="text-[11px] font-mono text-text-primary">
                        {formatTokens(tokens)}
                        {powerPct > 0 && (
                          <span className="text-text-muted ml-1">({powerPct.toFixed(1)}%)</span>
                        )}
                      </p>
                    </div>
                  </div>
                </div>

                {/* Power bar */}
                {val.status === "BOND_STATUS_BONDED" && powerPct > 0 && (
                  <div className="mt-2 h-1 bg-surface-3 rounded-full overflow-hidden">
                    <div
                      className="h-full rounded-full bg-accent/60 transition-all duration-500"
                      style={{ width: `${Math.max(1, powerPct)}%` }}
                    />
                  </div>
                )}

                {/* Website link */}
                {val.description?.website && (
                  <div className="mt-2">
                    <a
                      href={val.description.website.startsWith("http") ? val.description.website : `https://${val.description.website}`}
                      target="_blank"
                      rel="noopener noreferrer"
                      className="inline-flex items-center gap-1 text-[10px] text-accent hover:text-accent-glow transition-colors"
                    >
                      <ExternalLink size={9} />
                      {val.description.website.replace(/^https?:\/\//, "")}
                    </a>
                  </div>
                )}

                {/* Network URL (Edge Config) */}
                {(() => {
                  const registeredUrl = votingConfig?.vote_servers.find(
                    (s) => s.label === moniker
                  )?.url;
                  const isPir = votingConfig?.pir_servers.some(
                    (s) => s.label === moniker
                  );

                  if (registeredUrl) {
                    return (
                      <div className="mt-2 flex items-center gap-2">
                        <Server size={10} className="text-success shrink-0" />
                        <span className="text-[10px] text-text-secondary truncate">{registeredUrl}</span>
                        {isPir && (
                          <span className="text-[8px] bg-accent/15 text-accent px-1.5 py-0.5 rounded-full shrink-0">PIR</span>
                        )}
                        {wallet.address && (
                          <button
                            className="text-[9px] px-1.5 py-0.5 rounded bg-danger/15 text-danger hover:bg-danger/25 transition-colors shrink-0 disabled:opacity-50"
                            disabled={networkUpdating}
                            onClick={() => updateNetwork("remove", moniker)}
                          >
                            Remove
                          </button>
                        )}
                        {networkResult?.moniker === moniker && (
                          <span className={`text-[9px] ${networkResult.ok ? "text-success" : "text-danger"}`}>
                            {networkResult.msg}
                          </span>
                        )}
                      </div>
                    );
                  }

                  if (urlInputFor === moniker) {
                    return (
                      <div className="mt-2 space-y-1.5">
                        <input
                          type="text"
                          value={urlInput}
                          onChange={(e) => setUrlInput(e.target.value.trim())}
                          placeholder="https://validator.example.com"
                          spellCheck={false}
                          autoComplete="off"
                          className="w-full px-2 py-1 bg-surface-2 border border-border-subtle rounded text-[10px] text-text-primary placeholder:text-text-muted focus:outline-none focus:border-accent/50 font-mono"
                        />
                        <div className="flex items-center gap-3">
                          <label className="flex items-center gap-1 text-[10px] text-text-secondary cursor-pointer">
                            <input
                              type="checkbox"
                              checked={includePir}
                              onChange={(e) => setIncludePir(e.target.checked)}
                              className="rounded"
                            />
                            Also register as PIR server
                          </label>
                          <div className="flex-1" />
                          <button
                            className="text-[10px] text-text-muted hover:text-text-secondary cursor-pointer"
                            onClick={() => { setUrlInputFor(null); setUrlInput(""); setIncludePir(false); }}
                          >
                            Cancel
                          </button>
                          <button
                            className="text-[10px] px-2 py-0.5 rounded bg-accent/90 text-surface-0 hover:bg-accent transition-colors disabled:opacity-50 cursor-pointer"
                            disabled={!urlInput.startsWith("http") || networkUpdating}
                            onClick={() => updateNetwork("add", moniker, urlInput)}
                          >
                            {networkUpdating ? "Saving…" : "Save"}
                          </button>
                        </div>
                        {networkResult?.moniker === moniker && !networkResult.ok && (
                          <p className="text-[9px] text-danger">{networkResult.msg}</p>
                        )}
                      </div>
                    );
                  }

                  if (wallet.address) {
                    return (
                      <div className="mt-2">
                        <button
                          className="inline-flex items-center gap-1 text-[10px] text-text-muted hover:text-accent transition-colors cursor-pointer"
                          onClick={() => { setUrlInputFor(moniker); setNetworkResult(null); }}
                        >
                          <Server size={9} />
                          Register public URL
                        </button>
                      </div>
                    );
                  }

                  return null;
                })()}
              </div>
            );
          })}
        </div>
      </div>

      {/* Fund validator modal */}
      {showAddModal && (
        <FundValidatorModal
          wallet={wallet}
          onClose={() => setShowAddModal(false)}
        />
      )}
    </div>
  );
}

/* ── Add validator modal ────────────────────────────────────── */

function FundValidatorModal({
  wallet,
  onClose,
}: {
  wallet: UseWallet;
  onClose: () => void;
}) {
  const [devKey, setDevKey] = useState(DEFAULT_DEV_KEY);
  const [devKeyVisible, setDevKeyVisible] = useState(false);
  const [recipientAddress, setRecipientAddress] = useState("");
  const [addressError, setAddressError] = useState("");
  const [amount, setAmount] = useState("10000000");
  const [sending, setSending] = useState(false);
  const [result, setResult] = useState<{ success: boolean; txHash?: string; error?: string } | null>(null);
  const walletConnected = !!wallet.address;

  const handleConnectDev = async () => {
    await wallet.connectDev(devKey);
    setDevKey("");
  };

  const handleAddressChange = (value: string) => {
    const trimmed = value.trim();
    setRecipientAddress(trimmed);
    if (!trimmed) {
      setAddressError("");
      return;
    }
    try {
      fromBech32(trimmed);
      setAddressError("");
    } catch {
      setAddressError("Invalid bech32 address");
    }
  };

  const addressValid = recipientAddress.length > 0 && !addressError;
  const amountNum = parseInt(amount, 10);
  const amountValid = !isNaN(amountNum) && amountNum > 0;
  const canSubmit = addressValid && amountValid && !sending;

  const handleSubmit = async () => {
    if (!canSubmit || !wallet.signer) return;
    setSending(true);
    setResult(null);
    try {
      const base = chainApi.getApiBase();
      const res = await cosmosTx.fundValidator(base, wallet.signer, recipientAddress, amount);
      if (res.code === 0) {
        setResult({ success: true, txHash: res.tx_hash });
      } else {
        setResult({ success: false, error: res.log || `Transaction failed (code ${res.code})` });
      }
    } catch (e) {
      setResult({ success: false, error: e instanceof Error ? e.message : String(e) });
    } finally {
      setSending(false);
    }
  };

  // Display amount in SVOTE (1 SVOTE = 1,000,000 usvote)
  const displayAmount = amountValid ? `${(amountNum / 1_000_000).toLocaleString()} SVOTE` : "";

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60">
      <div className="bg-surface-1 border border-border rounded-xl shadow-xl max-w-md w-full mx-4">
        <div className="flex items-center justify-between px-5 py-4 border-b border-border-subtle">
          <h3 className="text-sm font-semibold text-text-primary">
            {walletConnected ? "Fund validator" : "Connect admin wallet"}
          </h3>
          <button
            onClick={onClose}
            className="p-1 hover:bg-surface-3 rounded text-text-muted cursor-pointer"
          >
            <X size={14} />
          </button>
        </div>

        <div className="px-5 py-4 space-y-3">
          {walletConnected ? (
            result?.success ? (
              /* Success state */
              <div className="space-y-3">
                <div className="bg-success/10 border border-success/30 rounded-lg p-4">
                  <div className="flex items-center gap-2 mb-2">
                    <CheckCircle2 size={16} className="text-success" />
                    <span className="text-xs font-semibold text-success">
                      Funds sent
                    </span>
                  </div>
                  <p className="text-[11px] text-text-secondary">
                    {displayAmount} sent to the validator address.
                  </p>
                </div>

                <div className="bg-surface-2 rounded-lg p-3">
                  <SettingsStubRow label="Recipient" value={
                    `${recipientAddress.slice(0, 14)}...${recipientAddress.slice(-8)}`
                  } />
                  <SettingsStubRow label="Amount" value={displayAmount} />
                  {result.txHash && (
                    <SettingsStubRow label="TX hash" value={
                      `${result.txHash.slice(0, 12)}...${result.txHash.slice(-8)}`
                    } />
                  )}
                </div>
              </div>
            ) : (
              /* Input state */
              <div className="space-y-3">
                <p className="text-[11px] text-text-secondary">
                  Send stake tokens to a validator address. Only meaningful when
                  connected with the bootstrap operator key.
                </p>

                <div>
                  <label className="block text-[11px] text-text-secondary mb-1.5">
                    Recipient address
                  </label>
                  <input
                    type="text"
                    value={recipientAddress}
                    onChange={(e) => handleAddressChange(e.target.value)}
                    placeholder="sv1..."
                    spellCheck={false}
                    autoComplete="off"
                    className={`w-full px-3 py-2 bg-surface-2 border rounded-lg text-xs text-text-primary placeholder:text-text-muted focus:outline-none font-mono ${
                      addressError ? "border-danger/50 focus:border-danger/70" : "border-border-subtle focus:border-accent/50"
                    }`}
                  />
                  {addressError && (
                    <p className="text-[10px] text-danger mt-1">{addressError}</p>
                  )}
                </div>

                <div>
                  <label className="block text-[11px] text-text-secondary mb-1.5">
                    Amount (usvote)
                  </label>
                  <input
                    type="text"
                    value={amount}
                    onChange={(e) => setAmount(e.target.value.trim())}
                    placeholder="10000000"
                    spellCheck={false}
                    autoComplete="off"
                    className="w-full px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary placeholder:text-text-muted focus:outline-none focus:border-accent/50 font-mono"
                  />
                  {amountValid && (
                    <p className="text-[10px] text-text-muted mt-1">
                      = {displayAmount}
                    </p>
                  )}
                </div>

                <SettingsStubRow label="From" value={
                  `${wallet.address!.slice(0, 12)}...${wallet.address!.slice(-6)}`
                } />

                {result && !result.success && (
                  <div className="flex items-start gap-2 bg-danger/10 border border-danger/30 rounded-lg p-3">
                    <AlertCircle size={13} className="text-danger mt-0.5 shrink-0" />
                    <p className="text-[11px] text-text-secondary break-all">{result.error}</p>
                  </div>
                )}
              </div>
            )
          ) : (
            /* Wallet connection state */
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
                  Paste dev private key
                </summary>
                <div className="mt-2 space-y-2">
                  <div className="relative">
                    <input
                      type="text"
                      value={devKey}
                      onChange={(e) => setDevKey(e.target.value.trim())}
                      placeholder="64-character hex private key"
                      spellCheck={false}
                      autoComplete="off"
                      data-1p-ignore
                      data-lpignore="true"
                      style={devKeyVisible ? undefined : { WebkitTextSecurity: "disc" } as React.CSSProperties}
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

        <div className="flex justify-end gap-2 px-5 py-3 border-t border-border-subtle">
          <button
            onClick={onClose}
            className="px-3 py-1.5 text-[11px] text-text-secondary hover:text-text-primary hover:bg-surface-2 rounded-md transition-colors cursor-pointer"
          >
            {result?.success ? "Done" : "Cancel"}
          </button>
          {walletConnected && !result?.success && (
            <button
              onClick={handleSubmit}
              disabled={!canSubmit}
              className="flex items-center gap-1.5 px-3 py-1.5 bg-accent/90 hover:bg-accent text-surface-0 rounded-md text-[11px] font-semibold transition-colors cursor-pointer disabled:opacity-50"
            >
              {sending ? (
                <><Loader2 size={12} className="animate-spin" /> Sending...</>
              ) : (
                "Send funds"
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
  SESSION_STATUS_PENDING: { label: "Pending", color: "bg-orange-500/20 text-orange-400" },
  SESSION_STATUS_ACTIVE: { label: "Active", color: "bg-success/20 text-success" },
  SESSION_STATUS_TALLYING: { label: "Tallying", color: "bg-warning/20 text-warning" },
  SESSION_STATUS_FINALIZED: { label: "Finalized", color: "bg-blue-500/20 text-blue-400" },
  4: { label: "Pending", color: "bg-orange-500/20 text-orange-400" },
  1: { label: "Active", color: "bg-success/20 text-success" },
  2: { label: "Tallying", color: "bg-warning/20 text-warning" },
  3: { label: "Finalized", color: "bg-blue-500/20 text-blue-400" },
};


const BALLOT_DIVISOR = 12_500_000; // zatoshi per ballot
function ballotsToZEC(ballots: number): string {
  const zatoshi = ballots * BALLOT_DIVISOR;
  const zec = zatoshi / 1e8;
  // Truncate to 2 decimal places (match iOS banker's rounding behavior)
  const truncated = Math.floor(zec * 100) / 100;
  return `${truncated.toFixed(2)} ZEC`;
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

function VoteStatusView({ expectRoundCount }: { expectRoundCount?: number | null }) {
  const [rounds, setRounds] = useState<chainApi.ChainRound[]>([]);
  const [summaries, setSummaries] = useState<Record<string, chainApi.VoteSummaryResponse>>({});
  const [summaryErrors, setSummaryErrors] = useState<Record<string, string>>({});
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const zcashChain = useChainInfo();
  const pollingRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const fetchAll = useCallback(async () => {
    setLoading(true);
    setError("");
    setSummaryErrors({});
    try {
      const resp = await chainApi.listRounds();
      const allRounds = (resp.rounds ?? []).sort((a, b) => {
        const ha = Number(a.created_at_height ?? 0);
        const hb = Number(b.created_at_height ?? 0);
        return ha - hb;
      });
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
      return allRounds.length;
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      return -1;
    } finally {
      setLoading(false);
    }
  }, []);

  // Poll until the expected round count is reached after a publish.
  useEffect(() => {
    let cancelled = false;
    let attempts = 0;
    const maxAttempts = 15; // ~15 seconds max

    const poll = async () => {
      const count = await fetchAll();
      if (cancelled) return;
      if (
        expectRoundCount != null &&
        count >= 0 &&
        count < expectRoundCount &&
        attempts < maxAttempts
      ) {
        attempts++;
        pollingRef.current = setTimeout(poll, 1000);
      }
    };

    poll();

    return () => {
      cancelled = true;
      if (pollingRef.current) clearTimeout(pollingRef.current);
    };
  }, [expectRoundCount, fetchAll]);

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
                Live proposal results from the Shielded-Vote chain
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
          {[...rounds].reverse().map((round, i) => {
            const roundIdx = rounds.length - 1 - i;
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
                <div className="px-5 py-4">
                  <div className="flex items-center justify-between">
                    <div className="flex items-center gap-2 min-w-0">
                      <h2 className="text-sm font-semibold text-text-primary">
                        {round.title || `Round ${roundIdx + 1}`}
                      </h2>
                      <span
                        className={`text-[9px] px-2 py-0.5 rounded-full shrink-0 ${statusInfo.color}`}
                      >
                        {statusInfo.label}
                      </span>
                    </div>
                    {isActive && !isExpired && (
                      <span className="relative flex h-2.5 w-2.5 shrink-0 ml-3">
                        <span className="animate-ping absolute inline-flex h-full w-full rounded-full bg-success opacity-75" />
                        <span className="relative inline-flex rounded-full h-2.5 w-2.5 bg-success" />
                      </span>
                    )}
                  </div>
                  {round.description && (
                    <p className="text-[11px] text-text-secondary mt-1 leading-relaxed">
                      {round.description}
                    </p>
                  )}
                  {endDate && (
                    <p className="text-[10px] text-text-muted mt-1">
                      {isFinalized
                        ? `Ended ${endDate.toLocaleDateString()}`
                        : isExpired
                          ? `Voting ended ${endDate.toLocaleDateString()} (tallying)`
                          : `Voting until ${endDate.toLocaleString()}`}
                    </p>
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

                {/* Ceremony log */}
                {round.ceremony_log && round.ceremony_log.length > 0 && (
                  <div className="px-5 pb-3">
                    <details className="group">
                      <summary className="text-[10px] text-text-muted cursor-pointer hover:text-text-secondary select-none">
                        Ceremony log ({round.ceremony_log.length} entries)
                      </summary>
                      <div className="mt-1.5 bg-surface-2 rounded-md p-2 max-h-40 overflow-y-auto">
                        {round.ceremony_log.map((entry, i) => (
                          <div key={i} className="text-[10px] font-mono text-text-secondary leading-relaxed">
                            {entry}
                          </div>
                        ))}
                      </div>
                    </details>
                  </div>
                )}

                {/* Proposals */}
                {summary?.proposals && summary.proposals.length > 0 && (
                  <div className="px-5 pb-4 space-y-3">
                    {summary.proposals.map((prop) => {
                      const options = prop.options ?? [];

                      // Finalized: use total_value for bars & result.
                      // Active: use ballot_count (shares) — no ZEC conversion possible.
                      // Detect winners (may be multiple if tied).
                      const winnerIndices: Set<number> = new Set();
                      const isTied = (() => {
                        if (!isFinalized) return false;
                        const maxVal = Math.max(
                          ...options.map((o) => Number(o.total_value ?? 0)),
                          0
                        );
                        if (maxVal <= 0) return false;
                        for (const o of options) {
                          if (Number(o.total_value ?? 0) === maxVal) {
                            winnerIndices.add(o.index ?? 0);
                          }
                        }
                        return winnerIndices.size > 1;
                      })();

                      // Result color for banner — uses the option palette
                      const winnerColor = (() => {
                        if (winnerIndices.size === 0) return optionColor(0, options.length);
                        const winnerIdx = [...winnerIndices][0];
                        return optionColor(winnerIdx, options.length);
                      })();

                      const totalValue = isFinalized
                        ? options.reduce((sum, o) => sum + Number(o.total_value ?? 0), 0)
                        : null;
                      const totalShares = options.reduce(
                        (sum, o) => sum + Number(o.ballot_count ?? 0),
                        0
                      );

                      return (
                        <div
                          key={prop.id}
                          className="bg-surface-2 rounded-lg p-3"
                        >
                          <div className="flex items-center gap-2 mb-2">
                            <span className="text-[10px] font-bold text-text-muted bg-surface-3 rounded px-1.5 py-0.5">
                              {String(prop.id ?? 0).padStart(2, "0")}
                            </span>
                            <span className="text-xs font-semibold text-text-primary flex-1">
                              {prop.title || "Untitled"}
                            </span>
                          </div>
                          {prop.description && (
                            <p className="text-[11px] text-text-secondary mb-2 leading-relaxed">
                              {prop.description}
                            </p>
                          )}

                          {/* Result banner — only when finalized */}
                          {isFinalized && winnerIndices.size > 0 && (
                            <div
                              className="flex items-center gap-1.5 mb-2 px-2 py-1 rounded-md"
                              style={{ backgroundColor: `${winnerColor}18` }}
                            >
                              <span className="text-xs" style={{ color: winnerColor }}>{isTied ? "⚖" : "✓"}</span>
                              <span className="text-[11px] font-semibold" style={{ color: winnerColor }}>
                                {isTied ? "Tie: " : "Result: "}
                                {options
                                  .filter((o) => winnerIndices.has(o.index ?? 0))
                                  .map((o) => o.label ?? `Option ${o.index}`)
                                  .join(", ")}
                              </span>
                            </div>
                          )}

                          {/* Option bars */}
                          <div className="space-y-3">
                            {options.map((opt) => {
                              const shares = Number(opt.ballot_count ?? 0);
                              const value = Number(opt.total_value ?? 0);
                              const barValue = isFinalized ? value : shares;

                              // Compute bar width relative to max in this proposal.
                              const allValues = options.map((o) =>
                                isFinalized
                                  ? Number(o.total_value ?? 0)
                                  : Number(o.ballot_count ?? 0)
                              );
                              const maxVal = Math.max(1, ...allValues);
                              const pct = (barValue / maxVal) * 100;
                              const isWinner = winnerIndices.has(opt.index ?? 0);

                              const oColor = optionColor(opt.index ?? 0, options.length);

                              return (
                                <div key={opt.index} className="space-y-0.5">
                                  <div className="flex items-center justify-between">
                                    <span
                                      className={`text-[11px] flex items-center gap-1.5 ${
                                        isWinner ? "font-semibold" : "text-text-secondary"
                                      }`}
                                      style={isWinner ? { color: oColor } : undefined}
                                    >
                                      <span
                                        className="w-2 h-2 rounded-full shrink-0 inline-block"
                                        style={{ backgroundColor: oColor }}
                                      />
                                      {isWinner && (isTied ? "⚖ " : "✓ ")}
                                      {opt.label ?? `Option ${opt.index}`}
                                    </span>
                                    <span className="text-[11px] font-mono text-text-primary">
                                      {isFinalized ? (
                                        <>{ballotsToZEC(value)}</>
                                      ) : (
                                        <>
                                          {shares} share{shares !== 1 ? "s" : ""}
                                        </>
                                      )}
                                    </span>
                                  </div>
                                  <div className="h-1.5 bg-surface-3 rounded-full overflow-hidden">
                                    <div
                                      className="h-full rounded-full transition-all duration-500"
                                      style={{
                                        width: `${Math.max(barValue > 0 ? 2 : 0, pct)}%`,
                                        backgroundColor: oColor,
                                        opacity: isWinner ? 1 : 0.6,
                                      }}
                                    />
                                  </div>
                                </div>
                              );
                            })}
                          </div>

                          {/* Total */}
                          {isFinalized && totalValue !== null && totalValue > 0 ? (
                            <div className="mt-2 pt-2 border-t border-border-subtle">
                              <span className="text-[10px] text-text-muted">
                                Total: {ballotsToZEC(totalValue)}
                              </span>
                            </div>
                          ) : totalShares > 0 ? (
                            <div className="mt-2 pt-2 border-t border-border-subtle">
                              <span className="text-[10px] text-text-muted">
                                Total: {totalShares} share{totalShares !== 1 ? "s" : ""}
                              </span>
                            </div>
                          ) : null}
                        </div>
                      );
                    })}
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
                          <span className="text-xs text-text-primary flex-1">
                            {p.title || "Untitled"}
                          </span>
                        </div>
                        {p.description && (
                          <p className="text-[11px] text-text-secondary mt-2 leading-relaxed">
                            {p.description}
                          </p>
                        )}
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
                  {p.options.map((opt, i) => (
                    <div
                      key={opt.id}
                      className="flex items-center gap-2 px-3 py-2 bg-surface-2 rounded-lg border border-border-subtle hover:border-accent/30 transition-colors cursor-pointer"
                    >
                      <div
                        className="w-3 h-3 rounded-full shrink-0"
                        style={{ backgroundColor: optionColor(i, p.options.length) }}
                      />
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
