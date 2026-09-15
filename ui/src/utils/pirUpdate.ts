import { sha256Hex, bytesToBase64 } from './attestEntry';
import { signCanonicalPayload, type VotingKeyInfo } from '../api/votingKey';
import keys from '../../../internal/pirupdate/keys.json';

export interface PIRPayload {
  config_sha256: string;
  linux_amd64_sha256: string;
  linux_arm64_sha256: string;
  snapshot_manifest_sha256: string;
  service_sha256: string;
}
export interface PIRProposal {
  scope: 'prod' | 'stage'; network: 'main' | 'test'; base_sha: string;
  current_config?: string; config: string; payload: PIRPayload;
}
export function pirSigningBytes(scope: string, p: PIRPayload): Uint8Array {
  if (scope !== 'prod' && scope !== 'stage') throw new Error('Invalid PIR scope');
  const hashes = [p.config_sha256, p.linux_amd64_sha256, p.linux_arm64_sha256,
    p.snapshot_manifest_sha256, p.service_sha256];
  if (!hashes.every(h => /^[0-9a-f]{64}$/.test(h))) throw new Error('Invalid artifact digest');
  return new TextEncoder().encode(`valargroup/pir-update/v1\n${scope}\n${hashes.join('\n')}\n`);
}
export async function validatePIRProposal(proposal: PIRProposal, scope: string, tag: string, height: number) {
  // The scope is derived from the connected chain, so a mismatch means the
  // wallet or endpoint points at a different environment than this svoted.
  if (proposal.scope !== scope) {
    throw new Error(
      `This dashboard authorizes the ${proposal.scope} environment, but the connected chain resolves to ${scope}. Connect to the matching chain and retry.`
    );
  }
  const cfg = JSON.parse(proposal.config);
  if (cfg.schema_version !== 1 || cfg.binary_tag !== tag || cfg.snapshot_height !== height ||
      Object.keys(cfg).sort().join(',') !== 'binary_tag,schema_version,snapshot_height') {
    throw new Error('Proposal does not match selected binary and snapshot');
  }
  if (await sha256Hex(new TextEncoder().encode(proposal.config)) !== proposal.payload.config_sha256) {
    throw new Error('Proposal digest does not match its config');
  }
  pirSigningBytes(scope, proposal.payload);
}
export async function signPIRProposal(proposal: PIRProposal, key: VotingKeyInfo) {
  const trusted = keys[proposal.scope].find(k => k.pubkey === key.publicKeyB64);
  if (!trusted) throw new Error('Connected coordinator key is not pinned for PIR updates in this environment');
  const sig = await signCanonicalPayload(bytesToBase64(pirSigningBytes(proposal.scope, proposal.payload)), key);
  return { schema_version: 1, payload: proposal.payload,
    signatures: [{key_id: trusted.key_id, alg: 'ed25519', sig}] };
}
