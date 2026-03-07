// Client-side Cosmos SDK transaction signing and REST broadcasting.
//
// MsgSetVoteManager and MsgCreateVotingSession are standard Cosmos SDK
// transactions. Instead of relying on a server-side handler, we sign them
// directly in the browser using cosmjs and broadcast via the chain's REST
// API (/cosmos/tx/v1beta1/txs).

import type { OfflineDirectSigner } from "@cosmjs/proto-signing";
import {
  Registry,
  makeSignDoc,
  makeAuthInfoBytes,
  encodePubkey,
} from "@cosmjs/proto-signing";
import { encodeSecp256k1Pubkey } from "@cosmjs/amino";
import { toBase64, fromBase64 } from "@cosmjs/encoding";
import { TxRaw } from "cosmjs-types/cosmos/tx/v1beta1/tx";
import { SignMode } from "cosmjs-types/cosmos/tx/signing/v1beta1/signing";
import { sha256 } from "@noble/hashes/sha2.js";
import type { BroadcastResult } from "./chain";

// All transactions are fee-exempt on this chain. Setting gas to "0" means
// Keplr computes fee = gasPrice × 0 = 0, so the user sees a zero fee.
const DEFAULT_GAS = "0";


// ── Protobuf mini-writer ────────────────────────────────────────

// Minimal protobuf Writer that produces valid wire-format bytes.
// Supports varint, length-delimited (string/bytes), and embedded messages.
class ProtoWriter {
  private parts: Uint8Array[] = [];

  static create(): ProtoWriter {
    return new ProtoWriter();
  }

  /** Write a varint (tags, uint32 values, lengths). */
  uint32(value: number): this {
    this.writeVarint(value >>> 0);
    return this;
  }

  /** Write a varint for uint64 values (safe up to Number.MAX_SAFE_INTEGER). */
  uint64(value: number): this {
    this.writeVarint(value);
    return this;
  }

  /** Write a length-prefixed UTF-8 string. */
  string(value: string): this {
    const encoded = new TextEncoder().encode(value);
    this.writeVarint(encoded.length);
    this.parts.push(encoded);
    return this;
  }

  /** Write length-prefixed raw bytes. */
  bytes(value: Uint8Array): this {
    this.writeVarint(value.length);
    this.parts.push(new Uint8Array(value));
    return this;
  }

  /** Encode a sub-message as a length-delimited field. */
  sub(fieldNumber: number, subWriter: ProtoWriter): this {
    const subBytes = subWriter.finish();
    this.uint32((fieldNumber << 3) | 2);
    this.bytes(subBytes);
    return this;
  }

  finish(): Uint8Array {
    let totalLength = 0;
    for (const p of this.parts) totalLength += p.length;
    const result = new Uint8Array(totalLength);
    let offset = 0;
    for (const p of this.parts) {
      result.set(p, offset);
      offset += p.length;
    }
    return result;
  }

  // Uses Math.floor so values > 2^32 (e.g. Unix timestamps) encode correctly.
  private writeVarint(value: number) {
    const buf: number[] = [];
    let v = value;
    while (v > 0x7f) {
      buf.push((v & 0x7f) | 0x80);
      v = Math.floor(v / 128);
    }
    buf.push(v & 0x7f);
    this.parts.push(new Uint8Array(buf));
  }
}

// ── Protobuf type: MsgSetVoteManager ────────────────────────────

// message MsgSetVoteManager { string creator = 1; string new_manager = 2; }
const MsgSetVoteManagerProto = {
  encode(
    message: { creator: string; newManager: string },
    writer: ProtoWriter = ProtoWriter.create(),
  ): ProtoWriter {
    if (message.creator !== "") writer.uint32(10).string(message.creator);
    if (message.newManager !== "") writer.uint32(18).string(message.newManager);
    return writer;
  },
  decode(): { creator: string; newManager: string } {
    throw new Error("decode not implemented");
  },
  fromPartial(
    object: Partial<{ creator: string; newManager: string }>,
  ): { creator: string; newManager: string } {
    return { creator: object.creator ?? "", newManager: object.newManager ?? "" };
  },
};

