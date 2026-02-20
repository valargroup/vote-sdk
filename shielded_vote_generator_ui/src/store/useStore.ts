import { useState, useCallback, useEffect } from "react";
import { v4 as uuidv4 } from "uuid";
import type { VotingRound, Proposal, ProposalType, RoundStatus } from "../types";

const STORAGE_KEY = "shielded-vote-rounds";
const SEEDED_KEY = "shielded-vote-seeded";

function makeBinaryProposal(title: string, description: string): Proposal {
  return {
    id: uuidv4(),
    title,
    description,
    type: "binary",
    options: [
      { id: uuidv4(), label: "Support" },
      { id: uuidv4(), label: "Oppose" },
    ],
    allowAbstain: false,
    metadata: [],
  };
}

function createSeedRound(): VotingRound {
  const now = new Date().toISOString();
  return {
    id: uuidv4(),
    name: "(SAMPLE) NU7 Sentiment Polling",
    status: "draft",
    proposals: [
      makeBinaryProposal(
        "Zcash Shielded Assets (ZSAs)",
        "What is your general sentiment toward including Zcash Shielded Assets (ZSAs) as a protocol feature?\n\nReference: ZIP-227"
      ),
      makeBinaryProposal(
        "Network Sustainability Mechanism (NSM)",
        "What is your general sentiment toward adding protocol support for the Network Sustainability Mechanism (NSM), including smoothing the issuance curve, which allows ZEC to be removed from circulation and later reissued as future block rewards to help sustain network security while preserving the 21 million ZEC supply cap?"
      ),
      makeBinaryProposal(
        "Fee Burning via NSM",
        "What is your general sentiment toward burning 60% of transaction fees via the Network Sustainability Mechanism (NSM)? The goals are to demonstrate Zcash's commitment to long-term sustainability, to burn ZEC so that it can be re-issued in the future without exceeding the 21M supply cap, and in the context of dynamic fees, to prevent miners from manipulating fees.\n\nReference: ZIP-235"
      ),
      makeBinaryProposal(
        "Memo Bundles",
        "What is your general sentiment toward including Memo Bundles, which let transactions include memos larger than 512 bytes and share a memo across multiple recipients, and also permits inclusion of authenticated reply-to addresses, as a protocol feature?\n\nReference: ZIP-231"
      ),
      makeBinaryProposal(
        "Explicit Fees",
        "What is your general sentiment toward adding protocol support to enable Explicit Fees, allowing transaction fees to be clearly specified and committed to in the transaction?\n\nReference: ZIP-2002"
      ),
      makeBinaryProposal(
        "Disallowing v4 Transactions",
        "What is your general sentiment toward reducing the complexity and attack surface of the Zcash protocol by disallowing v4 transactions? This would disable the ability to spend Sprout funds, for which there will be no wallet support in any case after the prior deprecation of zcashd.\n\nReference: ZIP-2003"
      ),
      makeBinaryProposal(
        "Project Tachyon",
        "What is your general sentiment toward deploying a new shielded protocol or pool to address scalability challenges as part of Project Tachyon?"
      ),
      makeBinaryProposal(
        "STARK Proof Verification via TZEs",
        "What is your general sentiment toward adding protocol support for STARK proof verification via Transparent Zcash Extensions (TZEs) to enable Layer-2 designs on Zcash?"
      ),
      makeBinaryProposal(
        "Dynamic Fee Mechanism",
        "What is your general sentiment toward adding protocol support for a comparable-based, dynamic fee mechanism?"
      ),
      makeBinaryProposal(
        "Consensus Accounts",
        "What is your general sentiment toward adding protocol support for consensus accounts, which generalize the functionality of the dev fund lockbox and reduce the operational expense of collecting ZCG funds and miner rewards?"
      ),
      makeBinaryProposal(
        "Orchard Quantum Recoverability",
        "What is your general sentiment toward Orchard quantum recoverability, which aims to ensure that if the security of elliptic curve-based cryptography came into doubt (due to the emergence of a cryptographically relevant quantum computer or otherwise), then new Orchard funds could remain recoverable by a later protocol — as opposed to having to be burnt in order to avoid an unbounded balance violation?\n\nReference: ZIP-2005"
      ),
    ],
    settings: {
      description:
        "Sentiment polling for Zcash Network Upgrade 7 (NU7) feature candidates. Each proposal is a binary Support/Oppose question on whether a feature should be included in NU7.",
      snapshotHeight: "",
      endTime: "",
      openUntilClosed: true,
      defaultProposalType: "binary",
      defaultLabels: ["Support", "Oppose"],
    },
    createdAt: now,
    updatedAt: now,
  };
}

function loadRounds(): VotingRound[] {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (raw) return JSON.parse(raw);
    // First-time user: seed with sample round
    if (!localStorage.getItem(SEEDED_KEY)) {
      const seed = [createSeedRound()];
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
      { id: uuidv4(), label: "Support" },
      { id: uuidv4(), label: "Oppose" },
    ],
    allowAbstain: false,
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
    setSaveState("saving");
    const t = setTimeout(() => {
      saveRounds(rounds);
      setSaveState("saved");
    }, 300);
    return () => clearTimeout(t);
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
              { id: uuidv4(), label: "Support" },
              { id: uuidv4(), label: "Oppose" },
            ]
          : [
              { id: uuidv4(), label: "Option A" },
              { id: uuidv4(), label: "Option B" },
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
