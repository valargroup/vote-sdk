import {
  Plus,
  LayoutList,
  FileText,
  Upload,
  Shield,
  Settings,
  BarChart3,
  Trash2,
  Users,
  Database,
} from "lucide-react";
import type { VotingRound, RoundStatus } from "../types";

const STATUS_COLORS: Record<RoundStatus, string> = {
  draft: "bg-surface-3 text-text-secondary",
  published: "bg-success/20 text-success",
  archived: "bg-surface-3 text-text-muted",
};

const STATUS_LABELS: Record<RoundStatus, string> = {
  draft: "Draft",
  published: "Published",
  archived: "Archived",
};

function timeAgo(iso: string): string {
  const diff = Date.now() - new Date(iso).getTime();
  const mins = Math.floor(diff / 60000);
  if (mins < 1) return "Just now";
  if (mins < 60) return `${mins}m ago`;
  const hrs = Math.floor(mins / 60);
  if (hrs < 24) return `${hrs}h ago`;
  const days = Math.floor(hrs / 24);
  return `${days}d ago`;
}

interface NavItem {
  label: string;
  icon: React.ReactNode;
  filter?: RoundStatus | "all";
  section?: string;
}

const NAV_ITEMS: NavItem[] = [
  { label: "All rounds", icon: <LayoutList size={15} />, filter: "all" },
  { label: "Published", icon: <Upload size={15} />, filter: "published" },
  { label: "Drafts", icon: <FileText size={15} />, filter: "draft" },
];


interface SidebarProps {
  rounds: VotingRound[];
  activeRoundId: string | null;
  activeFilter: RoundStatus | "all";
  onFilterChange: (filter: RoundStatus | "all") => void;
  onSelectRound: (id: string) => void;
  onCreateRound: () => void;
  onNavigate: (section: string) => void;
  onDeleteRound: (id: string) => void;
  currentSection: string;
}

