// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { AttestRoundEntryPage } from "./AttestRoundEntryPage";
import * as chainApi from "../api/chain";
import * as votingKey from "../api/votingKey";
import { useWallet } from "../hooks/useWallet";

vi.mock("../api/chain", () => ({
  TOKEN_HOLDER_VOTING_CONFIG_REPO_URL: "https://github.com/example/config",
  tokenHolderConfigUrl: () => "https://github.com/example/config",
  getApiBase: () => "https://vote.example",
  getActiveRounds: vi.fn(async () => ({ rounds: [{ vote_round_id: btoa("a".repeat(32)), ea_pk: btoa("b".repeat(32)), snapshot_height: "3459350", nullifier_imt_root: btoa("c".repeat(32)), status: "SESSION_STATUS_ACTIVE" }] })),
  isActiveRoundStatus: () => true,
  getVoteManagers: vi.fn(async () => ({ vote_manager_addresses: ["cosmos1test"] })),
  createConfigPr: vi.fn(async () => ({ html_url: "https://github.com/example/config/pull/1" })),
}));
vi.mock("../api/votingKey", () => ({
  deriveEd25519FromKeplr: vi.fn(async () => ({ signerId: "test", publicKeyB64: "test", createdAt: "today", sourceAddress: "cosmos1test", chainId: "test-chain" })),
}));
vi.mock("../hooks/useWallet", () => ({ useWallet: vi.fn() }));
vi.mock("../hooks/useDetectedChainId", () => ({ useDetectedChainId: () => "test-chain" }));
vi.mock("../store/uiConfigContext", () => ({ useUIConfig: () => ({ zcashNetwork: "test" }) }));
vi.mock("../utils/attestEntry", async (original) => ({
  ...await original<typeof import("../utils/attestEntry")>(),
  buildSignedRoundEntry: vi.fn(async () => ({ entry: { auth_version: 2, ea_pk: btoa("b".repeat(32)), signatures: [] }, signedPayloadHash: "d".repeat(64) })),
  sha256Hex: vi.fn(async () => "e".repeat(64)),
}));
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
let container: HTMLDivElement;
let root: Root;
const signPayload = vi.fn(async () => ({ signature: "signed", pubKey: "public" }));
beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(useWallet).mockReturnValue({ address: "cosmos1test", chainId: "test-chain", source: "keplr", signPayload, signKeplrPayload: vi.fn() } as unknown as ReturnType<typeof useWallet>);
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});
afterEach(async () => { await act(async () => root.unmount()); container.remove(); });
const button = (label: string) => Array.from(container.querySelectorAll("button")).find((b) => b.textContent?.trim() === label)!;
const checkbox = () => container.querySelector<HTMLInputElement>('input[type="checkbox"]')!;

it("requires acknowledgment for both attestation and PR creation and signs it into the request", async () => {
  await act(async () => root.render(<AttestRoundEntryPage />));
  expect(button("Create Attestation").disabled).toBe(true);
  await act(async () => button("Create Attestation").click());
  expect(votingKey.deriveEd25519FromKeplr).not.toHaveBeenCalled();
  await act(async () => checkbox().click());
  await act(async () => button("Create Attestation").click());
  expect(button("Add Attestation via Pull Request").disabled).toBe(false);
  await act(async () => checkbox().click());
  expect(button("Add Attestation via Pull Request").disabled).toBe(true);
  await act(async () => button("Add Attestation via Pull Request").click());
  expect(chainApi.createConfigPr).not.toHaveBeenCalled();
  await act(async () => checkbox().click());
  await act(async () => button("Add Attestation via Pull Request").click());
  expect(chainApi.createConfigPr).toHaveBeenCalledOnce();
  const request = vi.mocked(chainApi.createConfigPr).mock.calls[0][0];
  expect(JSON.parse(request.auth.payload).imt_verification).toEqual({ acknowledged: true, statement_version: 1 });
  expect(signPayload).toHaveBeenCalledWith(request.auth.payload);
});
