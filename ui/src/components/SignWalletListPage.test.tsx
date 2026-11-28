// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { ed25519 } from "@noble/curves/ed25519.js";
import vectors from "../../../testutil/testdata/proxy_delegation_v1.json";
import { SignWalletListPage } from "./SignWalletListPage";
import * as votingKey from "../api/votingKey";
import { useWallet } from "../hooks/useWallet";
import { useDetectedChainId } from "../hooks/useDetectedChainId";
import { base64ToBytes, bytesToBase64 } from "../utils/attestEntry";
import { parseWalletListDraft, walletListPayload } from "../utils/walletList";

// The vizor_list vector: Vizor's two-row list for zvote-1, signed by the
// deterministic "valargroup" test key.
const VECTOR = vectors.wallet_list_payload[0];
const DOCUMENT = VECTOR.expected.document!;
const LIST = parseWalletListDraft(DOCUMENT).list;
const SEED = Uint8Array.from(VECTOR.inputs.signer_secret_seed.match(/../g)!, (byte) => parseInt(byte, 16));

vi.mock("../api/chain", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api/chain")>();
  return {
    ...actual,
    getApiBase: () => "https://vote.example",
  };
});
vi.mock("../api/votingKey", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../api/votingKey")>();
  return { ...actual, deriveEd25519FromKeplr: vi.fn(async () => actual.importSeed(bytesToBase64(SEED), "keplr:sv1test")) };
});
vi.mock("../hooks/useWallet", () => ({ useWallet: vi.fn() }));
vi.mock("../hooks/useDetectedChainId", () => ({ useDetectedChainId: vi.fn() }));

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });
let container: HTMLDivElement;
let root: Root;

/** Use a Keplr wallet on chainId. */
function useKeplrOn(chainId: string) {
  vi.mocked(useWallet).mockReturnValue({ address: "sv1test", chainId, source: "keplr", signKeplrPayload: vi.fn() } as unknown as ReturnType<typeof useWallet>);
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(useDetectedChainId).mockReturnValue("zvote-1");
  useKeplrOn("zvote-1");
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});
afterEach(async () => { await act(async () => root.unmount()); container.remove(); });

/** The button labelled label, if rendered. */
const button = (label: string) => Array.from(container.querySelectorAll("button")).find((b) => b.textContent?.trim() === label);
/** Let file reads, hashing and async handlers finish. */
const settle = () => new Promise((resolve) => setTimeout(resolve, 50));

/** Render the page and pick text as the list file. */
async function renderWithList(text: string) {
  await act(async () => root.render(<SignWalletListPage />));
  const input = container.querySelector<HTMLInputElement>("#wallet-list-file")!;
  Object.defineProperty(input, "files", { value: [new File([text], "list.json", { type: "application/json" })], configurable: true });
  await act(async () => {
    input.dispatchEvent(new Event("change", { bubbles: true }));
    await settle();
  });
}

it("signs Vizor's list with the pinned key id", async () => {
  await renderWithList(DOCUMENT);
  expect(container.textContent).toContain("wallet vizor, chain zvote-1, version 1, 2 delegates");
  expect(container.textContent).toContain("prod/wallets/vizor/list.json");
  expect(container.querySelector<HTMLInputElement>("#wallet-list-key-id")!.value).toBe("valargroup");

  await act(async () => {
    button("Sign List")!.click();
    await settle();
  });
  expect(votingKey.deriveEd25519FromKeplr).toHaveBeenCalledOnce();
  const document = container.querySelector("pre")!.textContent!;
  const { list, signatures } = parseWalletListDraft(document);
  expect(list.issued_at).toBeGreaterThan(LIST.issued_at);
  expect({ ...list, issued_at: LIST.issued_at }).toEqual(LIST);
  expect(signatures).toHaveLength(1);
  expect(signatures[0].key_id).toBe("valargroup");
  const publicKey = ed25519.getPublicKey(SEED);
  expect(ed25519.verify(base64ToBytes(signatures[0].sig), walletListPayload(list), publicKey)).toBe(true);
});

it("shows every signed field before signing", async () => {
  await renderWithList(DOCUMENT);
  const text = container.textContent!;
  for (const name of LIST.reserved_names) expect(text).toContain(name);
  const [alice, bob] = LIST.delegates;
  expect(text).toContain(alice.dk);
  expect(text).toContain(bob.dk);
  expect(text).toContain(alice.name);
  expect(text).toContain(bob.name);
  expect(text).toContain("issued_at is set to the signing time");
});

it("defaults to the stage key id and refuses a list for another chain", async () => {
  vi.mocked(useDetectedChainId).mockReturnValue("svote-1");
  useKeplrOn("svote-1");
  await renderWithList(DOCUMENT);
  expect(container.querySelector<HTMLInputElement>("#wallet-list-key-id")!.value).toBe("valar-test-keplr-derived");
  expect(container.textContent).toContain("The list names chain zvote-1, but the selected voting server is on svote-1.");
  expect(button("Sign List")!.disabled).toBe(true);
});

it("shows the first rule a list breaks", async () => {
  await renderWithList(DOCUMENT.replace('"version": 1', '"version": 0'));
  expect(container.textContent).toContain("InvalidVersion");
  expect(button("Sign List")!.disabled).toBe(true);
});

it("blocks signing while the wallet is on another chain", async () => {
  useKeplrOn("svote-1");
  await renderWithList(DOCUMENT);
  expect(container.textContent).toContain("Reconnect the wallet before signing.");
  expect(button("Sign List")!.disabled).toBe(true);
});

it("refuses a key id the list format can't carry", async () => {
  await renderWithList(DOCUMENT);
  const input = container.querySelector<HTMLInputElement>("#wallet-list-key-id")!;
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, "k".repeat(65));
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
  expect(container.textContent).toContain("At most 64 bytes.");
  expect(button("Sign List")!.disabled).toBe(true);
  expect(votingKey.deriveEd25519FromKeplr).not.toHaveBeenCalled();
});
