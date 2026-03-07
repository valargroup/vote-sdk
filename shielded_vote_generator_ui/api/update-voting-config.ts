// Authenticated edge function for updating the voting-config key in Edge Config.
//
// Auth model: vote-manager wallet signature. The caller sends the update payload
// signed by their wallet. This function verifies the secp256k1 signature, derives
// the signer's cosmos address, queries the chain to confirm they are the current
// vote-manager, and only then writes to Edge Config via the Vercel REST API.
//
// Required env vars (set in Vercel project settings):
//   VERCEL_API_TOKEN   — Vercel REST API token with Edge Config write access
//   EDGE_CONFIG_ID     — ID of the Edge Config store (ecfg_...)
//   CHAIN_API_URL      — Public URL of a chain node REST API (e.g. https://46-101-255-48.sslip.io)

import { secp256k1 } from '@noble/curves/secp256k1.js';
import { sha256 } from '@noble/hashes/sha2.js';
import { ripemd160 } from '@noble/hashes/legacy.js';
import { bech32 } from 'bech32';

export const config = { runtime: 'edge' };

const BECH32_PREFIX = 'sv';

// Reconstruct the amino sign doc used by Keplr's signArbitrary.
// The data is base64-encoded in the sign doc.
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
  // Amino signing uses sorted, deterministic JSON (keys sorted alphabetically at every level).
  // The above object literal has keys in sorted order already.
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

export default async function handler(req: Request) {
  // Handle CORS preflight.
  if (req.method === 'OPTIONS') {
    return new Response(null, { status: 204, headers: corsHeaders() });
  }

  if (req.method !== 'POST') {
    return new Response(JSON.stringify({ error: 'Method not allowed' }), {
      status: 405,
      headers: { 'Content-Type': 'application/json', ...corsHeaders() },
    });
  }

  const VERCEL_API_TOKEN = process.env.VERCEL_API_TOKEN;
  const EDGE_CONFIG_ID = process.env.EDGE_CONFIG_ID;
  const CHAIN_API_URL = process.env.CHAIN_API_URL;

  if (!VERCEL_API_TOKEN || !EDGE_CONFIG_ID || !CHAIN_API_URL) {
    return new Response(
      JSON.stringify({ error: 'Server misconfigured: missing VERCEL_API_TOKEN, EDGE_CONFIG_ID, or CHAIN_API_URL' }),
      { status: 500, headers: { 'Content-Type': 'application/json', ...corsHeaders() } },
    );
  }

  let body: { payload: unknown; signature: string; pubKey: string; signerAddress: string };
  try {
    body = await req.json();
  } catch {
    return new Response(JSON.stringify({ error: 'Invalid JSON body' }), {
      status: 400,
      headers: { 'Content-Type': 'application/json', ...corsHeaders() },
    });
  }

  const { payload, signature, pubKey, signerAddress } = body;
  if (!payload || !signature || !pubKey || !signerAddress) {
    return new Response(
      JSON.stringify({ error: 'Missing required fields: payload, signature, pubKey, signerAddress' }),
      { status: 400, headers: { 'Content-Type': 'application/json', ...corsHeaders() } },
    );
  }

  // 1. Verify the secp256k1 signature over the amino sign doc.
  const payloadStr = JSON.stringify(payload);
  const signBytes = makeSignArbitraryDoc(signerAddress, payloadStr);
  const msgHash = sha256(signBytes);
  const sigBytes = base64ToBytes(signature);
  const pubKeyBytes = base64ToBytes(pubKey);

  let sigValid = false;
  try {
    // secp256k1.verify expects a DER or compact signature. Keplr produces 64-byte compact sigs.
    // prehash: false because msgHash is already SHA-256 hashed — noble-curves v2
    // defaults to prehash: true which would double-hash.
    sigValid = secp256k1.verify(sigBytes, msgHash, pubKeyBytes, { prehash: false });
  } catch {
    sigValid = false;
  }

  if (!sigValid) {
    return new Response(JSON.stringify({ error: 'Invalid signature' }), {
      status: 401,
      headers: { 'Content-Type': 'application/json', ...corsHeaders() },
    });
  }

  // 2. Derive the address from the public key and verify it matches the claimed signer.
  const derivedAddress = pubkeyToAddress(pubKeyBytes);
  if (derivedAddress !== signerAddress) {
    return new Response(
      JSON.stringify({ error: 'Public key does not match signer address' }),
      { status: 401, headers: { 'Content-Type': 'application/json', ...corsHeaders() } },
    );
  }

  // 3. Query the chain to confirm the signer is the current vote-manager.
  let voteManager: string;
  try {
    const resp = await fetch(`${CHAIN_API_URL}/shielded-vote/v1/vote-manager`);
    if (!resp.ok) throw new Error(`HTTP ${resp.status}`);
    const data = await resp.json();
    voteManager = data.address ?? '';
  } catch (err) {
    return new Response(
      JSON.stringify({ error: `Failed to query vote-manager: ${err}` }),
      { status: 502, headers: { 'Content-Type': 'application/json', ...corsHeaders() } },
    );
  }

  if (voteManager !== signerAddress) {
    return new Response(
      JSON.stringify({ error: `Signer ${signerAddress} is not the vote-manager (${voteManager})` }),
      { status: 403, headers: { 'Content-Type': 'application/json', ...corsHeaders() } },
    );
  }

  // 4. Update Edge Config via the Vercel REST API.
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
            {
              operation: 'upsert',
              key: 'voting-config',
              value: payload,
            },
          ],
        }),
      },
    );

    if (!resp.ok) {
      const text = await resp.text();
      return new Response(
        JSON.stringify({ error: `Edge Config update failed: HTTP ${resp.status} – ${text}` }),
        { status: 502, headers: { 'Content-Type': 'application/json', ...corsHeaders() } },
      );
    }

    return new Response(JSON.stringify({ status: 'ok' }), {
      headers: { 'Content-Type': 'application/json', ...corsHeaders() },
    });
  } catch (err) {
    return new Response(
      JSON.stringify({ error: `Edge Config update failed: ${err}` }),
      { status: 502, headers: { 'Content-Type': 'application/json', ...corsHeaders() } },
    );
  }
}
