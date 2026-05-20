// Client-side Cosmos SDK transaction signing and REST broadcasting.
//
// Coordinator-owned actions are submitted through MsgProposeCoordinatorAction.
// The embedded payload is the original action message encoded into
// google.protobuf.Any, so threshold=1 still uses the same authority path.

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
import { validateProposalOptions } from "../constants/vote";

export {
  MAX_VOTE_OPTIONS,
  MIN_VOTE_OPTIONS,
  validateProposalOptions,
  validateVoteChoice,
} from "../constants/vote";

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

// ── Protobuf type: MsgUpdateVoteManagers ─────────────────────────────
//
// message MsgUpdateVoteManagers {
//   string creator          = 1;
//   repeated string new_vote_managers = 2;
// }
// Atomically replaces the vote-manager set and threshold when executed through
// coordinator action approval. The new set must be non-empty and contain only
// valid bech32 addresses with no duplicates. Balances are not touched; missing
// auth accounts are initialized by the chain handler.
const MsgUpdateVoteManagersProto = {
  encode(
    message: { creator: string; newVoteManagers: string[]; newThreshold: number },
    writer: ProtoWriter = ProtoWriter.create(),
  ): ProtoWriter {
    if (message.creator !== "") writer.uint32(10).string(message.creator);
    for (const vm of message.newVoteManagers) {
      writer.uint32(18).string(vm);
    }
    if (message.newThreshold !== 0) writer.uint32(24).uint32(message.newThreshold);
    return writer;
  },
  decode(): { creator: string; newVoteManagers: string[]; newThreshold: number } {
    throw new Error("decode not implemented");
  },
  fromPartial(
    object: Partial<{ creator: string; newVoteManagers: string[]; newThreshold: number }>,
  ): { creator: string; newVoteManagers: string[]; newThreshold: number } {
    return {
      creator: object.creator ?? "",
      newVoteManagers: object.newVoteManagers ?? [],
      newThreshold: object.newThreshold ?? 0,
    };
  },
};

// google.protobuf.Any { string type_url = 1; bytes value = 2; }
const AnyProto = {
  encode(
    message: { typeUrl: string; value: Uint8Array },
    writer: ProtoWriter = ProtoWriter.create(),
  ): ProtoWriter {
    if (message.typeUrl !== "") writer.uint32(10).string(message.typeUrl);
    if (message.value.length) writer.uint32(18).bytes(message.value);
    return writer;
  },
  decode(): { typeUrl: string; value: Uint8Array } {
    throw new Error("decode not implemented");
  },
  fromPartial(
    object: Partial<{ typeUrl: string; value: Uint8Array }>,
  ): { typeUrl: string; value: Uint8Array } {
    return { typeUrl: object.typeUrl ?? "", value: object.value ?? new Uint8Array() };
  },
};

const MsgProposeCoordinatorActionProto = {
  encode(
    message: { creator: string; payload: { typeUrl: string; value: Uint8Array } },
    writer: ProtoWriter = ProtoWriter.create(),
  ): ProtoWriter {
    if (message.creator !== "") writer.uint32(10).string(message.creator);
    writer.sub(2, AnyProto.encode(message.payload));
    return writer;
  },
  decode(): { creator: string; payload: { typeUrl: string; value: Uint8Array } } {
    throw new Error("decode not implemented");
  },
  fromPartial(
    object: Partial<{ creator: string; payload: { typeUrl: string; value: Uint8Array } }>,
  ): { creator: string; payload: { typeUrl: string; value: Uint8Array } } {
    return {
      creator: object.creator ?? "",
      payload: object.payload ?? { typeUrl: "", value: new Uint8Array() },
    };
  },
};

const MsgApproveCoordinatorActionProto = {
  encode(
    message: { creator: string; actionId: number },
    writer: ProtoWriter = ProtoWriter.create(),
  ): ProtoWriter {
    if (message.creator !== "") writer.uint32(10).string(message.creator);
    if (message.actionId !== 0) writer.uint32(16).uint64(message.actionId);
    return writer;
  },
  decode(): { creator: string; actionId: number } {
    throw new Error("decode not implemented");
  },
  fromPartial(object: Partial<{ creator: string; actionId: number }>): { creator: string; actionId: number } {
    return { creator: object.creator ?? "", actionId: object.actionId ?? 0 };
  },
};

