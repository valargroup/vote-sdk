import { useState, useCallback, useEffect, useRef } from "react";
import type { OfflineDirectSigner } from "@cosmjs/proto-signing";
import {
  connectKeplr,
  connectWithPrivateKey,
  signArbitraryWithKey,
  signArbitraryWithKeplr,
} from "../api/wallet";
import type { ArbitrarySignature } from "../api/wallet";
import * as chainApi from "../api/chain";

type WalletSource = "keplr" | "privkey";

const SOURCE_KEY = "sv-wallet-source";
export const DEFAULT_DEV_KEY = "b7e910eded435dd4e19c581b9a0b8e65104dcc4ebca8a1d55aa5c803e72ba2ee";

// Tendermint RPC — defaults to same host as REST but on the standard RPC port.
const DEFAULT_RPC_URL = "http://localhost:26657";

function resolveUrls(): { restUrl: string; rpcUrl: string } {
  // In dev mode always use the Vite proxy origin for REST so /cosmos/* paths
  // are forwarded server-side, regardless of any stored chain URL.
  const restUrl = import.meta.env.DEV
    ? window.location.origin
    : chainApi.getChainUrl();

  return { restUrl, rpcUrl: DEFAULT_RPC_URL };
}

export interface UseWallet {
  address: string | null;
  signer: OfflineDirectSigner | null;
  source: WalletSource | null;
  connecting: boolean;
  error: string | null;
  connect: () => Promise<void>;
  connectDev: (privateKeyHex: string) => Promise<void>;
  disconnect: () => void;
  /** Sign arbitrary data using the connected wallet (Keplr or dev key). */
  signPayload: (data: string) => Promise<ArbitrarySignature>;
}

export function useWallet(): UseWallet {
  const [address, setAddress] = useState<string | null>(null);
  const [signer, setSigner] = useState<OfflineDirectSigner | null>(null);
  const [source, setSource] = useState<WalletSource | null>(null);
  const [connecting, setConnecting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Store the raw private key hex for dev-mode signArbitrary support.
  const privKeyRef = useRef<string | null>(null);

  const applyConnection = useCallback(
    (conn: { signer: OfflineDirectSigner; address: string }, src: WalletSource) => {
      setSigner(conn.signer);
      setAddress(conn.address);
      setSource(src);
      setError(null);
      localStorage.setItem(SOURCE_KEY, src);
    },
    [],
  );

  // Keplr connection
  const connect = useCallback(async () => {
    setConnecting(true);
    setError(null);
    try {
      const { restUrl, rpcUrl } = resolveUrls();
      const conn = await connectKeplr(restUrl, rpcUrl);
      applyConnection(conn, "keplr");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setConnecting(false);
    }
  }, [applyConnection]);

  // Dev private key connection
  const connectDev = useCallback(
    async (privateKeyHex: string) => {
      setConnecting(true);
      setError(null);
      try {
        const conn = await connectWithPrivateKey(privateKeyHex);
        privKeyRef.current = privateKeyHex;
        applyConnection(conn, "privkey");
      } catch (err) {
        setError(err instanceof Error ? err.message : String(err));
      } finally {
        setConnecting(false);
      }
    },
    [applyConnection],
  );

  const disconnect = useCallback(() => {
    setSigner(null);
    setAddress(null);
    setSource(null);
    setError(null);
    privKeyRef.current = null;
    localStorage.removeItem(SOURCE_KEY);
  }, []);

  // Sign arbitrary data for authenticated API calls (e.g. Edge Config updates).
  const signPayload = useCallback(
    async (data: string): Promise<ArbitrarySignature> => {
      if (!address) throw new Error("Wallet not connected");
      if (source === "privkey" && privKeyRef.current) {
        return signArbitraryWithKey(privKeyRef.current, address, data);
      }
      return signArbitraryWithKeplr(address, data);
    },
    [address, source],
  );

  // Auto-reconnect on page load: Keplr if previously used, otherwise default dev key.
  useEffect(() => {
    const saved = localStorage.getItem(SOURCE_KEY);
    if (saved === "keplr" && window.keplr) {
      connect();
    } else if (!saved) {
      connectDev(DEFAULT_DEV_KEY);
    }
  }, [connect, connectDev]);

  // Re-derive address when user switches Keplr accounts.
  useEffect(() => {
    if (source !== "keplr") return;

    const handler = () => { connect(); };
    window.addEventListener("keplr_keystorechange", handler);
    return () => window.removeEventListener("keplr_keystorechange", handler);
  }, [source, connect]);

  return {
    address,
    signer,
    source,
    connecting,
    error,
    connect,
    connectDev,
    disconnect,
    signPayload,
  };
}
