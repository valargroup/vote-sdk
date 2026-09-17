// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { BatchRoundsPage } from "./BatchRoundsPage";
import type { VotingRound } from "../types";
import type { UseWallet } from "../hooks/useWallet";
import * as chainApi from "../api/chain";
import * as cosmosTx from "../api/cosmosTx";
import * as votingKey from "../api/votingKey";
import { useDetectedChainId, useSelectedChainUrl } from "../hooks/useDetectedChainId";

vi.mock("../api/chain", () => {
  const rounds = ["a", "b", "c"].map((value, i) => ({
    vote_round_id: btoa(value.repeat(32)), title: `Batch ${i + 1}`,
    ea_pk: btoa("e".repeat(32)), snapshot_height: "3459350",
    snapshot_blockhash: btoa("h".repeat(32)), nullifier_imt_root: btoa("r".repeat(32)),
    status: "SESSION_STATUS_ACTIVE",
  }));
  return {
    getApiBase: () => "https://chain-b.example",
    getRoundOverview: vi.fn(async () => ({ current_rounds: rounds })),
    getRound: vi.fn(async (id: string) => ({ round: rounds.find((round) => Array.from(atob(round.vote_round_id), (b) => b.charCodeAt(0).toString(16)).join("") === id)! })),
    validatePublishedSnapshotManifest: vi.fn(async () => ({ status: "valid" })),
    getSnapshotStatus: vi.fn(async () => ({ phase: "serving", height: 3459350 })),
    isActiveRoundStatus: (status: string) => status === "SESSION_STATUS_ACTIVE",
    getVoteManagers: vi.fn(async () => ({ vote_manager_addresses: ["sv1test"] })),
    createConfigPrBatch: vi.fn(async () => ({ html_url: "https://github.com/example/config/pull/1" })),
  };
});
vi.mock("../api/cosmosTx", () => ({ createVotingSession: vi.fn() }));
vi.mock("../api/votingKey", () => ({ deriveEd25519FromKeplr: vi.fn(async () => ({})) }));
vi.mock("../hooks/useDetectedChainId", () => ({ useDetectedChainId: vi.fn(), useSelectedChainUrl: vi.fn() }));
vi.mock("../store/uiConfigContext", () => ({ useUIConfig: () => ({ zcashNetwork: "test", precomputedBaseURL: "https://snapshots.example" }) }));
vi.mock("../utils/attestEntry", async (original) => ({
  ...await original<typeof import("../utils/attestEntry")>(),
  buildSignedRoundEntry: vi.fn(async () => ({ entry: { auth_version: 2, ea_pk: btoa("e".repeat(32)), signatures: [] }, signedPayloadHash: "d".repeat(64) })),
  sha256Hex: vi.fn(async () => "f".repeat(64)),
}));

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
let container: HTMLDivElement;
let root: Root;
const signPayload = vi.fn(async () => ({ signature: "signed", pubKey: "public" }));
const writeText = vi.fn(async () => {});
const wallet = { address: "sv1test", chainId: "chain-a", source: "keplr", signer: {}, signPayload, signKeplrPayload: vi.fn() } as unknown as UseWallet;
const drafts: VotingRound[] = [{
  id: "draft", name: "Template", status: "draft", createdAt: "", updatedAt: "",
  proposals: [{ id: "p", title: "Test", description: "", type: "binary", options: [{ id: "yes", label: "Yes" }, { id: "no", label: "No" }], zipNumber: "", forumURL: "", metadata: [] }],
  settings: { snapshotHeight: "3459350", description: "", discussionURL: "", endTime: "", openUntilClosed: false, defaultProposalType: "binary", defaultLabels: ["Yes", "No"] },
}];
beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(useDetectedChainId).mockReturnValue("chain-b");
  vi.mocked(useSelectedChainUrl).mockReturnValue("https://chain-b.example");
  Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});
afterEach(async () => { await act(async () => root.unmount()); container.remove(); });
const button = (label: string) => Array.from(container.querySelectorAll("button")).find((b) => b.textContent?.trim() === label)!;
const checkbox = () => container.querySelector<HTMLInputElement>('input[type="checkbox"]')!;
const submit = () => button("Attest rounds and open PR");