// ── Protobuf type: MsgScheduleUpgrade ───────────────────────────
//
// message MsgScheduleUpgrade {
//   string creator = 1; string name = 2; int64 height = 3;
//   string info = 4; bool replace_existing = 5;
// }
const MsgScheduleUpgradeProto = {
  encode(
    message: {
      creator: string;
      name: string;
      height: number;
      info: string;
      replaceExisting: boolean;
    },
    writer: ProtoWriter = ProtoWriter.create(),
  ): ProtoWriter {
    if (message.creator !== "") writer.uint32(10).string(message.creator);
    if (message.name !== "") writer.uint32(18).string(message.name);
    if (message.height !== 0) writer.uint32(24).uint64(message.height);
    if (message.info !== "") writer.uint32(34).string(message.info);
    if (message.replaceExisting) writer.uint32(40).uint32(1);
    return writer;
  },
  decode(): {
    creator: string;
    name: string;
    height: number;
    info: string;
    replaceExisting: boolean;
  } {
    throw new Error("decode not implemented");
  },
  fromPartial(
    object: Partial<{
      creator: string;
      name: string;
      height: number;
      info: string;
      replaceExisting: boolean;
    }>,
  ): {
    creator: string;
    name: string;
    height: number;
    info: string;
    replaceExisting: boolean;
  } {
    return {
      creator: object.creator ?? "",
      name: object.name ?? "",
      height: object.height ?? 0,
      info: object.info ?? "",
      replaceExisting: object.replaceExisting ?? false,
    };
  },
};

// message MsgCancelUpgrade { string creator = 1; }
const MsgCancelUpgradeProto = {
  encode(
    message: { creator: string },
    writer: ProtoWriter = ProtoWriter.create(),
  ): ProtoWriter {
    if (message.creator !== "") writer.uint32(10).string(message.creator);
    return writer;
  },
  decode(): { creator: string } {
    throw new Error("decode not implemented");
  },
  fromPartial(object: Partial<{ creator: string }>): { creator: string } {
    return { creator: object.creator ?? "" };
  },
};

// ── Protobuf type: MsgSetEndorser ───────────────────────────────
//
// message MsgSetEndorser { string creator = 1; string endorser_id = 2; string address = 3; }
const MsgSetEndorserProto = {
  encode(
    message: { creator: string; endorserId: string; address: string },
    writer: ProtoWriter = ProtoWriter.create(),
  ): ProtoWriter {
    if (message.creator !== "") writer.uint32(10).string(message.creator);
    if (message.endorserId !== "") writer.uint32(18).string(message.endorserId);
    if (message.address !== "") writer.uint32(26).string(message.address);
    return writer;
  },
  decode(): { creator: string; endorserId: string; address: string } {
    throw new Error("decode not implemented");
  },
  fromPartial(
    object: Partial<{ creator: string; endorserId: string; address: string }>,
  ): { creator: string; endorserId: string; address: string } {
    return {
      creator: object.creator ?? "",
      endorserId: object.endorserId ?? "",
      address: object.address ?? "",
    };
  },
};

// ── Protobuf type: MsgEndorseRound ──────────────────────────────
//
// message MsgEndorseRound { string creator = 1; string endorser_id = 2; bytes vote_round_id = 3; }
const MsgEndorseRoundProto = {
  encode(
    message: { creator: string; endorserId: string; voteRoundId: Uint8Array },
    writer: ProtoWriter = ProtoWriter.create(),
  ): ProtoWriter {
    if (message.creator !== "") writer.uint32(10).string(message.creator);
    if (message.endorserId !== "") writer.uint32(18).string(message.endorserId);
    if (message.voteRoundId.length) writer.uint32(26).bytes(message.voteRoundId);
    return writer;
  },
  decode(): { creator: string; endorserId: string; voteRoundId: Uint8Array } {
    throw new Error("decode not implemented");
  },
  fromPartial(
    object: Partial<{ creator: string; endorserId: string; voteRoundId: Uint8Array }>,
  ): { creator: string; endorserId: string; voteRoundId: Uint8Array } {
    return {
      creator: object.creator ?? "",
      endorserId: object.endorserId ?? "",
      voteRoundId: object.voteRoundId ?? new Uint8Array(),
    };
  },
};