// ── Protobuf type: MsgCreateVotingSession ───────────────────────

// message VoteOption { uint32 index = 1; string label = 2; }
function encodeVoteOption(opt: { index: number; label: string }): ProtoWriter {
  const w = ProtoWriter.create();
  if (opt.index !== 0) w.uint32(8).uint32(opt.index);   // field 1, wire 0
  if (opt.label !== "") w.uint32(18).string(opt.label);  // field 2, wire 2
  return w;
}

// message Proposal { uint32 id = 1; string title = 2; string description = 3; repeated VoteOption options = 4; }
function encodeProposal(p: {
  id: number;
  title: string;
  description: string;
  options: Array<{ index: number; label: string }>;
}): ProtoWriter {
  const w = ProtoWriter.create();
  if (p.id !== 0) w.uint32(8).uint32(p.id);                // field 1, wire 0
  if (p.title !== "") w.uint32(18).string(p.title);         // field 2, wire 2
  if (p.description !== "") w.uint32(26).string(p.description); // field 3, wire 2
  for (const opt of p.options) {
    w.sub(4, encodeVoteOption(opt));                         // field 4, wire 2
  }
  return w;
}

export interface CreateVotingSessionValue {
  creator: string;
  snapshotHeight: number;
  snapshotBlockhash: Uint8Array;
  proposalsHash: Uint8Array;
  voteEndTime: number;
  nullifierImtRoot: Uint8Array;
  ncRoot: Uint8Array;
  vkZkp1: Uint8Array;
  vkZkp2: Uint8Array;
  vkZkp3: Uint8Array;
  proposals: Array<{
    id: number;
    title: string;
    description: string;
    options: Array<{ index: number; label: string }>;
  }>;
  description: string;
  title: string;
}

// message MsgCreateVotingSession { ... } — see sdk/proto/svote/v1/tx.proto
const MsgCreateVotingSessionProto = {
  encode(
    m: CreateVotingSessionValue,
    writer: ProtoWriter = ProtoWriter.create(),
  ): ProtoWriter {
    if (m.creator !== "")              writer.uint32(10).string(m.creator);              // 1 string
    if (m.snapshotHeight !== 0)        writer.uint32(16).uint64(m.snapshotHeight);       // 2 uint64
    if (m.snapshotBlockhash.length)    writer.uint32(26).bytes(m.snapshotBlockhash);     // 3 bytes
    if (m.proposalsHash.length)        writer.uint32(34).bytes(m.proposalsHash);         // 4 bytes
    if (m.voteEndTime !== 0)           writer.uint32(40).uint64(m.voteEndTime);          // 5 uint64
    if (m.nullifierImtRoot.length)     writer.uint32(50).bytes(m.nullifierImtRoot);      // 6 bytes
    if (m.ncRoot.length)               writer.uint32(58).bytes(m.ncRoot);                // 7 bytes
    if (m.vkZkp1.length)              writer.uint32(66).bytes(m.vkZkp1);                // 8 bytes
    if (m.vkZkp2.length)              writer.uint32(74).bytes(m.vkZkp2);                // 9 bytes
    if (m.vkZkp3.length)              writer.uint32(82).bytes(m.vkZkp3);                // 10 bytes
    for (const p of m.proposals) {
      writer.sub(11, encodeProposal(p));                                                 // 11 repeated
    }
    if (m.description !== "")          writer.uint32(98).string(m.description);          // 12 string
    if (m.title !== "")                writer.uint32(106).string(m.title);               // 13 string
    return writer;
  },
  decode(): CreateVotingSessionValue {
    throw new Error("decode not implemented");
  },
  fromPartial(object: Partial<CreateVotingSessionValue>): CreateVotingSessionValue {
    return {
      creator: object.creator ?? "",
      snapshotHeight: object.snapshotHeight ?? 0,
      snapshotBlockhash: object.snapshotBlockhash ?? new Uint8Array(),
      proposalsHash: object.proposalsHash ?? new Uint8Array(),
      voteEndTime: object.voteEndTime ?? 0,
      nullifierImtRoot: object.nullifierImtRoot ?? new Uint8Array(),
      ncRoot: object.ncRoot ?? new Uint8Array(),
      vkZkp1: object.vkZkp1 ?? new Uint8Array(),
      vkZkp2: object.vkZkp2 ?? new Uint8Array(),
      vkZkp3: object.vkZkp3 ?? new Uint8Array(),
      proposals: object.proposals ?? [],
      description: object.description ?? "",
      title: object.title ?? "",
    };
  },
};

