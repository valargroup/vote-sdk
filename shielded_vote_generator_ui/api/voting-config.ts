import { get } from '@vercel/edge-config';

export const config = { runtime: 'edge' };

export default async function handler() {
  const votingConfig = await get('voting-config');

  if (!votingConfig) {
    return new Response(JSON.stringify({ error: 'voting-config not found in Edge Config' }), {
      status: 404,
      headers: { 'Content-Type': 'application/json' },
    });
  }

  return new Response(JSON.stringify(votingConfig), {
    headers: {
      'Content-Type': 'application/json',
      'Access-Control-Allow-Origin': '*',
      'Cache-Control': 'public, max-age=0, s-maxage=0',
    },
  });
}