// ── Protobuf type: MsgClearRoundEndorsement ─────────────────────
//
// message MsgClearRoundEndorsement { string creator = 1; string endorser_id = 2; bytes vote_round_id = 3; }
const MsgClearRoundEndorsementProto = {
  encode(
    message: { creator: string; endorserId: string; voteRoundId: Uint8Array },
    writer: ProtoWriter = ProtoWriter.create(),
  ): ProtoWriter {
    if (message.creator !== "") writer.uint32(10).string(message.creator);
    if (message.endorserId !== "") writer.uint32(18).string(message.endorserId);
    if (message.voteRoundId.length) writer.uint32(26).bytes(message.voteRoundId);
    return writer;
  },
  decode(): { creator: string; endorserId: string; voteRoundId: Uint8Array } {
    throw new Error("decode not implemented");
  },
  fromPartial(
    object: Partial<{ creator: string; endorserId: string; voteRoundId: Uint8Array }>,
  ): { creator: string; endorserId: string; voteRoundId: Uint8Array } {
    return {
      creator: object.creator ?? "",
      endorserId: object.endorserId ?? "",
      voteRoundId: object.voteRoundId ?? new Uint8Array(),
    };
  },
};

// ── Protobuf type: MsgCreateVotingSession ───────────────────────

// message VoteOption { uint32 index = 1; string label = 2; string description = 3; }
function encodeVoteOption(opt: { index: number; label: string; description?: string }): ProtoWriter {
  const w = ProtoWriter.create();
  if (opt.index !== 0) w.uint32(8).uint32(opt.index);   // field 1, wire 0
  if (opt.label !== "") w.uint32(18).string(opt.label);  // field 2, wire 2
  if (opt.description) w.uint32(26).string(opt.description); // field 3, wire 2
  return w;
}

