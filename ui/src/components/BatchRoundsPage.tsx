import { useEffect, useMemo, useRef, useState } from "react";
import {
  AlertCircle,
  CheckCircle2,
  ExternalLink,
  Layers,
  Loader2,
  RefreshCw,
  ShieldCheck,
} from "lucide-react";
import * as chainApi from "../api/chain";
import * as cosmosTx from "../api/cosmosTx";
import * as votingKey from "../api/votingKey";
import type { UseWallet } from "../hooks/useWallet";
import { useUIConfig } from "../store/uiConfigContext";
import type { VotingRound } from "../types";
import { MAX_VOTE_OPTIONS, MIN_VOTE_OPTIONS } from "../constants/vote";
import {
  AUTHORIZATION_PIR_LAYOUT,
  buildSignedRoundEntry,
  sha256Hex,
} from "../utils/attestEntry";
import {
  batchRoundNames,
  buildBatchConfigPrIntent,
  matchBatchRounds,
} from "../utils/batchRounds";
import { buildChainOptions, isProposalValid } from "../utils/proposals";

const MAX_BATCH_ROUNDS = 20;
const POLL_INTERVAL_MS = 5_000;
const ACTIVATION_TIMEOUT_MS = 10 * 60 * 1000;

type BatchRoundState =
  | "pending"
  | "creating"
  | "waiting-active"
  | "ready"
  | "attested"
  | "error";

interface BatchRoundItem {
  index: number;
  name: string;
  roundIdHex?: string;
  eaPk?: string;
  state: BatchRoundState;
  error?: string;
}

type BatchPhase =
  | "idle"
  | "confirm-resume"
  | "running-create"
  | "signing"
  | "creating-pr"
  | "done"
  | "error";

const STATE_LABELS: Record<BatchRoundState, string> = {
  pending: "Waiting",
  creating: "Creating…",
  "waiting-active": "Waiting for ceremony…",
  ready: "Active",
  attested: "Attested",
  error: "Failed",
};

function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

// Local-timezone value for a datetime-local input, matching RoundEditor.
function toLocalInput(date: Date): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

function defaultEndTimeLocal(): string {
  return toLocalInput(new Date(Date.now() + 24 * 60 * 60 * 1000));
}

