import { useState } from "react";
import { Copy, Download, CheckCircle2, AlertTriangle, Check, ArrowLeft } from "lucide-react";
import type { VotingRound } from "../types";

interface JsonViewProps {
  round: VotingRound;
  onBack?: () => void;
}

function generateExportJson(round: VotingRound) {
  return {
    schema: "v1",
    round: {
      id: round.id,
      name: round.name,
      status: round.status,
      settings: round.settings,
      proposals: round.proposals.map((p, i) => ({
        index: i,
        id: p.id,
        title: p.title,
        description: p.description,
        type: p.type,
        options: p.options.map((o) => ({ id: o.id, label: o.label })),
        allowAbstain: p.allowAbstain,
        metadata: p.metadata,
      })),
    },
    generatedAt: new Date().toISOString(),
  };
}

function validateRound(round: VotingRound): string[] {
  const issues: string[] = [];
  if (!round.name.trim()) issues.push("Round name is empty");
  if (round.proposals.length === 0) issues.push("No proposals added");
  round.proposals.forEach((p, i) => {
    if (!p.title.trim()) issues.push(`round.proposals[${i}].title is empty`);
    if (p.options.length < 2) issues.push(`round.proposals[${i}].options has fewer than 2 choices`);
    p.options.forEach((o, j) => {
      if (!o.label.trim()) issues.push(`round.proposals[${i}].options[${j}].label is empty`);
    });
  });
  return issues;
}

export function JsonView({ round, onBack }: JsonViewProps) {
  const [copied, setCopied] = useState(false);
  const [validated, setValidated] = useState<string[] | null>(null);

  const json = JSON.stringify(generateExportJson(round), null, 2);
  const issues = validateRound(round);
  const isValid = issues.length === 0;

  const handleCopy = async () => {
    await navigator.clipboard.writeText(json);
    setCopied(true);
    setTimeout(() => setCopied(false), 2000);
  };

  const handleDownload = () => {
    const slug = round.name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/(^-|-$)/g, "");
    const date = new Date().toISOString().split("T")[0];
    const filename = `shielded-vote-round_${slug}_${date}.json`;
    const blob = new Blob([json], { type: "application/json" });
    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = filename;
    a.click();
    URL.revokeObjectURL(url);
  };

  return (
    <div className="flex flex-col h-full">
      {/* Header */}
      <div className="px-6 py-4 border-b border-border">
        {onBack && (
          <button
            onClick={onBack}
            className="flex items-center gap-1 text-[11px] text-text-muted hover:text-accent-glow transition-colors cursor-pointer mb-2"
          >
            <ArrowLeft size={12} />
            Back to builder
          </button>
        )}
        <h2 className="text-sm font-semibold text-text-primary">Raw JSON</h2>
        <p className="text-[11px] text-text-muted mt-0.5">
          This is the canonical export format for this round.
        </p>
        <div className="flex items-center gap-2 mt-3">
          <button
            onClick={handleCopy}
            className="flex items-center gap-1.5 px-3 py-1.5 bg-surface-3 hover:bg-accent-dim/30 text-text-secondary hover:text-accent-glow rounded-md text-[11px] transition-colors cursor-pointer"
          >
            {copied ? <Check size={12} /> : <Copy size={12} />}
            {copied ? "Copied!" : "Copy JSON"}
          </button>
          <button
            onClick={handleDownload}
            className="flex items-center gap-1.5 px-3 py-1.5 bg-surface-3 hover:bg-accent-dim/30 text-text-secondary hover:text-accent-glow rounded-md text-[11px] transition-colors cursor-pointer"
          >
            <Download size={12} /> Download .json
          </button>
          <button
            onClick={() => setValidated(issues)}
            className="flex items-center gap-1.5 px-3 py-1.5 bg-surface-3 hover:bg-accent-dim/30 text-text-secondary hover:text-accent-glow rounded-md text-[11px] transition-colors cursor-pointer"
          >
            {isValid ? <CheckCircle2 size={12} /> : <AlertTriangle size={12} />}
            Validate
          </button>
        </div>
      </div>

      {/* Validation errors */}
      {validated !== null && validated.length > 0 && (
        <div className="px-6 py-3 bg-danger/10 border-b border-danger/30">
          <p className="text-[11px] text-danger font-semibold mb-1.5">
            {validated.length} issue{validated.length !== 1 ? "s" : ""} found
          </p>
          <ul className="space-y-1">
            {validated.map((issue, i) => (
              <li key={i} className="text-[11px] text-danger/80">
                {issue}
              </li>
            ))}
          </ul>
        </div>
      )}
      {validated !== null && validated.length === 0 && (
        <div className="px-6 py-3 bg-success/10 border-b border-success/30">
          <p className="text-[11px] text-success font-semibold flex items-center gap-1">
            <CheckCircle2 size={12} /> Valid
          </p>
        </div>
      )}

      {/* JSON body */}
      <div className="flex-1 overflow-auto p-4">
        <pre className="text-[11px] leading-5 text-text-primary bg-surface-1 border border-border-subtle rounded-lg p-4 overflow-auto h-full">
          <code>{json}</code>
        </pre>
      </div>

      {/* Footer */}
      <div className="px-6 py-2 border-t border-border flex items-center gap-4 text-[10px] text-text-muted">
        <span>Schema: v1</span>
        <span>Last generated: {new Date().toLocaleTimeString()}</span>
        <span className="flex items-center gap-1">
          {isValid ? (
            <>
              <CheckCircle2 size={10} className="text-success" /> Valid
            </>
          ) : (
            <>
              <AlertTriangle size={10} className="text-warning" /> {issues.length} error{issues.length !== 1 ? "s" : ""}
            </>
          )}
        </span>
      </div>
    </div>
  );
}
