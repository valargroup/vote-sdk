export type ShareQueueVisibility = "loading" | "visible" | "hidden";

const PRODUCTION_CHAIN_ID = "zvote-1";

export function resolveShareQueueVisibility({
  chainId,
  walletAddress,
  voteManagerAddresses,
}: {
  chainId: string | null;
  walletAddress: string | null;
  voteManagerAddresses: string[] | null;
}): ShareQueueVisibility {
  if (!chainId) return "loading";
  if (chainId !== PRODUCTION_CHAIN_ID) return "visible";
  if (!walletAddress) return "hidden";
  if (!voteManagerAddresses) return "loading";

  return voteManagerAddresses.includes(walletAddress) ? "visible" : "hidden";
}
