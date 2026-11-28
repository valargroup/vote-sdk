// A wallet's signed list of curated delegates (proxy delegation
// contracts-v1 §12.3). Every signature covers WalletListPayloadV1:
//
//   "zcash-shielded-vote:wallet-list:v1" || lp8(wallet_id) || lp8(chain_id)
//   || u64le(version) || u64le(issued_at) || u16le(r) || r × lp8(reserved_name)
//   || u16le(n) || n × row
//   row = u32le(index) || u32le(provider) || lp8(handle) || dk(32)
//         || lp8(utf8(name)) || lp16(utf8(statement)) || lp16(picture)
//
// Config CI verifies the result with the Go code in
// internal/votingconfig/wallet_list.go; both follow the contract's rule order.

import { bech32m } from "bech32";
import { bytesToBase64 } from "./attestEntry";

export const WALLET_LIST_FORMAT = "zcash-vote-wallet-list/v1";
export const WALLET_LIST_DOMAIN = "zcash-shielded-vote:wallet-list:v1";
/** The largest document a wallet reads, in UTF-8 bytes. */
export const MAX_WALLET_LIST_BYTES = 4 * 1024 * 1024;

export interface WalletListRow {
  index: number;
  provider: number;
  handle: string;
  /** The reviewed key, zvdk1…. */
  dk: string;
  name: string;
  statement?: string;
  /** The WebP bytes. */
  picture?: Uint8Array;
}

export interface WalletList {
  wallet_id: string;
  chain_id: string;
  version: bigint;
  issued_at: bigint;
  reserved_names: string[];
  delegates: WalletListRow[];
}

export interface WalletListSignature {
  key_id: string;
  alg: "ed25519";
  sig: string;
}

/** A list's first problem, named as the contract names it (code). */
export class WalletListError extends Error {
  readonly code: string;

  constructor(code: string, detail: string) {
    super(`${code}: ${detail}`);
    this.code = code;
  }
}

/** Whether name is a reserved name: 1 to 64 lowercase ASCII letters, digits, _ or -. */
function isReservedName(name: string): boolean {
  return /^[a-z0-9_-]{1,64}$/.test(name);
}

/** Whether handle is the canonical form of an account name at provider (contracts-v1 §2.5). */
function isDelegateHandle(provider: number, handle: string): boolean {
  if (provider === 1) return /^[a-z0-9_]{1,15}$/.test(handle);
  return provider === 2 && /^[a-z0-9-]{1,39}$/.test(handle) && !handle.startsWith("-") && !handle.endsWith("-");
}

/** A delegate key's 32 bytes from its canonical zvdk1 form (contracts-v1 §11.2), or null. */
export function decodeDelegateKey(s: string): Uint8Array | null {
  const decoded = bech32m.decodeUnsafe(s);
  const bytes = decoded?.prefix === "zvdk" ? bech32m.fromWordsUnsafe(decoded.words) : undefined;
  if (!bytes || bytes.length !== 32) return null;
  // Only the one lowercase string with zero padding bits is canonical.
  return bech32m.encode("zvdk", bech32m.toWords(bytes)) === s ? Uint8Array.from(bytes) : null;
}

/** Whether id is a wallet_id: a lowercase letter, then up to 31 of a-z, 0-9 or -. */
export function isWalletId(id: string): boolean {
  return /^[a-z][a-z0-9-]{0,31}$/.test(id);
}

/** Whether id is a signature's key_id: 1 to 64 bytes of UTF-8. */
export function isSignatureKeyId(id: string): boolean {
  const size = new TextEncoder().encode(id).length;
  return size >= 1 && size <= 64;
}

/** The name and statement rule: 1 to max scalar values in NFC, no Cc but an allowed line feed, no bidi formatting. */
function isDisplayText(s: string, max: number, allowNewline: boolean): boolean {
  const chars = [...s];
  if (chars.length < 1 || chars.length > max || s.normalize("NFC") !== s) return false;
  return chars.every((ch) => {
    const cp = ch.codePointAt(0)!;
    if (cp >= 0xd800 && cp <= 0xdfff) return false; // a lone surrogate is not a scalar value
    if (cp === 0x061c || cp === 0x200e || cp === 0x200f) return false;
    if ((cp >= 0x202a && cp <= 0x202e) || (cp >= 0x2066 && cp <= 0x2069)) return false;
    if (cp <= 0x1f || (cp >= 0x7f && cp <= 0x9f)) return allowNewline && cp === 0x0a;
    return true;
  });
}

