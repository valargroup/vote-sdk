import type { ReactNode } from "react";
import { ChevronDown } from "lucide-react";
import type { Validator } from "../api/chain";

/** Groups non-bonded validators in a collapsed section and sorts jailed entries last. */
export function ValidatorList({ validators, children }: {
  validators: Validator[];
  children: (validator: Validator, index: number) => ReactNode;
}) {
  const sorted = [...validators].sort((a, b) => {
    const jailedOrder = Number(Boolean(a.jailed)) - Number(Boolean(b.jailed));
    if (jailedOrder !== 0) return jailedOrder;
    const aName = (a.description?.moniker ?? "").toLowerCase();
    const bName = (b.description?.moniker ?? "").toLowerCase();
    return aName.localeCompare(bName);
  });
  const active = sorted.filter((validator) => validator.status === "BOND_STATUS_BONDED");
  const inactive = sorted.filter((validator) => validator.status !== "BOND_STATUS_BONDED");

  return (
    <>
      <div className="space-y-2">{active.map(children)}</div>
      {inactive.length > 0 && (
        <details className="group mt-4">
          <summary className="flex items-center gap-2 rounded-lg p-2 text-xs text-text-secondary hover:bg-surface-3 cursor-pointer list-none [&::-webkit-details-marker]:hidden focus-visible:outline-2 focus-visible:outline-accent">
            <ChevronDown size={14} aria-hidden="true" className="-rotate-90 transition-transform group-open:rotate-0" />
            <span>Inactive validators</span>
            <span className="text-[10px] bg-surface-3 text-text-muted px-1.5 py-0.5 rounded-full">
              {inactive.length}
            </span>
          </summary>
          <p className="text-[10px] text-text-muted px-2 mt-1 mb-3">
            Unbonding and unbonded validators. Jailed validators appear last.
          </p>
          <div className="space-y-2">{inactive.map(children)}</div>
        </details>
      )}
    </>
  );
}
