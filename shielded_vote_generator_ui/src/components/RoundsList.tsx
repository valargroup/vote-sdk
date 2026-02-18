import { useState } from "react";
import { Search, MoreHorizontal, Copy, Archive } from "lucide-react";
import { StatusPill } from "./StatusPill";
import type { VotingRound, RoundStatus } from "../types";

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

interface RoundsListProps {
  rounds: VotingRound[];
  activeFilter: RoundStatus | "all";
  onFilterChange: (f: RoundStatus | "all") => void;
  onSelectRound: (id: string) => void;
  onDuplicate: (id: string) => void;
  onArchive: (id: string) => void;
}

const FILTERS: { label: string; value: RoundStatus | "all" }[] = [
  { label: "All", value: "all" },
  { label: "Draft", value: "draft" },
  { label: "In progress", value: "in_progress" },
  { label: "Published", value: "published" },
];

export function RoundsList({
  rounds,
  activeFilter,
  onFilterChange,
  onSelectRound,
  onDuplicate,
  onArchive,
}: RoundsListProps) {
  const [search, setSearch] = useState("");
  const [menuOpen, setMenuOpen] = useState<string | null>(null);

  const filtered = rounds.filter((r) => {
    if (activeFilter !== "all" && r.status !== activeFilter) return false;
    if (search && !r.name.toLowerCase().includes(search.toLowerCase())) return false;
    return true;
  });

  return (
    <div className="flex flex-col h-full">
      <div className="px-6 py-4 border-b border-border">
        <h2 className="text-sm font-semibold text-text-primary mb-3">
          Voting rounds
        </h2>
        <div className="flex items-center gap-3">
          <div className="relative flex-1 max-w-xs">
            <Search size={14} className="absolute left-2.5 top-1/2 -translate-y-1/2 text-text-muted" />
            <input
              type="text"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              placeholder="Search rounds..."
              className="w-full pl-8 pr-3 py-1.5 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary placeholder:text-text-muted focus:outline-none focus:border-accent/50"
            />
          </div>
          <div className="flex gap-1">
            {FILTERS.map((f) => (
              <button
                key={f.value}
                onClick={() => onFilterChange(f.value)}
                className={`px-2.5 py-1 rounded-full text-[10px] transition-colors cursor-pointer ${
                  activeFilter === f.value
                    ? "bg-accent/20 text-accent-glow"
                    : "bg-surface-2 text-text-muted hover:bg-surface-3 hover:text-text-secondary"
                }`}
              >
                {f.label}
              </button>
            ))}
          </div>
        </div>
      </div>

      <div className="flex-1 overflow-y-auto p-4">
        {filtered.length === 0 ? (
          <div className="text-center py-12">
            <p className="text-xs text-text-muted">No rounds found</p>
          </div>
        ) : (
          <div className="grid gap-3">
            {filtered.map((round) => (
              <div
                key={round.id}
                onClick={() => onSelectRound(round.id)}
                className="bg-surface-1 border border-border-subtle rounded-xl p-4 hover:border-accent/30 transition-colors cursor-pointer"
              >
                <div className="flex items-start justify-between">
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2 mb-1">
                      <span className="text-xs font-semibold text-text-primary truncate">
                        {round.name}
                      </span>
                      <StatusPill status={round.status} />
                    </div>
                    <div className="flex items-center gap-3 text-[10px] text-text-muted">
                      <span>
                        {round.proposals.length} proposal{round.proposals.length !== 1 ? "s" : ""}
                      </span>
                      <span>Edited {timeAgo(round.updatedAt)}</span>
                    </div>
                  </div>
                  <div className="flex items-center gap-1.5" onClick={(e) => e.stopPropagation()}>
                    <div className="relative">
                      <button
                        onClick={() => setMenuOpen(menuOpen === round.id ? null : round.id)}
                        className="p-1 rounded hover:bg-surface-3 text-text-muted cursor-pointer"
                      >
                        <MoreHorizontal size={14} />
                      </button>
                      {menuOpen === round.id && (
                        <div className="absolute right-0 top-7 z-10 bg-surface-2 border border-border rounded-lg shadow-lg py-1 min-w-[140px]">
                          <button
                            onClick={() => { onDuplicate(round.id); setMenuOpen(null); }}
                            className="w-full flex items-center gap-2 px-3 py-1.5 text-[11px] text-text-secondary hover:bg-surface-3 hover:text-text-primary cursor-pointer"
                          >
                            <Copy size={12} /> Duplicate
                          </button>
                          <button
                            onClick={() => { onArchive(round.id); setMenuOpen(null); }}
                            className="w-full flex items-center gap-2 px-3 py-1.5 text-[11px] text-text-secondary hover:bg-surface-3 hover:text-text-primary cursor-pointer"
                          >
                            <Archive size={12} /> Archive
                          </button>
                        </div>
                      )}
                    </div>
                  </div>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>
    </div>
  );
}