/** Throw the first field rule the list breaks, in the contract's table order. */
function checkFields(list: WalletList): void {
  const fail = (code: string, detail: string): never => {
    throw new WalletListError(code, detail);
  };
  if (!isWalletId(list.wallet_id)) fail("InvalidWalletId", `wallet_id ${JSON.stringify(list.wallet_id)}`);
  if (!/^[\x21-\x7e]{1,64}$/.test(list.chain_id)) fail("InvalidChainId", `chain_id ${JSON.stringify(list.chain_id)}`);
  if (list.version < 1n) fail("InvalidVersion", "version must be at least 1");
  if (list.issued_at < 1n) fail("InvalidIssuedAt", "issued_at must be at least 1");
  if (list.reserved_names.length > 1000) fail("TooManyReservedNames", `${list.reserved_names.length} reserved names`);
  const names = new Set<string>();
  list.reserved_names.forEach((name, i) => {
    if (!isReservedName(name)) fail("InvalidReservedName", `reserved_names[${i}]`);
    if (names.has(name)) fail("RepeatedReservedName", `reserved_names[${i}]`);
    names.add(name);
  });
  if (list.delegates.length > 1000) fail("TooManyDelegates", `${list.delegates.length} rows`);
  let previous = 0;
  list.delegates.forEach((row, i) => {
    const at = `delegates[${i}] (index ${row.index})`;
    if (row.index < 1) fail("InvalidIndex", at);
    if (row.index <= previous) fail("IndexNotAscending", at);
    previous = row.index;
    if (row.provider !== 1 && row.provider !== 2) fail("InvalidProvider", at);
    if (!isDelegateHandle(row.provider, row.handle)) fail("InvalidHandle", at);
    if (!decodeDelegateKey(row.dk)) fail("InvalidKey", `${at} ${JSON.stringify(row.dk)}`);
    if (!isDisplayText(row.name, 50, false)) fail("InvalidName", at);
    if (row.statement !== undefined && !isDisplayText(row.statement, 280, true)) fail("InvalidStatement", at);
    const p = row.picture;
    if (p !== undefined) {
      if (p.length > 12288) fail("PictureTooLarge", `${at} picture is ${p.length} bytes`);
      const tag = (from: number) => String.fromCharCode(...p.subarray(from, from + 4));
      const riffSize = p.length >= 8 ? new DataView(p.buffer, p.byteOffset, p.byteLength).getUint32(4, true) : -1;
      if (p.length < 12 || tag(0) !== "RIFF" || tag(8) !== "WEBP" || riffSize + 8 !== p.length) {
        fail("InvalidPicture", `${at} picture is not a WebP file`);
      }
    }
  });
}

/** Check the field rules and build the list's WalletListPayloadV1. */
export function walletListPayload(list: WalletList): Uint8Array {
  checkFields(list);
  const utf8 = (s: string) => new TextEncoder().encode(s);
  const parts: number[] = [...utf8(WALLET_LIST_DOMAIN)];
  const uint = (value: number | bigint, bytes: number) => {
    let v = BigInt(value);
    for (let i = 0; i < bytes; i += 1) {
      parts.push(Number(v & 0xffn));
      v >>= 8n;
    }
  };
  const lp8 = (b: Uint8Array) => {
    parts.push(b.length);
    parts.push(...b);
  };
  const lp16 = (b: Uint8Array) => {
    uint(b.length, 2);
    for (const byte of b) parts.push(byte);
  };
  lp8(utf8(list.wallet_id));
  lp8(utf8(list.chain_id));
  uint(list.version, 8);
  uint(list.issued_at, 8);
  uint(list.reserved_names.length, 2);
  list.reserved_names.forEach((name) => lp8(utf8(name)));
  uint(list.delegates.length, 2);
  for (const row of list.delegates) {
    uint(row.index, 4);
    uint(row.provider, 4);
    lp8(utf8(row.handle));
    parts.push(...decodeDelegateKey(row.dk)!);
    lp8(utf8(row.name));
    lp16(utf8(row.statement ?? ""));
    lp16(row.picture ?? new Uint8Array());
  }
  return new Uint8Array(parts);
}