// ── Protobuf type: MsgUnjail (cosmos.slashing.v1beta1) ──────────

// message MsgUnjail { string validator_addr = 1; }
const MsgUnjailProto = {
  encode(
    message: { validatorAddr: string },
    writer: ProtoWriter = ProtoWriter.create(),
  ): ProtoWriter {
    if (message.validatorAddr !== "") writer.uint32(10).string(message.validatorAddr);
    return writer;
  },
  decode(): { validatorAddr: string } {
    throw new Error("decode not implemented");
  },
  fromPartial(
    object: Partial<{ validatorAddr: string }>,
  ): { validatorAddr: string } {
    return { validatorAddr: object.validatorAddr ?? "" };
  },
};

// ── Protobuf type: MsgSend (cosmos.bank.v1beta1) ────────────────

// message Coin { string denom = 1; string amount = 2; }
// message MsgSend { string from_address = 1; string to_address = 2; repeated Coin amount = 3; }
const MsgSendProto = {
  encode(
    message: { fromAddress: string; toAddress: string; amount: Array<{ denom: string; amount: string }> },
    writer: ProtoWriter = ProtoWriter.create(),
  ): ProtoWriter {
    if (message.fromAddress !== "") writer.uint32(10).string(message.fromAddress); // field 1
    if (message.toAddress !== "")   writer.uint32(18).string(message.toAddress);   // field 2
    for (const coin of message.amount) {
      const coinWriter = ProtoWriter.create();
      if (coin.denom !== "")  coinWriter.uint32(10).string(coin.denom);  // Coin field 1
      if (coin.amount !== "") coinWriter.uint32(18).string(coin.amount); // Coin field 2
      writer.sub(3, coinWriter);                                          // field 3, repeated
    }
    return writer;
  },
  decode(): { fromAddress: string; toAddress: string; amount: Array<{ denom: string; amount: string }> } {
    throw new Error("decode not implemented");
  },
  fromPartial(
    object: Partial<{ fromAddress: string; toAddress: string; amount: Array<{ denom: string; amount: string }> }>,
  ): { fromAddress: string; toAddress: string; amount: Array<{ denom: string; amount: string }> } {
    return {
      fromAddress: object.fromAddress ?? "",
      toAddress: object.toAddress ?? "",
      amount: object.amount ?? [],
    };
  },
};

// ── Registry ────────────────────────────────────────────────────

function createRegistry(): Registry {
  const registry = new Registry();
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  registry.register("/svote.v1.MsgSetVoteManager", MsgSetVoteManagerProto as any);
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  registry.register("/svote.v1.MsgCreateVotingSession", MsgCreateVotingSessionProto as any);
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  registry.register("/cosmos.slashing.v1beta1.MsgUnjail", MsgUnjailProto as any);
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  registry.register("/cosmos.bank.v1beta1.MsgSend", MsgSendProto as any);
  return registry;
}

// ── REST helpers ────────────────────────────────────────────────

