// Statement version 1 acknowledges independently rebuilding the snapshot IMT
// and comparing its circuit root with the root committed by this round.
export const IMT_VERIFICATION_ACKNOWLEDGMENT = {
  acknowledged: true,
  statement_version: 1,
} as const;

export const IMT_VERIFICATION_GUIDE_URL =
  "https://github.com/valargroup/vote-nullifier-pir/blob/main/docs/verify-round-imt-ai.md";

export interface IMTVerificationRound {
  roundId: string;
  snapshotHeight?: string;
  circuitRoot?: string;
}

export function imtVerificationPrompt(roundId: string, chainId: string, network: string | null): string {
  return `Follow ${IMT_VERIFICATION_GUIDE_URL} to reproduce PIR's IMT root for round ${roundId} on voting chain ${chainId || "(confirm with me)"}.
Zcash network: ${network || "confirm with me"}.
Use my supplied voting CometBFT RPC and lightwalletd endpoints for this environment. For any endpoint I have not supplied, use the matching public default in the guide without asking. Ask only if required inputs remain missing or the chain and network are unclear or inconsistent.
Verify this exact round whether or not it is approved, attested, or endorsed. Run the normal PIR sync and tree construction in a fresh directory at the exact snapshot height, compare the circuit root, and check the round ID. Print the results. Do not substitute a newer round.
Use the default pir-sync mode. Raw-block verification is optional and only needed if I explicitly request it.
No file upload is needed. Do not sign, attest, endorse, check the acknowledgment, or create a PR.`;
}

export function rootHex(base64: string | undefined): string {
  if (!base64) return "";
  try {
    const bytes = atob(base64);
    if (bytes.length !== 32) return "";
    return Array.from(bytes, (byte) => byte.charCodeAt(0).toString(16).padStart(2, "0")).join("");
  } catch {
    return "";
  }
}

// The verification target comes from the selected server, not a cached wallet connection.
// Local private-key signers have no stored chain ID and sign for the selected server.
export function imtVerificationChainError(chainId: string | null, walletChainId: string | null): string | null {
  if (!chainId) return "Waiting to identify the selected voting server's chain.";
  if (walletChainId && walletChainId !== chainId) return `The selected voting server is on ${chainId}, but the wallet is connected to ${walletChainId}. Reconnect the wallet before acknowledging verification.`;
  return null;
}