/** Decode canonical base64, or null when s is not the one encoding of its bytes. */
function fromCanonicalBase64(s: string): Uint8Array | null {
  let binary: string;
  try {
    binary = atob(s);
  } catch {
    return null;
  }
  const bytes = Uint8Array.from(binary, (ch) => ch.charCodeAt(0));
  return bytesToBase64(bytes) === s ? bytes : null;
}

/**
 * Scan text, which must be valid JSON. repeated is the first key repeated
 * within one object, with that object's depth (1 for the top level), or
 * null; keys compare unescaped, as JSON.parse reads them, and JSON.parse
 * keeps only the last value of a repeated key. numbers maps each top-level
 * key whose value is a number to that number's text, which JSON.parse
 * rounds above 2^53.
 */
function scanJSON(text: string): { repeated: { key: string; depth: number } | null; numbers: Map<string, string> } {
  // One set of keys per open object, null per open array.
  const open: (Set<string> | null)[] = [];
  const numbers = new Map<string, string>();
  let expectKey = false;
  for (let i = 0; i < text.length; i += 1) {
    const ch = text[i];
    if (ch === '"') {
      let end = i + 1;
      while (text[end] !== '"') end += text[end] === "\\" ? 2 : 1;
      if (expectKey) {
        const keys = open[open.length - 1]!;
        const key = JSON.parse(text.slice(i, end + 1)) as string;
        if (keys.has(key)) return { repeated: { key, depth: open.length }, numbers };
        keys.add(key);
        expectKey = false;
        if (open.length === 1) {
          const number = /\s*:\s*(-?[0-9][0-9.eE+-]*)/y;
          number.lastIndex = end + 1;
          const match = number.exec(text);
          if (match) numbers.set(key, match[1]);
        }
      }
      i = end;
    } else if (ch === "{") {
      open.push(new Set());
      expectKey = true;
    } else if (ch === "[") {
      open.push(null);
    } else if (ch === "}" || ch === "]") {
      open.pop();
    } else if (ch === ",") {
      expectKey = open[open.length - 1] instanceof Set;
    }
  }
  return { repeated: null, numbers };
}

/** Whether a parsed JSON value holds null at any depth. */
function hasNull(v: unknown): boolean {
  if (v === null) return true;
  if (typeof v !== "object") return false;
  return Object.values(v).some(hasNull);
}

/**
 * Read a list file as signing tooling does, in the contract's verification
 * order up to the field rules: the size, the format, the members and their
 * types, then the field rules. "signatures" may be absent or empty. Unknown
 * members, repeated keys and nulls are refused, since re-signing would drop
 * them or keep a value the signer may not have read.
 */
