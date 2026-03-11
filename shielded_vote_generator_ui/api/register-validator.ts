// Edge function for validator self-registration.
//
// A validator signs { operator_address, url, moniker, timestamp } with their
// operator key. If the validator is already bonded on-chain, their URL is
// written directly to vote_servers in Edge Config. Otherwise, the entry is
// added to a pending-registrations queue with a 7-day expiry for the
// vote-manager to approve.
//
// Required env vars:
//   VERCEL_API_TOKEN   — Vercel REST API token with Edge Config write access
//   EDGE_CONFIG_ID     — ID of the Edge Config store (ecfg_...)
//   CHAIN_API_URL      — Public URL of a chain node REST API

import { get } from '@vercel/edge-config';
import { secp256k1 } from '@noble/curves/secp256k1.js';
import { sha256 } from '@noble/hashes/sha2.js';
import { ripemd160 } from '@noble/hashes/legacy.js';
import { bech32 } from 'bech32';

export const config = { runtime: 'edge' };

const BECH32_PREFIX = 'sv';
const VALOPER_PREFIX = 'svvaloper';
const TIMESTAMP_WINDOW_SECS = 300; // 5 minutes
const PENDING_EXPIRY_SECS = 7 * 24 * 60 * 60; // 7 days

// -- Crypto helpers (duplicated from update-voting-config.ts — edge functions
//    can't share modules without build config changes) --

function makeSignArbitraryDoc(signer: string, data: string): Uint8Array {
  const signDoc = {
    account_number: '0',
    chain_id: '',
    fee: { amount: [], gas: '0' },
    memo: '',
    msgs: [
      {
        type: 'sign/MsgSignData',
        value: {
          data: btoa(data),
          signer: signer,
        },
      },
    ],
    sequence: '0',
  };
  return new TextEncoder().encode(JSON.stringify(signDoc));
}

function pubkeyToAddress(compressedPubkey: Uint8Array): string {
  const hash = ripemd160(sha256(compressedPubkey));
  return bech32.encode(BECH32_PREFIX, bech32.toWords(hash));
}

function addressToValoper(address: string): string {
  const { words } = bech32.decode(address);
  return bech32.encode(VALOPER_PREFIX, words);
}

function base64ToBytes(b64: string): Uint8Array {
  const binary = atob(b64);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

function corsHeaders(): Record<string, string> {
  return {
    'Access-Control-Allow-Origin': '*',
    'Access-Control-Allow-Methods': 'POST, OPTIONS',
    'Access-Control-Allow-Headers': 'Content-Type',
  };
}

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...corsHeaders() },
  });
}

interface RegisterBody {
  operator_address: string;
  url: string;
  moniker: string;
  timestamp: number;
  signature: string;
  pub_key: string;
}

interface ServiceEntry {
  url: string;
  label: string;
  operator_address?: string;
}

interface VotingConfig {
  version: number;
  vote_servers: ServiceEntry[];
  pir_servers: ServiceEntry[];
}

interface PendingRegistration {
  operator_address: string;
  url: string;
  moniker: string;
  timestamp: number;
  signature: string;
  pub_key: string;
  expires_at: number;
}

