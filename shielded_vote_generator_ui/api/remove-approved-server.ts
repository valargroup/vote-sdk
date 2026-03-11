// Edge function for removing a server from approved-servers.
//
// Admin auth — the signer must be the vote-manager on-chain.
// Signs { action: "remove-approved", operator_address }.
// Removes the server from approved-servers, vote_servers in voting-config,
// and server-pulses in a single atomic Edge Config PATCH.
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

interface RemoveBody {
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
  const CHAIN_API_URL = process.env.CHAIN_API_URL;

  if (!VERCEL_API_TOKEN || !EDGE_CONFIG_ID || !CHAIN_API_URL) {
    return jsonResponse(
      { error: 'Server misconfigured: missing VERCEL_API_TOKEN, EDGE_CONFIG_ID, or CHAIN_API_URL' },
      500,
    );
  }

  let body: RemoveBody;
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

  if (payload.action !== 'remove-approved' || !payload.operator_address) {
    return jsonResponse(
      { error: 'Invalid payload: expected { action: "remove-approved", operator_address }' },
      400,
    );
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

  // 3. Verify the signer is the current vote-manager on-chain.
  let voteManager: string;
  try {
    const resp = await fetch(`${CHAIN_API_URL}/shielded-vote/v1/vote-manager`);
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
    const data = await resp.json();
    voteManager = data.address ?? '';
  } catch (err) {
    return jsonResponse({ error: `Failed to query vote-manager: ${err}` }, 502);
  }

  if (voteManager !== signerAddress) {
    return jsonResponse(
      { error: `Signer ${signerAddress} is not the vote-manager (${voteManager})` },
      403,
    );
  }

  // 4. Remove from approved-servers.
  const approvedServers = (await get('approved-servers') as ServiceEntry[] | null) ?? [];
  const target = approvedServers.find((s) => s.operator_address === payload.operator_address);
  if (!target) {
    return jsonResponse(
      { error: `No approved server found for ${payload.operator_address}` },
      404,
    );
  }

  const updatedApproved = approvedServers.filter(
    (s) => s.operator_address !== payload.operator_address,
  );

  // 5. Remove from vote_servers in voting-config.
  const currentConfig = (await get('voting-config') as VotingConfig | null) ?? {
    version: 1,
    vote_servers: [],
    pir_servers: [],
  };

  currentConfig.vote_servers = currentConfig.vote_servers.filter(
    (s) => s.operator_address !== payload.operator_address && s.url !== target.url,
  );

  // 6. Remove from server-pulses.
  const pulses = (await get('server-pulses') as ServerPulses | null) ?? {};
  delete pulses[target.url];

  // 7. Atomic Edge Config PATCH.
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
            { operation: 'upsert', key: 'approved-servers', value: updatedApproved },
            { operation: 'upsert', key: 'voting-config', value: currentConfig },
            { operation: 'upsert', key: 'server-pulses', value: pulses },
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
    status: 'removed',
    operator_address: payload.operator_address,
    url: target.url,
  });
}
