import { ed25519 } from "@noble/curves/ed25519.js";
import { hkdf } from "@noble/hashes/hkdf.js";
import { sha256 } from "@noble/hashes/sha2.js";

export const KEPLR_DERIVATION_PURPOSE = "shielded-vote/ea-pk-signer/v1";
const ED25519_SEED_SALT = sha256(
  new TextEncoder().encode("shielded-vote/ed25519-seed/v1")
);

const ED25519_PKCS8_PREFIX = new Uint8Array([
  0x30, 0x2e, 0x02, 0x01, 0x00, 0x30, 0x05, 0x06, 0x03, 0x2b, 0x65, 0x70,
  0x04, 0x22, 0x04, 0x20,
]);

export interface VotingKeyInfo {
  signerId: string;
  seedB64: string;
  publicKeyB64: string;
  createdAt: string;
}

export type GeneratedVotingKeyInfo = VotingKeyInfo;

export interface ImportedVotingKeyInfo extends VotingKeyInfo {
  importedAs: "seed" | "private_key";
}

export interface KeplrDerivedVotingKeyInfo extends VotingKeyInfo {
  sourceAddress: string;
  chainId: string;
}

export function bytesToBase64(bytes: Uint8Array): string {
  let binary = "";
  for (const byte of bytes) binary += String.fromCharCode(byte);
  return btoa(binary);
}

export function base64ToBytes(value: string): Uint8Array {
  const binary = atob(value);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i += 1) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

function publicKeyFromSeed(seed: Uint8Array): string {
  return bytesToBase64(ed25519.getPublicKey(seed));
}

export function deriveEd25519FromKeplrSignature(params: {
  address: string;
  chainId: string;
  signatureB64: string;
}): KeplrDerivedVotingKeyInfo {
  const address = params.address.trim();
  const chainId = params.chainId.trim();
  if (!address) throw new Error("Keplr address is required");
  if (!chainId) throw new Error("Keplr chain ID is required");

  const signatureBytes = base64ToBytes(params.signatureB64.trim());
  if (signatureBytes.length !== 64) {
    throw new Error("Keplr signArbitrary signature must decode to 64 bytes");
  }

  const info = new TextEncoder().encode(`${chainId}|${address}`);
  const seed = hkdf(sha256, signatureBytes, ED25519_SEED_SALT, info, 32);
  return {
    signerId: `keplr:${address}`,
    seedB64: bytesToBase64(seed),
    publicKeyB64: publicKeyFromSeed(seed),
    createdAt: new Date().toISOString(),
    sourceAddress: address,
    chainId,
  };
}

export async function deriveEd25519FromKeplr(
  address: string,
  chainId: string,
  signArbitrary: (data: string) => Promise<{ signature: string }>
): Promise<KeplrDerivedVotingKeyInfo> {
  const { signature } = await signArbitrary(KEPLR_DERIVATION_PURPOSE);
  return deriveEd25519FromKeplrSignature({
    address,
    chainId,
    signatureB64: signature,
  });
}

function bytesEqual(a: Uint8Array, b: Uint8Array): boolean {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i += 1) diff |= a[i] ^ b[i];
  return diff === 0;
}

function seedFromKeyMaterial(keyMaterialB64: string): {
  seed: Uint8Array;
  importedAs: ImportedVotingKeyInfo["importedAs"];
} {
  const keyMaterial = base64ToBytes(keyMaterialB64.trim());
  if (keyMaterial.length === 32) {
    return { seed: keyMaterial, importedAs: "seed" };
  }

  if (keyMaterial.length === 64) {
    const seed = keyMaterial.slice(0, 32);
    const expectedPublicKey = keyMaterial.slice(32);
    const actualPublicKey = ed25519.getPublicKey(seed);
    if (!bytesEqual(expectedPublicKey, actualPublicKey)) {
      throw new Error("64-byte private key public half does not match its seed");
    }
    return { seed, importedAs: "private_key" };
  }

  throw new Error("Key material must decode to either 32-byte seed or 64-byte Ed25519 private key");
}

function pkcs8FromSeed(seed: Uint8Array): Uint8Array {
  const pkcs8 = new Uint8Array(ED25519_PKCS8_PREFIX.length + seed.length);
  pkcs8.set(ED25519_PKCS8_PREFIX);
  pkcs8.set(seed, ED25519_PKCS8_PREFIX.length);
  return pkcs8;
}

function arrayBufferFromBytes(bytes: Uint8Array): ArrayBuffer {
  return bytes.buffer.slice(
    bytes.byteOffset,
    bytes.byteOffset + bytes.byteLength
  ) as ArrayBuffer;
}

async function signWithWebCrypto(seed: Uint8Array, payload: Uint8Array): Promise<Uint8Array> {
  const cryptoKey = await crypto.subtle.importKey(
    "pkcs8",
    arrayBufferFromBytes(pkcs8FromSeed(seed)),
    { name: "Ed25519" },
    false,
    ["sign"]
  );
  const sig = await crypto.subtle.sign(
    { name: "Ed25519" },
    cryptoKey,
    arrayBufferFromBytes(payload)
  );
  return new Uint8Array(sig);
}

export function generateKeypair(signerId: string): GeneratedVotingKeyInfo {
  const normalizedSignerId = signerId.trim();
  if (!normalizedSignerId) throw new Error("signer_id is required");

  const seed = new Uint8Array(32);
  crypto.getRandomValues(seed);
  const seedB64 = bytesToBase64(seed);
  return {
    signerId: normalizedSignerId,
    seedB64,
    publicKeyB64: publicKeyFromSeed(seed),
    createdAt: new Date().toISOString(),
  };
}

export function importSeed(seedB64: string, signerId: string): ImportedVotingKeyInfo {
  return importKeyMaterial(seedB64, signerId);
}

export function importKeyMaterial(
  keyMaterialB64: string,
  signerId: string
): ImportedVotingKeyInfo {
  const normalizedSignerId = signerId.trim();
  if (!normalizedSignerId) throw new Error("signer_id is required");

  const { seed, importedAs } = seedFromKeyMaterial(keyMaterialB64);

  return {
    signerId: normalizedSignerId,
    seedB64: bytesToBase64(seed),
    publicKeyB64: publicKeyFromSeed(seed),
    createdAt: new Date().toISOString(),
    importedAs,
  };
}

export async function signCanonicalPayload(
  payloadB64: string,
  key: VotingKeyInfo
): Promise<string> {
  const seed = base64ToBytes(key.seedB64);
  const payload = base64ToBytes(payloadB64);
  try {
    return bytesToBase64(await signWithWebCrypto(seed, payload));
  } catch {
    return bytesToBase64(ed25519.sign(payload, seed));
  }
}
