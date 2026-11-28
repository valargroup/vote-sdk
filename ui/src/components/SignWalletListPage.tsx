import { useEffect, useRef, useState } from "react";
import { AlertCircle, ExternalLink, ListChecks, Wallet } from "lucide-react";
import * as chainApi from "../api/chain";
import { TOKEN_HOLDER_VOTING_CONFIG_REPO_URL, tokenHolderConfigFolder } from "../api/chain";
import * as votingKey from "../api/votingKey";
import { useWallet } from "../hooks/useWallet";
import { useDetectedChainId } from "../hooks/useDetectedChainId";
import { bytesToBase64, sha256Hex } from "../utils/attestEntry";
import {
  isSignatureKeyId,
  isWalletId,
  parseWalletListDraft,
  walletListDocument,
  walletListPayload,
  type WalletList,
} from "../utils/walletList";
import { CopyButton } from "./CopyButton";

/** The key id each environment's wallet builds pin for a list Valar signs (contracts-v1 §12.3). */
const DEFAULT_SIGNER_KEY_ID: Record<string, string> = {
  prod: "valargroup",
  stage: "valar-test-keplr-derived",
};

interface LoadedList {
  /** Tells loads apart, so a result never outlives the file it came from. */
  loadId: number;
  fileName: string;
  list: WalletList;
}

interface SignedList {
  context: string;
  document: string;
  issuedAt: bigint;
  publicKeyB64: string;
  payloadSha256: string;
}

/** Read a picked file as UTF-8 text, refusing invalid UTF-8. */
function readFileText(file: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => {
      try {
        resolve(new TextDecoder("utf-8", { fatal: true }).decode(new Uint8Array(reader.result as ArrayBuffer)));
      } catch {
        reject(new Error("The list is not valid UTF-8."));
      }
    };
    reader.onerror = () => reject(reader.error ?? new Error("Could not read the file."));
    reader.readAsArrayBuffer(file);
  });
}

/** Explain why the wallet and the selected voting server disagree, if they do. */
function chainMismatch(chainId: string | null, walletChainId: string | null): string | null {
  if (!chainId) return "Waiting to identify the selected voting server's chain.";
  if (walletChainId && walletChainId !== chainId) {
    return `The selected voting server is on ${chainId}, but the wallet is connected to ${walletChainId}. Reconnect the wallet before signing.`;
  }
  return null;
}

/**
 * Signs a wallet's list of curated delegates (contracts-v1 §12.3) with the
 * Keplr-derived Ed25519 key that attests rounds. The signer then commits the
 * result to <env>/wallets/<wallet_id>/list.json in the config repo by hand.
 * The signing time becomes the list's issued_at; everything else is signed
 * as loaded.
 */