async function prepareExistingBatch(connectedWallet = wallet) {
  await act(async () => root.render(<BatchRoundsPage wallet={connectedWallet} rounds={drafts} />));
  await act(async () => {
    const input = container.querySelector<HTMLInputElement>('input[placeholder="Load test"]')!;
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "Batch");
    input.dispatchEvent(new Event("input", { bubbles: true }));
    const select = container.querySelector("select")!;
    select.value = "draft";
    select.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await act(async () => button("Create 3 rounds").click());
  await act(async () => button("Resume batch").click());
  expect(cosmosTx.createVotingSession).not.toHaveBeenCalled();
  expect(checkbox()).not.toBeNull();
}

it("names the selected server's chain and blocks a cached wallet from another chain", async () => {
  await prepareExistingBatch();
  await act(async () => button("Copy AI prompt").click());
  expect(writeText).toHaveBeenCalledWith(expect.stringContaining("on voting chain chain-b"));
  expect(writeText).not.toHaveBeenCalledWith(expect.stringContaining("on voting chain chain-a"));
  expect(checkbox().disabled).toBe(true);
  expect(submit().disabled).toBe(true);
  expect(container.textContent).toContain("Reconnect the wallet");
  await act(async () => checkbox().click());
  await act(async () => submit().click());
  expect(votingKey.deriveEd25519FromKeplr).not.toHaveBeenCalled();
  expect(chainApi.createConfigPrBatch).not.toHaveBeenCalled();

  await act(async () => root.render(<BatchRoundsPage wallet={{ ...wallet, chainId: "chain-b" }} rounds={drafts} />));
  expect(checkbox().checked).toBe(false);
  expect(checkbox().disabled).toBe(false);
  await act(async () => checkbox().click());
  expect(submit().disabled).toBe(false);
  await act(async () => submit().click());
  expect(chainApi.createConfigPrBatch).toHaveBeenCalledOnce();
  const request = vi.mocked(chainApi.createConfigPrBatch).mock.calls[0][0];
  expect(request.rounds).toHaveLength(3);
  expect(JSON.parse(request.auth.payload).rounds.every((round: { imt_verification: { acknowledged: boolean } }) => round.imt_verification.acknowledged)).toBe(true);
});

it("waits for chain detection and resets acknowledgment if the detected chain changes", async () => {
  vi.mocked(useDetectedChainId).mockReturnValue(null);
  const matchingWallet = { ...wallet, chainId: "chain-b" };
  await prepareExistingBatch(matchingWallet);
  expect(checkbox().disabled).toBe(true);
  expect(submit().disabled).toBe(true);
  vi.mocked(useDetectedChainId).mockReturnValue("chain-b");
  await act(async () => root.render(<BatchRoundsPage wallet={matchingWallet} rounds={drafts} />));
  await act(async () => checkbox().click());
  expect(submit().disabled).toBe(false);
  vi.mocked(useDetectedChainId).mockReturnValue("chain-c");
  await act(async () => root.render(<BatchRoundsPage wallet={matchingWallet} rounds={drafts} />));
  expect(checkbox().checked).toBe(false);
  expect(submit().disabled).toBe(true);
});

it("does not submit an in-flight attestation after the selected server changes", async () => {
  const matchingWallet = { ...wallet, chainId: "chain-b" };
  await prepareExistingBatch(matchingWallet);
  await act(async () => checkbox().click());
  let resolveSignature!: (value: { signature: string; pubKey: string }) => void;
  signPayload.mockImplementationOnce(() => new Promise((resolve) => { resolveSignature = resolve; }));
  await act(async () => submit().click());
  expect(signPayload).toHaveBeenCalledOnce();
  vi.mocked(useSelectedChainUrl).mockReturnValue("https://chain-c.example");
  vi.mocked(useDetectedChainId).mockReturnValue(null);
  await act(async () => root.render(<BatchRoundsPage wallet={matchingWallet} rounds={drafts} />));
  await act(async () => resolveSignature({ signature: "signed", pubKey: "public" }));
  expect(chainApi.createConfigPrBatch).not.toHaveBeenCalled();
  expect(checkbox().checked).toBe(false);
  expect(submit().disabled).toBe(true);
});
