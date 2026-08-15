// Tailwind safelist for dynamically-constructed binary-vote classes:
// bg-success bg-success/10 bg-success/60 bg-danger bg-danger/10 bg-danger/60 text-success text-danger
import { Fragment, useState, useCallback, useRef, useEffect, useMemo } from "react";
import { Sidebar } from "./components/Sidebar";
import { TopBar } from "./components/TopBar";
import { ProposalEditor } from "./components/ProposalEditor";
import { JsonView } from "./components/JsonView";
import { RoundEditor } from "./components/RoundEditor";
import { SnapshotSettingsPage } from "./components/SnapshotSettingsPage";
import { PendingOperatorsPage } from "./components/PendingOperatorsPage";
import { PirFleetStatus } from "./components/PirFleetStatus";
import { RoundsList } from "./components/RoundsList";
import { AttestRoundEntryPage } from "./components/AttestRoundEntryPage";
import { EndorsersPage } from "./components/EndorsersPage";
import { UpgradesPage } from "./components/UpgradesPage";
import { QueueMonitorPage } from "./components/QueueMonitorPage";
import { VoteManagerKeysPage } from "./components/VoteManagerKeysPage";
import { useDetectedChainId } from "./hooks/useDetectedChainId";
import { useStore } from "./store/useStore";
import { SAMPLE_ROUND_TEMPLATES, type SampleRoundTemplateId } from "./store/sampleRounds";
import { Shield, Plus, FileText, Settings, Settings2, RefreshCw, CheckCircle2, AlertCircle, AlertTriangle, X, Loader2, Server, Database, Eye, EyeOff, Wallet, Unplug, BarChart3, Copy, Check, Users, ExternalLink, ShieldAlert, ShieldCheck, GripVertical, MoreHorizontal, Trash2, Lock, ChevronDown, ArrowLeft, ClipboardCheck, Menu } from "lucide-react";
import type { Proposal, RoundSettings, RoundStatus, VotingRound } from "./types";
import { MAX_VOTE_OPTIONS, MIN_VOTE_OPTIONS } from "./constants/vote";
import {
  LIGHTWALLETD_ENDPOINTS,
  getStoredRpc,
  setStoredRpc,
  useChainInfo,
  estimateTimestamp,
} from "./store/rpc";
import * as chainApi from "./api/chain";
import { TOKEN_HOLDER_VOTING_CONFIG_REPO_URL, tokenHolderConfigUrl } from "./api/chain";
import * as cosmosTx from "./api/cosmosTx";
import { describeCoordinatorActionPayload } from "./api/coordinatorActions";
import { useWallet } from "./hooks/useWallet";
import type { UseWallet } from "./hooks/useWallet";
import { useUIConfig } from "./store/uiConfigContext";
import {
  COMPLETED_ROUNDS_PAGE_SIZE,
  isTerminalVoteRoundStatus,
  partitionVoteStatusRounds,
  shouldEagerlyLoadVoteSummary,
} from "./utils/voteStatus";

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

type Section =
  | "about"
  | "rounds"
  | "builder"
  | "json"
  | "downloads"
  | "preview"
  | "settings"
  | "vote-status"
  | "queue-monitor"
  | "validators"
  | "validator-join"
  | "coordinator-actions"
  | "attest-round"
  | "endorsers"
  | "upgrades"
  | "snapshot"
  | "vote-manager-keys";

const SECTION_PATHS: Record<Section, string> = {
  about: "/",
  rounds: "/rounds",
  builder: "/builder",
  json: "/json",
  downloads: "/downloads",
  preview: "/preview",
  settings: "/settings",
  "vote-status": "/vote-status",
  "queue-monitor": "/queue-monitor",
  validators: "/validators",
  "validator-join": "/validator-join",
  "coordinator-actions": "/approvals",
  "attest-round": "/attest-round",
  endorsers: "/endorsements",
  upgrades: "/upgrades",
  snapshot: "/snapshot",
  "vote-manager-keys": "/vote-manager-keys",
};

const PATH_TO_SECTION: Record<string, Section> = Object.fromEntries(
  Object.entries(SECTION_PATHS).map(([s, p]) => [p, s as Section])
) as Record<string, Section>;
PATH_TO_SECTION["/endorsers"] = "endorsers";
PATH_TO_SECTION["/coordinator-actions"] = "coordinator-actions";

interface AppRoute {
  section: Section;
  voteStatusRoundId: string | null;
}

function routeFromPath(): AppRoute {
  const path = window.location.pathname.replace(/\/+$/, "") || "/";
  if (path === SECTION_PATHS["vote-status"]) {
    return { section: "vote-status", voteStatusRoundId: null };
  }
  if (path.startsWith(`${SECTION_PATHS["vote-status"]}/`)) {
    const encodedRoundId = path.slice(SECTION_PATHS["vote-status"].length + 1);
    let roundId = encodedRoundId;
    try {
      roundId = decodeURIComponent(encodedRoundId);
    } catch {
      // Leave malformed escape sequences for the detail view's invalid-ID state.
    }
    return {
      section: "vote-status",
      voteStatusRoundId: roundId || null,
    };
  }
  return { section: PATH_TO_SECTION[path] ?? "about", voteStatusRoundId: null };
}

function App() {
  const store = useStore();
  const wallet = useWallet();
  const { precomputedBaseURL, zcashNetwork } = useUIConfig();
  const [route, setRouteState] = useState<AppRoute>(routeFromPath);
  const section = route.section;
  const [filter, setFilter] = useState<RoundStatus | "all">("all");
  // Sidebar is a slide-in drawer on mobile; always-visible on md+ via CSS.
  // Initial state: open on desktop, collapsed on mobile.
  const [sidebarOpen, setSidebarOpen] = useState(
    () => typeof window !== "undefined" && window.innerWidth >= 768,
  );
  const importRef = useRef<HTMLInputElement>(null);
  const [publishModal, setPublishModal] = useState<string | null>(null); // round id
  const [publishStatus, setPublishStatus] = useState<"idle" | "publishing" | "ok" | "error">("idle");
  const [publishResult, setPublishResult] = useState<string>("");
  const [publishError, setPublishError] = useState("");
  const [expectedRoundCount, setExpectedRoundCount] = useState<number | null>(null);

  // Sync section ↔ URL path, keeping nav instant (no full reload).
  const setSection = useCallback((s: Section) => {
    setRouteState({ section: s, voteStatusRoundId: null });
    const path = SECTION_PATHS[s];
    if (window.location.pathname !== path) {
      window.history.pushState(null, "", path);
    }
  }, []);

  const setVoteStatusRound = useCallback((roundIdHex: string) => {
    const path = `${SECTION_PATHS["vote-status"]}/${encodeURIComponent(roundIdHex)}`;
    setRouteState({ section: "vote-status", voteStatusRoundId: roundIdHex });
    if (window.location.pathname !== path) {
      window.history.pushState(null, "", path);
    }
  }, []);

  // Handle browser back/forward buttons.
  useEffect(() => {
    const onPopState = () => setRouteState(routeFromPath());
    window.addEventListener("popstate", onPopState);
    return () => window.removeEventListener("popstate", onPopState);
  }, []);

  const handleSelectRound = useCallback(
    (id: string) => {
      store.setActiveRoundId(id);
      store.setActiveProposalId(null);
      setSection("builder");
    },
    [store, setSection]
  );

  const handleCreateRound = useCallback(() => {
    store.createRound();
    setSection("builder");
  }, [store, setSection]);

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
    [store, setSection]
  );

  const handlePublish = useCallback(
    (roundId: string) => {
      setPublishModal(roundId);
      setPublishStatus("idle");
      setPublishResult("");
      setPublishError("");
    },
    []
  );

  const handlePublishConfirm = useCallback(async () => {
    if (!publishModal) return;
    const round = store.rounds.find((r) => r.id === publishModal);
    if (!round) return;

    if (!wallet.signer) {
      setPublishStatus("error");
      setPublishError(
        "No wallet connected. Go to Settings → Wallet to connect Keplr or paste a private key."
      );
      return;
    }

    const invalidProposalIndex = round.proposals.findIndex((p) => !isProposalValid(p));
    if (invalidProposalIndex !== -1) {
      setPublishStatus("error");
      setPublishError(
        `Proposal ${invalidProposalIndex + 1} must have ${MIN_VOTE_OPTIONS}-${MAX_VOTE_OPTIONS} non-empty options.`
      );
      return;
    }

    const snapshotHeight = parseInt(round.settings.snapshotHeight, 10) || 0;

    if (snapshotHeight === 0) {
      setPublishStatus("error");
      setPublishError("Snapshot height is not available. Enter a height in Round Settings.");
      return;
    }

    if (!precomputedBaseURL) {
      setPublishStatus("error");
      setPublishError(
        "Cannot validate the published PIR snapshot because this svoted did not expose SVOTE_PRECOMPUTED_BASE_URL."
      );
      return;
    }
    if (!zcashNetwork) {
      setPublishStatus("error");
      setPublishError(
        "Cannot validate the published PIR snapshot because this svoted did not expose SVOTE_ZCASH_NETWORK."
      );
      return;
    }
    const validation = await chainApi.validatePublishedSnapshotManifest(
      precomputedBaseURL,
      zcashNetwork,
      snapshotHeight
    );
    if (validation.status !== "valid") {
      const detail =
        validation.status === "missing"
          ? "No manifest.json exists for this height."
          : validation.status === "invalid"
            ? `Manifest is invalid: ${(validation.issues ?? []).join("; ")}`
            : validation.message ?? "Manifest validation failed.";
      setPublishStatus("error");
      setPublishError(detail);
      return;
    }

    try {
      const pirStatus = await chainApi.getSnapshotStatus();
      if (pirStatus.phase === "rebuilding") {
        setPublishStatus("error");
        setPublishError(
          "PIR server is currently rebuilding. Wait for it to complete, then try again."
        );
        return;
      }
      if (pirStatus.phase === "serving" && pirStatus.height != null && pirStatus.height !== snapshotHeight) {
        setPublishStatus("error");
        setPublishError(
          `Cannot publish height ${snapshotHeight.toLocaleString()} because the PIR server is serving ${pirStatus.height.toLocaleString()}. Use ${pirStatus.height.toLocaleString()} as the snapshot height, or update the PIR server first. The chain needs PIR root data at the exact round height to build the transaction.`
        );
        return;
      }
    } catch {
      // Keep createVotingSession as the final source of truth when the PIR
      // status preflight is unavailable.
    }

    if (!round.settings.endTime) {
      setPublishStatus("error");
      setPublishError("Voting end time must be set in Round Settings.");
      return;
    }

    const voteEndTime = Math.floor(new Date(round.settings.endTime).getTime() / 1000);

    const proposals = round.proposals.map((p, i) => ({
      id: i + 1,
      title: p.title,
      description: p.description,
      options: buildChainOptions(p),
      zipNumber: p.zipNumber || undefined,
      forumURL: p.forumURL || undefined,
    }));

    setPublishStatus("publishing");
    setPublishError("");
    try {
      const base = chainApi.getApiBase();
      let roundCountBefore: number | null = null;
      try {
        const before = await chainApi.listRounds();
        roundCountBefore = (before.rounds ?? []).length;
      } catch {
        roundCountBefore = null;
      }
      const result = await cosmosTx.createVotingSession(base, wallet.signer, {
        snapshotHeight,
        voteEndTime,
        proposals,
        description: round.settings.description || round.name,
        title: round.name,
        discussionURL: round.settings.discussionURL || "",
      });
      if (result.code !== 0) {
        setPublishError(result.log || `Transaction failed with code ${result.code}`);
        setPublishStatus("error");
      } else {
        setPublishResult(result.tx_hash);
        setPublishStatus("ok");
        try {
          const resp = await chainApi.listRounds();
          const nextCount = (resp.rounds ?? []).length;
          if (roundCountBefore != null && nextCount > roundCountBefore) {
            store.setRoundStatus(publishModal, "published");
            setExpectedRoundCount(nextCount);
          } else {
            setExpectedRoundCount(null);
          }
        } catch {
          setExpectedRoundCount(null);
        }
      }
    } catch (err) {
      setPublishError(err instanceof Error ? err.message : String(err));
      setPublishStatus("error");
    }
  }, [precomputedBaseURL, zcashNetwork, publishModal, store, wallet.signer]);

  const handleNavigate = useCallback(
    (s: string) => {
      setSection(s as Section);
    },
    [setSection]
  );

  const handleCreateSampleRound = useCallback((templateId: SampleRoundTemplateId) => {
    store.createSampleRound(templateId);
    setSection("builder");
  }, [store, setSection]);

  return (
    <div className="flex h-screen overflow-hidden bg-surface-0">
      <input
        ref={importRef}
        type="file"
        accept=".json"
        className="hidden"
        onChange={handleFileImport}
      />

      {/* Mobile backdrop: tapping it closes the drawer */}
      {sidebarOpen && (
        <div
          className="md:hidden fixed inset-0 bg-black/50 z-30"
          onClick={() => setSidebarOpen(false)}
          aria-hidden
        />
      )}

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
        isOpen={sidebarOpen}
        onClose={() => setSidebarOpen(false)}
      />

      <main className="flex-1 flex flex-col overflow-hidden min-w-0">
        {/* Mobile-only top strip with hamburger */}
        <div className="md:hidden flex items-center px-2 py-2 bg-surface-1 border-b border-border">
          <button
            onClick={() => setSidebarOpen(true)}
            className="p-1.5 rounded-md hover:bg-surface-2 text-text-secondary cursor-pointer"
            aria-label="Open menu"
          >
            <Menu size={18} />
          </button>
        </div>
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

        {section === "validator-join" && <PendingOperatorsPage wallet={wallet} />}

        {section === "coordinator-actions" && <CoordinatorActionsPage wallet={wallet} />}

        {section === "attest-round" && <AttestRoundEntryPage />}

        {section === "vote-manager-keys" && <VoteManagerKeysPage wallet={wallet} />}

        {section === "endorsers" && <EndorsersPage wallet={wallet} />}

        {section === "upgrades" && <UpgradesPage wallet={wallet} />}

        {/* Vote status */}
        {section === "vote-status" && (
          <VoteStatusView
            expectRoundCount={expectedRoundCount}
            selectedRoundIdHex={route.voteStatusRoundId}
            onSelectRound={setVoteStatusRound}
            onBackToList={() => setSection("vote-status")}
          />
        )}

        {section === "queue-monitor" && <QueueMonitorPage />}

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
  return (
    p.title.trim().length > 0 &&
    p.options.length >= MIN_VOTE_OPTIONS &&
    p.options.length <= MAX_VOTE_OPTIONS &&
    p.options.every((option) => option.label.trim().length > 0)
  );
}

