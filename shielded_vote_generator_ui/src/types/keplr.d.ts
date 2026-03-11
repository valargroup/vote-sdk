import type { OfflineDirectSigner } from "@cosmjs/proto-signing";

interface KeplrChainInfo {
  chainId: string;
  chainName: string;
  rpc: string;
  rest: string;
  bip44: { coinType: number };
  bech32Config: {
    bech32PrefixAccAddr: string;
    bech32PrefixAccPub: string;
    bech32PrefixValAddr: string;
    bech32PrefixValPub: string;
    bech32PrefixConsAddr: string;
    bech32PrefixConsPub: string;
  };
  currencies: Array<{
    coinDenom: string;
    coinMinimalDenom: string;
    coinDecimals: number;
  }>;
  feeCurrencies: Array<{
    coinDenom: string;
    coinMinimalDenom: string;
    coinDecimals: number;
    gasPriceStep?: { low: number; average: number; high: number };
  }>;
  stakeCurrency: {
    coinDenom: string;
    coinMinimalDenom: string;
    coinDecimals: number;
  };
  features?: string[];
}

interface KeplrSignArbitraryResult {
  pub_key: { type: string; value: string };
  signature: string;
}

interface Keplr {
  experimentalSuggestChain(chainInfo: KeplrChainInfo): Promise<void>;
  enable(chainId: string): Promise<void>;
  getOfflineSigner(chainId: string): OfflineDirectSigner;
  signArbitrary(chainId: string, signer: string, data: string): Promise<KeplrSignArbitraryResult>;
}

declare global {
  interface Window {
    keplr?: Keplr;
  }
}

export type { Keplr, KeplrChainInfo };