async function fetchAccountInfo(
  apiBase: string,
  address: string,
): Promise<{ accountNumber: number; sequence: number }> {
  const resp = await fetch(`${apiBase}/cosmos/auth/v1beta1/accounts/${address}`);
  if (!resp.ok) {
    const text = await resp.text();
    throw new Error(`Failed to fetch account info: HTTP ${resp.status} – ${text}`);
  }
  const data = await resp.json();
  const account = data.account ?? {};
  return {
    accountNumber: parseInt(account.account_number ?? "0", 10),
    sequence: parseInt(account.sequence ?? "0", 10),
  };
}

async function fetchChainId(apiBase: string): Promise<string> {
  const resp = await fetch(`${apiBase}/cosmos/base/tendermint/v1beta1/node_info`);
  if (!resp.ok) {
    const text = await resp.text();
    throw new Error(`Failed to fetch node info: HTTP ${resp.status} – ${text}`);
  }
  const data = await resp.json();
  return data.default_node_info?.network ?? "";
}

async function broadcastTxRest(
  apiBase: string,
  txBytes: Uint8Array,
): Promise<BroadcastResult> {
  const resp = await fetch(`${apiBase}/cosmos/tx/v1beta1/txs`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      tx_bytes: toBase64(txBytes),
      mode: "BROADCAST_MODE_SYNC",
    }),
  });
  if (!resp.ok) {
    const text = await resp.text();
    throw new Error(`Broadcast failed: HTTP ${resp.status} – ${text}`);
  }
  const data = await resp.json();
  const txResp = data.tx_response ?? {};
  return {
    tx_hash: txResp.txhash ?? "",
    code: txResp.code ?? -1,
    log: txResp.raw_log ?? "",
  };
}

/**
 * Poll the chain until a TX is included in a block, confirming it actually landed.
 * BROADCAST_MODE_SYNC only guarantees the TX passed CheckTx — the TX can still
 * be dropped from the mempool or fail during DeliverTx. This function queries
 * the TX by hash until it appears on chain or the timeout expires.
 */
async function confirmTx(
  apiBase: string,
  txHash: string,
  timeoutMs = 15_000,
  intervalMs = 2_000,
): Promise<BroadcastResult> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    await new Promise((r) => setTimeout(r, intervalMs));
    try {
      const resp = await fetch(`${apiBase}/cosmos/tx/v1beta1/txs/${txHash}`);
      if (!resp.ok) continue; // TX not indexed yet
      const data = await resp.json();
      const txResp = data.tx_response ?? {};
      const code = txResp.code ?? -1;
      if (code !== 0) {
        return {
          tx_hash: txHash,
          code,
          log: txResp.raw_log ?? `Transaction failed during block execution (code ${code})`,
        };
      }
      return { tx_hash: txHash, code: 0, log: "" };
    } catch {
      // Network error — retry
    }
  }
  throw new Error(
    `Transaction ${txHash} was not confirmed within ${timeoutMs / 1000}s. ` +
    `It may still land — check the chain explorer.`
  );
}

// ── Signing ─────────────────────────────────────────────────────

interface SignAndBroadcastOptions {
  apiBase: string;
  signer: OfflineDirectSigner;
  messages: Array<{ typeUrl: string; value: Record<string, unknown> }>;
  memo?: string;
  gas?: string;
}

async function signAndBroadcast({
  apiBase,
  signer,
  messages,
  memo = "",
  gas = DEFAULT_GAS,
}: SignAndBroadcastOptions): Promise<BroadcastResult> {
  const [account] = await signer.getAccounts();

  const [{ accountNumber, sequence }, chainId] = await Promise.all([
    fetchAccountInfo(apiBase, account.address),
    fetchChainId(apiBase),
  ]);

  const registry = createRegistry();
  const txBodyBytes = registry.encodeTxBody({ messages, memo });

  const pubkey = encodePubkey(encodeSecp256k1Pubkey(account.pubkey));
  const gasLimit = parseInt(gas, 10);
  const authInfoBytes = makeAuthInfoBytes(
    [{ pubkey, sequence }],
    [{ denom: "usvote", amount: "0" }],
    gasLimit,
    undefined,
    undefined,
    SignMode.SIGN_MODE_DIRECT,
  );

  const signDoc = makeSignDoc(txBodyBytes, authInfoBytes, chainId, accountNumber);
  const { signature, signed } = await signer.signDirect(account.address, signDoc);

  const txRaw = TxRaw.fromPartial({
    bodyBytes: signed.bodyBytes,
    authInfoBytes: signed.authInfoBytes,
    signatures: [fromBase64(signature.signature)],
  });
  const txBytes = TxRaw.encode(txRaw).finish();

  const broadcastResult = await broadcastTxRest(apiBase, txBytes);
  // If CheckTx failed, return immediately — no point polling
  if (broadcastResult.code !== 0) return broadcastResult;
  // Poll until the TX is included in a block (DeliverTx confirmation)
  return confirmTx(apiBase, broadcastResult.tx_hash);
}