function buildChainOptions(p: Proposal): Array<{ index: number; label: string; description: string }> {
  return p.options.map((opt, j) => ({
    index: j,
    label: opt.label,
    description: opt.description ?? "",
  }));
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
  onOpenSample: (templateId: SampleRoundTemplateId) => void;
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
          {SAMPLE_ROUND_TEMPLATES.map((template) => (
            <button
              key={template.id}
              onClick={() => onOpenSample(template.id)}
              className="w-full flex items-start gap-3 bg-surface-1 border border-border-subtle hover:border-accent/30 rounded-xl p-4 text-left transition-colors cursor-pointer group"
            >
              <div className="w-8 h-8 rounded-lg bg-accent/10 flex items-center justify-center shrink-0 mt-0.5">
                <FileText size={16} className="text-accent" />
              </div>
              <div>
                <p className="text-xs font-semibold text-text-primary group-hover:text-accent-glow transition-colors">
                  Start from {template.name.replace(/^\(SAMPLE\)\s*/, "")}
                </p>
                <p className="text-[11px] text-text-muted mt-0.5">
                  {template.summary}
                </p>
              </div>
            </button>
          ))}

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

function useNullifierStatus(
  selectedUrl: string
) {
  const [data, setData] = useState<chainApi.NullifierStatus | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const refresh = useCallback(() => {
    setLoading(true);
    setError(null);
    setData(null);
    chainApi.getNullifierStatus()
      .then((status) => {
        setData(status);
        setLoading(false);
      })
      .catch((err) => {
        setError(err instanceof Error ? err.message : String(err));
        setLoading(false);
      });
  }, []);

  useEffect(() => {
    const timer = window.setTimeout(refresh, 0);
    return () => window.clearTimeout(timer);
  }, [refresh, selectedUrl]);

  return { data, loading, error, refresh };
}

function coordinatorActionID(action: chainApi.CoordinatorAction): number {
  const raw = action.action_id ?? 0;
  return typeof raw === "number" ? raw : parseInt(raw, 10) || 0;
}

function coordinatorActionLabel(action: chainApi.CoordinatorAction): string {
  const typeURL = action.payload?.type_url ?? "";
  const short = typeURL.split(".").pop() || typeURL;
  return short.replace(/^Msg/, "") || "Coordinator action";
}

function coordinatorActionTime(value: number | string | undefined): string {
  const seconds = typeof value === "number" ? value : parseInt(value ?? "0", 10);
  if (!seconds) return "—";
  return new Date(seconds * 1000).toLocaleString();
}

function sameCoordinatorAddress(a: string, b: string): boolean {
  return a.trim().toLowerCase() === b.trim().toLowerCase();
}

function normalizeCoordinatorAddress(addr: string): string {
  return addr.trim().toLowerCase();
}

function currentCoordinatorApprovalSet(
  action: chainApi.CoordinatorAction,
  voteManagers: string[],
): Set<string> {
  const currentManagers = new Set(voteManagers.map(normalizeCoordinatorAddress));
  const seen = new Set<string>();
  for (const approval of action.approvals ?? []) {
    const canonical = normalizeCoordinatorAddress(approval);
    if (currentManagers.has(canonical)) {
      seen.add(canonical);
    }
  }
  return seen;
}

function staleCoordinatorApprovals(
  action: chainApi.CoordinatorAction,
  voteManagers: string[],
): string[] {
  const currentManagers = new Set(voteManagers.map(normalizeCoordinatorAddress));
  const seen = new Set<string>();
  const stale: string[] = [];
  for (const approval of action.approvals ?? []) {
    const canonical = normalizeCoordinatorAddress(approval);
    if (!canonical || seen.has(canonical)) continue;
    seen.add(canonical);
    if (!currentManagers.has(canonical)) {
      stale.push(approval);
    }
  }
  return stale;
}

function shortCoordinatorAddress(addr: string): string {
  if (addr.length <= 24) return addr;
  return `${addr.slice(0, 12)}...${addr.slice(-8)}`;
}

function coordinatorSignatureStatus(approvalCount: number, threshold: number): string {
  const remaining = Math.max(threshold - approvalCount, 0);
  if (remaining === 0) return "Ready to execute";
  return `Needs ${remaining} more signature${remaining === 1 ? "" : "s"}`;
}

function CoordinatorActionsPage({ wallet }: { wallet: UseWallet }) {
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState("");
  const [latestBlock, setLatestBlock] = useState<chainApi.LatestBlockInfo | null>(null);
  const [voteManagers, setVoteManagers] = useState<string[]>([]);
  const [voteManagerThreshold, setVoteManagerThreshold] = useState(1);
  const [minCeremonyValidators, setMinCeremonyValidators] = useState(1);
  const [pendingCoordinatorActions, setPendingCoordinatorActions] = useState<chainApi.CoordinatorAction[]>([]);
  const [approvingActionID, setApprovingActionID] = useState<number | null>(null);
  const [approvalTxHash, setApprovalTxHash] = useState("");
  const [approvalError, setApprovalError] = useState("");
  const [devKey, setDevKey] = useState("");
  const [devKeyVisible, setDevKeyVisible] = useState(false);
  const [vmNewAddrs, setVmNewAddrs] = useState("");
  const [vmNewThreshold, setVmNewThreshold] = useState("1");
  const [vmNewMinCeremonyValidators, setVmNewMinCeremonyValidators] = useState("1");
  const [vmDraftInitialized, setVmDraftInitialized] = useState(false);
  const [vmTxStatus, setVmTxStatus] = useState<"idle" | "sending" | "ok" | "error">("idle");
  const [vmTxError, setVmTxError] = useState("");
  const [vmTxHash, setVmTxHash] = useState("");

  const refreshCoordinatorState = useCallback(async () => {
    setLoading(true);
    setLoadError("");
    try {
      const [block, vmResp, pendingResp] = await Promise.all([
        chainApi.getLatestBlock(),
        chainApi.getVoteManagers(),
        chainApi.getPendingCoordinatorActions(),
      ]);
      setLatestBlock(block);
      setVoteManagers(vmResp.vote_manager_addresses ?? []);
      setVoteManagerThreshold(vmResp.threshold ?? 1);
      setMinCeremonyValidators(vmResp.min_ceremony_validators ?? 1);
      setPendingCoordinatorActions(pendingResp.actions ?? []);
    } catch (err) {
      setLoadError(err instanceof Error ? err.message : String(err));
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    refreshCoordinatorState();
  }, [refreshCoordinatorState]);

  useEffect(() => {
    if (vmDraftInitialized || voteManagers.length === 0) return;
    setVmNewAddrs(voteManagers.join("\n"));
    setVmNewThreshold(String(voteManagerThreshold || 1));
    setVmNewMinCeremonyValidators(String(minCeremonyValidators || 1));
    setVmDraftInitialized(true);
  }, [vmDraftInitialized, voteManagers, voteManagerThreshold, minCeremonyValidators]);

  const handleConnectDev = async () => {
    await wallet.connectDev(devKey);
    setDevKey("");
  };

  const handleApproveCoordinatorAction = async (action: chainApi.CoordinatorAction) => {
    if (!wallet.signer) {
      setApprovalError("Connect a coordinator wallet before approving.");
      return;
    }
    const actionID = coordinatorActionID(action);
    if (!actionID) {
      setApprovalError("Coordinator action is missing an action ID.");
      return;
    }

    setApprovingActionID(actionID);
    setApprovalError("");
    setApprovalTxHash("");
    try {
      const result = await cosmosTx.approveCoordinatorAction(chainApi.getApiBase(), wallet.signer, actionID);
      if (result.code !== 0) {
        setApprovalError(result.log || `Transaction failed with code ${result.code}`);
        return;
      }
      setApprovalTxHash(result.tx_hash);
      await refreshCoordinatorState();
    } catch (err) {
      setApprovalError(err instanceof Error ? err.message : String(err));
    } finally {
      setApprovingActionID(null);
    }
  };

  const handleUpdateVoteManagers = async () => {
    if (!wallet.signer) {
      setVmTxStatus("error");
      setVmTxError("Connect a coordinator wallet before proposing a policy change.");
      return;
    }
    const newManagers = vmNewAddrs
      .split(/[\s,]+/)
      .map((addr) => addr.trim())
      .filter(Boolean);
    const newThreshold = parseInt(vmNewThreshold, 10);
    const newMinCeremonyValidators = parseInt(vmNewMinCeremonyValidators, 10);
    const uniqueManagers = new Set(newManagers.map(normalizeCoordinatorAddress));
    if (newManagers.length === 0) {
      setVmTxStatus("error");
      setVmTxError("Enter at least one vote-manager address.");
      return;
    }
    if (uniqueManagers.size !== newManagers.length) {
      setVmTxStatus("error");
      setVmTxError("Vote-manager addresses must be unique.");
      return;
    }
    if (!Number.isFinite(newThreshold) || newThreshold < 1 || newThreshold > newManagers.length) {
      setVmTxStatus("error");
      setVmTxError("Threshold must be at least 1 and no greater than the number of vote managers.");
      return;
    }
    if (!Number.isFinite(newMinCeremonyValidators) || newMinCeremonyValidators < 1) {
      setVmTxStatus("error");
      setVmTxError("Min ceremony validators must be at least 1.");
      return;
    }

    setVmTxStatus("sending");
    setVmTxError("");
    setVmTxHash("");
    try {
      const result = await cosmosTx.updateVoteManagers(
        chainApi.getApiBase(),
        wallet.signer,
        newManagers,
        newThreshold,
        newMinCeremonyValidators,
      );
      if (result.code !== 0) {
        setVmTxStatus("error");
        setVmTxError(result.log || `Transaction failed with code ${result.code}`);
        return;
      }
      setVmTxHash(result.tx_hash);
      setVmTxStatus("ok");
      await refreshCoordinatorState();
    } catch (err) {
      setVmTxStatus("error");
      setVmTxError(err instanceof Error ? err.message : String(err));
    }
  };

  const walletIsCoordinator = !!wallet.address && voteManagers.some((addr) => sameCoordinatorAddress(addr, wallet.address ?? ""));

  return (
    <div className="flex-1 overflow-y-auto">
      <div className="max-w-5xl mx-auto px-6 py-10">
        <div className="flex items-center justify-between gap-4 mb-6">
          <div className="flex items-center gap-3">
            <div className="w-10 h-10 rounded-xl bg-surface-3 flex items-center justify-center">
              <ClipboardCheck size={22} className="text-text-secondary" />
            </div>
            <div>
              <h1 className="text-lg font-bold text-text-primary">Approvals</h1>
              <p className="text-[11px] text-text-muted">
                Review coordinator actions, signatures, and threshold status
              </p>
            </div>
          </div>
          <button
            type="button"
            onClick={() => void refreshCoordinatorState()}
            disabled={loading}
            className="flex items-center gap-2 px-3 py-2 bg-surface-2 hover:bg-surface-3 text-text-secondary rounded-lg text-[11px] font-semibold transition-colors cursor-pointer disabled:opacity-50"
          >
            <RefreshCw size={13} className={loading ? "animate-spin" : ""} />
            Refresh
          </button>
        </div>

        {loadError && (
          <div className="mb-5 flex items-start gap-2 text-[11px] text-danger bg-danger/10 border border-danger/30 rounded-lg p-3">
            <AlertCircle size={13} className="mt-0.5 shrink-0" />
            <span>{loadError}</span>
          </div>
        )}

        <div className="grid gap-5 lg:grid-cols-[minmax(0,1fr)_320px]">
          <div className="space-y-5">
            <section className="bg-surface-1 border border-border-subtle rounded-xl p-5">
              <div className="flex items-center justify-between gap-3 mb-4">
                <div>
                  <h2 className="text-xs font-semibold text-text-primary">Pending actions</h2>
                  <p className="text-[11px] text-text-muted mt-0.5">
                    Every approval signs the exact payload shown below.
                  </p>
                </div>
                <span className="text-[11px] text-text-muted">
                  {pendingCoordinatorActions.length} pending
                </span>
              </div>

              {!wallet.signer && (
                <div className="mb-4 flex items-start gap-2 text-[11px] text-text-secondary bg-surface-2 border border-border-subtle rounded-lg p-3">
                  <Wallet size={13} className="mt-0.5 shrink-0" />
                  <span>Connect a coordinator wallet to approve or recheck actions.</span>
                </div>
              )}
              {wallet.signer && !walletIsCoordinator && (
                <div className="mb-4 flex items-start gap-2 text-[11px] text-warning bg-warning/10 border border-warning/30 rounded-lg p-3">
                  <AlertTriangle size={13} className="mt-0.5 shrink-0" />
                  <span>The connected wallet is not in the current coordinator set.</span>
                </div>
              )}

              {approvalTxHash && (
                <div className="mb-4 bg-success/10 border border-success/30 rounded-lg p-3">
                  <p className="text-[11px] text-success font-semibold">Approval transaction accepted</p>
                  <p className="text-[10px] text-text-secondary font-mono mt-0.5 break-all">TX: {approvalTxHash}</p>
                </div>
              )}
              {approvalError && (
                <div className="mb-4 bg-danger/10 border border-danger/30 rounded-lg p-3">
                  <p className="text-[11px] text-danger">{approvalError}</p>
                </div>
              )}

              {pendingCoordinatorActions.length === 0 ? (
                <p className="text-[11px] text-text-muted italic">No pending coordinator actions.</p>
              ) : (
                <div className="space-y-3">
                  {pendingCoordinatorActions.map((action) => {
                    const actionID = coordinatorActionID(action);
                    const approvalSet = currentCoordinatorApprovalSet(action, voteManagers);
                    const approvalCount = approvalSet.size;
                    const alreadyApproved = !!wallet.address && approvalSet.has(normalizeCoordinatorAddress(wallet.address));
                    const canRecheckExecution = alreadyApproved && approvalCount >= voteManagerThreshold;
                    const payloadDetails = describeCoordinatorActionPayload(action);
                    const staleApprovals = staleCoordinatorApprovals(action, voteManagers);
                    const canSubmitApproval = !!wallet.signer && walletIsCoordinator;
                    const disabled =
                      !actionID ||
                      !canSubmitApproval ||
                      (alreadyApproved && !canRecheckExecution) ||
                      approvingActionID === actionID ||
                      !payloadDetails.canApprove;

                    return (
                      <article key={actionID} className="border border-border-subtle rounded-lg bg-surface-2 p-4 space-y-4">
                        <div className="flex items-start justify-between gap-4">
                          <div className="min-w-0">
                            <div className="flex items-center gap-2 mb-1">
                              <span className="text-[11px] font-mono text-text-muted">#{actionID}</span>
                              <h3 className="text-sm font-semibold text-text-primary">{coordinatorActionLabel(action)}</h3>
                            </div>
                            <p className="text-[11px] text-text-secondary">
                              {approvalCount}/{voteManagerThreshold} current approvals · {coordinatorSignatureStatus(approvalCount, voteManagerThreshold)}
                            </p>
                            <p className="text-[10px] text-text-muted mt-1">
                              Proposed by <span className="font-mono">{shortCoordinatorAddress(action.proposer || "unknown")}</span> · expires {coordinatorActionTime(action.expires_at)}
                            </p>
                          </div>
                          <button
                            type="button"
                            onClick={() => void handleApproveCoordinatorAction(action)}
                            disabled={disabled}
                            className="shrink-0 px-3 py-1.5 bg-accent/90 hover:bg-accent text-surface-0 rounded-lg text-[11px] font-semibold transition-colors cursor-pointer disabled:opacity-50"
                            title={
                              payloadDetails.error ||
                              (!wallet.signer
                                ? "Connect a coordinator wallet"
                                : !walletIsCoordinator
                                  ? "Connected wallet is not a current coordinator"
                                  : canRecheckExecution
                                    ? "Recheck coordinator action execution"
                                    : "Approve coordinator action")
                            }
                          >
                            {approvingActionID === actionID
                              ? "Approving..."
                              : alreadyApproved
                                ? (canRecheckExecution ? "Recheck" : "Approved")
                                : "Approve"}
                          </button>
                        </div>

                        <div className="rounded-md bg-surface-1 border border-border-subtle p-3 space-y-1.5">
                          {payloadDetails.rows.map((row) => (
                            <div key={row.label} className="grid gap-1 sm:grid-cols-[132px_minmax(0,1fr)]">
                              <span className="text-[10px] text-text-muted">{row.label}</span>
                              <span className={`text-[10px] text-text-primary whitespace-pre-wrap break-all ${row.mono ? "font-mono" : ""}`}>
                                {row.value}
                              </span>
                            </div>
                          ))}
                        </div>
                        {payloadDetails.error && (
                          <p className="text-[10px] text-danger bg-danger/10 border border-danger/30 rounded-md p-2">
                            {payloadDetails.error}
                          </p>
                        )}

                        <div>
                          <div className="flex items-center justify-between mb-2">
                            <p className="text-[11px] font-semibold text-text-primary">Coordinator signatures</p>
                            <span className="text-[10px] text-text-muted">
                              {coordinatorSignatureStatus(approvalCount, voteManagerThreshold)}
                            </span>
                          </div>
                          <div className="grid gap-2 md:grid-cols-2">
                            {voteManagers.map((addr) => {
                              const signed = approvalSet.has(normalizeCoordinatorAddress(addr));
                              return (
                                <div key={addr} className="flex items-center justify-between gap-2 rounded-md bg-surface-1 border border-border-subtle px-2.5 py-2">
                                  <span className="text-[10px] text-text-primary font-mono break-all" title={addr}>
                                    {shortCoordinatorAddress(addr)}
                                  </span>
                                  <span className={`shrink-0 text-[10px] ${signed ? "text-success" : "text-text-muted"}`}>
                                    {signed ? "Signed" : "Waiting"}
                                  </span>
                                </div>
                              );
                            })}
                          </div>
                          {staleApprovals.length > 0 && (
                            <div className="mt-2 rounded-md bg-warning/10 border border-warning/30 p-2">
                              <p className="text-[10px] text-warning font-semibold mb-1">Ignored under current policy</p>
                              <div className="space-y-0.5">
                                {staleApprovals.map((addr) => (
                                  <p key={addr} className="text-[10px] text-text-secondary font-mono break-all">{addr}</p>
                                ))}
                              </div>
                            </div>
                          )}
                        </div>
                      </article>
                    );
                  })}
                </div>
              )}
            </section>

            <section className="bg-surface-1 border border-border-subtle rounded-xl p-5">
              <h2 className="text-xs font-semibold text-text-primary mb-3">
                Propose coordinator policy change
              </h2>
              <div className="space-y-3">
                {wallet.signer ? (
                  <div className="bg-surface-2 rounded-lg px-3 py-2">
                    <p className="text-[10px] text-text-muted mb-0.5">Signing as</p>
                    <p className="text-[11px] text-text-primary font-mono break-all">{wallet.address}</p>
                  </div>
                ) : (
                  <p className="text-[11px] text-text-muted">
                    Connect a coordinator wallet before proposing policy changes.
                  </p>
                )}
                <div>
                  <label className="block text-[11px] text-text-secondary mb-1">
                    New vote-manager addresses
                  </label>
                  <textarea
                    value={vmNewAddrs}
                    onChange={(e) => {
                      setVmDraftInitialized(true);
                      setVmNewAddrs(e.target.value);
                    }}
                    placeholder="sv1..., sv1..., sv1..."
                    rows={4}
                    className="w-full px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary placeholder:text-text-muted focus:outline-none focus:border-accent/50 font-mono"
                  />
                  <p className="text-[10px] text-text-muted mt-1">
                    Comma- or newline-separated. This replaces the entire coordinator set after approval.
                  </p>
                </div>
                <div>
                  <label className="block text-[11px] text-text-secondary mb-1">
                    New threshold
                  </label>
                  <input
                    type="number"
                    min={1}
                    value={vmNewThreshold}
                    onChange={(e) => {
                      setVmDraftInitialized(true);
                      setVmNewThreshold(e.target.value);
                    }}
                    className="w-28 px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary focus:outline-none focus:border-accent/50"
                  />
                </div>
                <div>
                  <label className="block text-[11px] text-text-secondary mb-1">
                    New min ceremony validators
                  </label>
                  <input
                    type="number"
                    min={1}
                    value={vmNewMinCeremonyValidators}
                    onChange={(e) => {
                      setVmDraftInitialized(true);
                      setVmNewMinCeremonyValidators(e.target.value);
                    }}
                    className="w-28 px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary focus:outline-none focus:border-accent/50"
                  />
                  <p className="text-[10px] text-text-muted mt-1">
                    Minimum bonded validators with registered Pallas keys required to create a voting session.
                  </p>
                </div>
                <button
                  type="button"
                  onClick={handleUpdateVoteManagers}
                  disabled={!wallet.signer || !vmNewAddrs.trim() || vmTxStatus === "sending"}
                  className="px-3 py-1.5 bg-accent/90 hover:bg-accent text-surface-0 rounded-lg text-[11px] font-semibold transition-colors cursor-pointer disabled:opacity-50"
                >
                  {vmTxStatus === "sending" ? (
                    <span className="flex items-center gap-1.5">
                      <Loader2 size={12} className="animate-spin" /> Signing & broadcasting...
                    </span>
                  ) : (
                    "Propose action"
                  )}
                </button>
                {vmTxStatus === "ok" && (
                  <div className="bg-success/10 border border-success/30 rounded-lg p-2.5">
                    <p className="text-[11px] text-success font-semibold">Coordinator action submitted</p>
                    {vmTxHash && (
                      <p className="text-[10px] text-text-secondary font-mono mt-0.5 break-all">TX: {vmTxHash}</p>
                    )}
                  </div>
                )}
                {vmTxStatus === "error" && (
                  <div className="bg-danger/10 border border-danger/30 rounded-lg p-2.5">
                    <p className="text-[11px] text-danger">{vmTxError}</p>
                  </div>
                )}
              </div>
            </section>
          </div>

          <aside className="space-y-5">
            <section className="bg-surface-1 border border-border-subtle rounded-xl p-5 space-y-3">
              <h2 className="text-xs font-semibold text-text-primary">Current policy</h2>
              <SettingsStubRow label="Threshold" value={`${voteManagerThreshold} of ${voteManagers.length}`} />
              <SettingsStubRow label="Min ceremony validators" value={String(minCeremonyValidators)} />
              {latestBlock && (
                <>
                  <SettingsStubRow label="Chain ID" value={latestBlock.chainId} />
                  <SettingsStubRow label="Latest height" value={latestBlock.height.toLocaleString()} />
                </>
              )}
              <div className="pt-2 border-t border-border-subtle">
                <p className="text-[10px] text-text-muted mb-2">Current coordinators</p>
                {voteManagers.length === 0 ? (
                  <p className="text-[11px] text-text-muted italic">none set</p>
                ) : (
                  <div className="space-y-1.5">
                    {voteManagers.map((addr) => (
                      <p key={addr} className="text-[10px] text-text-primary font-mono break-all">{addr}</p>
                    ))}
                  </div>
                )}
              </div>
            </section>

            <section className="bg-surface-1 border border-border-subtle rounded-xl p-5 space-y-4">
              <h2 className="text-xs font-semibold text-text-primary">Signing wallet</h2>
              {wallet.address ? (
                <div className="space-y-3">
                  <div className="flex items-center justify-between gap-2">
                    <div className="flex items-center gap-2 min-w-0">
                      <Wallet size={14} className={walletIsCoordinator ? "text-success" : "text-warning"} />
                      <span className="text-xs text-text-secondary">
                        {walletIsCoordinator ? "Coordinator connected" : "Wallet connected"}
                      </span>
                    </div>
                    <button
                      type="button"
                      onClick={wallet.disconnect}
                      className="flex items-center gap-1 px-2 py-1 text-[10px] text-text-muted hover:text-danger hover:bg-danger/10 rounded transition-colors cursor-pointer"
                    >
                      <Unplug size={10} /> Disconnect
                    </button>
                  </div>
                  <div className="bg-surface-2 rounded-lg px-3 py-2">
                    <p className="text-[10px] text-text-muted mb-0.5">Address</p>
                    <p className="text-[11px] text-text-primary font-mono break-all">{wallet.address}</p>
                  </div>
                  <p className="text-[10px] text-text-muted">
                    Source: {wallet.source === "keplr" ? "Keplr" : "pasted key"}
                  </p>
                </div>
              ) : (
                <div className="space-y-3">
                  <button
                    type="button"
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
                      Connect with private key
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
                        type="button"
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
            </section>
          </aside>
        </div>
      </div>
    </div>
  );
}

function SettingsPage({ wallet }: { wallet: UseWallet }) {
  const [rpcUrl, setRpcUrl] = useState(getStoredRpc);
  const chain = useChainInfo();
  const [selectedNullifierUrl, setSelectedNullifierUrl] = useState(
    () => chainApi.getNullifierUrl() ?? "",
  );
  const nullifier = useNullifierStatus(selectedNullifierUrl);
  const isCustom = !LIGHTWALLETD_ENDPOINTS.some((e) => e.url === rpcUrl);

  // Voting config (PIR endpoints + vote servers)
  const [votingConfig, setVotingConfig] = useState<chainApi.VotingConfig | null>(null);
  const [configLoaded, setConfigLoaded] = useState(false);
  const pirEndpoints = votingConfig?.pir_endpoints ?? [];
  const voteServers = votingConfig?.vote_servers ?? [];
  const defaultPirUrl = configLoaded
    ? chainApi.resolveDefaultPirUrl(votingConfig)
    : chainApi.getDefaultPirUrl();
  const isKnownNullifier =
    selectedNullifierUrl === defaultPirUrl ||
    selectedNullifierUrl === chainApi.LOCAL_PIR_URL ||
    chainApi.isDeprecatedNullifierUrl(selectedNullifierUrl) ||
    pirEndpoints.some((e) => e.url === selectedNullifierUrl);
  const isCustomNullifier = configLoaded && !isKnownNullifier;

  useEffect(() => {
    chainApi.getVotingConfig().then((cfg) => {
      if (cfg) {
        setVotingConfig(cfg);
        const resolved = chainApi.resolveDefaultPirUrl(cfg);
        if (resolved) {
          chainApi.setResolvedDefaultPirUrl(resolved);
          setSelectedNullifierUrl((current) => {
            if (!current || chainApi.shouldMigrateNullifierUrl(current, resolved)) {
              chainApi.setNullifierUrl(resolved);
              return resolved;
            }
            return current;
          });
        }
      }
      setConfigLoaded(true);
    });
  }, []);

  // Voting chain state
  const [chainUrl, setChainUrlLocal] = useState(chainApi.getChainUrl);
  const isCustomChain = configLoaded && chainUrl !== "" && chainUrl !== window.location.origin
    && !voteServers.some((e) => e.url === chainUrl);
  const [connStatus, setConnStatus] = useState<"idle" | "testing" | "ok" | "error">("idle");
  const [connError, setConnError] = useState("");
  const [ceremony, setCeremony] = useState<chainApi.CeremonyState | null>(null);
  const [latestBlock, setLatestBlock] = useState<chainApi.LatestBlockInfo | null>(null);
  const [helperStatus, setHelperStatus] = useState<chainApi.HelperStatus | null>(null);
  const [voteManagers, setVoteManagers] = useState<string[]>([]);
  const [voteManagerThreshold, setVoteManagerThreshold] = useState(1);
  const [activeRounds, setActiveRounds] = useState<chainApi.ChainRound[]>([]);
  const [chainDetailsOpen, setChainDetailsOpen] = useState(false);
  const [devKey, setDevKey] = useState("");
  const [devKeyVisible, setDevKeyVisible] = useState(false);

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

      const [state, vmResp, helper, activeRoundsResp] = await Promise.all([
        chainApi.testConnection(),
        chainApi.getVoteManagers(),
        chainApi.getHelperStatus().catch(() => null),
        chainApi.getActiveRounds().catch(() => ({ rounds: [] })),
      ]);
      setCeremony(state);
      setVoteManagers(vmResp.vote_manager_addresses ?? []);
      setVoteManagerThreshold(vmResp.threshold ?? 1);
      setHelperStatus(helper);
      setActiveRounds(activeRoundsResp.rounds);
      setConnStatus("ok");
    } catch (err) {
      setConnError(err instanceof Error ? err.message : String(err));
      setConnStatus("error");
    }
  };

  const handleConnectDev = async () => {
    await wallet.connectDev(devKey);
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
          Nullifier service (PIR)
        </h2>
        <div className="bg-surface-1 border border-border-subtle rounded-xl p-5 space-y-4 mb-6">
          <div>
            <label className="block text-[11px] text-text-secondary mb-1.5">
              Server
            </label>
            <select
              value={isCustomNullifier ? "__custom__" : selectedNullifierUrl}
              onChange={(e) => {
                const val = e.target.value;
                if (val === "__custom__") {
                  setSelectedNullifierUrl("https://");
                  chainApi.setNullifierUrl("https://");
                  return;
                }
                setSelectedNullifierUrl(val);
                chainApi.setNullifierUrl(val);
              }}
              className="w-full px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary focus:outline-none focus:border-accent/50 cursor-pointer [color-scheme:dark]"
            >
              {defaultPirUrl && (
                <option value={defaultPirUrl}>
                  PIR primary (default) — {defaultPirUrl}
                </option>
              )}
              {pirEndpoints
                .filter((ep) => ep.url !== defaultPirUrl && ep.url !== chainApi.LOCAL_PIR_URL)
                .map((ep) => (
                <option key={ep.url} value={ep.url}>
                  {ep.label} — {ep.url}
                </option>
              ))}
              <option value={chainApi.LOCAL_PIR_URL}>Local PIR — /nullifier</option>
              <option value="__custom__">Custom URL...</option>
            </select>
          </div>

          {isCustomNullifier && (
            <div>
              <label className="block text-[11px] text-text-secondary mb-1">
                Custom URL
              </label>
              <input
                type="text"
                value={selectedNullifierUrl}
                onChange={(e) => {
                  const url = e.target.value;
                  setSelectedNullifierUrl(url);
                  chainApi.setNullifierUrl(url);
                }}
                placeholder="https://pir.example.com"
                className="w-full px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary placeholder:text-text-muted focus:outline-none focus:border-accent/50 font-mono"
              />
            </div>
          )}

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

          {/* Fleet-wide status across every endpoint in the published
              voting-config. The selector above is about "which endpoint
              does my wallet use?" — this table is the operator's view
              of "are all replicas converged on the same snapshot?",
              used as step 5 of the snapshot-bumps runbook. */}
          {pirEndpoints.length > 0 && (
            <div className="pt-4 border-t border-border-subtle/50">
              <PirFleetStatus
                endpoints={pirEndpoints}
                selectedUrl={selectedNullifierUrl || undefined}
                expectedHeights={activeRounds
                  .map((round) => Number(round.snapshot_height ?? 0))
                  .filter((height) => Number.isFinite(height) && height > 0)}
              />
            </div>
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
                    ({wallet.source === "keplr" ? "Keplr" : "pasted key"})
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
                  Connect with private key
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
          <div>
            <label className="block text-[11px] text-text-secondary mb-1.5">
              Server
            </label>
            <select
              value={isCustomChain ? "__custom__" : chainUrl}
              onChange={(e) => {
                const val = e.target.value;
                if (val === "__custom__") {
                  handleChainUrlChange("https://");
                  return;
                }
                handleChainUrlChange(val);
              }}
              className="w-full px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary focus:outline-none focus:border-accent/50 cursor-pointer [color-scheme:dark]"
            >
              <option value="">Default (same-origin)</option>
              {voteServers.map((ep) => (
                <option key={ep.url} value={ep.url}>
                  {ep.label} — {ep.url}
                </option>
              ))}
              <option value="__custom__">Custom URL...</option>
            </select>
          </div>

          {isCustomChain && (
            <div>
              <label className="block text-[11px] text-text-secondary mb-1">
                Custom URL
              </label>
              <input
                type="text"
                value={chainUrl}
                onChange={(e) => handleChainUrlChange(e.target.value)}
                placeholder="https://vote-chain.example.com"
                className="w-full px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary placeholder:text-text-muted focus:outline-none focus:border-accent/50 font-mono"
              />
            </div>
          )}

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

              {/* Coordinator policy */}
              {connStatus === "ok" && (
                <div className="border-t border-border-subtle pt-3 space-y-3">
                  <div>
                    <div className="text-xs text-text-secondary mb-1.5">Vote coordinator policy</div>
                    <SettingsStubRow
                      label="Threshold"
                      value={`${voteManagerThreshold} of ${voteManagers.length}`}
                    />
                    {voteManagers.length === 0 ? (
                      <span className="text-[11px] text-text-muted italic">none set</span>
                    ) : (
                      <ul className="space-y-1">
                        {voteManagers.map((addr) => (
                          <li
                            key={addr}
                            className="text-[11px] font-mono text-text-primary break-all"
                          >
                            {addr}
                          </li>
                        ))}
                      </ul>
                    )}
                  </div>
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
  const [devKey, setDevKey] = useState("");
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
            {walletConnected ? "Publish to chain" : "Connect wallet"}
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
                    Coordinator action submitted
                  </p>
                  <p className="text-[10px] text-text-secondary font-mono break-all">
                    TX: {result}
                  </p>
                </div>
              )}

              {status === "error" && (
                <div className="bg-danger/10 border border-danger/30 rounded-lg p-3">
                  <p className="text-[11px] text-danger break-words">{error}</p>
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
                  Paste private key
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
                  <Loader2 size={12} className="animate-spin" /> Proposing...
                </>
              ) : (
                "Propose on chain"
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
  const detectedChainId = useDetectedChainId();
  const chainId = wallet.chainId || detectedChainId;
  const dynamicConfigFileUrl =
    tokenHolderConfigUrl({ file: "dynamic", chainId }) ?? TOKEN_HOLDER_VOTING_CONFIG_REPO_URL;
  const dynamicConfigEditUrl =
    tokenHolderConfigUrl({ file: "dynamic", chainId, mode: "edit" }) ??
    TOKEN_HOLDER_VOTING_CONFIG_REPO_URL;
  const [validators, setValidators] = useState<chainApi.Validator[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [ceremony, setCeremony] = useState<chainApi.CeremonyState | null>(null);
  const [pallasKeys, setPallasKeys] = useState<Set<string>>(new Set());
  const [unjailing, setUnjailing] = useState<string | null>(null); // operator_address being unjailed
  const [unjailResult, setUnjailResult] = useState<{ addr: string; ok: boolean; msg: string } | null>(null);

  // Dynamic voting config network management state.
  const [votingConfig, setVotingConfig] = useState<chainApi.VotingConfig | null>(null);
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
      const [valResp, ceremonyResp, pallasResp, vcResp] = await Promise.all([
        chainApi.getValidators(),
        chainApi.getCeremonyState().catch(() => null),
        chainApi.getPallasKeys().catch(() => ({ validators: [] })),
        chainApi.getVotingConfig().catch(() => null),
      ]);
      setValidators(valResp.validators ?? []);
      setCeremony(ceremonyResp);
      setPallasKeys(new Set(pallasResp.validators.map((v) => v.validator_address)));
      setVotingConfig(vcResp);
    } catch (err) {
      if (!silent) setError(err instanceof Error ? err.message : String(err));
    } finally {
      if (!silent) setLoading(false);
    }
  };

  const openVotingConfigEditor = (moniker: string) => {
    setNetworkUpdating(true);
    setNetworkResult(null);
    try {
      window.open(
        dynamicConfigEditUrl,
        "_blank",
        "noopener,noreferrer",
      );
      setNetworkResult({
        moniker,
        ok: true,
        msg: "Opened dynamic-voting-config.json.",
      });
    } finally {
      setNetworkUpdating(false);
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
            </div>
            <p className="text-[10px] text-text-secondary mb-1">
              All bonded validators on-chain. A validator can be bonded and producing blocks but not listed as an approved submission server if it has been taken out of client rotation by an admin.
              To become a recommended configuration for wallets, server owners should open a pull request in{" "}
              <a
                href={dynamicConfigFileUrl}
                target="_blank"
                rel="noopener noreferrer"
                className="text-accent hover:text-accent-glow"
              >
                dynamic-voting-config.json
              </a>
              {" "}that updates the published <code className="font-mono">vote_servers</code> list.
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

                {/* Network URL (dynamic voting config) */}
                {(() => {
                  const registeredUrl = votingConfig?.vote_servers.find(
                    (s) => s.label === moniker
                  )?.url;
                  const configActionCopy = registeredUrl
                    ? `Edit or remove the vote_servers entry for "${moniker}" via a pull request to dynamic-voting-config.json.`
                    : `Add a vote_servers entry for "${moniker}" via a pull request to dynamic-voting-config.json.`;
                  if (registeredUrl) {
                    return (
                      <div className="mt-2 space-y-1">
                        <div className="flex flex-wrap items-center gap-2">
                          <Server size={10} className="text-success shrink-0" />
                          <span className="text-[10px] text-text-secondary truncate">{registeredUrl}</span>
                          <button
                            type="button"
                            className="text-[9px] px-1.5 py-0.5 rounded bg-surface-3 text-text-secondary hover:bg-surface-2 hover:text-text-primary transition-colors shrink-0 disabled:opacity-50 cursor-pointer"
                            disabled={networkUpdating}
                            onClick={() => openVotingConfigEditor(moniker)}
                          >
                            Edit URL
                          </button>
                        </div>
                        <p className="text-[9px] text-text-muted">{configActionCopy}</p>
                        {networkResult?.moniker === moniker && (
                          <span className={`text-[9px] ${networkResult.ok ? "text-success" : "text-danger"}`}>
                            {networkResult.msg}
                          </span>
                        )}
                      </div>
                    );
                  }

                  return (
                    <div className="mt-2 space-y-1">
                      <p className="text-[9px] text-text-muted">{configActionCopy}</p>
                      <button
                        type="button"
                        className="inline-flex items-center gap-1 text-[10px] text-text-muted hover:text-accent transition-colors cursor-pointer"
                        disabled={networkUpdating}
                        onClick={() => openVotingConfigEditor(moniker)}
                      >
                        <Server size={9} />
                        Register URL
                      </button>
                      {networkResult?.moniker === moniker && (
                        <span className={`ml-2 text-[9px] ${networkResult.ok ? "text-success" : "text-danger"}`}>
                          {networkResult.msg}
                        </span>
                      )}
                    </div>
                  );
                })()}
              </div>
            );
          })}
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
  SESSION_STATUS_CEREMONY_FAILED: { label: "Ceremony Failed", color: "bg-danger/20 text-danger" },
  4: { label: "Pending", color: "bg-orange-500/20 text-orange-400" },
  1: { label: "Active", color: "bg-success/20 text-success" },
  2: { label: "Tallying", color: "bg-warning/20 text-warning" },
  3: { label: "Finalized", color: "bg-blue-500/20 text-blue-400" },
  5: { label: "Ceremony Failed", color: "bg-danger/20 text-danger" },
};


const BALLOT_DIVISOR = 12_500_000; // zatoshi per ballot
function ballotsToZEC(ballots: number): string {
  const zatoshi = ballots * BALLOT_DIVISOR;
  const zec = zatoshi / 1e8;
  return `${zec.toFixed(3)} ZEC`;
}

function base64ToHex(b64: string): string {
  const bytes = atob(b64);
  return Array.from(bytes, (c) =>
    c.charCodeAt(0).toString(16).padStart(2, "0")
  ).join("");
}

function normalizeHex(s: string): string {
  return s.trim().toLowerCase();
}

function isRoundIdHex(s: string): boolean {
  return /^[0-9a-f]{64}$/i.test(s.trim());
}

function truncateMiddle(value: string, head = 12, tail = 8): string {
  if (value.length <= head + tail + 3) return value;
  return `${value.slice(0, head)}...${value.slice(-tail)}`;
}

function statusMatches(value: string | number | undefined, code: number, name: string): boolean {
  return Number(value) === code || value === name;
}

function humanizeEnum(value: string | number | undefined, prefix: string): string {
  if (value == null || value === "") return "Unknown";
  if (typeof value === "number" || /^\d+$/.test(String(value))) {
    const numeric = Number(value);
    if (prefix === "CEREMONY_STATUS_") {
      return CEREMONY_STATUS_NAMES[numeric]
        ? CEREMONY_STATUS_NAMES[numeric][0].toUpperCase() + CEREMONY_STATUS_NAMES[numeric].slice(1)
        : String(value);
    }
    return STATUS_MAP[numeric]?.label ?? String(value);
  }
  return String(value)
    .replace(prefix, "")
    .toLowerCase()
    .replace(/(^|_)([a-z])/g, (_match, sep: string, letter: string) =>
      `${sep ? " " : ""}${letter.toUpperCase()}`
    );
}

/* ── Copyable field helper ────────────────────────────────────── */

function CopyableField({
  label,
  value,
  copyValue = value,
  mono = true,
}: {
  label: string;
  value: string;
  copyValue?: string;
  mono?: boolean;
}) {
  const [copied, setCopied] = useState(false);

  const handleCopy = () => {
    navigator.clipboard.writeText(copyValue).then(() => {
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
          title={copyValue === value ? "Copy to clipboard" : `Copy ${copyValue} to clipboard`}
        >
          {copied ? <Check size={11} className="text-success" /> : <Copy size={11} />}
        </button>
      </div>
    </div>
  );
}

function CopyableCode({
  value,
  title,
  head = 12,
  tail = 8,
}: {
  value: string;
  title?: string;
  head?: number;
  tail?: number;
}) {
  const [copied, setCopied] = useState(false);

  const handleCopy = () => {
    navigator.clipboard.writeText(value).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  };

  return (
    <button
      onClick={handleCopy}
      title={title ?? value}
      className="inline-flex min-w-0 items-center gap-1.5 rounded text-left font-mono text-[11px] text-text-primary hover:text-accent transition-colors cursor-pointer"
    >
      <span className="truncate">{truncateMiddle(value, head, tail)}</span>
      {copied ? (
        <Check size={11} className="text-success shrink-0" />
      ) : (
        <Copy size={11} className="text-text-muted shrink-0" />
      )}
    </button>
  );
}

/* ── Vote status view ────────────────────────────────────────── */

interface VoteStatusViewProps {
  expectRoundCount?: number | null;
  selectedRoundIdHex?: string | null;
  onSelectRound: (roundIdHex: string) => void;
  onBackToList: () => void;
}

interface VoteSummaryBatch {
  summaries: Record<string, chainApi.VoteSummaryResponse>;
  errors: Record<string, string>;
}

async function fetchVoteSummaryBatch(
  rounds: chainApi.ChainRound[]
): Promise<VoteSummaryBatch> {
  const entries = await Promise.all(
    rounds.map(async (round) => {
      const id = round.vote_round_id ?? "";
      if (!id) return null;
      try {
        const summary = await chainApi.getVoteSummary(base64ToHex(id));
        return { id, summary, error: null };
      } catch (err) {
        const message = err instanceof Error ? err.message : String(err);
        console.warn(`VoteSummary failed for ${id.slice(0, 12)}:`, message);
        return { id, summary: null, error: message };
      }
    })
  );

  const summaries: Record<string, chainApi.VoteSummaryResponse> = {};
  const errors: Record<string, string> = {};
  for (const entry of entries) {
    if (!entry) continue;
    if (entry.summary) summaries[entry.id] = entry.summary;
    if (entry.error) errors[entry.id] = entry.error;
  }
  return { summaries, errors };
}

function VoteStatusView({
  expectRoundCount,
  selectedRoundIdHex,
  onSelectRound,
  onBackToList,
}: VoteStatusViewProps) {
  const { precomputedBaseURL, zcashNetwork } = useUIConfig();
  const [rounds, setRounds] = useState<chainApi.ChainRound[]>([]);
  const [summaries, setSummaries] = useState<Record<string, chainApi.VoteSummaryResponse>>({});
  const [summaryErrors, setSummaryErrors] = useState<Record<string, string>>({});
  const [snapshotWarnings, setSnapshotWarnings] = useState<Record<string, string>>({});
  const [endorsedByRound, setEndorsedByRound] = useState<Record<string, string[]>>({});
  const [endorsementError, setEndorsementError] = useState("");
  const [validatorMonikers, setValidatorMonikers] = useState<Record<string, string>>({});
  const [completedRoundsOpen, setCompletedRoundsOpen] = useState(false);
  const [completedRoundLimit, setCompletedRoundLimit] = useState(COMPLETED_ROUNDS_PAGE_SIZE);
  const [completedSummariesLoading, setCompletedSummariesLoading] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const zcashChain = useChainInfo();
  const pollingRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const completedSummaryInFlightRef = useRef(new Set<string>());

  const fetchAll = useCallback(async () => {
    setLoading(true);
    setError("");
    setSummaryErrors({});
    setSnapshotWarnings({});
    setEndorsementError("");
    try {
      const resp = await chainApi.listRounds();
      const allRounds = (resp.rounds ?? []).sort((a, b) => {
        const ha = Number(a.created_at_height ?? 0);
        const hb = Number(b.created_at_height ?? 0);
        return ha - hb;
      });
      setRounds(allRounds);

      const roundById = new Map(
        allRounds.map((round) => [round.vote_round_id ?? "", round])
      );
      setSummaries((current) => Object.fromEntries(
        Object.entries(current).filter(([id, summary]) => {
          const round = roundById.get(id);
          return (
            round !== undefined &&
            isTerminalVoteRoundStatus(round.status) &&
            isTerminalVoteRoundStatus(summary.status)
          );
        })
      ));

      const { currentRounds } = partitionVoteStatusRounds(allRounds);
      const eagerSummaryRounds = currentRounds.filter(
        (round) => shouldEagerlyLoadVoteSummary(round.status)
      );

      // These data sources are independent. Let current results appear as soon
      // as their small batch completes instead of gating them behind snapshot
      // and endorsement checks.
      const summaryPromise = fetchVoteSummaryBatch(eagerSummaryRounds).then((batch) => {
        setSummaries((current) => ({ ...current, ...batch.summaries }));
        setSummaryErrors((current) => ({ ...current, ...batch.errors }));
      });

      const snapshotPromise = (async () => {
        const activeSnapshotEntries = chainApi.getActiveRoundsFromList(allRounds)
          .map((round) => ({
            roundId: round.vote_round_id ?? "",
            height: Number(round.snapshot_height ?? 0),
          }))
          .filter((entry) => entry.roundId && Number.isFinite(entry.height) && entry.height > 0);
        if (activeSnapshotEntries.length === 0) return;
        if (!precomputedBaseURL || !zcashNetwork) {
          setSnapshotWarnings(Object.fromEntries(
            activeSnapshotEntries.map((entry) => [
              entry.roundId,
              "Cannot validate the published PIR snapshot because this svoted did not expose its snapshot base and Zcash network.",
            ])
          ));
          return;
        }

        const validations = await Promise.all(
          activeSnapshotEntries.map(async (entry) => {
            const validation = await chainApi.validatePublishedSnapshotManifest(
              precomputedBaseURL,
              zcashNetwork,
              entry.height
            );
            if (validation.status === "valid") return null;
            const message =
              validation.status === "missing"
                ? `No published PIR snapshot exists for height ${entry.height.toLocaleString()}.`
                : validation.status === "invalid"
                  ? `Published PIR snapshot manifest is invalid: ${(validation.issues ?? []).join("; ")}`
                  : validation.message ?? "Could not validate published PIR snapshot.";
            return [entry.roundId, message] as const;
          })
        );
        setSnapshotWarnings(Object.fromEntries(validations.filter((entry) => entry !== null)));
      })();

      const endorsementPromise = (async () => {
        try {
          const endorsersResp = await chainApi.getEndorsers();
          const endorsementEntries = await Promise.all(
            endorsersResp.endorsers.map(async (endorser) => {
              try {
                const endorsed = await chainApi.getEndorsedRounds(endorser.endorser_id);
                return {
                  endorserID: endorser.endorser_id,
                  roundIDs: endorsed.vote_round_ids.map(base64ToHex),
                  error: "",
                };
              } catch (err) {
                return {
                  endorserID: endorser.endorser_id,
                  roundIDs: [],
                  error: err instanceof Error ? err.message : String(err),
                };
              }
            })
          );
          const byRound: Record<string, string[]> = {};
          const failedEndorsers: string[] = [];
          for (const entry of endorsementEntries) {
            if (entry.error) {
              failedEndorsers.push(entry.endorserID);
              continue;
            }
            for (const roundID of entry.roundIDs) {
              byRound[roundID] = [...(byRound[roundID] ?? []), entry.endorserID];
            }
          }
          setEndorsedByRound(byRound);
          if (failedEndorsers.length > 0) {
            setEndorsementError(`Endorsements unavailable for ${failedEndorsers.join(", ")}.`);
          }
        } catch (err) {
          setEndorsedByRound({});
          setEndorsementError(`Endorsements unavailable: ${err instanceof Error ? err.message : String(err)}`);
        }
      })();

      await Promise.all([summaryPromise, snapshotPromise, endorsementPromise]);
      return allRounds.length;
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      return -1;
    } finally {
      setLoading(false);
    }
  }, [precomputedBaseURL, zcashNetwork]);

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

  useEffect(() => {
    if (!selectedRoundIdHex) return;
    let cancelled = false;

    chainApi.getValidators()
      .then((resp) => {
        if (cancelled) return;
        const monikers: Record<string, string> = {};
        for (const val of resp.validators ?? []) {
          const addr = val.operator_address;
          const moniker = val.description?.moniker;
          if (addr && moniker) monikers[addr] = moniker;
        }
        setValidatorMonikers(monikers);
      })
      .catch(() => {
        if (!cancelled) setValidatorMonikers({});
      });

    return () => {
      cancelled = true;
    };
  }, [selectedRoundIdHex]);

  const { currentRounds, completedRounds } = useMemo(
    () => partitionVoteStatusRounds(rounds),
    [rounds]
  );
  const visibleCompletedRounds = useMemo(
    () => completedRounds.slice(0, completedRoundLimit),
    [completedRoundLimit, completedRounds]
  );
  const displayedRounds = useMemo(
    () => [
      ...currentRounds,
      ...(completedRoundsOpen ? visibleCompletedRounds : []),
    ],
    [completedRoundsOpen, currentRounds, visibleCompletedRounds]
  );
  const roundIndexById = useMemo(
    () => new Map(rounds.map((round, index) => [round.vote_round_id ?? "", index])),
    [rounds]
  );

  useEffect(() => {
    if (!completedRoundsOpen) return;
    const missingRounds = visibleCompletedRounds.filter((round) => {
      const id = round.vote_round_id ?? "";
      return (
        id !== "" &&
        summaries[id] === undefined &&
        summaryErrors[id] === undefined &&
        !completedSummaryInFlightRef.current.has(id)
      );
    });
    if (missingRounds.length === 0) return;

    for (const round of missingRounds) {
      const id = round.vote_round_id;
      if (id) completedSummaryInFlightRef.current.add(id);
    }
    setCompletedSummariesLoading(true);
    void fetchVoteSummaryBatch(missingRounds)
      .then((batch) => {
        setSummaries((current) => ({ ...current, ...batch.summaries }));
        setSummaryErrors((current) => ({ ...current, ...batch.errors }));
      })
      .finally(() => {
        for (const round of missingRounds) {
          const id = round.vote_round_id;
          if (id) completedSummaryInFlightRef.current.delete(id);
        }
        setCompletedSummariesLoading(completedSummaryInFlightRef.current.size > 0);
      });
  }, [
    completedRoundsOpen,
    summaries,
    summaryErrors,
    visibleCompletedRounds,
  ]);

  const completedRoundsControl = completedRounds.length > 0 ? (
    <button
      type="button"
      onClick={() => setCompletedRoundsOpen((open) => !open)}
      aria-expanded={completedRoundsOpen}
      className="flex w-full items-center justify-between gap-3 rounded-xl border border-border-subtle bg-surface-1 px-5 py-4 text-left transition-colors hover:bg-surface-2"
    >
      <span className="flex min-w-0 items-center gap-3">
        <ChevronDown
          size={15}
          className={`shrink-0 text-text-muted transition-transform ${completedRoundsOpen ? "rotate-180" : ""}`}
        />
        <span className="min-w-0">
          <span className="block text-xs font-semibold text-text-primary">
            Completed rounds
          </span>
          <span className="block truncate text-[10px] text-text-muted">
            {completedRoundsOpen
              ? `Showing ${visibleCompletedRounds.length} of ${completedRounds.length}`
              : "Hidden until you need historical results"}
          </span>
        </span>
      </span>
      <span className="flex shrink-0 items-center gap-2">
        {completedSummariesLoading && (
          <Loader2 size={12} className="animate-spin text-text-muted" />
        )}
        <span className="rounded-full bg-surface-3 px-2 py-0.5 font-mono text-[10px] text-text-secondary">
          {completedRounds.length.toLocaleString()}
        </span>
      </span>
    </button>
  ) : null;

  const normalizedSelectedRoundId = selectedRoundIdHex
    ? normalizeHex(selectedRoundIdHex)
    : null;
  const selectedRound =
    normalizedSelectedRoundId && isRoundIdHex(normalizedSelectedRoundId)
      ? rounds.find((round) => normalizeHex(base64ToHex(round.vote_round_id ?? "")) === normalizedSelectedRoundId)
      : null;

  if (selectedRoundIdHex) {
    return (
      <VoteRoundEaDetail
        round={selectedRound ?? null}
        roundIdHex={selectedRoundIdHex}
        loading={loading}
        error={error}
        validatorMonikers={validatorMonikers}
        zcashChain={zcashChain}
        onBack={onBackToList}
      />
    );
  }

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

        {endorsementError && (
          <div className="flex items-center gap-2 bg-warning/10 border border-warning/30 rounded-lg p-3 mb-4">
            <AlertTriangle size={14} className="text-warning shrink-0" />
            <p className="text-[11px] text-warning">{endorsementError}</p>
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
          {displayedRounds.map((round, i) => {
            const roundId = round.vote_round_id ?? "";
            const roundIdx = roundIndexById.get(roundId) ?? i;
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
            const endorsingIDs = endorsedByRound[roundIdHex] ?? [];
            const snapshotWarning = isActive ? snapshotWarnings[roundId] : undefined;
            const snapshotHeight = Number(round.snapshot_height ?? 0);
            const snapshotTime =
              snapshotHeight > 0 && zcashChain.latestHeight && zcashChain.latestTimestamp
                ? estimateTimestamp(snapshotHeight, zcashChain.latestHeight, zcashChain.latestTimestamp)
                : null;

            return (
              <Fragment key={roundId}>
                {completedRoundsOpen && i === currentRounds.length && completedRoundsControl}
                <div className="bg-surface-1 border border-border-subtle rounded-xl overflow-hidden">
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
                      {snapshotWarning && (
                        <div className="relative group shrink-0 text-warning">
                          <button
                            type="button"
                            className="text-warning hover:text-warning/80 cursor-default"
                            title="Snapshot warning"
                          >
                            <AlertTriangle size={12} />
                          </button>
                          <div className="absolute left-0 z-20 mt-2 hidden w-80 max-w-[calc(100vw-4rem)] rounded-lg border border-warning/30 bg-surface-1 p-3 shadow-xl group-hover:block">
                            <p className="text-[10px] text-warning font-semibold leading-snug">
                              Published PIR snapshot warning
                            </p>
                            <p className="text-[10px] text-text-muted leading-snug mt-0.5 break-words">
                              {snapshotWarning}
                            </p>
                          </div>
                        </div>
                      )}
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
                      copyValue={String(endTimeSec)}
                      mono={false}
                    />
                  )}
                  <div className="rounded-lg border border-border-subtle bg-surface-2 px-3 py-2">
                    <p className="text-[10px] uppercase tracking-wider text-text-muted">Endorsed By</p>
                    {endorsingIDs.length > 0 ? (
                      <div className="mt-2 flex flex-wrap gap-1.5">
                        {endorsingIDs.map((endorserID) => (
                          <span
                            key={endorserID}
                            className="rounded-full bg-success/10 px-2 py-0.5 text-[10px] font-semibold text-success"
                          >
                            {endorserID}
                          </span>
                        ))}
                      </div>
                    ) : (
                      <p className="mt-1 text-[11px] text-text-muted">No Endorsements.</p>
                    )}
                  </div>
                  <div className="pt-1">
                    <button
                      type="button"
                      onClick={() => onSelectRound(roundIdHex)}
                      disabled={!isRoundIdHex(roundIdHex)}
                      className="inline-flex items-center gap-1.5 rounded-md bg-surface-2 px-2.5 py-1.5 text-[11px] font-semibold text-text-secondary hover:bg-surface-3 hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-40 cursor-pointer transition-colors"
                    >
                      <Users size={12} />
                      EA validators
                    </button>
                  </div>
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
                    {summary.proposals.map((prop, proposalIndex) => {
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
                          key={prop.id ?? proposalIndex}
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
                            {options.map((opt, optionIndex) => {
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
                                <div key={opt.index ?? optionIndex} className="space-y-0.5">
                                  <div className="flex items-center justify-between">
                                    <span className="min-w-0 flex-1 pr-3">
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
                                        <span className="truncate">
                                          {opt.label ?? `Option ${opt.index}`}
                                        </span>
                                      </span>
                                      {opt.description && (
                                        <span className="block pl-3.5 text-[10px] text-text-muted leading-snug">
                                          {opt.description}
                                        </span>
                                      )}
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
                    {round.proposals.map((p, proposalIndex) => (
                      <div
                        key={p.id ?? proposalIndex}
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
              </Fragment>
            );
          })}
          {!completedRoundsOpen && completedRoundsControl}
          {completedRoundsOpen && visibleCompletedRounds.length < completedRounds.length && (
            <button
              type="button"
              onClick={() =>
                setCompletedRoundLimit((current) =>
                  Math.min(current + COMPLETED_ROUNDS_PAGE_SIZE, completedRounds.length)
                )
              }
              className="w-full rounded-lg border border-border-subtle bg-surface-1 px-4 py-3 text-[11px] font-semibold text-text-secondary transition-colors hover:bg-surface-2 hover:text-text-primary"
            >
              Load {Math.min(
                COMPLETED_ROUNDS_PAGE_SIZE,
                completedRounds.length - visibleCompletedRounds.length
              )} more completed rounds
            </button>
          )}
        </div>
      </div>
    </div>
  );
}

function isCeremonyConfirmed(round: chainApi.ChainRound): boolean {
  return statusMatches(round.ceremony_status, 3, "CEREMONY_STATUS_CONFIRMED") && !!round.ea_pk;
}

function isRoundPending(round: chainApi.ChainRound): boolean {
  return statusMatches(round.status, 4, "SESSION_STATUS_PENDING");
}

function isRoundFinalized(round: chainApi.ChainRound): boolean {
  return statusMatches(round.status, 3, "SESSION_STATUS_FINALIZED");
}

function getEaLifecycle(round: chainApi.ChainRound): {
  label: string;
  description: string;
  className: string;
} {
  if (isCeremonyConfirmed(round)) {
    return {
      label: "Confirmed EA validators",
      description: "This round established an EA key with these ceremony validators.",
      className: "border-success/30 bg-success/10 text-success",
    };
  }

  if (isRoundPending(round)) {
    return {
      label: "Ceremony validator snapshot",
      description: "This round is still pending. The EA is not established until the ceremony confirms.",
      className: "border-warning/30 bg-warning/10 text-warning",
    };
  }

  if (isRoundFinalized(round)) {
    return {
      label: "Failed ceremony snapshot",
      description: "No EA was established for this round.",
      className: "border-danger/30 bg-danger/10 text-danger",
    };
  }

  return {
    label: "Ceremony validator snapshot",
    description: "These validators were selected for this round's EA ceremony.",
    className: "border-border-subtle bg-surface-2 text-text-secondary",
  };
}

function DetailStat({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="rounded-lg border border-border-subtle bg-surface-2 px-3 py-2">
      <p className="text-[10px] uppercase tracking-wider text-text-muted">{label}</p>
      <div className="mt-1 text-[12px] font-semibold text-text-primary">{value}</div>
    </div>
  );
}

function VoteRoundEaDetail({
  round,
  roundIdHex,
  loading,
  error,
  validatorMonikers,
  zcashChain,
  onBack,
}: {
  round: chainApi.ChainRound | null;
  roundIdHex: string;
  loading: boolean;
  error: string;
  validatorMonikers: Record<string, string>;
  zcashChain: ReturnType<typeof useChainInfo>;
  onBack: () => void;
}) {
  const normalizedRoundId = normalizeHex(roundIdHex);
  const validRoundId = isRoundIdHex(normalizedRoundId);

  const statusKey = round?.status ?? "";
  const statusInfo = STATUS_MAP[statusKey] ?? {
    label: humanizeEnum(statusKey, "SESSION_STATUS_"),
    color: "bg-surface-3 text-text-muted",
  };
  const lifecycle = round ? getEaLifecycle(round) : null;
  const validators = round?.ceremony_validators ?? [];
  const snapshotHeight = Number(round?.snapshot_height ?? 0);
  const snapshotTime =
    snapshotHeight > 0 && zcashChain.latestHeight && zcashChain.latestTimestamp
      ? estimateTimestamp(snapshotHeight, zcashChain.latestHeight, zcashChain.latestTimestamp)
      : null;
  const endTimeSec = round?.vote_end_time
    ? parseInt(String(round.vote_end_time), 10)
    : 0;
  const endDate = endTimeSec > 0 ? new Date(endTimeSec * 1000) : null;
  const threshold = round?.threshold != null && Number(round.threshold) > 0
    ? String(round.threshold)
    : "Not set";
  const ceremonyStatus = humanizeEnum(round?.ceremony_status, "CEREMONY_STATUS_");

  return (
    <div className="flex-1 overflow-y-auto">
      <div className="max-w-4xl mx-auto px-6 py-12">
        <div className="mb-6 flex items-start justify-between gap-4">
          <div className="min-w-0">
            <button
              type="button"
              onClick={onBack}
              className="mb-4 inline-flex items-center gap-1.5 rounded-md px-2 py-1 text-[11px] text-text-muted hover:bg-surface-2 hover:text-text-primary cursor-pointer transition-colors"
            >
              <ArrowLeft size={12} />
              Vote status
            </button>
            <div className="flex items-center gap-3">
              <div className="w-10 h-10 rounded-xl bg-accent/15 flex items-center justify-center shrink-0">
                <Users size={22} className="text-accent" />
              </div>
              <div className="min-w-0">
                <h1 className="text-lg font-bold text-text-primary">
                  EA validators for this round
                </h1>
                <p className="text-[11px] text-text-muted truncate">
                  {round?.title || "On-chain voting round"}
                </p>
              </div>
            </div>
          </div>
          {round && (
            <span className={`mt-9 text-[9px] px-2 py-0.5 rounded-full shrink-0 ${statusInfo.color}`}>
              {statusInfo.label}
            </span>
          )}
        </div>

        {error && (
          <div className="mb-4 flex items-center gap-2 rounded-lg border border-danger/30 bg-danger/10 p-3">
            <AlertCircle size={14} className="text-danger shrink-0" />
            <p className="text-[11px] text-danger">{error}</p>
          </div>
        )}

        {!validRoundId && (
          <div className="rounded-xl border border-border-subtle bg-surface-1 p-5 text-center">
            <p className="text-xs font-semibold text-text-primary">Invalid round ID</p>
            <p className="mt-1 text-[11px] text-text-muted">
              Round detail URLs use the 64-character hex round ID.
            </p>
          </div>
        )}

        {validRoundId && loading && !round && (
          <div className="flex items-center justify-center py-16">
            <Loader2 size={22} className="text-text-muted animate-spin" />
          </div>
        )}

        {validRoundId && !loading && !round && !error && (
          <div className="rounded-xl border border-border-subtle bg-surface-1 p-5 text-center">
            <p className="text-xs font-semibold text-text-primary">Round not found</p>
            <p className="mt-1 text-[11px] text-text-muted">
              No on-chain round matched this round ID.
            </p>
          </div>
        )}

        {round && lifecycle && (
          <div className="space-y-5">
            <div className={`rounded-xl border p-4 ${lifecycle.className}`}>
              <div className="flex items-start gap-2">
                {isCeremonyConfirmed(round) ? (
                  <ShieldCheck size={15} className="mt-0.5 shrink-0" />
                ) : isRoundFinalized(round) ? (
                  <AlertTriangle size={15} className="mt-0.5 shrink-0" />
                ) : (
                  <Shield size={15} className="mt-0.5 shrink-0" />
                )}
                <div>
                  <p className="text-xs font-semibold">{lifecycle.label}</p>
                  <p className="mt-1 text-[11px]">{lifecycle.description}</p>
                </div>
              </div>
            </div>

            <div className="rounded-xl border border-border-subtle bg-surface-1 p-5">
              <div className="mb-4 grid gap-3 sm:grid-cols-3">
                <DetailStat label="Validators" value={validators.length.toLocaleString()} />
                <DetailStat label="Threshold" value={threshold} />
                <DetailStat label="Ceremony status" value={ceremonyStatus} />
              </div>

              <div className="space-y-2">
                <CopyableField label="Round ID" value={normalizedRoundId} />
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
                    copyValue={String(endTimeSec)}
                    mono={false}
                  />
                )}
                {round.ea_pk && (
                  <CopyableField label="EA public key" value={round.ea_pk} />
                )}
              </div>
            </div>

            {validators.length === 0 ? (
              <div className="rounded-xl border border-border-subtle bg-surface-1 p-5 text-center">
                <p className="text-xs font-semibold text-text-primary">No ceremony validators</p>
                <p className="mt-1 text-[11px] text-text-muted">
                  This round does not expose a ceremony validator snapshot.
                </p>
              </div>
            ) : (
              <div className="overflow-hidden rounded-xl border border-border-subtle bg-surface-1">
                <div className="border-b border-border-subtle px-5 py-3">
                  <h2 className="text-xs font-semibold text-text-primary">
                    Validators
                  </h2>
                </div>
                <div className="divide-y divide-border-subtle">
                  {validators.map((validator, index) => {
                    const address = validator.validator_address;
                    const moniker = validatorMonikers[address] ?? "Unknown";
                    const shamirIndex = validator.shamir_index != null
                      ? String(validator.shamir_index)
                      : "Not set";

                    return (
                      <div
                        key={`${address}-${index}`}
                        className="grid gap-3 px-5 py-4 lg:grid-cols-[90px_160px_minmax(0,1.2fr)_minmax(0,1fr)] lg:items-center"
                      >
                        <div>
                          <p className="text-[10px] uppercase tracking-wider text-text-muted">Index</p>
                          <p className="mt-1 font-mono text-xs text-text-primary">{shamirIndex}</p>
                        </div>
                        <div className="min-w-0">
                          <p className="text-[10px] uppercase tracking-wider text-text-muted">Moniker</p>
                          <p className="mt-1 truncate text-xs font-semibold text-text-primary">{moniker}</p>
                        </div>
                        <div className="min-w-0">
                          <p className="text-[10px] uppercase tracking-wider text-text-muted">Operator</p>
                          <div className="mt-1 min-w-0">
                            {address ? (
                              <CopyableCode value={address} head={16} tail={10} />
                            ) : (
                              <span className="text-[11px] text-text-muted">Not set</span>
                            )}
                          </div>
                        </div>
                        <div className="min-w-0">
                          <p className="text-[10px] uppercase tracking-wider text-text-muted">Pallas key</p>
                          <div className="mt-1 min-w-0">
                            {validator.pallas_pk ? (
                              <CopyableCode value={validator.pallas_pk} />
                            ) : (
                              <span className="text-[11px] text-text-muted">Not set</span>
                            )}
                          </div>
                        </div>
                      </div>
                    );
                  })}
                </div>
              </div>
            )}
          </div>
        )}
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
                      className="flex items-start gap-2 px-3 py-2 bg-surface-2 rounded-lg border border-border-subtle hover:border-accent/30 transition-colors cursor-pointer"
                    >
                      <div
                        className="w-3 h-3 rounded-full shrink-0 mt-0.5"
                        style={{ backgroundColor: optionColor(i, p.options.length) }}
                      />
                      <div className="min-w-0">
                        <p className="text-xs text-text-primary">
                          {opt.label}
                        </p>
                        {opt.description && (
                          <p className="text-[11px] text-text-muted leading-snug mt-0.5">
                            {opt.description}
                          </p>
                        )}
                      </div>
                    </div>
                  ))}
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
