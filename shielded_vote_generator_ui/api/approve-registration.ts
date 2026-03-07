// Edge function for approving or rejecting a pending validator registration.
//
// Bootstrap admin auth — the signer must match ADMIN_ADDRESS. The admin
// signs { action: "approve"|"reject", operator_address }. On approve, the
// pending entry is moved to vote_servers in voting-config. On reject, it is
// simply removed from pending-registrations. Both updates use a single Edge
// Config PATCH for atomicity.
//
// Required env vars:
//   VERCEL_API_TOKEN   — Vercel REST API token with Edge Config write access
//   EDGE_CONFIG_ID     — ID of the Edge Config store (ecfg_...)
//   ADMIN_ADDRESS      — Bech32 address of the bootstrap admin

import { get } from '@vercel/edge-config';
import { secp256k1 } from '@noble/curves/secp256k1.js';
import { sha256 } from '@noble/hashes/sha2.js';
import { ripemd160 } from '@noble/hashes/legacy.js';
import { bech32 } from 'bech32';

export const config = { runtime: 'edge' };

const BECH32_PREFIX = 'sv';

// -- Crypto helpers (duplicated from update-voting-config.ts) --

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

interface ApproveBody {
  payload: { action: string; operator_address: string };
  signature: string;
  pubKey: string;
  signerAddress: string;
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

  if (!VERCEL_API_TOKEN || !EDGE_CONFIG_ID) {
    return jsonResponse(
      { error: 'Server misconfigured: missing VERCEL_API_TOKEN or EDGE_CONFIG_ID' },
      500,
    );
  }

  let body: ApproveBody;
  try {
    body = await req.json();
  } catch {
    return jsonResponse({ error: 'Invalid JSON body' }, 400);
  }

  const { payload, signature, pubKey, signerAddress } = body;
  if (!payload || !signature || !pubKey || !signerAddress) {
    return jsonResponse(
      { error: 'Missing required fields: payload, signature, pubKey, signerAddress' },
      400,
    );
  }

  if ((payload.action !== 'approve' && payload.action !== 'reject') || !payload.operator_address) {
    return jsonResponse({ error: 'Invalid payload: expected { action: "approve"|"reject", operator_address }' }, 400);
  }

  // 1. Verify secp256k1 signature.
  const payloadStr = JSON.stringify(payload);
  const signBytes = makeSignArbitraryDoc(signerAddress, payloadStr);
  const msgHash = sha256(signBytes);
  const sigBytes = base64ToBytes(signature);
  const pubKeyBytes = base64ToBytes(pubKey);

  let sigValid = false;
  try {
    sigValid = secp256k1.verify(sigBytes, msgHash, pubKeyBytes, { prehash: false });
  } catch {
    sigValid = false;
  }

  if (!sigValid) {
    return jsonResponse({ error: 'Invalid signature' }, 401);
  }

  // 2. Derive address and verify it matches the signer.
  const derivedAddress = pubkeyToAddress(pubKeyBytes);
  if (derivedAddress !== signerAddress) {
    return jsonResponse({ error: 'Public key does not match signer address' }, 401);
  }

  // 3. Verify the signer is the bootstrap admin.
  const ADMIN_ADDRESS = process.env.ADMIN_ADDRESS;
  if (!ADMIN_ADDRESS) {
    return jsonResponse({ error: 'Server misconfigured: missing ADMIN_ADDRESS' }, 500);
  }

  if (signerAddress !== ADMIN_ADDRESS) {
    return jsonResponse(
      { error: `Signer ${signerAddress} is not the admin (${ADMIN_ADDRESS})` },
      403,
    );
  }

  // 4. Find the pending registration.
  const currentPending = (await get('pending-registrations') as PendingRegistration[] | null) ?? [];
  const entry = currentPending.find((p) => p.operator_address === payload.operator_address);

  if (!entry) {
    return jsonResponse(
      { error: `No pending registration found for ${payload.operator_address}` },
      404,
    );
  }

  // Remove from pending (used by both approve and reject).
  const updatedPending = currentPending.filter(
    (p) => p.operator_address !== payload.operator_address,
  );

  if (payload.action === 'reject') {
    // Just remove from pending — no vote_servers update.
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
              { operation: 'upsert', key: 'pending-registrations', value: updatedPending },
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

    return jsonResponse({ status: 'rejected', operator_address: entry.operator_address });
  }

  // 5. Approve: move to vote_servers in voting-config and approved-servers.
  const currentConfig = (await get('voting-config') as VotingConfig | null) ?? {
    version: 1,
    vote_servers: [],
    pir_servers: [],
  };

  const serviceEntry: ServiceEntry = {
    url: entry.url,
    label: entry.moniker,
    operator_address: entry.operator_address,
  };

  // Both URL and operator_address are unique keys — evict any existing entry
  // matching either field to prevent duplicates, then append the new entry.
  currentConfig.vote_servers = currentConfig.vote_servers.filter(
    (s) => s.url !== entry.url && s.operator_address !== entry.operator_address,
  );
  currentConfig.vote_servers.push(serviceEntry);

  // Also persist in approved-servers so the server survives pulse gaps.
  const approvedServers = (await get('approved-servers') as ServiceEntry[] | null) ?? [];
  const updatedApproved = approvedServers.filter(
    (s) => s.url !== entry.url && s.operator_address !== entry.operator_address,
  );
  updatedApproved.push(serviceEntry);

  // 6. Atomic Edge Config PATCH with all three keys.
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
            { operation: 'upsert', key: 'pending-registrations', value: updatedPending },
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

  return jsonResponse({ status: 'approved', operator_address: entry.operator_address, url: entry.url });
}
