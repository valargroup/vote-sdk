import { useState, useEffect, useCallback } from "react";

const RPC_STORAGE_KEY = "shielded-vote-rpc";
const BLOCK_TIME_SECONDS = 75;
const ANCHOR_INTERVAL = 1000;

export interface LightwalletdEndpoint {
  label: string;
  url: string;
  region: string;
}

// Mainnet lightwalletd gRPC endpoints (for future direct chain submission)
export const LIGHTWALLETD_ENDPOINTS: LightwalletdEndpoint[] = [
  { label: "Stardust US", url: "https://us.zec.stardust.rest", region: "US" },
  { label: "Stardust EU", url: "https://eu.zec.stardust.rest", region: "EU" },
  { label: "Stardust EU2", url: "https://eu2.zec.stardust.rest", region: "EU" },
  { label: "Stardust JP", url: "https://jp.zec.stardust.rest", region: "JP" },
  { label: "zec.rocks", url: "https://zec.rocks", region: "Global" },
  { label: "zec.rocks NA", url: "https://na.zec.rocks", region: "NA" },
  { label: "zec.rocks SA", url: "https://sa.zec.rocks", region: "SA" },
  { label: "zec.rocks EU", url: "https://eu.zec.rocks", region: "EU" },
  { label: "zec.rocks AP", url: "https://ap.zec.rocks", region: "AP" },
];

export const DEFAULT_RPC = import.meta.env.VITE_LIGHTWALLETD_RPC || LIGHTWALLETD_ENDPOINTS[0].url;

export function getStoredRpc(): string {
  return localStorage.getItem(RPC_STORAGE_KEY) || DEFAULT_RPC;
}

export function setStoredRpc(url: string) {
  localStorage.setItem(RPC_STORAGE_KEY, url);
}

export { ANCHOR_INTERVAL, BLOCK_TIME_SECONDS };

export interface ChainInfo {
  latestHeight: number | null;
  latestTimestamp: number | null; // unix seconds
  loading: boolean;
  error: string | null;
}

// Estimate the unix timestamp for a given block height based on
// the latest known block and 75s/block extrapolation.
export function estimateTimestamp(
  targetHeight: number,
  latestHeight: number,
  latestTimestamp: number
): Date {
  const blockDiff = targetHeight - latestHeight;
  const secondsDiff = blockDiff * BLOCK_TIME_SECONDS;
  return new Date((latestTimestamp + secondsDiff) * 1000);
}

// Snap a height value to the nearest anchor interval multiple.
export function snapToAnchorInterval(height: number): number {
  return Math.round(height / ANCHOR_INTERVAL) * ANCHOR_INTERVAL;
}

// Fetch latest block info from the Blockchair REST API.
// Lightwalletd endpoints are raw gRPC (not gRPC-web) and can't be
// called from a browser. Blockchair provides CORS-friendly REST.
async function fetchLatestBlock(): Promise<{ height: number; timestamp: number }> {
  const res = await fetch("https://api.blockchair.com/zcash/stats");
  if (!res.ok) {
    throw new Error(`Blockchair HTTP ${res.status}`);
  }
  const json = await res.json();
  const data = json.data;
  if (!data?.best_block_height || !data?.best_block_time) {
    throw new Error("Unexpected Blockchair response");
  }
  const height = data.best_block_height as number;
  // best_block_time is "YYYY-MM-DD HH:MM:SS" in UTC
  const timestamp = Math.floor(new Date(data.best_block_time + "Z").getTime() / 1000);
  return { height, timestamp };
}

export function useChainInfo(): ChainInfo & { refresh: () => void } {
  const [state, setState] = useState<ChainInfo>({
    latestHeight: null,
    latestTimestamp: null,
    loading: false,
    error: null,
  });

  const refresh = useCallback(() => {
    setState((s) => ({ ...s, loading: true, error: null }));
    fetchLatestBlock()
      .then(({ height, timestamp }) => {
        setState({
          latestHeight: height,
          latestTimestamp: timestamp,
          loading: false,
          error: null,
        });
      })
      .catch((err) => {
        setState((s) => ({
          ...s,
          loading: false,
          error: err instanceof Error ? err.message : "Failed to fetch",
        }));
      });
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh]);

  return { ...state, refresh };
}
