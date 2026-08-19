import { useEffect, useState, useSyncExternalStore } from "react";
import {
  fetchChainId,
  getChainUrl,
  subscribeToChainUrlChanges,
} from "../api/chain";

const CHAIN_ID_RETRY_INTERVAL_MS = 5_000;

interface ChainDetection {
  endpoint: string;
  chainId: string | null;
}

/** Return the currently selected voting-chain REST endpoint. */
export function useSelectedChainUrl(): string {
  return useSyncExternalStore(
    subscribeToChainUrlChanges,
    getChainUrl,
    () => "",
  );
}

/**
 * Detect the selected endpoint's chain id. Used by pages that need to render
 * environment-specific behavior before the user has connected Keplr. Returns
 * null while loading and retries transient failures.
 */
export function useDetectedChainId(): string | null {
  const endpoint = useSelectedChainUrl();
  const [detection, setDetection] = useState<ChainDetection>({
    endpoint,
    chainId: null,
  });

  useEffect(() => {
    let cancelled = false;
    let retryTimer: number | undefined;

    const detect = () => {
      fetchChainId(endpoint)
        .then((id) => {
          if (!cancelled) setDetection({ endpoint, chainId: id });
        })
        .catch(() => {
          if (cancelled) return;
          setDetection({ endpoint, chainId: null });
          retryTimer = window.setTimeout(detect, CHAIN_ID_RETRY_INTERVAL_MS);
        });
    };

    detect();
    return () => {
      cancelled = true;
      if (retryTimer !== undefined) window.clearTimeout(retryTimer);
    };
  }, [endpoint]);

  return detection.endpoint === endpoint ? detection.chainId : null;
}