// ── Stub byte fields ────────────────────────────────────────────
// Matching the e2e test pattern (see e2e-tests/src/payloads.rs).

function filledBytes(byte: number, len: number): Uint8Array {
  const arr = new Uint8Array(len);
  arr.fill(byte);
  return arr;
}

const STUB_VK_ZKP1            = filledBytes(0xf1, 64);
const STUB_VK_ZKP2            = filledBytes(0xf2, 64);
const STUB_VK_ZKP3            = filledBytes(0xf3, 64);

/** Compute a SHA-256 hash of the serialized proposals for use as proposals_hash.
 *  This ensures each round with different proposals gets a unique vote_round_id
 *  (the chain derives round ID from snapshot_height, blockhash, proposals_hash,
 *  vote_end_time, nullifier_imt_root, and nc_root).
 *
 *  Uses @noble/hashes instead of crypto.subtle so it works on non-secure
 *  origins (plain HTTP dev servers) where crypto.subtle is undefined. */
function computeProposalsHash(
  proposals: Array<{
    id: number;
    title: string;
    description: string;
    options: Array<{ index: number; label: string }>;
  }>,
): Uint8Array {
  const canonical = JSON.stringify(
    proposals.map((p) => ({
      id: p.id,
      title: p.title,
      description: p.description,
      options: p.options.map((o) => ({ index: o.index, label: o.label })),
    })),
  );
  const encoded = new TextEncoder().encode(canonical);
  return sha256(encoded);
}

// ── Helpers ──────────────────────────────────────────────────────

function hexToBytes(hex: string): Uint8Array {
  const clean = hex.startsWith("0x") ? hex.slice(2) : hex;
  const bytes = new Uint8Array(clean.length / 2);
  for (let i = 0; i < bytes.length; i++) {
    bytes[i] = parseInt(clean.substring(i * 2, i * 2 + 2), 16);
  }
  return bytes;
}

/**
 * Fetch real snapshot data (nc_root, nullifier_imt_root, blockhash) from the
 * chain's snapshot-data endpoint. Throws on failure — creating a round with
 * stub roots would cause delegation proofs (ZKP #1) to fail silently.
 */
async function fetchSnapshotData(
  apiBase: string,
  snapshotHeight: number,
): Promise<{
  ncRoot: Uint8Array;
  nullifierImtRoot: Uint8Array;
  snapshotBlockhash: Uint8Array;
}> {
  const resp = await fetch(`${apiBase}/shielded-vote/v1/snapshot-data/${snapshotHeight}`);
  if (!resp.ok) {
    const body = await resp.text().catch(() => "");
    throw new Error(
      `Failed to fetch snapshot data for height ${snapshotHeight}: HTTP ${resp.status}${body ? ` – ${body}` : ""}`,
    );
  }
  const data: { nc_root: string; nullifier_imt_root: string; snapshot_blockhash: string } =
    await resp.json();

  return {
    ncRoot: hexToBytes(data.nc_root),
    nullifierImtRoot: hexToBytes(data.nullifier_imt_root),
    snapshotBlockhash: hexToBytes(data.snapshot_blockhash),
  };
}

// ── Public API ──────────────────────────────────────────────────

