import { useState, useCallback, useEffect } from "react";
import { v4 as uuidv4 } from "uuid";
import type { VotingRound, Proposal, ProposalType, RoundStatus } from "../types";
import {
  applySampleTemplateOptionDescriptions,
  createSampleRoundFromTemplate,
  createSampleRounds,
  type SampleRoundTemplateId,
} from "./sampleRounds";
import type { ImportedRound } from "../utils/roundJson";

const STORAGE_KEY = "shielded-vote-rounds";
const SEEDED_KEY = "shielded-vote-seeded";

function loadRounds(): VotingRound[] {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw) {
      const stored = JSON.parse(raw) as VotingRound[];
      const hydrated = applySampleTemplateOptionDescriptions(stored);
      if (hydrated !== stored) saveRounds(hydrated);
      return hydrated;
    }
    // First-time user: seed with sample round
    if (!localStorage.getItem(SEEDED_KEY)) {
      const seed = createSampleRounds();
      localStorage.setItem(SEEDED_KEY, "true");
      localStorage.setItem(STORAGE_KEY, JSON.stringify(seed));
      return seed;
    }
    return [];
  } catch {
    return [];
  }
}

function saveRounds(rounds: VotingRound[]) {
  localStorage.setItem(STORAGE_KEY, JSON.stringify(rounds));
}

function createDefaultProposal(): Proposal {
  return {
    id: uuidv4(),
    title: "",
    description: "",
    type: "binary",
    options: [
      { id: uuidv4(), label: "Support", description: "" },
      { id: uuidv4(), label: "Oppose", description: "" },
    ],
    zipNumber: "",
    forumURL: "",
    metadata: [],
  };
}

function defaultEndTime(): string {
  const d = new Date();
  d.setMinutes(d.getMinutes() + 10, 0, 0);
  return d.toISOString();
}

function createDefaultRound(name: string): VotingRound {
  const now = new Date().toISOString();
  return {
    id: uuidv4(),
    name,
    status: "draft",
    proposals: [createDefaultProposal()],
    settings: {
      description: "",
      snapshotHeight: "",
      endTime: defaultEndTime(),
      openUntilClosed: true,
      defaultProposalType: "binary",
      defaultLabels: ["Support", "Oppose"],
      discussionURL: "",
    },
    createdAt: now,
    updatedAt: now,
  };
}

