import { tokenHolderConfigFolder } from "../api/chain";
import type { TokenHolderConfigFolder } from "../api/chain";

/**
 * Which environment the PIR update page is authorizing for.
 *
 * The scope is never chosen by the operator: svoted derives it server-side
 * from its own config URL and echoes it on every proposal. The UI resolves it
 * independently from the chain ID purely so the page can name its environment
 * before a proposal exists, and so a mismatch with the server can be caught.
 *
 * `unknown` is deliberately distinct from `loading`: an unrecognised chain ID
 * disables the page rather than defaulting to prod, because the scope selects
 * which pinned coordinator key may authorize the update.
 */
export type PIRUpdateScopeState =
  | { status: "loading" }
  | { status: "unknown"; chainId: string }
  | { status: "ready"; scope: TokenHolderConfigFolder; chainId: string };

export function resolvePIRUpdateScope(chainId: string | null | undefined): PIRUpdateScopeState {
  if (!chainId) return { status: "loading" };

  const scope = tokenHolderConfigFolder(chainId);
  if (!scope) return { status: "unknown", chainId };

  return { status: "ready", scope, chainId };
}

export const PIR_SCOPE_LABELS: Record<TokenHolderConfigFolder, string> = {
  prod: "production",
  stage: "staging",
};
