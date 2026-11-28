import { describe, expect, it } from "vitest";
import vectors from "../../../testutil/testdata/proxy_delegation_v1.json";
import { signCanonicalPayload } from "../api/votingKey";
import {
  decodeDelegateKey,
  parseWalletListDraft,
  walletListDocument,
  walletListPayload,
  WALLET_LIST_DOMAIN,
  WalletListError,
  type WalletList,
} from "./walletList";

// The delegate_key, wallet_list_payload and wallet_list_document golden
// vectors of proxy delegation contracts-v1 §13, read from the vote-sdk copy
// of the client file (testutil/testdata/proxy_delegation_v1.json).

/** Lowercase hex of bytes. */
function toHex(bytes: Uint8Array): string {
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
}

/** Bytes of lowercase hex. */
function fromHex(hex: string): Uint8Array {
  return Uint8Array.from(hex.match(/../g) ?? [], (byte) => parseInt(byte, 16));
}

/** Base64 of bytes. */
function toBase64(bytes: Uint8Array): string {
  return btoa(String.fromCharCode(...bytes));
}

type PayloadCase = (typeof vectors.wallet_list_payload)[number];

/** A payload vector's inputs as a list. */
function vectorList(inputs: PayloadCase["inputs"]): WalletList {
  return {
    wallet_id: inputs.wallet_id,
    chain_id: inputs.chain_id,
    version: BigInt(inputs.version),
    issued_at: BigInt(inputs.issued_at),
    reserved_names: inputs.reserved_names,
    delegates: inputs.delegates,
  };
}

/** The contract name of the error fn throws, or null. */
function errorCode(fn: () => unknown): string | null {
  try {
    fn();
    return null;
  } catch (err) {
    if (err instanceof WalletListError) return err.code;
    throw err;
  }
}

describe("WalletListPayloadV1", () => {
  it.each(vectors.wallet_list_payload.map((c) => [c.name, c] as const))("matches the %s vector", async (_, vector) => {
    const list = vectorList(vector.inputs);
    const { expected } = vector;
    if (expected.error !== null) {
      expect(errorCode(() => walletListPayload(list))).toBe(expected.error);
      return;
    }
    const payload = walletListPayload(list);
    expect(toHex(payload)).toBe(expected.payload);

    // The admin UI signs with the Keplr-derived key through this call.
    const seed = fromHex(vector.inputs.signer_secret_seed);
    const sig = await signCanonicalPayload(toBase64(payload), {
      signerId: vector.inputs.signer_key_id,
      seedB64: toBase64(seed),
      publicKeyB64: "",
      createdAt: "",
    });
    expect(toHex(Uint8Array.from(atob(sig), (ch) => ch.charCodeAt(0)))).toBe(expected.signature);
    expect(walletListDocument(list, [{ key_id: vector.inputs.signer_key_id, alg: "ed25519", sig }])).toBe(expected.document);

    const parsed = parseWalletListDraft(expected.document!);
    expect(toHex(walletListPayload(parsed.list))).toBe(expected.payload);
    expect(parsed.signatures).toEqual([{ key_id: vector.inputs.signer_key_id, alg: "ed25519", sig }]);
  });
});

describe("decodeDelegateKey", () => {
  // The list checks the string's form only (contracts-v1 §12.3), so the
  // canonical string of a key that fails the key rule decodes too.
  it.each(vectors.delegate_key.decode.map((c) => [c.name, c] as const))("reads the %s string", (_, vector) => {
    const got = decodeDelegateKey(vector.inputs.text);
    if (vector.expected.dk !== null) {
      expect(toHex(got!)).toBe(vector.expected.dk);
    } else {
      expect(got !== null).toBe(vector.expected.error === "InvalidKey");
    }
  });
});

describe("parseWalletListDraft", () => {
  // The draft parser checks everything but the signer and the pins.
  const signerErrors = new Set(["NoPinnedSignature", "WrongWallet", "WrongChain"]);

  it.each(vectors.wallet_list_document.map((c) => [c.name, c] as const))("reads the %s document", (_, vector) => {
    const want = vector.expected.error;
    const got = errorCode(() => parseWalletListDraft(vector.inputs.document));
    expect(got).toBe(want === null || signerErrors.has(want) ? null : want);
  });

  it("reads an unsigned draft and writes it without signatures", () => {
    const document = vectors.wallet_list_payload[0].expected.document!;
    const { list } = parseWalletListDraft(document);
    const draft = walletListDocument(list, []);
    expect(draft).not.toContain('"signatures"');
    expect(parseWalletListDraft(draft).signatures).toEqual([]);
  });

  it("refuses nulls and indexes over 32 bits", () => {
    const document = vectors.wallet_list_payload[0].expected.document!;
    for (const edited of [
      document.replace('"handle": "alice"', '"handle": null'),
      document.replace('"index": 17', '"index": 4294967296'),
      document.replace(/"signatures": \[[\s\S]*\]/, '"signatures": null'),
    ]) {
      expect(edited).not.toBe(document);
      expect(errorCode(() => parseWalletListDraft(edited))).toBe("Malformed");
    }
  });

  it("refuses a key repeated at any level, compared unescaped", () => {
    const document = vectors.wallet_list_payload[0].expected.document!;
    for (const edited of [
      document.replace('"version": 1', '"version": 1, "version": 2'),
      document.replace('"name": "Alice Example"', '"name": "Alice Example", "name": "Mallory"'),
      document.replace('"name": "Alice Example"', '"name": "Alice Example", "n\\u0061me": "Mallory"'),
    ]) {
      expect(edited).not.toBe(document);
      expect(errorCode(() => parseWalletListDraft(edited))).toBe("Malformed");
    }
    // The same key in two objects is not repeated.
    expect(errorCode(() => parseWalletListDraft(document))).toBeNull();
  });

  // Each case edits the vizor_list document's version; expected is the
  // payload's u64le version field, or the parser's error.
  const VERSION_OFFSET = WALLET_LIST_DOMAIN.length + 1 + "vizor".length + 1 + "zvote-1".length;
  it.each([
    { name: "2^53 + 1", text: "9007199254740993", expected: "0100000000002000" },
    { name: "u64 max", text: "18446744073709551615", expected: "ffffffffffffffff" },
    { name: "2^64", text: "18446744073709551616", expected: "Malformed" },
    { name: "a fraction", text: "1.0", expected: "Malformed" },
    { name: "an exponent", text: "1e3", expected: "Malformed" },
    { name: "negative", text: "-1", expected: "Malformed" },
  ])("reads a version of $name exactly", ({ text, expected }) => {
    const document = vectors.wallet_list_payload[0].expected.document!.replace('"version": 1,', `"version": ${text},`);
    if (expected === "Malformed") {
      expect(errorCode(() => parseWalletListDraft(document))).toBe("Malformed");
      return;
    }
    const { list, signatures } = parseWalletListDraft(document);
    expect(list.version).toBe(BigInt(text));
    expect(toHex(walletListPayload(list).subarray(VERSION_OFFSET, VERSION_OFFSET + 8))).toBe(expected);
    expect(walletListDocument(list, signatures)).toBe(document);
  });

  it("refuses a document over 1 MiB", () => {
    const document = vectors.wallet_list_payload[0].expected.document!;
    const padding = 1024 * 1024 - new TextEncoder().encode(document).length;
    expect(errorCode(() => parseWalletListDraft(document + " ".repeat(padding)))).toBeNull();
    expect(errorCode(() => parseWalletListDraft(document + " ".repeat(padding + 1)))).toBe("TooLarge");
  });
});
