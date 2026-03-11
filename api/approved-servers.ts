// Read-only endpoint: returns the approved-servers list from Edge Config.

import { get } from '@vercel/edge-config';

export const config = { runtime: 'edge' };

export default async function handler() {
  const approved = await get('approved-servers');

  return new Response(JSON.stringify(approved ?? []), {
    headers: {
      'Content-Type': 'application/json',
      'Access-Control-Allow-Origin': '*',
      'Cache-Control': 'public, max-age=0, s-maxage=0',
    },
  });
}
