// Vercel cron: evict servers whose heartbeat pulse is stale (>6 hours).
//
// Safety net for when all servers are down simultaneously and nobody is
// pulsing to trigger the piggybacked eviction in server-heartbeat.ts.
// Only entries tracked in server-pulses are subject to eviction — servers
// added by register-validator that haven't adopted the pulse system are
// left untouched (the existing health-check-servers cron handles those).
//
// Required env vars:
//   VERCEL_API_TOKEN   — Vercel REST API token with Edge Config write access
//   EDGE_CONFIG_ID     — ID of the Edge Config store (ecfg_...)

import { get } from '@vercel/edge-config';

export const config = { runtime: 'edge' };

const STALE_PULSE_SECS = 21600; // 6 hours (3× the 2-hour pulse interval)

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

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

export default async function handler(req: Request) {
  if (req.method !== 'GET') {
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

  const pulses = (await get('server-pulses') as ServerPulses | null) ?? {};
  const currentConfig = (await get('voting-config') as VotingConfig | null);

  if (!currentConfig || Object.keys(pulses).length === 0) {
    return jsonResponse({ status: 'nothing_to_evict' });
  }

  const now = Math.floor(Date.now() / 1000);

  // Find stale URLs — those with a pulse older than STALE_PULSE_SECS.
  const staleUrls: string[] = [];
  const freshPulses: ServerPulses = {};

  for (const [url, pulseTime] of Object.entries(pulses)) {
    if (now - pulseTime > STALE_PULSE_SECS) {
      staleUrls.push(url);
    } else {
      freshPulses[url] = pulseTime;
    }
  }

  if (staleUrls.length === 0) {
    return jsonResponse({ status: 'all_fresh', tracked: Object.keys(pulses).length });
  }

  // Remove stale entries from vote_servers.
  const updatedConfig: VotingConfig = {
    ...currentConfig,
    vote_servers: currentConfig.vote_servers.filter(
      (s) => !staleUrls.includes(s.url),
    ),
  };

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
            { operation: 'upsert', key: 'voting-config', value: updatedConfig },
            { operation: 'upsert', key: 'server-pulses', value: freshPulses },
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
    status: 'evicted',
    removed: staleUrls,
    remaining: updatedConfig.vote_servers.map((s) => s.url),
  });
}