/**
 * Sign and broadcast a MsgSetVoteManager transaction.
 *
 * The `creator` field is derived from the signer (must be the current vote
 * manager or a bonded validator).
 */
export async function setVoteManager(
  apiBase: string,
  signer: OfflineDirectSigner,
  newManager: string,
): Promise<BroadcastResult> {
  const [account] = await signer.getAccounts();
  return signAndBroadcast({
    apiBase,
    signer,
    messages: [
      {
        typeUrl: "/svote.v1.MsgSetVoteManager",
        value: { creator: account.address, newManager },
      },
    ],
  });
}

/**
 * Sign and broadcast a MsgCreateVotingSession transaction.
 *
 * Fetches real nc_root and nullifier_imt_root from the chain's snapshot-data
 * endpoint (which calls lightwalletd and the PIR server). Throws if snapshot
 * data cannot be fetched.
 */
export async function createVotingSession(
  apiBase: string,
  signer: OfflineDirectSigner,
  params: {
    snapshotHeight: number;
    voteEndTime: number;
    description: string;
    title: string;
    nullifierApiBase: string;
    proposals: Array<{
      id: number;
      title: string;
      description: string;
      options: Array<{ index: number; label: string }>;
    }>;
  },
): Promise<BroadcastResult> {
  const [account] = await signer.getAccounts();

  // Fetch real snapshot data (nc_root, nullifier_imt_root, blockhash).
  const [snapshot] = await Promise.all([
    fetchSnapshotData(apiBase, params.snapshotHeight),
  ]);
  const proposalsHash = computeProposalsHash(params.proposals);

  return signAndBroadcast({
    apiBase,
    signer,
    messages: [
      {
        typeUrl: "/svote.v1.MsgCreateVotingSession",
        value: {
          creator: account.address,
          snapshotHeight: params.snapshotHeight,
          snapshotBlockhash: snapshot.snapshotBlockhash,
          proposalsHash,
          voteEndTime: params.voteEndTime,
          nullifierImtRoot: snapshot.nullifierImtRoot,
          ncRoot: snapshot.ncRoot,
          vkZkp1: STUB_VK_ZKP1,
          vkZkp2: STUB_VK_ZKP2,
          vkZkp3: STUB_VK_ZKP3,
          proposals: params.proposals,
          description: params.description,
          title: params.title,
        } satisfies CreateVotingSessionValue,
      },
    ],
  });
}

/**
 * Sign and broadcast a cosmos.bank.v1beta1.MsgSend transaction.
 *
 * Used by the "Fund validator" UI to transfer stake tokens from the
 * bootstrap operator to a validator address.
 *
 * @param amountUsvote - amount in micro-tokens (usvote), e.g. "1000000" for 1 SVOTE
 */
export async function fundValidator(
  apiBase: string,
  signer: OfflineDirectSigner,
  toAddress: string,
  amountUsvote: string,
): Promise<BroadcastResult> {
  const [account] = await signer.getAccounts();
  return signAndBroadcast({
    apiBase,
    signer,
    messages: [
      {
        typeUrl: "/cosmos.bank.v1beta1.MsgSend",
        value: {
          fromAddress: account.address,
          toAddress,
          amount: [{ denom: "usvote", amount: amountUsvote }],
        },
      },
    ],
  });
}

/**
 * Sign and broadcast a standard cosmos.slashing.v1beta1.MsgUnjail transaction.
 *
 * The signer must be the jailed validator's operator account.
 * `validatorAddress` is the valoper bech32 address of the jailed validator.
 */
export async function unjailValidator(
  apiBase: string,
  signer: OfflineDirectSigner,
  validatorAddress: string,
): Promise<BroadcastResult> {
  return signAndBroadcast({
    apiBase,
    signer,
    messages: [
      {
        typeUrl: "/cosmos.slashing.v1beta1.MsgUnjail",
        value: { validatorAddr: validatorAddress },
      },
    ],
  });
}