export function parseWalletListDraft(text: string): { list: WalletList; signatures: WalletListSignature[] } {
  const malformed = (detail: string): never => {
    throw new WalletListError("Malformed", detail);
  };
  const size = new TextEncoder().encode(text).length;
  if (size > MAX_WALLET_LIST_BYTES) throw new WalletListError("TooLarge", `${size} bytes`);
  let doc: unknown;
  try {
    doc = JSON.parse(text);
  } catch {
    return malformed("not JSON");
  }
  const isObject = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);
  if (!isObject(doc)) return malformed("the top level is not an object");
  const { repeated, numbers } = scanJSON(text);
  if (repeated?.depth === 1) return malformed(`repeated key ${JSON.stringify(repeated.key)}`);
  if (typeof doc.format !== "string") return malformed("no format string");
  if (doc.format !== WALLET_LIST_FORMAT) throw new WalletListError("UnsupportedFormat", `format ${JSON.stringify(doc.format)}`);
  if (repeated) return malformed(`repeated key ${JSON.stringify(repeated.key)}`);
  if (hasNull(doc)) return malformed("null member");

  const members = (obj: Record<string, unknown>, where: string, required: string[], optional: string[]) => {
    for (const key of Object.keys(obj)) {
      if (!required.includes(key) && !optional.includes(key)) malformed(`${where} has unknown member ${JSON.stringify(key)}`);
    }
    for (const key of required) if (!(key in obj)) malformed(`${where} has no ${key}`);
  };
  const str = (v: unknown, where: string) => (typeof v === "string" ? v : malformed(`${where} is not a string`));
  const int = (v: unknown, where: string) =>
    typeof v === "number" && Number.isSafeInteger(v) && v >= 0 ? v : malformed(`${where} is not a whole number`);
  // Read from the number's text, so every u64 is exact.
  const u64 = (key: string) => {
    const raw = numbers.get(key);
    return typeof doc[key] === "number" && raw !== undefined && /^(0|[1-9][0-9]*)$/.test(raw) && BigInt(raw) <= 0xffffffffffffffffn
      ? BigInt(raw)
      : malformed(`${key} is not a 64-bit whole number`);
  };
  const u32 = (v: unknown, where: string) => {
    const n = int(v, where);
    return n <= 0xffffffff ? n : malformed(`${where} does not fit 32 bits`);
  };
  const strings = (v: unknown, where: string) =>
    Array.isArray(v) ? v.map((item, i) => str(item, `${where}[${i}]`)) : malformed(`${where} is not an array`);

  members(doc, "the list", ["format", "wallet_id", "chain_id", "version", "issued_at", "reserved_names", "delegates"], ["signatures"]);
  if (!Array.isArray(doc.delegates)) return malformed("delegates is not an array");
  const delegates = doc.delegates.map((raw, i): WalletListRow => {
    const where = `delegates[${i}]`;
    if (!isObject(raw)) return malformed(`${where} is not an object`);
    members(raw, where, ["index", "provider", "handle", "dk", "name"], ["statement", "picture"]);
    const row: WalletListRow = {
      index: u32(raw.index, `${where}.index`),
      provider: u32(raw.provider, `${where}.provider`),
      handle: str(raw.handle, `${where}.handle`),
      dk: str(raw.dk, `${where}.dk`),
      name: str(raw.name, `${where}.name`),
    };
    if ("statement" in raw) row.statement = str(raw.statement, `${where}.statement`);
    if ("picture" in raw) {
      row.picture = fromCanonicalBase64(str(raw.picture, `${where}.picture`)) ?? malformed(`${where}.picture is not canonical base64`);
    }
    return row;
  });
  const sigs = "signatures" in doc ? doc.signatures : [];
  if (!Array.isArray(sigs) || sigs.length > 8) return malformed("signatures must be an array of at most 8");
  const signatures = sigs.map((raw, i): WalletListSignature => {
    const where = `signatures[${i}]`;
    if (!isObject(raw)) return malformed(`${where} is not an object`);
    members(raw, where, ["key_id", "alg", "sig"], []);
    const keyId = str(raw.key_id, `${where}.key_id`);
    const sig = str(raw.sig, `${where}.sig`);
    if (!isSignatureKeyId(keyId) || raw.alg !== "ed25519" || fromCanonicalBase64(sig)?.length !== 64) {
      malformed(`${where} needs a key_id of 1 to 64 bytes, alg ed25519 and a 64-byte sig`);
    }
    return { key_id: keyId, alg: "ed25519", sig };
  });
  const list: WalletList = {
    wallet_id: str(doc.wallet_id, "wallet_id"),
    chain_id: str(doc.chain_id, "chain_id"),
    version: u64("version"),
    issued_at: u64("issued_at"),
    reserved_names: strings(doc.reserved_names, "reserved_names"),
    delegates,
  };
  checkFields(list);
  return { list, signatures };
}

/**
 * The published document: members in the contract's order, two-space
 * indented, with a trailing newline, as voting-config writes it.
 */
export function walletListDocument(list: WalletList, signatures: WalletListSignature[]): string {
  const doc = {
    format: WALLET_LIST_FORMAT,
    wallet_id: list.wallet_id,
    chain_id: list.chain_id,
    // Placeholders for the u64s, which JSON.stringify can't write exactly.
    version: 0,
    issued_at: 0,
    reserved_names: list.reserved_names,
    delegates: list.delegates.map((row) => ({
      index: row.index,
      provider: row.provider,
      handle: row.handle,
      dk: row.dk,
      name: row.name,
      ...(row.statement !== undefined ? { statement: row.statement } : {}),
      ...(row.picture !== undefined ? { picture: bytesToBase64(row.picture) } : {}),
    })),
    ...(signatures.length > 0 ? { signatures } : {}),
  };
  // Only top-level members sit at a two-space indent, and string values
  // escape their line feeds, so this line pair occurs once.
  const placeholders = '\n  "version": 0,\n  "issued_at": 0,\n';
  return `${JSON.stringify(doc, null, 2).replace(placeholders, `\n  "version": ${list.version},\n  "issued_at": ${list.issued_at},\n`)}\n`;
}
