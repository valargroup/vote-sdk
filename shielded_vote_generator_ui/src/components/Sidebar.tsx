import {
  Plus,
  Upload,
  LayoutList,
  Clock,
  FileText,
  Archive,
  Code2,
  Download,
  Shield,
  Info,
  Settings,
  Server,
} from "lucide-react";
import type { VotingRound, RoundStatus } from "../types";

const STATUS_COLORS: Record<RoundStatus, string> = {
  draft: "bg-surface-3 text-text-secondary",
  in_progress: "bg-accent-dim/40 text-accent-glow",
  published: "bg-success/20 text-success",
  archived: "bg-surface-3 text-text-muted",
};

const STATUS_LABELS: Record<RoundStatus, string> = {
  draft: "Draft",
  in_progress: "In Progress",
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
  { label: "In progress", icon: <Clock size={15} />, filter: "in_progress" },
  { label: "Drafts", icon: <FileText size={15} />, filter: "draft" },
  { label: "Archived", icon: <Archive size={15} />, filter: "archived" },
];

const RESULTS_ITEMS: NavItem[] = [
  { label: "On-chain rounds", icon: <Server size={15} />, section: "chain-rounds" },
  { label: "Raw JSON", icon: <Code2 size={15} />, section: "json" },
  { label: "Downloads", icon: <Download size={15} />, section: "downloads" },
];

interface SidebarProps {
  rounds: VotingRound[];
  activeRoundId: string | null;
  activeFilter: RoundStatus | "all";
  onFilterChange: (filter: RoundStatus | "all") => void;
  onSelectRound: (id: string) => void;
  onCreateRound: () => void;
  onImportJson: () => void;
  onNavigate: (section: string) => void;
  currentSection: string;
}

export function Sidebar({
  rounds,
  activeRoundId,
  activeFilter,
  onFilterChange,
  onSelectRound,
  onCreateRound,
  onImportJson,
  onNavigate,
  currentSection,
}: SidebarProps) {
  const recentRounds = rounds.slice(0, 5);

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
        <button
          onClick={onImportJson}
          className="flex items-center gap-2 px-3 py-2 bg-surface-2 hover:bg-surface-3 text-text-secondary rounded-lg text-xs transition-colors border border-border-subtle cursor-pointer"
        >
          <Upload size={14} />
          Import JSON
        </button>
      </div>

      {/* Navigation */}
      <nav className="px-3 flex-1 overflow-y-auto">
        {/* About */}
        <button
          onClick={() => onNavigate("about")}
          className={`w-full flex items-center gap-2.5 px-2.5 py-1.5 rounded-md text-xs transition-colors cursor-pointer mt-1 ${
            currentSection === "about"
              ? "bg-surface-3 text-text-primary"
              : "text-text-secondary hover:bg-surface-2 hover:text-text-primary"
          }`}
        >
          <Info size={15} />
          About
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

        <p className="text-[10px] uppercase tracking-wider text-text-muted px-2 mt-4 mb-1">
          Results & exports
        </p>
        {RESULTS_ITEMS.map((item) => (
          <button
            key={item.label}
            onClick={() => onNavigate(item.section!)}
            className={`w-full flex items-center gap-2.5 px-2.5 py-1.5 rounded-md text-xs transition-colors cursor-pointer ${
              currentSection === item.section
                ? "bg-surface-3 text-text-primary"
                : "text-text-secondary hover:bg-surface-2 hover:text-text-primary"
            }`}
          >
            {item.icon}
            {item.label}
          </button>
        ))}

        {/* Recent rounds */}
        <p className="text-[10px] uppercase tracking-wider text-text-muted px-2 mt-4 mb-1">
          Recent rounds
        </p>
        {recentRounds.length === 0 ? (
          <p className="text-[11px] text-text-muted px-2 py-2 italic">
            No rounds yet.{"\n"}Create your first shielded voting round.
          </p>
        ) : (
          <div className="flex flex-col gap-0.5">
            {recentRounds.map((round) => (
              <button
                key={round.id}
                onClick={() => {
                  onSelectRound(round.id);
                  onNavigate("builder");
                }}
                className={`w-full text-left px-2.5 py-2 rounded-md transition-colors cursor-pointer ${
                  activeRoundId === round.id && currentSection === "builder"
                    ? "bg-surface-3"
                    : "hover:bg-surface-2"
                }`}
              >
                <div className="flex items-center justify-between">
                  <span className="text-xs text-text-primary truncate max-w-[140px]">
                    {round.name}
                  </span>
                  <span
                    className={`text-[9px] px-1.5 py-0.5 rounded-full ${STATUS_COLORS[round.status]}`}
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
            ))}
          </div>
        )}
      </nav>

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