export function BatchRoundsPage({
  wallet,
  rounds,
}: {
  wallet: UseWallet;
  rounds: VotingRound[];
}) {
  const { precomputedBaseURL, zcashNetwork } = useUIConfig();
  const [baseName, setBaseName] = useState("");
  const [count, setCount] = useState(3);
  const [templateId, setTemplateId] = useState("");
  // Rounds share this end time instead of the template's, which may be stale
  // by the time a batch runs. Defaults to 24 hours from when the page opened.
  const [endTimeLocal, setEndTimeLocal] = useState(defaultEndTimeLocal);
  const [items, setItems] = useState<BatchRoundItem[]>([]);
  const [phase, setPhase] = useState<BatchPhase>("idle");
  const [runError, setRunError] = useState("");
  const [prUrl, setPrUrl] = useState("");
  const [resumeNames, setResumeNames] = useState<string[]>([]);
  // Bumped on unmount and on every new run so a superseded run's async loop
  // stops touching state.
  const runIdRef = useRef(0);

  useEffect(() => {
    return () => {
      runIdRef.current += 1;
    };
  }, []);

  const draftRounds = useMemo(
    () => rounds.filter((round) => round.status === "draft"),
    [rounds]
  );
  const template = draftRounds.find((round) => round.id === templateId) ?? null;
  const running =
    phase === "running-create" || phase === "signing" || phase === "creating-pr";
  const keplrConnected =
    !!wallet.address && wallet.source === "keplr" && !!wallet.chainId;

  const updateItem = (runId: number, index: number, patch: Partial<BatchRoundItem>) => {
    if (runIdRef.current !== runId) return;
    setItems((prev) =>
      prev.map((item) => (item.index === index ? { ...item, ...patch } : item))
    );
  };

  const preflight = async (round: VotingRound): Promise<string | null> => {
    if (!wallet.signer) {
      return "No wallet connected. Go to Settings → Wallet to connect Keplr.";
    }
    if (!keplrConnected) {
      return "Batch attestation derives its signing key from Keplr; connect Keplr (not a pasted key).";
    }
    const invalidProposalIndex = round.proposals.findIndex((p) => !isProposalValid(p));
    if (invalidProposalIndex !== -1) {
      return `Proposal ${invalidProposalIndex + 1} must have ${MIN_VOTE_OPTIONS}-${MAX_VOTE_OPTIONS} non-empty options.`;
    }
    const snapshotHeight = parseInt(round.settings.snapshotHeight, 10) || 0;
    if (snapshotHeight === 0) {
      return "The template round has no snapshot height. Set one in Round Settings.";
    }
    const endTimeMs = new Date(endTimeLocal).getTime();
    if (!endTimeLocal || Number.isNaN(endTimeMs)) {
      return "Enter a valid voting end time.";
    }
    if (endTimeMs <= Date.now()) {
      return "The voting end time must be in the future.";
    }
    if (!precomputedBaseURL) {
      return "Cannot validate the published PIR snapshot because this svoted did not expose SVOTE_PRECOMPUTED_BASE_URL.";
    }
    if (!zcashNetwork) {
      return "Cannot validate the published PIR snapshot because this svoted did not expose SVOTE_ZCASH_NETWORK.";
    }
    const validation = await chainApi.validatePublishedSnapshotManifest(
      precomputedBaseURL,
      zcashNetwork,
      snapshotHeight
    );
    if (validation.status !== "valid") {
      return validation.status === "missing"
        ? "No manifest.json exists for this snapshot height."
        : validation.status === "invalid"
          ? `Manifest is invalid: ${(validation.issues ?? []).join("; ")}`
          : (validation.message ?? "Manifest validation failed.");
    }
    try {
      const pirStatus = await chainApi.getSnapshotStatus();
      if (pirStatus.phase === "rebuilding") {
        return "PIR server is currently rebuilding. Wait for it to complete, then try again.";
      }
      if (
        pirStatus.phase === "serving" &&
        pirStatus.height != null &&
        pirStatus.height !== snapshotHeight
      ) {
        return `Cannot publish height ${snapshotHeight.toLocaleString()} because the PIR server is serving ${pirStatus.height.toLocaleString()}.`;
      }
    } catch {
      // Keep createVotingSession as the final source of truth when the PIR
      // status preflight is unavailable.
    }
    return null;
  };

  const waitForActiveRound = async (
    runId: number,
    name: string
  ): Promise<{ roundIdHex: string; eaPk: string }> => {
    const deadline = Date.now() + ACTIVATION_TIMEOUT_MS;
    for (;;) {
      if (runIdRef.current !== runId) throw new Error("cancelled");
      const overview = await chainApi.getRoundOverview().catch(() => null);
      const match = matchBatchRounds([name], overview?.current_rounds).get(name);
      if (match?.roundIdHex && match.isActive && match.eaPk) {
        return { roundIdHex: match.roundIdHex, eaPk: match.eaPk };
      }
      if (Date.now() > deadline) {
        throw new Error(
          "Ceremony did not complete within 10 minutes. Make sure enough ceremony validators are online, then retry to resume."
        );
      }
      await sleep(POLL_INTERVAL_MS);
    }
  };

  const handleStart = async (resume: boolean) => {
    if (!template) {
      setRunError("Pick a draft round to use as the shared configuration.");
      setPhase("error");
      return;
    }
    if (!baseName.trim()) {
      setRunError("Enter a base name for the batch.");
      setPhase("error");
      return;
    }
    const runId = runIdRef.current + 1;
    runIdRef.current = runId;
    setRunError("");
    setPrUrl("");
    setResumeNames([]);

    const names = batchRoundNames(baseName, count);
    setItems(
      names.map((name, i) => ({ index: i, name, state: "pending" as const }))
    );
    setPhase("running-create");

    try {
      const preflightError = await preflight(template);
      if (preflightError) throw new Error(preflightError);

      // Pre-run collision guard: titles are the only way to recapture round
      // ids, so refuse to start over pre-existing batch titles unless the
      // operator explicitly resumes.
      const overview = await chainApi.getRoundOverview();
      const existing = matchBatchRounds(names, overview.current_rounds);
      if (existing.size > 0 && !resume) {
        if (runIdRef.current !== runId) return;
        setResumeNames([...existing.keys()]);
        setPhase("confirm-resume");
        return;
      }

      const snapshotHeight = parseInt(template.settings.snapshotHeight, 10) || 0;
      const voteEndTime = Math.floor(new Date(endTimeLocal).getTime() / 1000);
      const proposals = template.proposals.map((p, i) => ({
        id: i + 1,
        title: p.title,
        description: p.description,
        options: buildChainOptions(p),
        zipNumber: p.zipNumber || undefined,
        forumURL: p.forumURL || undefined,
      }));

      // Phase A: strictly sequential creation. The chain allows only one
      // PENDING round at a time, and signAndBroadcast refetches the account
      // sequence per call, so each create must be awaited (never parallel)
      // and each round must activate before the next create.
      const ready: Array<{ name: string; roundIdHex: string; eaPk: string }> = [];
      for (const [i, name] of names.entries()) {
        if (runIdRef.current !== runId) return;
        const adopted = matchBatchRounds([name], overview.current_rounds).get(name);
        if (!adopted) {
          updateItem(runId, i, { state: "creating" });
          const result = await cosmosTx.createVotingSession(
            chainApi.getApiBase(),
            wallet.signer!,
            {
              snapshotHeight,
              voteEndTime,
              proposals,
              description: template.settings.description || name,
              title: name,
              discussionURL: template.settings.discussionURL || "",
            }
          );
          if (result.code !== 0) {
            throw Object.assign(
              new Error(result.log || `Transaction failed with code ${result.code}`),
              { itemIndex: i }
            );
          }
        }
        updateItem(runId, i, { state: "waiting-active" });
        try {
          const active = await waitForActiveRound(runId, name);
          updateItem(runId, i, {
            state: "ready",
            roundIdHex: active.roundIdHex,
            eaPk: active.eaPk,
          });
          ready.push({ name, ...active });
        } catch (err) {
          throw Object.assign(
            err instanceof Error ? err : new Error(String(err)),
            { itemIndex: i }
          );
        }
      }

      // Phase B: derive the Ed25519 key once (one Keplr popup) and sign all
      // entries locally, then a single intent signature and one batch PR.
      if (runIdRef.current !== runId) return;
      setPhase("signing");
      const key = await votingKey.deriveEd25519FromKeplr(
        wallet.address!,
        wallet.chainId!,
        wallet.signKeplrPayload
      );
      const signedRounds: chainApi.ConfigPRBatchRoundInput[] = [];
      const intentRounds = [];
      for (const round of ready) {
        const signed = await buildSignedRoundEntry(round.roundIdHex, round.eaPk, key);
        signedRounds.push({
          round_id: round.roundIdHex,
          entry: signed.entry,
          signed_payload_hash: signed.signedPayloadHash,
        });
        intentRounds.push({
          round_id: round.roundIdHex,
          signed_payload_hash: signed.signedPayloadHash,
          entry_sha256: await sha256Hex(
            new TextEncoder().encode(JSON.stringify(signed.entry))
          ),
        });
      }

      if (runIdRef.current !== runId) return;
      setPhase("creating-pr");
      const managers = await chainApi.getVoteManagers();
      const isVoteManager = (managers.vote_manager_addresses ?? []).some(
        (address) => address.toLowerCase() === wallet.address?.toLowerCase()
      );
      if (!isVoteManager) {
        throw new Error("Connected wallet is not in the current vote-manager set.");
      }
      const payload = buildBatchConfigPrIntent(
        intentRounds,
        Math.floor(Date.now() / 1000)
      );
      const signature = await wallet.signPayload(payload);
      const resp = await chainApi.createConfigPrBatch({
        rounds: signedRounds,
        pir_layout: AUTHORIZATION_PIR_LAYOUT,
        batch_name: baseName.trim(),
        auth: {
          signer_address: wallet.address!,
          payload,
          signature: signature.signature,
          pub_key: signature.pubKey,
        },
      });
      if (runIdRef.current !== runId) return;
      setItems((prev) => prev.map((item) => ({ ...item, state: "attested" as const })));
      setPrUrl(resp.html_url);
      setPhase("done");
    } catch (err) {
      if (runIdRef.current !== runId) return;
      const message = err instanceof Error ? err.message : String(err);
      if (message === "cancelled") return;
      const itemIndex = (err as { itemIndex?: number }).itemIndex;
      if (itemIndex !== undefined) {
        updateItem(runId, itemIndex, { state: "error", error: message });
      }
      setRunError(message);
      setPhase("error");
    }
  };

  return (
    <div className="flex-1 overflow-y-auto">
      <div className="max-w-3xl mx-auto px-6 py-10 space-y-6">
        <div>
          <div className="flex items-center gap-2 mb-2">
            <Layers size={18} className="text-accent" />
            <h1 className="text-lg font-bold text-text-primary">Batch Rounds</h1>
            <span className="rounded-full border border-warning/40 bg-warning/10 px-2 py-0.5 text-[10px] font-semibold text-warning">
              Testnet only
            </span>
          </div>
          <p className="text-[11px] text-text-muted max-w-2xl">
            Creates a configured number of voting rounds sharing one draft
            configuration, waits for each ceremony to complete (the chain
            allows only one pending round at a time), then signs attestations
            for all of them and opens a single config pull request. Expect one
            Keplr signature per created round plus two for the attestation.
            Rounds only activate when enough ceremony validators are online.
          </p>
        </div>

        <section className="bg-surface-1 border border-border-subtle rounded-xl p-5 space-y-4">
          <h2 className="text-xs font-semibold text-text-primary">Configuration</h2>
          <div className="grid gap-4 md:grid-cols-2">
            <div>
              <label className="block text-[11px] text-text-secondary mb-1">
                Base name
              </label>
              <input
                value={baseName}
                onChange={(e) => setBaseName(e.target.value)}
                disabled={running}
                placeholder="Load test"
                className="w-full px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary placeholder:text-text-muted focus:outline-none focus:border-accent/50 disabled:opacity-50"
              />
              <p className="mt-1 text-[10px] text-text-muted">
                Rounds are named "{baseName.trim() || "Load test"} 1" …
                "{baseName.trim() || "Load test"} {count}".
              </p>
            </div>
            <div>
              <label className="block text-[11px] text-text-secondary mb-1">
                Number of rounds
              </label>
              <input
                type="number"
                min={1}
                max={MAX_BATCH_ROUNDS}
                value={count}
                onChange={(e) =>
                  setCount(
                    Math.max(
                      1,
                      Math.min(MAX_BATCH_ROUNDS, parseInt(e.target.value, 10) || 1)
                    )
                  )
                }
                disabled={running}
                className="w-full px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary focus:outline-none focus:border-accent/50 disabled:opacity-50 [color-scheme:dark]"
              />
            </div>
          </div>
          <div>
            <label className="block text-[11px] text-text-secondary mb-1">
              Shared configuration (draft round)
            </label>
            <select
              value={template ? templateId : ""}
              onChange={(e) => setTemplateId(e.target.value)}
              disabled={running}
              className="w-full px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary focus:outline-none focus:border-accent/50 cursor-pointer disabled:opacity-50 [color-scheme:dark]"
            >
              <option value="">
                {draftRounds.length === 0
                  ? "No draft rounds — create one in the builder first"
                  : "Pick a draft round…"}
              </option>
              {draftRounds.map((round) => (
                <option key={round.id} value={round.id}>
                  {round.name} ({round.proposals.length} proposal
                  {round.proposals.length === 1 ? "" : "s"}, snapshot{" "}
                  {round.settings.snapshotHeight || "unset"})
                </option>
              ))}
            </select>
            <p className="mt-1 text-[10px] text-text-muted">
              Snapshot height, proposals, description, and discussion URL are
              taken from this draft; the round titles and the end time below
              differ.
            </p>
          </div>
          <div>
            <label className="block text-[11px] text-text-secondary mb-1">
              Voting end time
            </label>
            <div className="flex items-center gap-2">
              <input
                type="datetime-local"
                value={endTimeLocal}
                onChange={(e) => setEndTimeLocal(e.target.value)}
                disabled={running}
                className="flex-1 px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary focus:outline-none focus:border-accent/50 disabled:opacity-50 [color-scheme:dark]"
              />
              <button
                onClick={() => setEndTimeLocal(defaultEndTimeLocal())}
                disabled={running}
                className="shrink-0 px-3 py-2 bg-surface-3 hover:bg-surface-1 text-text-secondary rounded-lg text-[11px] font-semibold transition-colors cursor-pointer disabled:opacity-50"
              >
                +24h from now
              </button>
            </div>
            <p className="mt-1 text-[10px] text-text-muted">
              Applied to every round in the batch, overriding the template's
              end time (which may be stale). Defaults to 24 hours from now.
            </p>
          </div>
          <div className="flex items-center justify-end gap-2">
            {phase === "confirm-resume" ? (
              <>
                <button
                  onClick={() => {
                    setResumeNames([]);
                    setItems([]);
                    setPhase("idle");
                  }}
                  className="px-3 py-2 bg-surface-3 hover:bg-surface-1 text-text-secondary rounded-lg text-[11px] font-semibold transition-colors cursor-pointer"
                >
                  Cancel
                </button>
                <button
                  onClick={() => void handleStart(true)}
                  className="px-3 py-2 bg-accent/90 hover:bg-accent text-surface-0 rounded-lg text-[11px] font-semibold transition-colors cursor-pointer"
                >
                  Resume batch
                </button>
              </>
            ) : (
              <button
                onClick={() => void handleStart(phase === "error")}
                disabled={running || !template || !baseName.trim()}
                className="px-3 py-2 bg-accent/90 hover:bg-accent text-surface-0 rounded-lg text-[11px] font-semibold transition-colors cursor-pointer disabled:opacity-50"
              >
                {running ? (
                  <span className="inline-flex items-center gap-1.5">
                    <Loader2 size={12} className="animate-spin" /> Running…
                  </span>
                ) : phase === "error" ? (
                  <span className="inline-flex items-center gap-1.5">
                    <RefreshCw size={12} /> Retry
                  </span>
                ) : (
                  `Create ${count} round${count === 1 ? "" : "s"} + attest`
                )}
              </button>
            )}
          </div>
        </section>

        {phase === "confirm-resume" && (
          <div className="flex items-start gap-2 bg-warning/10 border border-warning/30 rounded-lg p-3">
            <AlertCircle size={14} className="text-warning mt-0.5 shrink-0" />
            <p className="text-[11px] text-text-secondary">
              Rounds titled {resumeNames.map((name) => `"${name}"`).join(", ")}{" "}
              already exist on chain. Resume adopts them and only creates the
              missing rounds; cancel and pick a different base name to start a
              fresh batch.
            </p>
          </div>
        )}

        {items.length > 0 && phase !== "confirm-resume" && (
          <section className="bg-surface-1 border border-border-subtle rounded-xl p-5 space-y-3">
            <h2 className="text-xs font-semibold text-text-primary">Progress</h2>
            <ul className="space-y-1.5">
              {items.map((item) => (
                <li
                  key={item.index}
                  className="flex items-center gap-2 rounded-lg bg-surface-2 px-3 py-2"
                >
                  {item.state === "attested" || item.state === "ready" ? (
                    <CheckCircle2
                      size={14}
                      className={
                        item.state === "attested" ? "text-success" : "text-accent"
                      }
                    />
                  ) : item.state === "error" ? (
                    <AlertCircle size={14} className="text-danger" />
                  ) : item.state === "pending" ? (
                    <span className="inline-block w-3.5 h-3.5 rounded-full border border-border-subtle" />
                  ) : (
                    <Loader2 size={14} className="animate-spin text-text-muted" />
                  )}
                  <span className="text-[11px] text-text-primary min-w-0 flex-1 truncate">
                    {item.name}
                  </span>
                  {item.roundIdHex && (
                    <span className="text-[10px] text-text-muted font-mono">
                      {item.roundIdHex.slice(0, 12)}…
                    </span>
                  )}
                  <span
                    className={`text-[10px] font-semibold ${
                      item.state === "error"
                        ? "text-danger"
                        : item.state === "attested"
                          ? "text-success"
                          : "text-text-secondary"
                    }`}
                  >
                    {STATE_LABELS[item.state]}
                  </span>
                </li>
              ))}
            </ul>
            {(phase === "signing" || phase === "creating-pr") && (
              <p className="text-[10px] text-text-muted">
                {phase === "signing"
                  ? "Signing attestation entries with the derived Ed25519 key…"
                  : "Opening the combined config pull request…"}
              </p>
            )}
          </section>
        )}

        {runError && (
          <div className="flex items-start gap-2 bg-danger/10 border border-danger/30 rounded-lg p-3">
            <AlertCircle size={14} className="text-danger mt-0.5 shrink-0" />
            <p className="text-[11px] text-danger">
              {runError} {phase === "error" && "Retry resumes from the failed step."}
            </p>
          </div>
        )}

        {prUrl && (
          <div className="flex items-start gap-2 bg-success/10 border border-success/30 rounded-lg p-3">
            <ShieldCheck size={14} className="text-success mt-0.5 shrink-0" />
            <p className="text-[11px] text-text-secondary">
              All rounds are active and attested.{" "}
              <a
                href={prUrl}
                target="_blank"
                rel="noreferrer"
                className="inline-flex items-center gap-1 text-accent hover:text-accent/80 underline-offset-2 hover:underline font-semibold"
              >
                Review the config PR
                <ExternalLink size={10} />
              </a>
            </p>
          </div>
        )}
      </div>
    </div>
  );
}
