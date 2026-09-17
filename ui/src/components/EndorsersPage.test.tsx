// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { EndorsersPage } from "./EndorsersPage";
import type { UseWallet } from "../hooks/useWallet";
import * as cosmosTx from "../api/cosmosTx";

vi.mock("../api/chain", () => ({
  getApiBase: () => "https://vote.example",
  getEndorsers: vi.fn(async () => ({ endorsers: [{ endorser_id: "zodl", address: "cosmos1test" }] })),
  listRounds: vi.fn(async () => ({ rounds: [{ vote_round_id: btoa("a".repeat(32)), snapshot_height: "3459350", nullifier_imt_root: btoa("b".repeat(32)), status: "SESSION_STATUS_PENDING" }] })),
  getEndorsedRounds: vi.fn(async () => ({ vote_round_ids: [] })),
}));
vi.mock("../api/cosmosTx", () => ({ endorseRound: vi.fn(async () => ({ code: 0 })) }));
vi.mock("../hooks/useDetectedChainId", () => ({ useDetectedChainId: () => "test-chain", useSelectedChainUrl: () => "https://vote.example" }));
vi.mock("../store/uiConfigContext", () => ({ useUIConfig: () => ({ zcashNetwork: "test" }) }));
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
let container: HTMLDivElement;
let root: Root;
const wallet = { address: "cosmos1test", chainId: "test-chain", signer: {} } as UseWallet;
beforeEach(() => {
  vi.clearAllMocks();
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});
afterEach(async () => { await act(async () => root.unmount()); container.remove(); });
const endorse = () => Array.from(container.querySelectorAll("button")).find((b) => b.textContent === "Endorse")!;

it("gates endorsement and expires the submission guard when the wallet changes", async () => {
  await act(async () => root.render(<EndorsersPage wallet={wallet} />));
  expect(endorse().disabled).toBe(true);
  await act(async () => endorse().click());
  expect(cosmosTx.endorseRound).not.toHaveBeenCalled();
  await act(async () => container.querySelector<HTMLInputElement>('input[type="checkbox"]')!.click());
  expect(endorse().disabled).toBe(false);
  await act(async () => endorse().click());
  expect(cosmosTx.endorseRound).toHaveBeenCalledOnce();
  const guard = vi.mocked(cosmosTx.endorseRound).mock.calls[0][4]!;
  expect(() => guard(wallet.address!, "test-chain")).not.toThrow();
  expect(() => guard(wallet.address!, "different-chain")).toThrow("changed");
  await act(async () => root.render(<EndorsersPage wallet={{ ...wallet, address: "cosmos1other" }} />));
  expect(endorse().disabled).toBe(true);
  expect(() => guard(wallet.address!, "test-chain")).toThrow("Acknowledge");
});
