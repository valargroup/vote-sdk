import { useState } from "react";
import { Check, Copy } from "lucide-react";

interface CopyButtonProps {
  value: string;
  label: string;
  className?: string;
}

export function CopyButton({ value, label, className }: CopyButtonProps) {
  const [copied, setCopied] = useState(false);

  const copy = async () => {
    await navigator.clipboard.writeText(value);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  };

  return (
    <button
      onClick={copy}
      disabled={!value}
      className={
        className ??
        "inline-flex items-center gap-1.5 px-2.5 py-1.5 bg-surface-3 hover:bg-surface-2 text-text-secondary hover:text-text-primary rounded-md text-[11px] transition-colors cursor-pointer disabled:opacity-50"
      }
    >
      {copied ? <Check size={12} className="text-success" /> : <Copy size={12} />}
      {copied ? "Copied" : label}
    </button>
  );
}
