import { useState } from "react";
import type { CoordinatorActionDescription } from "../api/coordinatorActions";
import { CopyButton } from "./CopyButton";

interface CoordinatorActionPayloadViewProps {
  description: CoordinatorActionDescription;
}

type PayloadView = "details" | "json";

export function CoordinatorActionPayloadView({
  description,
}: CoordinatorActionPayloadViewProps) {
  const [view, setView] = useState<PayloadView>("details");
  const json = JSON.stringify(description.json, null, 2);

  const viewButtonClass = (buttonView: PayloadView) =>
    `px-2.5 py-1 rounded text-[10px] font-semibold transition-colors cursor-pointer ${
      view === buttonView
        ? "bg-surface-3 text-text-primary"
        : "text-text-muted hover:text-text-secondary"
    }`;

  return (
    <div>
      <div className="flex items-center justify-between gap-2 mb-2">
        <div
          role="group"
          aria-label="Payload view"
          className="inline-flex items-center rounded-md bg-surface-1 border border-border-subtle p-0.5"
        >
          <button
            type="button"
            aria-pressed={view === "details"}
            onClick={() => setView("details")}
            className={viewButtonClass("details")}
          >
            Details
          </button>
          <button
            type="button"
            aria-pressed={view === "json"}
            onClick={() => setView("json")}
            className={viewButtonClass("json")}
          >
            JSON
          </button>
        </div>

        {view === "json" && (
          <CopyButton
            value={json}
            label="Copy JSON"
            className="inline-flex items-center gap-1.5 px-2.5 py-1 bg-surface-1 hover:bg-surface-3 text-text-secondary hover:text-text-primary border border-border-subtle rounded-md text-[10px] transition-colors cursor-pointer disabled:opacity-50"
          />
        )}
      </div>

      {view === "details" ? (
        <div className="rounded-md bg-surface-1 border border-border-subtle p-3 space-y-1.5">
          {description.rows.map((row) => (
            <div key={row.label} className="grid gap-1 sm:grid-cols-[132px_minmax(0,1fr)]">
              <span className="text-[10px] text-text-muted">{row.label}</span>
              <span
                className={`text-[10px] text-text-primary whitespace-pre-wrap break-all ${
                  row.mono ? "font-mono" : ""
                }`}
              >
                {row.value}
              </span>
            </div>
          ))}
        </div>
      ) : (
        <div>
          <pre className="max-h-[560px] overflow-auto rounded-md bg-surface-1 border border-border-subtle p-3 text-[10px] leading-5 text-text-primary">
            <code>{json}</code>
          </pre>
          <p className="text-[10px] text-text-muted mt-1.5">
            {description.jsonDecoded
              ? "Decoded from the signed protobuf payload. Byte fields are shown as hex."
              : "Raw payload envelope. Decoded JSON is unavailable for this action."}
          </p>
        </div>
      )}
    </div>
  );
}
