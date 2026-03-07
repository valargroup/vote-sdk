// Edge function for server heartbeat (registration + pulse).
//
// Called by each helper server on startup and every 30s thereafter.
// The server signs { operator_address, url, moniker, timestamp } with its
// operator key (same ADR-036 format as register-validator).
//
// If the server's operator_address is in approved-servers, the URL is
// upserted into vote_servers and the pulse timestamp is recorded. Stale
// entries (no pulse for >2 minutes) are evicted from vote_servers on each
// call (piggybacked eviction).
//
// If the server is NOT in approved-servers, it is added to the
// pending-registrations queue for admin approval (same queue as
// register-validator).
//
// Validators should call POST /api/register-validator on startup first
// to ensure they are in approved-servers (via on-chain bonding check),
// then pulse via this endpoint every 30s.
//
// Required env vars:
//   VERCEL_API_TOKEN   — Vercel REST API token with Edge Config write access
//   EDGE_CONFIG_ID     — ID of the Edge Config store (ecfg_...)

import { get } from '@vercel/edge-config';
import { secp256k1 } from '@noble/curves/secp256k1.js';
import { sha256 } from '@noble/hashes/sha2.js';
import { ripemd160 } from '@noble/hashes/legacy.js';
import { bech32 } from 'bech32';

export const config = { runtime: 'edge' };

const BECH32_PREFIX = 'sv';
const TIMESTAMP_WINDOW_SECS = 300; // 5 minutes
const PENDING_EXPIRY_SECS = 7 * 24 * 60 * 60; // 7 days
const STALE_PULSE_SECS = 120; // 2 minutes — evict from vote_servers after this

// -- Crypto helpers (duplicated — edge functions can't share modules) --

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

interface HeartbeatBody {
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

// server-pulses: { [url]: unix_timestamp }
type ServerPulses = Record<string, number>;

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

  let body: HeartbeatBody;
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

  // 4. Check if this server is in the approved-servers list.
  const approvedServers = (await get('approved-servers') as ServiceEntry[] | null) ?? [];
  const isApproved = approvedServers.some(
    (s) => s.operator_address === operator_address,
  );

  if (!isApproved) {
    // Not approved — add to pending-registrations (same queue as register-validator).
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

    return jsonResponse({ status: 'pending', expires_at: now + PENDING_EXPIRY_SECS });
  }

  // 5. Server is approved — activate it.
  const currentConfig = (await get('voting-config') as VotingConfig | null) ?? {
    version: 1,
    vote_servers: [],
    pir_servers: [],
  };
  const pulses = (await get('server-pulses') as ServerPulses | null) ?? {};

  // Record this server's pulse.
  pulses[url] = now;

  // Upsert into vote_servers (use the approved entry's label if URL changed).
  const approvedEntry = approvedServers.find((s) => s.operator_address === operator_address)!;
  const entry: ServiceEntry = { url, label: moniker, operator_address };
  currentConfig.vote_servers = currentConfig.vote_servers.filter(
    (s) => s.url !== url && s.operator_address !== operator_address,
  );
  currentConfig.vote_servers.push(entry);

  // Also update the approved entry's URL if the server re-registered with a new one.
  if (approvedEntry.url !== url) {
    approvedEntry.url = url;
    approvedEntry.label = moniker;
  }

  // 6. Evict stale entries — only those tracked in server-pulses.
  const staleUrls: string[] = [];
  for (const [pulseUrl, pulseTime] of Object.entries(pulses)) {
    if (pulseUrl === url) continue; // just updated above
    if (now - pulseTime > STALE_PULSE_SECS) {
      staleUrls.push(pulseUrl);
      delete pulses[pulseUrl];
    }
  }

  if (staleUrls.length > 0) {
    currentConfig.vote_servers = currentConfig.vote_servers.filter(
      (s) => !staleUrls.includes(s.url),
    );
  }

  // 7. Atomic write: voting-config, server-pulses, and approved-servers (URL update).
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
            { operation: 'upsert', key: 'server-pulses', value: pulses },
            { operation: 'upsert', key: 'approved-servers', value: approvedServers },
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

  return jsonResponse({
    status: 'active',
    evicted: staleUrls.length > 0 ? staleUrls : undefined,
  });
}
