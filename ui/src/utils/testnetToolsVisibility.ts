export type TestnetToolsVisibility = "loading" | "visible" | "hidden";

const PRODUCTION_CHAIN_ID = "zvote-1";

// UX gating only: the batch tools stay hidden on production, but the real
// protection is the server-side vote-manager authorization on every action.
export function resolveTestnetToolsVisibility(
  chainId: string | null
): TestnetToolsVisibility {
  if (!chainId) return "loading";
  return chainId === PRODUCTION_CHAIN_ID ? "hidden" : "visible";
}
