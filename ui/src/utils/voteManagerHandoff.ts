export interface HandoffMessageInput {
  sv1Address: string;
  trustedKeyEntryJSON: string;
  staticConfigPath: string;
}

export function buildHandoffMessage({
  sv1Address,
  trustedKeyEntryJSON,
  staticConfigPath,
}: HandoffMessageInput): string {
  return [
    "Please add me as a Vote coordinator multisig member.",
    "",
    "On-chain address, for the chain coordinator policy vote_manager_addresses:",
    sv1Address,
    "",
    `Ed25519 trusted key entry, for token-holder-voting-config ${staticConfigPath} trusted_keys[]:`,
    trustedKeyEntryJSON,
  ].join("\n");
}

export interface TrustedKeyEntryInput {
  signerId: string;
  publicKeyB64: string;
  sourceAddress: string;
}

export function buildTrustedKeyEntry({
  signerId,
  publicKeyB64,
  sourceAddress,
}: TrustedKeyEntryInput): string {
  return JSON.stringify(
    {
      key_id: signerId,
      alg: "ed25519",
      pubkey: publicKeyB64,
      notes: `derived key for ${sourceAddress}`,
    },
    null,
    2
  );
}
