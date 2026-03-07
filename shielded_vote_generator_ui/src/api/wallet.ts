// Wallet connection abstraction.
//
// Supports two sources:
//   1. Keplr browser extension (production path)
//   2. Raw secp256k1 private key via DirectSecp256k1Wallet (dev/testing)
//
// Both return an OfflineDirectSigner with identical getAccounts()/signDirect()
// interfaces, so the rest of the signing pipeline (cosmosTx.ts) is agnostic.

import { DirectSecp256k1Wallet } from "@cosmjs/proto-signing";
import type { OfflineDirectSigner } from "@cosmjs/proto-signing";
import { fromHex, toBase64 } from "@cosmjs/encoding";
import { sha256 } from "@noble/hashes/sha2.js";
import { secp256k1 } from "@noble/curves/secp256k1.js";
import type { KeplrChainInfo } from "../types/keplr";

const BECH32_PREFIX = "sv";

const COIN = {
  coinDenom: "SVOTE",
  coinMinimalDenom: "usvote",
  coinDecimals: 6,
};

export interface WalletConnection {
  signer: OfflineDirectSigner;
  address: string;
}

async function fetchChainId(restUrl: string): Promise<string> {
  const resp = await fetch(`${restUrl}/cosmos/base/tendermint/v1beta1/node_info`);
  if (!resp.ok) {
    throw new Error(`Failed to fetch chain ID: HTTP ${resp.status}`);
  }
  const data = await resp.json();
  return data.default_node_info?.network ?? "";
}

function buildChainInfo(chainId: string, restUrl: string, rpcUrl: string): KeplrChainInfo {
  return {
    chainId,
    chainName: "Shielded-Vote",
    rpc: rpcUrl,
    rest: restUrl,
    bip44: { coinType: 133 },
    bech32Config: {
      bech32PrefixAccAddr: BECH32_PREFIX,
      bech32PrefixAccPub: `${BECH32_PREFIX}pub`,
      bech32PrefixValAddr: `${BECH32_PREFIX}valoper`,
      bech32PrefixValPub: `${BECH32_PREFIX}valoperpub`,
      bech32PrefixConsAddr: `${BECH32_PREFIX}valcons`,
      bech32PrefixConsPub: `${BECH32_PREFIX}valconspub`,
    },
    currencies: [COIN],
    feeCurrencies: [
      {
        ...COIN,
        gasPriceStep: { low: 0, average: 0, high: 0 },
      },
    ],
    stakeCurrency: COIN,
    features: [],
  };
}

/**
 * Connect via the Keplr browser extension.
 *
 * `restUrl` should be the fully-qualified chain REST URL (e.g. http://localhost:1318).
 * For dev-mode proxy, pass the origin (window.location.origin) so that Keplr can
 * reach the node. `rpcUrl` is the Tendermint RPC endpoint (e.g. http://localhost:26657).
 */
// Stored after successful Keplr connection so signArbitrary can use it.
let keplrChainId = "";

export async function connectKeplr(restUrl: string, rpcUrl: string): Promise<WalletConnection> {
  if (!window.keplr) {
    throw new Error("Keplr extension not found. Please install Keplr to connect your wallet.");
  }

  const chainId = await fetchChainId(restUrl);
  if (!chainId) {
    throw new Error("Could not determine chain ID from the node.");
  }

  const chainInfo = buildChainInfo(chainId, restUrl, rpcUrl);
  await window.keplr.experimentalSuggestChain(chainInfo);
  await window.keplr.enable(chainId);
  keplrChainId = chainId;

  const signer = window.keplr.getOfflineSigner(chainId);
  const [account] = await signer.getAccounts();

  return { signer, address: account.address };
}

/**
 * Connect using a raw hex-encoded secp256k1 private key.
 * Intended for local development against a test chain.
 */
export async function connectWithPrivateKey(privateKeyHex: string): Promise<WalletConnection> {
  const privkey = fromHex(privateKeyHex);
  const signer = await DirectSecp256k1Wallet.fromKey(privkey, BECH32_PREFIX);
  const [account] = await signer.getAccounts();
  return { signer, address: account.address };
}

// ── signArbitrary ────────────────────────────────────────────────
//
// Signs an arbitrary string payload, compatible with Keplr's signArbitrary.
// Returns { signature, pubKey } as base64 strings for verification by the
// edge function.

export interface ArbitrarySignature {
  signature: string; // base64-encoded 64-byte compact secp256k1 sig
  pubKey: string;    // base64-encoded 33-byte compressed public key
}

/**
 * Reconstruct the amino sign doc used by Keplr's signArbitrary.
 * Keys must be alphabetically sorted at every level for determinism.
 */
function makeSignArbitraryDoc(signer: string, data: string): Uint8Array {
  const signDoc = {
    account_number: "0",
    chain_id: "",
    fee: { amount: [] as never[], gas: "0" },
    memo: "",
    msgs: [
      {
        type: "sign/MsgSignData",
        value: {
          data: btoa(data),
          signer: signer,
        },
      },
    ],
    sequence: "0",
  };
  return new TextEncoder().encode(JSON.stringify(signDoc));
}

/**
 * Sign arbitrary data using a raw private key (dev/testing path).
 * Produces the same format as Keplr's signArbitrary.
 */
export function signArbitraryWithKey(
  privateKeyHex: string,
  signerAddress: string,
  data: string,
): ArbitrarySignature {
  const privKey = fromHex(privateKeyHex);
  const signBytes = makeSignArbitraryDoc(signerAddress, data);
  const msgHash = sha256(signBytes);

  const sig = secp256k1.sign(msgHash, privKey, { prehash: false });
  const pubKey = secp256k1.getPublicKey(privKey, true); // compressed

  return {
    signature: toBase64(sig),
    pubKey: toBase64(pubKey),
  };
}

/**
 * Sign arbitrary data using Keplr's built-in signArbitrary.
 */
export async function signArbitraryWithKeplr(
  signerAddress: string,
  data: string,
): Promise<ArbitrarySignature> {
  if (!window.keplr) {
    throw new Error("Keplr not available");
  }
  // Use the chain ID from the last successful connection. Keplr requires the
  // chain to be enabled before signArbitrary works.
  const result = await window.keplr.signArbitrary(keplrChainId, signerAddress, data);
  return {
    signature: result.signature,
    pubKey: result.pub_key.value,
  };
}
