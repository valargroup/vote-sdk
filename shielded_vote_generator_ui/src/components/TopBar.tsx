import { useState, useRef, useEffect } from "react";
import { Eye, Code2, MoreHorizontal, Copy, Archive, Trash2, Check } from "lucide-react";
import { StatusPill } from "./StatusPill";
import type { VotingRound } from "../types";

interface TopBarProps {
  round: VotingRound;
  saveState: "saved" | "saving";
  onUpdateName: (name: string) => void;
  onPublish: () => void;
  onPreview: () => void;
  onDuplicate: () => void;
  onArchive: () => void;
  onDelete: () => void;
  onNavigate: (section: string) => void;
}

export function TopBar({
  round,
  saveState,
  onUpdateName,
  onPublish,
  onPreview,
  onDuplicate,
  onArchive,
  onDelete,
  onNavigate,
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

  const allValid = round.proposals.length > 0 && round.proposals.every(
    (p) => p.title.trim().length > 0 && p.options.length >= 2
  );

  return (
    <div className="flex items-center justify-between px-4 py-2.5 bg-surface-1 border-b border-border min-h-[48px]">
      {/* Left */}
      <div className="flex items-center gap-3 min-w-0">
        {editing ? (
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
              setTempName(round.name);
              setEditing(true);
            }}
            className="text-sm font-semibold text-text-primary cursor-pointer hover:text-accent-glow truncate"
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
          disabled={!allValid || round.status === "published"}
          className="px-3 py-1.5 text-[11px] font-semibold rounded-md transition-colors cursor-pointer disabled:opacity-40 disabled:cursor-not-allowed bg-accent/90 hover:bg-accent text-surface-0"
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
                onClick={() => { onArchive(); setMenuOpen(false); }}
                className="w-full flex items-center gap-2 px-3 py-1.5 text-[11px] text-text-secondary hover:bg-surface-3 hover:text-text-primary cursor-pointer"
              >
                <Archive size={12} /> Archive
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
