import * as chainApi from "../api/chain";
import * as votingKey from "../api/votingKey";
import {
  assertMatchingRoundAuthV2Response,
  canonicalPayloadV2,
} from "./roundAuth";

// Intentionally fixed at the authorization point: signing must not depend on a
// network fetch that could fail or change what the operator is authorizing.
// Update this constant together with the published config when it changes.
export const AUTHORIZATION_PIR_LAYOUT: chainApi.PirLayout = {
  pir_depth: 19,
  tier0_layers: 12,
  tier1_layers: 7,
  poly_len: 4096,
};

export function bytesToHex(bytes: Uint8Array): string {
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
}

export function base64ToBytes(value: string): Uint8Array {
  const binary = atob(value);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

export function bytesToBase64(bytes: Uint8Array): string {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

function arrayBufferFromBytes(bytes: Uint8Array): ArrayBuffer {
  return bytes.buffer.slice(
    bytes.byteOffset,
    bytes.byteOffset + bytes.byteLength
  ) as ArrayBuffer;
}

export async function sha256Hex(bytes: Uint8Array): Promise<string> {
  const digest = await crypto.subtle.digest("SHA-256", arrayBufferFromBytes(bytes));
  return bytesToHex(new Uint8Array(digest));
}

export function normalizeRoundId(value: string | undefined): string | null {
  if (!value) return null;
  const trimmed = value.trim();
  if (/^[0-9a-f]{64}$/.test(trimmed)) return trimmed;
  try {
    const hex = bytesToHex(base64ToBytes(trimmed));
    return /^[0-9a-f]{64}$/.test(hex) ? hex : null;
  } catch {
    return null;
  }
}

export function validateEaPK(value: string): boolean {
  try {
    return base64ToBytes(value.trim()).length === 32;
  } catch {
    return false;
  }
}

export interface SignedRoundEntry {
  entry: chainApi.ConfigRoundEntry;
  signedPayloadHash: string;
  usedLocalFallback: boolean;
}

export async function buildSignedRoundEntry(
  roundIdHex: string,
  eaPkB64: string,
  key: votingKey.VotingKeyInfo
): Promise<SignedRoundEntry> {
  const expectedPayload = canonicalPayloadV2(
    roundIdHex,
    eaPkB64,
    AUTHORIZATION_PIR_LAYOUT
  );
  const expectedResponse: chainApi.AttestRoundEntryResponse = {
    canonical_payload_b64: bytesToBase64(expectedPayload),
    signed_payload_hash: await sha256Hex(expectedPayload),
    auth_version: 2,
  };
  let response = expectedResponse;
  let usedLocalFallback = false;
  try {
    response = await chainApi.attestRoundEntry({
      round_id: roundIdHex,
      ea_pk: eaPkB64,
      auth_version: 2,
      pir_layout: AUTHORIZATION_PIR_LAYOUT,
    });
    assertMatchingRoundAuthV2Response(
      response,
      expectedResponse.canonical_payload_b64,
      expectedResponse.signed_payload_hash
    );
  } catch {
    // Local fallback builds the same round- and layout-bound v2 preimage;
    // it must never downgrade to the legacy raw-ea_pk (v1) payload.
    response = expectedResponse;
    usedLocalFallback = true;
  }
  const sigB64 = await votingKey.signCanonicalPayload(
    response.canonical_payload_b64,
    key
  );
  return {
    entry: {
      auth_version: 2,
      ea_pk: eaPkB64,
      signatures: [
        {
          key_id: key.signerId,
          alg: "ed25519",
          sig: sigB64,
        },
      ],
    },
    signedPayloadHash: response.signed_payload_hash,
    usedLocalFallback,
  };
}
