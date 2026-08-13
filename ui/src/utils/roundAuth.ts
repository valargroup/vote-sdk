import type { AttestRoundEntryResponse, PirLayout } from "../api/chain";

// Round-auth v2 signed preimage: domain tag || round_id (32 raw bytes) ||
// ea_pk (32 bytes) || pir_depth || tier0_layers || tier1_layers || poly_len
// (each u32 LE). Must match internal/votingconfig.CanonicalPayloadV2 and the
// wallet-side (librustvoting) verifier byte-for-byte.
const ROUND_AUTH_DOMAIN_TAG_V2 = "zcash-shielded-vote:round-auth:v2";

function base64ToBytes(value: string): Uint8Array {
  const binary = atob(value);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

function hexToBytes(hex: string): Uint8Array {
  const bytes = new Uint8Array(hex.length / 2);
  for (let i = 0; i < bytes.length; i += 1) {
    bytes[i] = parseInt(hex.slice(i * 2, i * 2 + 2), 16);
  }
  return bytes;
}

function u32le(value: number): Uint8Array {
  const bytes = new Uint8Array(4);
  new DataView(bytes.buffer).setUint32(0, value, true);
  return bytes;
}

export function canonicalPayloadV2(
  roundIdHex: string,
  eaPKB64: string,
  layout: PirLayout
): Uint8Array {
  const tag = new TextEncoder().encode(ROUND_AUTH_DOMAIN_TAG_V2);
  const roundIdBytes = hexToBytes(roundIdHex);
  const eaPKBytes = base64ToBytes(eaPKB64);
  const boundBytes = new Uint8Array(16);
  boundBytes.set(u32le(layout.pir_depth), 0);
  boundBytes.set(u32le(layout.tier0_layers), 4);
  boundBytes.set(u32le(layout.tier1_layers), 8);
  boundBytes.set(u32le(layout.poly_len), 12);
  const payload = new Uint8Array(
    tag.length + roundIdBytes.length + eaPKBytes.length + boundBytes.length
  );
  payload.set(tag, 0);
  payload.set(roundIdBytes, tag.length);
  payload.set(eaPKBytes, tag.length + roundIdBytes.length);
  payload.set(boundBytes, tag.length + roundIdBytes.length + eaPKBytes.length);
  return payload;
}

export function assertMatchingRoundAuthV2Response(
  response: AttestRoundEntryResponse,
  expectedPayloadB64: string,
  expectedPayloadHash: string
): void {
  if (response.auth_version !== 2) {
    throw new Error(
      `sign-config-entry returned auth_version ${response.auth_version}; expected 2`
    );
  }
  if (response.canonical_payload_b64 !== expectedPayloadB64) {
    throw new Error(
      "sign-config-entry returned a canonical payload that does not bind the requested round authorization fields"
    );
  }
  if (response.signed_payload_hash !== expectedPayloadHash) {
    throw new Error(
      "sign-config-entry returned a signed payload hash that does not match the canonical payload"
    );
  }
}
