import { useId } from "react";
import { CopyButton } from "./CopyButton";
import { IMT_VERIFICATION_GUIDE_URL, imtVerificationPrompt, type IMTVerificationRound } from "../utils/imtVerification";

export function IMTVerificationAcknowledgment({ rounds, chainId, network, checked, onChange, disabled = false }: {
  rounds: IMTVerificationRound[];
  chainId: string;
  network: string | null;
  checked: boolean;
  onChange: (checked: boolean) => void;
  disabled?: boolean;
}) {
  const id = useId();
  return (
    <div className="rounded-lg border border-border-subtle bg-surface-2 p-3 space-y-3">
      {rounds.map((round) => (
        <div key={round.roundId} className="space-y-1 text-[11px]">
          <p className="font-mono break-all text-text-secondary">Round: {round.roundId}</p>
          <p className="text-text-muted">Zcash snapshot: {round.snapshotHeight || "unavailable"}</p>
          <p className="font-mono break-all text-text-secondary">IMT root: {round.circuitRoot || "unavailable"}</p>
          <CopyButton value={imtVerificationPrompt(round.roundId, chainId, network)} label="Copy AI prompt" />
        </div>
      ))}
      <div className="flex items-start gap-2 text-xs text-text-primary">
        <input id={id} type="checkbox" checked={checked} disabled={disabled}
          onChange={(event) => onChange(event.target.checked)} className="mt-0.5" />
        <label htmlFor={id}>
          I have <a href={IMT_VERIFICATION_GUIDE_URL} target="_blank" rel="noreferrer"
            onClick={(event) => event.stopPropagation()}
            className="text-accent underline">independently verified the IMT root</a>{" "}
          for {rounds.length === 1 ? "this round’s" : "each listed round’s"} Zcash snapshot and confirmed that the rebuilt root matches the on-chain root.
        </label>
      </div>
      <p className="text-[11px] text-text-muted">
        Give the linked instructions to your AI assistant to run the verification. Review the results, then check the box to continue. No file upload is needed.
      </p>
    </div>
  );
}
