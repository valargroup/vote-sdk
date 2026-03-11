// Unauthenticated GET endpoint for fetching pending validator registrations.
// Returns entries from the pending-registrations Edge Config key, filtering
// out expired entries.

import { get } from '@vercel/edge-config';

export const config = { runtime: 'edge' };

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
    return new Response(null, {
      status: 204,
      headers: {
        'Access-Control-Allow-Origin': '*',
        'Access-Control-Allow-Methods': 'GET, OPTIONS',
        'Access-Control-Allow-Headers': 'Content-Type',
      },
    });
  }

  if (req.method !== 'GET') {
    return new Response(JSON.stringify({ error: 'Method not allowed' }), {
      status: 405,
      headers: { 'Content-Type': 'application/json' },
    });
  }

  const pending = (await get('pending-registrations') as PendingRegistration[] | null) ?? [];
  const now = Math.floor(Date.now() / 1000);
  const active = pending.filter((p) => p.expires_at > now);

  return new Response(JSON.stringify(active), {
    headers: {
      'Content-Type': 'application/json',
      'Access-Control-Allow-Origin': '*',
      'Cache-Control': 'public, max-age=0, s-maxage=0',
    },
  });
}
