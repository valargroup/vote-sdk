/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_CHAIN_URL?: string;
  readonly VITE_LIGHTWALLETD_RPC?: string;
  readonly VITE_NULLIFIER_URL?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
