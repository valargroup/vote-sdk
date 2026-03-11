// Read-only endpoint: returns the server-pulses map from Edge Config.

import { get } from '@vercel/edge-config';

export const config = { runtime: 'edge' };

export default async function handler() {
  const pulses = await get('server-pulses');

  return new Response(JSON.stringify(pulses ?? {}), {
    headers: {
      'Content-Type': 'application/json',
      'Access-Control-Allow-Origin': '*',
      'Cache-Control': 'public, max-age=0, s-maxage=0',
    },
  });
}
