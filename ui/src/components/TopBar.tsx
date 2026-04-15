import { useState, useRef, useEffect } from "react";
import { Eye, Code2, MoreHorizontal, Copy, Trash2, Check } from "lucide-react";
import { StatusPill } from "./StatusPill";
import type { VotingRound } from "../types";

interface TopBarProps {
  round: VotingRound;
  saveState: "saved" | "saving";
  onUpdateName: (name: string) => void;
  onPublish: () => void;
  onPreview: () => void;
  onDuplicate: () => void;
  onDelete: () => void;
  onNavigate: (section: string) => void;
  isReadonly?: boolean;
}

export function TopBar({
  round,
  saveState,
  onUpdateName,
  onPublish,
  onPreview,
  onDuplicate,
  onDelete,
  onNavigate,
  isReadonly = false,
}: TopBarProps) {
  const [editing, setEditing] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);
  const [tempName, setTempName] = useState(round.name);
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (editing && inputRef.current) {
      inputRef.current.focus();
      inputRef.current.select();
    }
  }, [editing]);

  const commitName = () => {
    setEditing(false);
    if (tempName.trim()) {
      onUpdateName(tempName.trim());
    } else {
      setTempName(round.name);
    }
  };

  const hasEndTime = round.settings.endTime.length > 0;
  const hasSnapshot = parseInt(round.settings.snapshotHeight, 10) > 0;
  const hasProposals = round.proposals.length > 0;
  const proposalsValid = round.proposals.every(
    (p) => p.title.trim().length > 0 && p.options.length >= 2
  );
  const canPublish = hasEndTime && hasSnapshot && hasProposals && proposalsValid && round.status !== "published";

  const publishDisabledReason = round.status === "published"
    ? "Already published"
    : !hasEndTime
      ? "Set a voting end time"
      : !hasSnapshot
        ? "Set a snapshot height"
        : !hasProposals
          ? "Add at least one proposal"
          : !proposalsValid
            ? "Fix incomplete proposals"
            : "";

  return (
    <div className="flex items-center justify-between px-4 py-2.5 bg-surface-1 border-b border-border min-h-[48px]">
      {/* Left */}
      <div className="flex items-center gap-3 min-w-0">
        {editing && !isReadonly ? (
          <input
            ref={inputRef}
            value={tempName}
            onChange={(e) => setTempName(e.target.value)}
            onBlur={commitName}
            onKeyDown={(e) => {
              if (e.key === "Enter") commitName();
              if (e.key === "Escape") {
                setTempName(round.name);
                setEditing(false);
              }
            }}
            className="text-sm font-semibold bg-transparent border-b border-accent text-text-primary focus:outline-none min-w-[200px]"
          />
        ) : (
          <h2
            onClick={() => {
              if (isReadonly) return;
              setTempName(round.name);
              setEditing(true);
            }}
            className={`text-sm font-semibold text-text-primary truncate ${isReadonly ? "" : "cursor-pointer hover:text-accent-glow"}`}
          >
            {round.name}
          </h2>
        )}
        <StatusPill status={round.status} />
        <span className="text-[10px] text-text-muted flex items-center gap-1">
          {saveState === "saving" ? (
            "Saving..."
          ) : (
            <>
              <Check size={10} /> Saved
            </>
          )}
        </span>
      </div>

      {/* Right */}
      <div className="flex items-center gap-2">
        <button
          onClick={onPreview}
          className="flex items-center gap-1.5 px-2.5 py-1.5 text-[11px] text-text-secondary hover:text-text-primary hover:bg-surface-2 rounded-md transition-colors cursor-pointer"
        >
          <Eye size={13} /> Preview
        </button>
        <button
          onClick={() => onNavigate("json")}
          className="flex items-center gap-1.5 px-2.5 py-1.5 text-[11px] text-text-secondary hover:text-text-primary hover:bg-surface-2 rounded-md transition-colors cursor-pointer"
        >
          <Code2 size={13} /> Export JSON
        </button>
        <button
          onClick={onPublish}
          disabled={!canPublish}
          title={publishDisabledReason}
          className={`px-3 py-1.5 text-[11px] font-semibold rounded-md transition-colors ${
            canPublish
              ? "bg-accent/90 hover:bg-accent text-surface-0 cursor-pointer"
              : "bg-surface-3 text-text-muted cursor-not-allowed"
          }`}
        >
          Publish round
        </button>
        <div className="relative">
          <button
            onClick={() => setMenuOpen(!menuOpen)}
            className="p-1.5 rounded-md hover:bg-surface-2 text-text-muted cursor-pointer"
          >
            <MoreHorizontal size={16} />
          </button>
          {menuOpen && (
            <div className="absolute right-0 top-8 z-20 bg-surface-2 border border-border rounded-lg shadow-lg py-1 min-w-[150px]">
              <button
                onClick={() => { onDuplicate(); setMenuOpen(false); }}
                className="w-full flex items-center gap-2 px-3 py-1.5 text-[11px] text-text-secondary hover:bg-surface-3 hover:text-text-primary cursor-pointer"
              >
                <Copy size={12} /> Duplicate
              </button>
              <button
                onClick={() => { onDelete(); setMenuOpen(false); }}
                className="w-full flex items-center gap-2 px-3 py-1.5 text-[11px] text-danger hover:bg-surface-3 cursor-pointer"
              >
                <Trash2 size={12} /> Delete
              </button>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