// message Proposal { uint32 id = 1; string title = 2; string description = 3; repeated VoteOption options = 4; string zip_number = 5; string forum_url = 6; }
function encodeProposal(p: {
  id: number;
  title: string;
  description: string;
  options: Array<{ index: number; label: string; description?: string }>;
  zipNumber?: string;
  forumURL?: string;
}): ProtoWriter {
  const w = ProtoWriter.create();
  if (p.id !== 0) w.uint32(8).uint32(p.id);                // field 1, wire 0
  if (p.title !== "") w.uint32(18).string(p.title);         // field 2, wire 2
  if (p.description !== "") w.uint32(26).string(p.description); // field 3, wire 2
  for (const opt of p.options) {
    w.sub(4, encodeVoteOption(opt));                         // field 4, wire 2
  }
  if (p.zipNumber) w.uint32(42).string(p.zipNumber);        // field 5, wire 2
  if (p.forumURL) w.uint32(50).string(p.forumURL);          // field 6, wire 2
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
  proposals: Array<{
    id: number;
    title: string;
    description: string;
    options: Array<{ index: number; label: string; description?: string }>;
    zipNumber?: string;
    forumURL?: string;
  }>;
  description: string;
  title: string;
  discussionURL: string;
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
    for (const p of m.proposals) {
      writer.sub(8, encodeProposal(p));                                                  // 8 repeated
    }
    if (m.description !== "")          writer.uint32(74).string(m.description);          // 9 string
    if (m.title !== "")                writer.uint32(82).string(m.title);                // 10 string
    if (m.discussionURL !== "")        writer.uint32(90).string(m.discussionURL);        // 11 string
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
      proposals: object.proposals ?? [],
      description: object.description ?? "",
      title: object.title ?? "",
      discussionURL: object.discussionURL ?? "",
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

// ── Protobuf type: MsgAuthorizedSend (svote.v1) ─────────────────
// Bank MsgSend is disabled at the ante-handler level; privileged transfers use
// MsgAuthorizedSend only as coordinator action payloads.
//
// message MsgAuthorizedSend {
//   string creator = 1; string to_address = 2; string amount = 3;
// }
const MsgAuthorizedSendProto = {
  encode(
    message: { creator: string; toAddress: string; amount: string },
    writer: ProtoWriter = ProtoWriter.create(),
  ): ProtoWriter {
    if (message.creator !== "")   writer.uint32(10).string(message.creator);   // field 1
    if (message.toAddress !== "") writer.uint32(18).string(message.toAddress); // field 2
    if (message.amount !== "")    writer.uint32(26).string(message.amount);    // field 3
    return writer;
  },
  decode(): { creator: string; toAddress: string; amount: string } {
    throw new Error("decode not implemented");
  },
  fromPartial(
    object: Partial<{ creator: string; toAddress: string; amount: string }>,
  ): { creator: string; toAddress: string; amount: string } {
    return {
      creator: object.creator ?? "",
      toAddress: object.toAddress ?? "",
      amount: object.amount ?? "",
    };
  },
};

// ── Registry ────────────────────────────────────────────────────

function createRegistry(): Registry {
  const registry = new Registry();
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  registry.register("/svote.v1.MsgProposeCoordinatorAction", MsgProposeCoordinatorActionProto as any);
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  registry.register("/svote.v1.MsgApproveCoordinatorAction", MsgApproveCoordinatorActionProto as any);
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  registry.register("/cosmos.slashing.v1beta1.MsgUnjail", MsgUnjailProto as any);
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  registry.register("/svote.v1.MsgAuthorizedSend", MsgAuthorizedSendProto as any);
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  registry.register("/svote.v1.MsgEndorseRound", MsgEndorseRoundProto as any);
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  registry.register("/svote.v1.MsgClearRoundEndorsement", MsgClearRoundEndorsementProto as any);
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
    options: Array<{ index: number; label: string; description?: string }>;
  }>,
): Uint8Array {
  const canonical = JSON.stringify(
    proposals.map((p) => ({
      id: p.id,
      title: p.title,
      description: p.description,
      options: p.options.map((o) => {
        const description = o.description ?? "";
        return description === ""
          ? { index: o.index, label: o.label }
          : { index: o.index, label: o.label, description };
      }),
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

interface MiniProto<T> {
  encode(message: T, writer?: ProtoWriter): ProtoWriter;
}

function encodePayload<T>(codec: MiniProto<T>, value: T): Uint8Array {
  return codec.encode(value).finish();
}

async function proposeCoordinatorPayload(
  apiBase: string,
  signer: OfflineDirectSigner,
  payloadTypeUrl: string,
  payloadValue: Uint8Array,
): Promise<BroadcastResult> {
  const [account] = await signer.getAccounts();
  return signAndBroadcast({
    apiBase,
    signer,
    messages: [
      {
        typeUrl: "/svote.v1.MsgProposeCoordinatorAction",
        value: {
          creator: account.address,
          payload: { typeUrl: payloadTypeUrl, value: payloadValue },
        },
      },
    ],
  });
}

// ── Public API ──────────────────────────────────────────────────

/** Sign and broadcast a MsgApproveCoordinatorAction transaction. */
export async function approveCoordinatorAction(
  apiBase: string,
  signer: OfflineDirectSigner,
  actionId: number,
): Promise<BroadcastResult> {
  const [account] = await signer.getAccounts();
  return signAndBroadcast({
    apiBase,
    signer,
    messages: [
      {
        typeUrl: "/svote.v1.MsgApproveCoordinatorAction",
        value: { creator: account.address, actionId },
      },
    ],
  });
}

/**
 * Propose a MsgUpdateVoteManagers coordinator action.
 *
 * Atomically replaces the vote-manager set with `newVoteManagers`. The
 * `creator` field is derived from the signer and the payload executes after
 * coordinator threshold approval. Balances are not moved; missing auth
 * accounts are initialized by the chain handler.
 */
export async function updateVoteManagers(
  apiBase: string,
  signer: OfflineDirectSigner,
  newVoteManagers: string[],
  newThreshold = 1,
): Promise<BroadcastResult> {
  const [account] = await signer.getAccounts();
  return proposeCoordinatorPayload(
    apiBase,
    signer,
    "/svote.v1.MsgUpdateVoteManagers",
    encodePayload(MsgUpdateVoteManagersProto, {
      creator: account.address,
      newVoteManagers,
      newThreshold,
    }),
  );
}

/** Propose a MsgScheduleUpgrade coordinator action. */
export async function scheduleUpgrade(
  apiBase: string,
  signer: OfflineDirectSigner,
  params: {
    name: string;
    height: number;
    info: string;
    replaceExisting: boolean;
  },
): Promise<BroadcastResult> {
  const [account] = await signer.getAccounts();
  return proposeCoordinatorPayload(
    apiBase,
    signer,
    "/svote.v1.MsgScheduleUpgrade",
    encodePayload(MsgScheduleUpgradeProto, {
      creator: account.address,
      name: params.name,
      height: params.height,
      info: params.info,
      replaceExisting: params.replaceExisting,
    }),
  );
}

/** Propose a MsgCancelUpgrade coordinator action. */
export async function cancelUpgrade(
  apiBase: string,
  signer: OfflineDirectSigner,
): Promise<BroadcastResult> {
  const [account] = await signer.getAccounts();
  return proposeCoordinatorPayload(
    apiBase,
    signer,
    "/svote.v1.MsgCancelUpgrade",
    encodePayload(MsgCancelUpgradeProto, { creator: account.address }),
  );
}

/** Propose a MsgSetEndorser coordinator action. Empty address clears the mapping. */
export async function setEndorser(
  apiBase: string,
  signer: OfflineDirectSigner,
  endorserId: string,
  address: string,
): Promise<BroadcastResult> {
  const [account] = await signer.getAccounts();
  return proposeCoordinatorPayload(
    apiBase,
    signer,
    "/svote.v1.MsgSetEndorser",
    encodePayload(MsgSetEndorserProto, { creator: account.address, endorserId, address }),
  );
}

/** Sign and broadcast a MsgEndorseRound transaction. */
export async function endorseRound(
  apiBase: string,
  signer: OfflineDirectSigner,
  endorserId: string,
  roundIdHex: string,
): Promise<BroadcastResult> {
  const [account] = await signer.getAccounts();
  return signAndBroadcast({
    apiBase,
    signer,
    messages: [
      {
        typeUrl: "/svote.v1.MsgEndorseRound",
        value: {
          creator: account.address,
          endorserId,
          voteRoundId: hexToBytes(roundIdHex),
        },
      },
    ],
  });
}

/** Sign and broadcast a MsgClearRoundEndorsement transaction. */
export async function clearRoundEndorsement(
  apiBase: string,
  signer: OfflineDirectSigner,
  endorserId: string,
  roundIdHex: string,
): Promise<BroadcastResult> {
  const [account] = await signer.getAccounts();
  return signAndBroadcast({
    apiBase,
    signer,
    messages: [
      {
        typeUrl: "/svote.v1.MsgClearRoundEndorsement",
        value: {
          creator: account.address,
          endorserId,
          voteRoundId: hexToBytes(roundIdHex),
        },
      },
    ],
  });
}

/**
 * Propose a MsgCreateVotingSession coordinator action.
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
    discussionURL: string;
    proposals: Array<{
      id: number;
      title: string;
      description: string;
      options: Array<{ index: number; label: string; description?: string }>;
      zipNumber?: string;
      forumURL?: string;
    }>;
  },
): Promise<BroadcastResult> {
  params.proposals.forEach((proposal, index) => {
    validateProposalOptions(`proposal ${proposal.id || index + 1}`, proposal.options);
  });

  const [account] = await signer.getAccounts();

  // Fetch real snapshot data (nc_root, nullifier_imt_root, blockhash).
  const [snapshot] = await Promise.all([
    fetchSnapshotData(apiBase, params.snapshotHeight),
  ]);
  const proposalsHash = computeProposalsHash(params.proposals);

  return proposeCoordinatorPayload(
    apiBase,
    signer,
    "/svote.v1.MsgCreateVotingSession",
    encodePayload(MsgCreateVotingSessionProto, {
      creator: account.address,
      snapshotHeight: params.snapshotHeight,
      snapshotBlockhash: snapshot.snapshotBlockhash,
      proposalsHash,
      voteEndTime: params.voteEndTime,
      nullifierImtRoot: snapshot.nullifierImtRoot,
      ncRoot: snapshot.ncRoot,
      proposals: params.proposals,
      description: params.description,
      title: params.title,
      discussionURL: params.discussionURL,
    } satisfies CreateVotingSessionValue),
  );
}

/**
 * Propose an svote.v1.MsgAuthorizedSend coordinator action.
 *
 * Used to transfer stake tokens from the vote_funding module account to a
 * validator address after coordinator approval.
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
  return proposeCoordinatorPayload(
    apiBase,
    signer,
    "/svote.v1.MsgAuthorizedSend",
    encodePayload(MsgAuthorizedSendProto, {
      creator: account.address,
      toAddress,
      amount: amountUsvote,
    }),
  );
}

/** Default manual approval amount for validator self-delegation (10 USVOTE) plus headroom. */
export const VALIDATOR_JOIN_FUND_USVOTE = "10500000";

/**
 * Manually approve a pending validator operator account by sending its join stake.
 * Vote managers use this from the Join queue UI.
 */
export async function fundValidatorJoin(
  apiBase: string,
  signer: OfflineDirectSigner,
  operatorAddress: string,
  amountUsvote: string = VALIDATOR_JOIN_FUND_USVOTE,
): Promise<BroadcastResult> {
  return fundValidator(apiBase, signer, operatorAddress, amountUsvote);
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