export function SignWalletListPage() {
  const wallet = useWallet();
  const chainId = useDetectedChainId();
  const chainError = chainMismatch(chainId, wallet.chainId);
  const envFolder = tokenHolderConfigFolder(chainId);
  const [walletId, setWalletId] = useState("vizor");
  const [keyIdInput, setKeyIdInput] = useState<string | null>(null);
  const [loaded, setLoaded] = useState<LoadedList | null>(null);
  const [loadError, setLoadError] = useState("");
  const [signing, setSigning] = useState(false);
  const [error, setError] = useState("");
  const [signed, setSigned] = useState<SignedList | null>(null);

  const keyIdText = keyIdInput ?? (envFolder ? DEFAULT_SIGNER_KEY_ID[envFolder] : "");
  const keyId = keyIdText.trim();
  const list = loaded?.list ?? null;
  const listError = !list
    ? ""
    : list.wallet_id !== walletId
      ? `The list is for wallet ${list.wallet_id}, not ${walletId}.`
      : chainId && list.chain_id !== chainId
        ? `The list names chain ${list.chain_id}, but the selected voting server is on ${chainId}.`
        : "";
  const keplrConnected = !!wallet.address && wallet.source === "keplr" && !!wallet.chainId;
  const canSign =
    keplrConnected && !chainError && !!envFolder && !!list && !listError && isWalletId(walletId) && isSignatureKeyId(keyId) && !signing;
  // Everything a signature is bound to; a result shows only while these
  // inputs are unchanged.
  const context = JSON.stringify([chainId, chainApi.getApiBase(), wallet.address, wallet.chainId, walletId, keyId, loaded?.loadId]);
  const signedList = signed?.context === context ? signed : null;
  const contextRef = useRef(context);
  useEffect(() => {
    contextRef.current = context;
  }, [context]);
  const assertUnchanged = (captured: string) => {
    if (contextRef.current !== captured) {
      throw new Error("The list, wallet id, key id, wallet or chain changed while waiting for Keplr; start again.");
    }
  };
  const listPath = `${envFolder ?? "<env>"}/wallets/${walletId || "<wallet_id>"}`;

  const handleFile = async (file: File | undefined) => {
    setLoaded(null);
    setLoadError("");
    if (!file) return;
    try {
      const { list } = parseWalletListDraft(await readFileText(file));
      setLoaded({ loadId: Date.now() + Math.random(), fileName: file.name, list });
    } catch (err) {
      setLoadError(err instanceof Error ? err.message : String(err));
    }
  };

  const handleSign = async () => {
    const signingContext = context;
    setError("");
    setSigning(true);
    try {
      // The sign button is enabled only while canSign holds.
      const key = await votingKey.deriveEd25519FromKeplr(wallet.address!, wallet.chainId!, wallet.signKeplrPayload);
      assertUnchanged(signingContext);
      // Earlier signatures covered the old issued_at, so they are dropped.
      const toSign = { ...list!, issued_at: BigInt(Math.floor(Date.now() / 1000)) };
      const payload = walletListPayload(toSign);
      const sig = await votingKey.signCanonicalPayload(bytesToBase64(payload), key);
      const signedDocument = walletListDocument(toSign, [{ key_id: keyId, alg: "ed25519", sig }]);
      // Show only a document the wallet's parser reads.
      parseWalletListDraft(signedDocument);
      setSigned({
        context: signingContext,
        document: signedDocument,
        issuedAt: toSign.issued_at,
        publicKeyB64: key.publicKeyB64,
        payloadSha256: await sha256Hex(payload),
      });
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setSigning(false);
    }
  };

  const inputClass =
    "w-full px-3 py-2 bg-surface-2 border border-border-subtle rounded-lg text-xs text-text-primary placeholder:text-text-muted focus:outline-none focus:border-accent/50 font-mono";

  return (
    <div className="flex-1 overflow-y-auto">
      <div className="max-w-3xl mx-auto px-6 py-10 space-y-6">
        <div>
          <div className="flex items-center gap-2 mb-2">
            <ListChecks size={18} className="text-accent" />
            <h1 className="text-lg font-bold text-text-primary">Sign Wallet List</h1>
          </div>
          <p className="text-[11px] text-text-muted max-w-2xl">
            Signs a wallet&apos;s list of curated delegates with the same Keplr-derived Ed25519 key that attests
            rounds. Open a PR that puts the signed list at <code>{listPath}/list.json</code> in{" "}
            <a
              href={TOKEN_HOLDER_VOTING_CONFIG_REPO_URL}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1 text-accent hover:text-accent/80 underline-offset-2 hover:underline"
            >
              token-holder-voting-config
              <ExternalLink size={10} />
            </a>
            . Each wallet build pins the key ids and public keys it accepts for each chain. Load the list as
            JSON, signed or not; signing sets <code>issued_at</code> to now, so raise <code>version</code> in the
            file whenever anything in it changes.
          </p>
        </div>

        <section className="bg-surface-1 border border-border-subtle rounded-xl p-5 space-y-4">
          <div className="flex flex-col gap-2 md:flex-row md:items-center md:justify-between">
            <div className="flex items-center gap-2 min-w-0 text-[11px] text-text-primary font-mono break-all">
              <Wallet size={12} className="text-text-muted shrink-0" />
              {wallet.address ? `${wallet.address}${wallet.chainId ? ` (${wallet.chainId})` : ""}` : "No Keplr wallet connected"}
            </div>
            {wallet.address ? (
              <button
                onClick={wallet.disconnect}
                className="px-3 py-2 bg-surface-3 hover:bg-surface-1 text-text-secondary rounded-lg text-[11px] font-semibold cursor-pointer"
              >
                Disconnect
              </button>
            ) : (
              <button
                onClick={wallet.connect}
                disabled={wallet.connecting}
                className="px-3 py-2 bg-accent/90 hover:bg-accent text-surface-0 rounded-lg text-[11px] font-semibold cursor-pointer disabled:opacity-50"
              >
                {wallet.connecting ? "Connecting..." : "Connect Keplr"}
              </button>
            )}
          </div>
          {wallet.error && <p className="text-[10px] text-danger">{wallet.error}</p>}
          {chainError && <p className="text-[10px] text-warning">{chainError}</p>}
          {chainId && !envFolder && (
            <p className="text-[10px] text-warning">
              Chain {chainId} has no folder in the config repo; wallet lists exist for zvote-1 (prod) and svote-1 (stage).
            </p>
          )}

          <div className="grid gap-4 md:grid-cols-2">
            <div>
              <label className="block text-[11px] text-text-secondary mb-1" htmlFor="wallet-list-id">
                Wallet id
              </label>
              <input id="wallet-list-id" value={walletId} onChange={(e) => setWalletId(e.target.value.trim())} spellCheck={false} className={inputClass} />
              {walletId && !isWalletId(walletId) && (
                <p className="mt-1 text-[10px] text-danger">A lowercase letter, then up to 31 lowercase letters, digits or -.</p>
              )}
            </div>
            <div>
              <label className="block text-[11px] text-text-secondary mb-1" htmlFor="wallet-list-key-id">
                Signing key id <span className="text-text-muted">(as the wallet build pins it)</span>
              </label>
              <input id="wallet-list-key-id" value={keyIdText} onChange={(e) => setKeyIdInput(e.target.value)} spellCheck={false} className={inputClass} />
              {keyId && !isSignatureKeyId(keyId) && <p className="mt-1 text-[10px] text-danger">At most 64 bytes.</p>}
            </div>
          </div>

          <div>
            <label className="block text-[11px] text-text-secondary mb-1" htmlFor="wallet-list-file">
              List file
            </label>
            <input
              id="wallet-list-file"
              type="file"
              accept="application/json,.json"
              onChange={(e) => void handleFile(e.target.files?.[0])}
              className="block w-full text-[11px] text-text-secondary file:mr-3 file:px-3 file:py-1.5 file:rounded-md file:border-0 file:bg-surface-3 file:text-text-secondary"
            />
            {loadError && <p className="mt-1.5 text-[10px] text-danger">{loadError}</p>}
            {listError && <p className="mt-1.5 text-[10px] text-danger">{listError}</p>}
            {list && (
              <div className="mt-2 space-y-2">
                <p className="text-[10px] text-text-muted font-mono break-all">
                  {loaded?.fileName}: wallet {list.wallet_id}, chain {list.chain_id}, version {list.version},{" "}
                  {list.delegates.length} delegates, {list.reserved_names.length} reserved names
                </p>
                {/* Everything below is signed, so the signer sees all of it first. */}
                <p className="text-[10px] text-text-muted">
                  issued_at is set to the signing time, replacing the file&apos;s {list.issued_at}.
                </p>
                <p className="text-[10px] text-text-secondary font-mono break-all">
                  Reserved names: {list.reserved_names.length > 0 ? list.reserved_names.join(" ") : "none"}
                </p>
                <table className="w-full text-[10px] text-text-secondary">
                  <thead>
                    <tr className="text-left text-text-muted">
                      <th className="font-normal pr-2">Index</th>
                      <th className="font-normal pr-2">Account</th>
                      <th className="font-normal pr-2">Name</th>
                      <th className="font-normal">Reviewed key</th>
                    </tr>
                  </thead>
                  <tbody>
                    {list.delegates.map((row) => (
                      <tr key={row.index} className="align-top">
                        <td className="pr-2 font-mono">#{row.index}</td>
                        <td className="pr-2">
                          {row.provider === 1 ? "X" : "GitHub"} @{row.handle}
                        </td>
                        <td className="pr-2">{row.name}</td>
                        <td className="font-mono break-all">{row.dk}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </div>

          <div className="flex flex-col gap-2 md:flex-row md:items-center md:justify-between">
            <p className="text-[10px] text-text-muted">
              Signing asks Keplr to derive the Ed25519 key in flight and discards it after signing.
            </p>
            <button
              onClick={handleSign}
              disabled={!canSign}
              className="px-3 py-2 bg-accent/90 hover:bg-accent text-surface-0 rounded-lg text-[11px] font-semibold transition-colors cursor-pointer disabled:opacity-50"
            >
              {signing ? "Signing..." : "Sign List"}
            </button>
          </div>
        </section>

        {error && (
          <div className="flex items-start gap-2 bg-danger/10 border border-danger/30 rounded-lg p-3">
            <AlertCircle size={14} className="text-danger mt-0.5 shrink-0" />
            <p className="text-[11px] text-danger">{error}</p>
          </div>
        )}

        {signedList && (
          <section className="bg-surface-1 border border-border-subtle rounded-xl p-5 space-y-3">
            <div className="flex flex-wrap items-center justify-between gap-2">
              <h2 className="text-xs font-semibold text-text-primary">Signed list</h2>
              <CopyButton value={signedList.document} label="Copy JSON" />
            </div>
            <div className="space-y-1 text-[10px] text-text-muted font-mono break-all">
              <p>signer public key: {signedList.publicKeyB64}</p>
              <p>issued_at: {signedList.issuedAt}</p>
              <p>signed_payload_sha256: {signedList.payloadSha256}</p>
            </div>
            <pre className="max-h-96 text-[11px] text-text-primary font-mono whitespace-pre-wrap break-all bg-surface-2 rounded-lg px-3 py-2 overflow-auto">
              {signedList.document}
            </pre>
          </section>
        )}
      </div>
    </div>
  );
}
