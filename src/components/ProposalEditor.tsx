import { useState } from "react";
import { Plus, Trash2, ChevronDown, ChevronRight, Copy } from "lucide-react";
import { v4 as uuidv4 } from "uuid";
import type { Proposal, ProposalType } from "../types";

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

interface ProposalEditorProps {
  proposal: Proposal;
  onUpdate: (patch: Partial<Proposal>) => void;
  readonly?: boolean;
}

export function ProposalEditor({ proposal, onUpdate, readonly = false }: ProposalEditorProps) {
  const [descTab, setDescTab] = useState<"write" | "preview">("write");
  const [advancedOpen, setAdvancedOpen] = useState(false);

  const handleTypeChange = (type: ProposalType) => {
    const options =
      type === "binary"
        ? [
            { id: uuidv4(), label: "Support" },
            { id: uuidv4(), label: "Oppose" },
          ]
        : [
            { id: uuidv4(), label: "Option A" },
            { id: uuidv4(), label: "Option B" },
          ];
    onUpdate({ type, options });
  };

  const handleOptionChange = (optionId: string, label: string) => {
    onUpdate({
      options: proposal.options.map((o) =>
        o.id === optionId ? { ...o, label } : o
      ),
    });
  };

  const handleAddOption = () => {
    onUpdate({
      options: [
        ...proposal.options,
        { id: uuidv4(), label: "" },
      ],
    });
  };

  const handleRemoveOption = (optionId: string) => {
    if (proposal.options.length <= 2) return;
    onUpdate({
      options: proposal.options.filter((o) => o.id !== optionId),
    });
  };

  return (
    <div className="space-y-4">
        {/* Title */}
        <div>
          <label className="block text-[11px] text-text-secondary mb-1">
            Title
          </label>
          <input
            type="text"
            value={proposal.title}
            onChange={(e) => onUpdate({ title: e.target.value })}
            placeholder="Proposal title"
            readOnly={readonly}
            className={`w-full px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary placeholder:text-text-muted focus:outline-none focus:border-accent/50 ${readonly ? "opacity-60 cursor-default" : ""}`}
          />
        </div>

        {/* Description */}
        <div>
          <label className="block text-[11px] text-text-secondary mb-1">
            Description
          </label>
          <div className="flex gap-1 mb-1">
            <button
              onClick={() => setDescTab("write")}
              className={`px-2 py-0.5 rounded text-[10px] cursor-pointer ${
                descTab === "write"
                  ? "bg-surface-3 text-text-primary"
                  : "text-text-muted hover:text-text-secondary"
              }`}
            >
              Write
            </button>
            <button
              onClick={() => setDescTab("preview")}
              className={`px-2 py-0.5 rounded text-[10px] cursor-pointer ${
                descTab === "preview"
                  ? "bg-surface-3 text-text-primary"
                  : "text-text-muted hover:text-text-secondary"
              }`}
            >
              Preview
            </button>
          </div>
          {descTab === "write" ? (
            <textarea
              value={proposal.description}
              onChange={(e) => onUpdate({ description: e.target.value })}
              placeholder="Describe this proposal..."
              rows={4}
              readOnly={readonly}
              className={`w-full px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary placeholder:text-text-muted focus:outline-none focus:border-accent/50 resize-none ${readonly ? "opacity-60 cursor-default" : ""}`}
            />
          ) : (
            <div className="w-full px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary min-h-[80px]">
              {proposal.description || (
                <span className="text-text-muted italic">Nothing to preview</span>
              )}
            </div>
          )}
          <p className="text-[10px] text-text-muted mt-1">
            Markdown supported. Links, lists, headings.
          </p>
        </div>

        {/* Supported options */}
        <div>
          <label className="block text-[11px] text-text-secondary mb-1">
            Supported options
          </label>
          <div className="space-y-1.5">
            {proposal.options.map((option, i) => (
              <div key={option.id} className="flex items-center gap-2">
                <span
                  className="w-2.5 h-2.5 rounded-full shrink-0"
                  style={{ backgroundColor: optionColor(i, proposal.options.length) }}
                />
                <span className="text-xs text-text-primary">{option.label || "Unnamed"}</span>
              </div>
            ))}
          </div>
        </div>

        {/* Proposal Type */}
        <div>
          <label className="block text-[11px] text-text-secondary mb-1.5">
            Proposal Type
          </label>
          <div className="flex rounded-lg overflow-hidden border border-border-subtle">
            <button
              onClick={() => !readonly && handleTypeChange("binary")}
              disabled={readonly}
              className={`flex-1 py-1.5 text-[11px] text-center transition-colors border-r border-border-subtle ${
                readonly
                  ? "cursor-default opacity-60"
                  : "cursor-pointer"
              } ${
                proposal.type === "binary"
                  ? "bg-accent/20 text-accent-glow"
                  : "bg-surface-2 text-text-muted hover:bg-surface-3"
              }`}
            >
              Binary
            </button>
            <button
              onClick={() => !readonly && handleTypeChange("multi-choice")}
              disabled={readonly}
              className={`flex-1 py-1.5 text-[11px] text-center transition-colors ${
                readonly
                  ? "cursor-default opacity-60"
                  : "cursor-pointer"
              } ${
                proposal.type === "multi-choice"
                  ? "bg-accent/20 text-accent-glow"
                  : "bg-surface-2 text-text-muted hover:bg-surface-3"
              }`}
            >
              Multi-Choice
            </button>
          </div>
        </div>

        {/* Options Editor */}
        <div>
          <div className="space-y-1.5">
            {proposal.options.map((option, i) => (
              <div key={option.id} className="flex items-center gap-2">
                <span
                  className="w-2.5 h-2.5 rounded-full shrink-0"
                  style={{ backgroundColor: optionColor(i, proposal.options.length) }}
                />
                <input
                  type="text"
                  value={option.label}
                  onChange={(e) => handleOptionChange(option.id, e.target.value)}
                  readOnly={readonly}
                  className={`flex-1 px-2.5 py-1.5 bg-surface-2 border border-border-subtle rounded-md text-xs text-text-primary focus:outline-none focus:border-accent/50 ${readonly ? "opacity-60 cursor-default" : ""}`}
                  placeholder="Option label"
                />
                {!readonly && proposal.type === "multi-choice" && proposal.options.length > 2 && (
                  <button
                    onClick={() => handleRemoveOption(option.id)}
                    className="p-1 text-text-muted hover:text-danger rounded cursor-pointer"
                  >
                    <Trash2 size={12} />
                  </button>
                )}
              </div>
            ))}
          </div>
          {!readonly && proposal.type === "multi-choice" && (
            <button
              onClick={handleAddOption}
              className="flex items-center gap-1 mt-2 text-[11px] text-text-muted hover:text-accent-glow transition-colors cursor-pointer"
            >
              <Plus size={12} /> Add choice
            </button>
          )}
        </div>

        {/* Advanced */}
        <div className="border-t border-border-subtle pt-3">
          <button
            onClick={() => setAdvancedOpen(!advancedOpen)}
            className="flex items-center gap-1.5 text-[11px] text-text-secondary hover:text-text-primary cursor-pointer"
          >
            {advancedOpen ? <ChevronDown size={12} /> : <ChevronRight size={12} />}
            Advanced
          </button>
          {advancedOpen && (
            <div className="mt-3 space-y-3">
              <div>
                <label className="block text-[10px] text-text-muted mb-1">
                  Proposal ID
                </label>
                <div className="flex items-center gap-2">
                  <code className="flex-1 px-2 py-1 bg-surface-2 rounded text-[10px] text-text-muted truncate">
                    {proposal.id}
                  </code>
                  <button
                    onClick={() => navigator.clipboard.writeText(proposal.id)}
                    className="p-1 text-text-muted hover:text-text-secondary cursor-pointer"
                  >
                    <Copy size={12} />
                  </button>
                </div>
              </div>
              <div className="flex items-center gap-2">
                <input
                  type="checkbox"
                  checked={proposal.allowAbstain}
                  onChange={(e) => onUpdate({ allowAbstain: e.target.checked })}
                  disabled={readonly}
                  className={`accent-accent ${readonly ? "opacity-60 cursor-default" : ""}`}
                />
                <label className="text-[11px] text-text-secondary">
                  Allow abstain
                </label>
              </div>
            </div>
          )}
        </div>
    </div>
  );
}