export function useStore() {
  const [rounds, setRounds] = useState<VotingRound[]>(loadRounds);
  const [activeRoundId, setActiveRoundId] = useState<string | null>(null);
  const [activeProposalId, setActiveProposalId] = useState<string | null>(null);
  const [saveState, setSaveState] = useState<"saved" | "saving">("saved");

  useEffect(() => {
    const markSavingTimer = window.setTimeout(() => setSaveState("saving"), 0);
    const saveTimer = window.setTimeout(() => {
      saveRounds(rounds);
      setSaveState("saved");
    }, 300);
    return () => {
      window.clearTimeout(markSavingTimer);
      window.clearTimeout(saveTimer);
    };
  }, [rounds]);

  const activeRound = rounds.find((r) => r.id === activeRoundId) ?? null;
  const activeProposal =
    activeRound?.proposals.find((p) => p.id === activeProposalId) ?? null;

  const updateRound = useCallback(
    (id: string, patch: Partial<VotingRound>) => {
      setRounds((prev) =>
        prev.map((r) =>
          r.id === id ? { ...r, ...patch, updatedAt: new Date().toISOString() } : r
        )
      );
    },
    []
  );

  const createRound = useCallback((name?: string) => {
    const round = createDefaultRound(name ?? "Untitled Round");
    setRounds((prev) => [round, ...prev]);
    setActiveRoundId(round.id);
    setActiveProposalId(round.proposals[0]?.id ?? null);
    return round;
  }, []);

  const importRound = useCallback((draft: ImportedRound) => {
    const now = new Date().toISOString();
    const round: VotingRound = {
      id: uuidv4(),
      name: draft.name,
      status: "draft",
      settings: {
        ...draft.settings,
        defaultLabels: [
          draft.settings.defaultLabels[0],
          draft.settings.defaultLabels[1],
        ],
      },
      proposals: draft.proposals.map((proposal) => ({
        ...proposal,
        id: uuidv4(),
        options: proposal.options.map((option) => ({
          ...option,
          id: uuidv4(),
        })),
        metadata: proposal.metadata.map((item) => ({ ...item })),
      })),
      createdAt: now,
      updatedAt: now,
    };
    setRounds((prev) => [round, ...prev]);
    setActiveRoundId(round.id);
    setActiveProposalId(round.proposals[0]?.id ?? null);
    return round;
  }, []);

  const createSampleRound = useCallback((templateId?: SampleRoundTemplateId) => {
    const round = createSampleRoundFromTemplate(templateId);
    setRounds((prev) => [round, ...prev]);
    setActiveRoundId(round.id);
    setActiveProposalId(round.proposals[0]?.id ?? null);
    return round;
  }, []);

  const deleteRound = useCallback(
    (id: string) => {
      setRounds((prev) => prev.filter((r) => r.id !== id));
      if (activeRoundId === id) {
        setActiveRoundId(null);
        setActiveProposalId(null);
      }
    },
    [activeRoundId]
  );

  const duplicateRound = useCallback(
    (id: string) => {
      const source = rounds.find((r) => r.id === id);
      if (!source) return;
      const now = new Date().toISOString();
      const newRound: VotingRound = {
        ...structuredClone(source),
        id: uuidv4(),
        name: `${source.name} (copy)`,
        status: "draft",
        createdAt: now,
        updatedAt: now,
      };
      // regenerate IDs
      newRound.proposals = newRound.proposals.map((p) => ({
        ...p,
        id: uuidv4(),
        options: p.options.map((o) => ({ ...o, id: uuidv4() })),
      }));
      setRounds((prev) => [newRound, ...prev]);
      setActiveRoundId(newRound.id);
    },
    [rounds]
  );

  const setRoundStatus = useCallback(
    (id: string, status: RoundStatus) => {
      updateRound(id, { status });
    },
    [updateRound]
  );

  const addProposal = useCallback(
    (roundId: string) => {
      const proposal = createDefaultProposal();
      setRounds((prev) =>
        prev.map((r) =>
          r.id === roundId
            ? { ...r, proposals: [...r.proposals, proposal], updatedAt: new Date().toISOString() }
            : r
        )
      );
      setActiveProposalId(proposal.id);
      return proposal;
    },
    []
  );

  const updateProposal = useCallback(
    (roundId: string, proposalId: string, patch: Partial<Proposal>) => {
      setRounds((prev) =>
        prev.map((r) =>
          r.id === roundId
            ? {
                ...r,
                proposals: r.proposals.map((p) =>
                  p.id === proposalId ? { ...p, ...patch } : p
                ),
                updatedAt: new Date().toISOString(),
              }
            : r
        )
      );
    },
    []
  );

  const deleteProposal = useCallback(
    (roundId: string, proposalId: string) => {
      setRounds((prev) =>
        prev.map((r) =>
          r.id === roundId
            ? {
                ...r,
                proposals: r.proposals.filter((p) => p.id !== proposalId),
                updatedAt: new Date().toISOString(),
              }
            : r
        )
      );
      if (activeProposalId === proposalId) {
        setActiveProposalId(null);
      }
    },
    [activeProposalId]
  );

  const duplicateProposal = useCallback(
    (roundId: string, proposalId: string) => {
      const round = rounds.find((r) => r.id === roundId);
      const source = round?.proposals.find((p) => p.id === proposalId);
      if (!source) return;
      const newProposal: Proposal = {
        ...structuredClone(source),
        id: uuidv4(),
        title: `${source.title} (copy)`,
        options: source.options.map((o) => ({ ...o, id: uuidv4() })),
      };
      setRounds((prev) =>
        prev.map((r) =>
          r.id === roundId
            ? { ...r, proposals: [...r.proposals, newProposal], updatedAt: new Date().toISOString() }
            : r
        )
      );
      setActiveProposalId(newProposal.id);
    },
    [rounds]
  );

  const reorderProposals = useCallback(
    (roundId: string, fromIndex: number, toIndex: number) => {
      setRounds((prev) =>
        prev.map((r) => {
          if (r.id !== roundId) return r;
          const proposals = [...r.proposals];
          const [moved] = proposals.splice(fromIndex, 1);
          proposals.splice(toIndex, 0, moved);
          return { ...r, proposals, updatedAt: new Date().toISOString() };
        })
      );
    },
    []
  );

  const setProposalType = useCallback(
    (roundId: string, proposalId: string, type: ProposalType) => {
      const defaultOptions =
        type === "binary"
          ? [
              { id: uuidv4(), label: "Support", description: "" },
              { id: uuidv4(), label: "Oppose", description: "" },
            ]
          : [
              { id: uuidv4(), label: "Option A", description: "" },
              { id: uuidv4(), label: "Option B", description: "" },
            ];
      updateProposal(roundId, proposalId, { type, options: defaultOptions });
    },
    [updateProposal]
  );

  return {
    rounds,
    activeRound,
    activeRoundId,
    activeProposal,
    activeProposalId,
    saveState,
    setActiveRoundId,
    setActiveProposalId,
    createRound,
    importRound,
    createSampleRound,
    updateRound,
    deleteRound,
    duplicateRound,
    setRoundStatus,
    addProposal,
    updateProposal,
    deleteProposal,
    duplicateProposal,
    reorderProposals,
    setProposalType,
  };
}
