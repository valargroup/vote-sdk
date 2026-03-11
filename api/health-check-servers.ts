// Vercel cron: probe each vote_server and remove unreachable ones.
//
// Runs on a schedule (see vercel.json crons). Also callable manually via GET.
// Each server is probed with a request to /api/v1/status (helper server health).
// Servers that fail the probe are removed from the Edge Config voting-config.
//
// Required env vars:
//   VERCEL_API_TOKEN   — Vercel REST API token with Edge Config write access
//   EDGE_CONFIG_ID     — ID of the Edge Config store (ecfg_...)

import { get } from '@vercel/edge-config';

export const config = { runtime: 'edge' };

const PROBE_TIMEOUT_MS = 5_000; // 5s per server

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

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

async function probeServer(url: string): Promise<boolean> {
  try {
    const resp = await fetch(`${url}/api/v1/status`, {
      signal: AbortSignal.timeout(PROBE_TIMEOUT_MS),
    });
    return resp.ok;
  } catch {
    return false;
  }
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

  const currentConfig = (await get('voting-config') as VotingConfig | null);
  if (!currentConfig || currentConfig.vote_servers.length === 0) {
    return jsonResponse({ status: 'no_servers' });
  }

  // Probe all servers in parallel.
  const results = await Promise.all(
    currentConfig.vote_servers.map(async (server) => ({
      server,
      healthy: await probeServer(server.url),
    })),
  );

  const healthy = results.filter((r) => r.healthy).map((r) => r.server);
  const unhealthy = results.filter((r) => !r.healthy).map((r) => r.server);

  if (unhealthy.length === 0) {
    return jsonResponse({ status: 'all_healthy', count: healthy.length });
  }

  // Remove unhealthy servers from the config.
  const updatedConfig: VotingConfig = {
    ...currentConfig,
    vote_servers: healthy,
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
          items: [{ operation: 'upsert', key: 'voting-config', value: updatedConfig }],
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
    status: 'cleaned',
    removed: unhealthy.map((s) => ({ url: s.url, label: s.label })),
    remaining: healthy.map((s) => ({ url: s.url, label: s.label })),
  });
}
