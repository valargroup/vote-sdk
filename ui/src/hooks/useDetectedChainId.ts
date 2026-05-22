import { useEffect, useState } from "react";
import { fetchChainId } from "../api/chain";

/**
 * Fetch the connected chain id once on mount. Used by pages that need to
 * render env-specific links (prod/ vs stage/) before the user has connected
 * Keplr. Returns null while loading or when the request fails.
 */
export function useDetectedChainId(): string | null {
  const [chainId, setChainId] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    fetchChainId()
      .then((id) => {
        if (!cancelled) setChainId(id || null);
      })
      .catch(() => {
        if (!cancelled) setChainId(null);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  return chainId;
}