export default async function handler(req: Request) {
  if (req.method === 'OPTIONS') {
    return new Response(null, { status: 204, headers: corsHeaders() });
  }

  if (req.method !== 'POST') {
    return jsonResponse({ error: 'Method not allowed' }, 405);
  }

  const VERCEL_API_TOKEN = process.env.VERCEL_API_TOKEN;
  const EDGE_CONFIG_ID = process.env.EDGE_CONFIG_ID;
  const CHAIN_API_URL = process.env.CHAIN_API_URL;

  if (!VERCEL_API_TOKEN || !EDGE_CONFIG_ID || !CHAIN_API_URL) {
    return jsonResponse(
      { error: 'Server misconfigured: missing VERCEL_API_TOKEN, EDGE_CONFIG_ID, or CHAIN_API_URL' },
      500,
    );
  }

  let body: RegisterBody;
  try {
    body = await req.json();
  } catch {
    return jsonResponse({ error: 'Invalid JSON body' }, 400);
  }

  const { operator_address, url, moniker, timestamp, signature, pub_key } = body;
  if (!operator_address || !url || !moniker || !timestamp || !signature || !pub_key) {
    return jsonResponse(
      { error: 'Missing required fields: operator_address, url, moniker, timestamp, signature, pub_key' },
      400,
    );
  }

  // 1. Validate timestamp (replay protection).
  const now = Math.floor(Date.now() / 1000);
  if (Math.abs(now - timestamp) > TIMESTAMP_WINDOW_SECS) {
    return jsonResponse({ error: 'Timestamp too far from server time (>5min)' }, 400);
  }

  // 2. Verify secp256k1 signature over the payload.
  const payloadStr = JSON.stringify({ operator_address, url, moniker, timestamp });
  const signBytes = makeSignArbitraryDoc(operator_address, payloadStr);
  const msgHash = sha256(signBytes);
  const sigBytes = base64ToBytes(signature);
  const pubKeyBytes = base64ToBytes(pub_key);

  let sigValid = false;
  try {
    sigValid = secp256k1.verify(sigBytes, msgHash, pubKeyBytes, { prehash: false });
  } catch {
    sigValid = false;
  }

  if (!sigValid) {
    return jsonResponse({ error: 'Invalid signature' }, 401);
  }

  // 3. Derive address from pubkey and verify it matches the claimed operator_address.
  const derivedAddress = pubkeyToAddress(pubKeyBytes);
  if (derivedAddress !== operator_address) {
    return jsonResponse({ error: 'Public key does not match operator_address' }, 401);
  }

  // 4a. If already in approved-servers, short-circuit — no bonding check needed.
  // This avoids re-adding to pending-registrations when the chain API is temporarily
  // unreachable (e.g. during a simultaneous restart).
  const existingApproved = (await get('approved-servers') as ServiceEntry[] | null) ?? [];
  if (existingApproved.some((s) => s.operator_address === operator_address)) {
    return jsonResponse({ status: 'registered', phase: 'already-approved' });
  }

  // 4b. Check if this validator is bonded on-chain.
  const valoperAddress = addressToValoper(operator_address);
  let isBonded = false;
  try {
    const resp = await fetch(
      `${CHAIN_API_URL}/cosmos/staking/v1beta1/validators/${valoperAddress}`,
    );
    if (resp.ok) {
      const data = await resp.json();
      isBonded = data.validator?.status === 'BOND_STATUS_BONDED';
    }
  } catch {
    // If the chain query fails, treat as not bonded (goes to pending queue).
  }

  // 5. Read current state from Edge Config.
  const currentConfig = (await get('voting-config') as VotingConfig | null) ?? {
    version: 1,
    vote_servers: [],
    pir_servers: [],
  };

  if (isBonded) {
    // Phase 2: Directly upsert into vote_servers and approved-servers.
    // Both URL and operator_address are unique keys — evict any existing entry
    // matching either field to prevent duplicates, then append the new entry.
    const entry: ServiceEntry = { url, label: moniker, operator_address };
    currentConfig.vote_servers = currentConfig.vote_servers.filter(
      (s) => s.url !== url && s.operator_address !== operator_address,
    );
    currentConfig.vote_servers.push(entry);

    // Also persist in approved-servers so the server survives pulse gaps.
    const approvedServers = (await get('approved-servers') as ServiceEntry[] | null) ?? [];
    const updatedApproved = approvedServers.filter(
      (s) => s.url !== url && s.operator_address !== operator_address,
    );
    updatedApproved.push(entry);

    try {
      const resp = await fetch(
        `https://api.vercel.com/v1/edge-config/${EDGE_CONFIG_ID}/items`,
        {
          method: 'PATCH',
          headers: {
            Authorization: `Bearer ${VERCEL_API_TOKEN}`,
            'Content-Type': 'application/json',
          },
          body: JSON.stringify({
            items: [
              { operation: 'upsert', key: 'voting-config', value: currentConfig },
              { operation: 'upsert', key: 'approved-servers', value: updatedApproved },
            ],
          }),
        },
      );
      if (!resp.ok) {
        const text = await resp.text();
        return jsonResponse({ error: `Edge Config update failed: HTTP ${resp.status} – ${text}` }, 502);
      }
    } catch (err) {
      return jsonResponse({ error: `Edge Config update failed: ${err}` }, 502);
    }

    return jsonResponse({ status: 'registered', phase: 'bonded' });
  }

  // Phase 1: Add to pending-registrations queue.
  const currentPending = (await get('pending-registrations') as PendingRegistration[] | null) ?? [];

  const pendingEntry: PendingRegistration = {
    operator_address,
    url,
    moniker,
    timestamp,
    signature,
    pub_key,
    expires_at: now + PENDING_EXPIRY_SECS,
  };

  // Upsert by operator_address (replace existing entry if re-registering).
  const pendingIdx = currentPending.findIndex((p) => p.operator_address === operator_address);
  if (pendingIdx >= 0) {
    currentPending[pendingIdx] = pendingEntry;
  } else {
    currentPending.push(pendingEntry);
  }

  try {
    const resp = await fetch(
      `https://api.vercel.com/v1/edge-config/${EDGE_CONFIG_ID}/items`,
      {
        method: 'PATCH',
        headers: {
          Authorization: `Bearer ${VERCEL_API_TOKEN}`,
          'Content-Type': 'application/json',
        },
        body: JSON.stringify({
          items: [
            { operation: 'upsert', key: 'pending-registrations', value: currentPending },
          ],
        }),
      },
    );
    if (!resp.ok) {
      const text = await resp.text();
      return jsonResponse({ error: `Edge Config update failed: HTTP ${resp.status} – ${text}` }, 502);
    }
  } catch (err) {
    return jsonResponse({ error: `Edge Config update failed: ${err}` }, 502);
  }

  return jsonResponse({ status: 'pending', phase: 'unbonded', expires_at: pendingEntry.expires_at });
}
