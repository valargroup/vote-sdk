// Statement version 1 acknowledges independently rebuilding the snapshot IMT
// and comparing its circuit root with the root committed by this round.
export const IMT_VERIFICATION_ACKNOWLEDGMENT = {
  acknowledged: true,
  statement_version: 1,
} as const;

export const IMT_VERIFICATION_GUIDE_URL =
  "https://github.com/valargroup/vote-nullifier-pir/blob/a0f7e6b3455000c59c13d711f413378bca3ebc1b/docs/verify-round-imt-ai.md";

export interface IMTVerificationRound {
  roundId: string;
  snapshotHeight?: string;
  circuitRoot?: string;
}

export function imtVerificationPrompt(roundId: string, chainId: string, network: string | null): string {
  return `Follow ${IMT_VERIFICATION_GUIDE_URL} to independently verify the IMT root for round ${roundId} on voting chain ${chainId || "(confirm with me)"}.
Zcash network: ${network || "confirm with me"}.
Use a voting node and independent snapshot trust source from my available context. Ask if either is missing.
Verify this exact round whether or not it is approved, attested, or endorsed. Run the full rebuild and round-ID check and print the results. Do not substitute a newer round.
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