export function Sidebar({
  rounds,
  activeRoundId,
  activeFilter,
  onFilterChange,
  onSelectRound,
  onCreateRound,
  onNavigate,
  onDeleteRound,
  currentSection,
}: SidebarProps) {
  const recentRounds = [...rounds]
    .sort((a, b) => new Date(b.updatedAt).getTime() - new Date(a.updatedAt).getTime())
    .slice(0, 5);

  return (
    <aside className="w-[280px] min-w-[280px] h-screen bg-surface-1 border-r border-border flex flex-col">
      {/* Header */}
      <div className="p-4 border-b border-border-subtle">
        <button
          onClick={() => onNavigate("about")}
          className="flex items-center gap-2 mb-1 cursor-pointer hover:opacity-80 transition-opacity"
        >
          <Shield size={18} className="text-accent" />
          <span className="text-sm font-semibold text-text-primary">
            Shielded Vote Creator
          </span>
        </button>
        <p className="text-[11px] text-text-muted">
          Private voting rounds & proposals
        </p>
      </div>

      {/* Actions */}
      <div className="p-3 flex flex-col gap-2">
        <button
          onClick={onCreateRound}
          className="flex items-center gap-2 px-3 py-2 bg-accent/90 hover:bg-accent text-surface-0 rounded-lg text-xs font-semibold transition-colors cursor-pointer"
        >
          <Plus size={14} />
          New voting round
        </button>
      </div>

      {/* Navigation */}
      <nav className="px-3 flex-1 overflow-y-auto">
        <p className="text-[10px] uppercase tracking-wider text-text-muted px-2 mt-3 mb-1">
          Monitor
        </p>
        <button
          onClick={() => onNavigate("vote-status")}
          className={`w-full flex items-center gap-2.5 px-2.5 py-1.5 rounded-md text-xs transition-colors cursor-pointer ${
            currentSection === "vote-status"
              ? "bg-surface-3 text-text-primary"
              : "text-text-secondary hover:bg-surface-2 hover:text-text-primary"
          }`}
        >
          <BarChart3 size={15} />
          Vote status
        </button>

        <button
          onClick={() => onNavigate("validators")}
          className={`w-full flex items-center gap-2.5 px-2.5 py-1.5 rounded-md text-xs transition-colors cursor-pointer ${
            currentSection === "validators"
              ? "bg-surface-3 text-text-primary"
              : "text-text-secondary hover:bg-surface-2 hover:text-text-primary"
          }`}
        >
          <Users size={15} />
          Validators
        </button>

        <button
          onClick={() => onNavigate("snapshot")}
          className={`w-full flex items-center gap-2.5 px-2.5 py-1.5 rounded-md text-xs transition-colors cursor-pointer ${
            currentSection === "snapshot"
              ? "bg-surface-3 text-text-primary"
              : "text-text-secondary hover:bg-surface-2 hover:text-text-primary"
          }`}
        >
          <Database size={15} />
          Snapshot
        </button>

        <p className="text-[10px] uppercase tracking-wider text-text-muted px-2 mt-3 mb-1">
          Voting rounds
        </p>
        {NAV_ITEMS.map((item) => (
          <button
            key={item.label}
            onClick={() => {
              onFilterChange(item.filter!);
              onNavigate("rounds");
            }}
            className={`w-full flex items-center gap-2.5 px-2.5 py-1.5 rounded-md text-xs transition-colors cursor-pointer ${
              currentSection === "rounds" && activeFilter === item.filter
                ? "bg-surface-3 text-text-primary"
                : "text-text-secondary hover:bg-surface-2 hover:text-text-primary"
            }`}
          >
            {item.icon}
            {item.label}
          </button>
        ))}


      </nav>

      {/* Recent rounds — bottom-aligned */}
      <div className="px-3 border-t border-border-subtle">
        <p className="text-[10px] uppercase tracking-wider text-text-muted px-2 mt-3 mb-1">
          Recent rounds
        </p>
        {recentRounds.length === 0 ? (
          <p className="text-[11px] text-text-muted px-2 py-2 italic">
            No rounds yet.{"\n"}Create your first shielded voting round.
          </p>
        ) : (
          <div className="flex flex-col gap-0.5">
            {recentRounds.map((round) => (
              <div
                key={round.id}
                className={`group relative flex items-stretch rounded-md transition-colors ${
                  activeRoundId === round.id && currentSection === "builder"
                    ? "bg-surface-3"
                    : "hover:bg-surface-2"
                }`}
              >
                <button
                  onClick={() => {
                    onSelectRound(round.id);
                    onNavigate("builder");
                  }}
                  className="flex-1 text-left px-2.5 py-2 cursor-pointer min-w-0"
                >
                  <div className="flex items-center justify-between gap-1">
                    <span className="text-xs text-text-primary truncate">
                      {round.name}
                    </span>
                    <span
                      className={`shrink-0 text-[9px] w-[62px] text-center py-1 leading-none rounded-full ${STATUS_COLORS[round.status]}`}
                    >
                      {STATUS_LABELS[round.status]}
                    </span>
                  </div>
                  <div className="flex items-center gap-2 mt-0.5">
                    <span className="text-[10px] text-text-muted">
                      Edited {timeAgo(round.updatedAt)}
                    </span>
                    <span className="text-[10px] text-text-muted">
                      {round.proposals.length} proposal
                      {round.proposals.length !== 1 ? "s" : ""}
                    </span>
                  </div>
                </button>
                {round.status !== "published" && (
                  <button
                    onClick={(e) => {
                      e.stopPropagation();
                      onDeleteRound(round.id);
                    }}
                    title="Delete round"
                    className="absolute right-1 top-1/2 -translate-y-1/2 opacity-0 group-hover:opacity-100 flex items-center justify-center p-1.5 text-text-muted hover:text-danger transition-opacity cursor-pointer"
                  >
                    <Trash2 size={12} />
                  </button>
                )}
              </div>
            ))}
          </div>
        )}
      </div>

      {/* Settings at bottom */}
      <div className="p-3 border-t border-border-subtle">
        <button
          onClick={() => onNavigate("settings")}
          className={`w-full flex items-center gap-2.5 px-2.5 py-1.5 rounded-md text-xs transition-colors cursor-pointer ${
            currentSection === "settings"
              ? "bg-surface-3 text-text-primary"
              : "text-text-secondary hover:bg-surface-2 hover:text-text-primary"
          }`}
        >
          <Settings size={15} />
          Settings
        </button>
      </div>
    </aside>
  );
}
