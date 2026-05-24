import { useEffect, useMemo, useState } from "react";
import {
  AlertCircle,
  ExternalLink,
  KeyRound,
  Wallet,
} from "lucide-react";
import * as votingKey from "../api/votingKey";
import { CopyButton } from "./CopyButton";
import type { UseWallet } from "../hooks/useWallet";
import { useDetectedChainId } from "../hooks/useDetectedChainId";
import {
  TOKEN_HOLDER_VOTING_CONFIG_REPO_URL,
  tokenHolderConfigFolder,
  tokenHolderConfigUrl,
} from "../api/chain";
import {
  buildHandoffMessage,
  buildTrustedKeyEntry,
} from "../utils/voteManagerHandoff";

interface DerivedKeyInfo {
  signerId: string;
  publicKeyB64: string;
  sourceAddress: string;
  chainId: string;
  createdAt: string;
}

const KEPLR_LINKS = {
  download: "https://www.keplr.app/get",
  chromeStore:
    "https://chromewebstore.google.com/detail/keplr/dmkamcknogkgcdfhhbddcghachkejeap",
  appStore: "https://apps.apple.com/us/app/keplr-wallet/id1567851089",
  playStore:
    "https://play.google.com/store/apps/details?id=com.chainapsis.keplr",
};

type KeplrPlatform = "ios" | "android" | "desktop";

function detectKeplrPlatform(): KeplrPlatform {
  if (typeof navigator === "undefined") return "desktop";
  const ua = navigator.userAgent;
  if (/iPad|iPhone|iPod/.test(ua)) return "ios";
  if (/Android/.test(ua)) return "android";
  return "desktop";
}

const ENV_LABELS: Record<"prod" | "stage", string> = {
  prod: "production",
  stage: "staging",
};

export function VoteManagerKeysPage({ wallet }: { wallet: UseWallet }) {
  const [derivedKeyInfo, setDerivedKeyInfo] = useState<DerivedKeyInfo | null>(null);
  const [derivingKey, setDerivingKey] = useState(false);
  const [error, setError] = useState("");
  const [deriveNotice, setDeriveNotice] = useState("");

  // Prefer the wallet's chain id once Keplr connects, otherwise fall back to
  // the chain id fetched from the configured REST endpoint on mount.
  const detectedChainId = useDetectedChainId();
  const chainId = wallet.chainId || detectedChainId;
  const env = tokenHolderConfigFolder(chainId);
  const envLabel = env ? ENV_LABELS[env] : null;
  const staticConfigPath = env
    ? `${env}/static-voting-config.json`
    : "static-voting-config.json (under prod/ or stage/)";
  const staticConfigUrl =
    tokenHolderConfigUrl({ file: "static", chainId }) ??
    TOKEN_HOLDER_VOTING_CONFIG_REPO_URL;

  const keplrConnected =
    !!wallet.address && wallet.source === "keplr" && !!wallet.chainId;

  const platform = useMemo(detectKeplrPlatform, []);
  const installLink = useMemo(() => {
    if (platform === "ios") {
      return {
        href: KEPLR_LINKS.appStore,
        storeName: "App Store",
        buttonLabel: "Install from App Store",
      };
    }
    if (platform === "android") {
      return {
        href: KEPLR_LINKS.playStore,
        storeName: "Google Play",
        buttonLabel: "Install from Google Play",
      };
    }
    return {
      href: KEPLR_LINKS.chromeStore,
      storeName: "Chrome Web Store",
      buttonLabel: "Install from Chrome Web Store",
    };
  }, [platform]);
  const isMobile = platform !== "desktop";

  // Clear derived key when user switches Keplr accounts.
  useEffect(() => {
    const clear = () => {
      setDerivedKeyInfo(null);
      setDeriveNotice("");
    };
    window.addEventListener("keplr_keystorechange", clear);
    return () => window.removeEventListener("keplr_keystorechange", clear);
  }, []);

  // Reset derived state if wallet disconnects or switches.
  useEffect(() => {
    if (!wallet.address) {
      setDerivedKeyInfo(null);
      setDeriveNotice("");
    }
  }, [wallet.address]);

  const handleDerive = async () => {
    setError("");
    setDeriveNotice("");
    if (!wallet.address || wallet.source !== "keplr" || !wallet.chainId) {
      setError("Connect Keplr before deriving the Ed25519 public key.");
      return;
    }
    setDerivingKey(true);
    try {
      const derived = await votingKey.deriveEd25519FromKeplr(
        wallet.address,
        wallet.chainId,
        wallet.signKeplrPayload
      );
      setDerivedKeyInfo({
        signerId: derived.signerId,
        publicKeyB64: derived.publicKeyB64,
        sourceAddress: derived.sourceAddress,
        chainId: derived.chainId,
        createdAt: derived.createdAt,
      });
      setDeriveNotice(
        "Signed the challenge. The same Ed25519 public key will appear every time you sign with this account."
      );
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setDerivingKey(false);
    }
  };

  const trustedKeyEntryJSON = useMemo(
    () =>
      derivedKeyInfo
        ? buildTrustedKeyEntry({
            signerId: derivedKeyInfo.signerId,
            publicKeyB64: derivedKeyInfo.publicKeyB64,
            sourceAddress: derivedKeyInfo.sourceAddress,
          })
        : "",
    [derivedKeyInfo]
  );

  const handoffMessage = useMemo(() => {
    if (!wallet.address || !derivedKeyInfo) return "";
    return buildHandoffMessage({
      sv1Address: wallet.address,
      trustedKeyEntryJSON,
      staticConfigPath,
    });
  }, [wallet.address, derivedKeyInfo, trustedKeyEntryJSON, staticConfigPath]);

  return (
    <div className="flex-1 overflow-y-auto">
      <div className="max-w-3xl mx-auto px-6 py-10 space-y-6">
        {/* Header */}
        <div>
          <div className="flex items-center gap-2 mb-2">
            <KeyRound size={18} className="text-accent" />
            <h1 className="text-lg font-bold text-text-primary">
              Vote Coordinator Keys
            </h1>
          </div>
          <p className="text-[11px] text-text-muted max-w-2xl">
            Use this page to generate the two public keys needed to become a
            Vote coordinator. Connect Keplr, sign a challenge,
            and copy the resulting handoff message.
          </p>
        </div>

        {/* Why both public keys are needed */}
        <section className="bg-surface-1 border border-border-subtle rounded-xl p-5 space-y-4">
          <h2 className="text-xs font-semibold text-text-primary">
            Why both public keys are needed
          </h2>
          <div className="grid gap-3 md:grid-cols-2">
            <div className="rounded-lg bg-surface-2 border border-border-subtle p-4 space-y-2">
              <p className="text-[11px] font-semibold text-text-primary">
                On-chain vote-manager address{" "}
                <span className="text-text-muted font-normal">(sv1…)</span>
              </p>
              <p className="text-[11px] text-text-secondary leading-relaxed">
                The Cosmos account address derived from Keplr&apos;s secp256k1
                key. It&apos;s added to the chain coordinator policy under{" "}
                <code className="text-[10px] bg-surface-3 px-1 py-0.5 rounded">
                  vote_manager_addresses
                </code>{" "}
                via{" "}
                <code className="text-[10px] bg-surface-3 px-1 py-0.5 rounded">
                  MsgUpdateVoteManagers
                </code>
                . This is the address that signs on-chain coordinator actions.
              </p>
            </div>
            <div className="rounded-lg bg-surface-2 border border-border-subtle p-4 space-y-2">
              <p className="text-[11px] font-semibold text-text-primary">
                Ed25519 trusted key{" "}
                <span className="text-text-muted font-normal">(public)</span>
              </p>
              <p className="text-[11px] text-text-secondary leading-relaxed">
                An off-chain attestation key derived from a Keplr-signed
                challenge. It&apos;s added to{" "}
                <code className="text-[10px] bg-surface-3 px-1 py-0.5 rounded">
                  trusted_keys[]
                </code>{" "}
                in{" "}
                <a
                  href={staticConfigUrl}
                  target="_blank"
                  rel="noreferrer"
                  className="inline-flex items-center gap-1 text-accent hover:text-accent/80 underline-offset-2 hover:underline"
                >
                  <code className="text-[10px] bg-surface-3 px-1 py-0.5 rounded">
                    {staticConfigPath}
                  </code>
                  <ExternalLink size={10} />
                </a>
                . Shipped wallets check this key before trusting round
                attestations you sign.
              </p>
            </div>
          </div>
        </section>

        {/* Install Keplr */}
        <section className="bg-surface-1 border border-border-subtle rounded-xl p-5 space-y-4">
          <h2 className="text-xs font-semibold text-text-primary">
            1 · Install Keplr
          </h2>
          <ol className="space-y-2 text-[11px] text-text-secondary list-decimal list-inside">
            {isMobile ? (
              <li>
                Install Keplr Mobile from the{" "}
                <a
                  href={installLink.href}
                  target="_blank"
                  rel="noreferrer"
                  className="inline-flex items-center gap-1 text-accent hover:text-accent/80 underline-offset-2 hover:underline"
                >
                  {installLink.storeName}
                  <ExternalLink size={10} />
                </a>
                , then open this page in the Keplr in-app browser. Already
                have it? Open Keplr Mobile, tap the browser tab, and paste
                this page&apos;s URL.
              </li>
            ) : (
              <li>
                Install the Keplr extension from the{" "}
                <a
                  href={installLink.href}
                  target="_blank"
                  rel="noreferrer"
                  className="inline-flex items-center gap-1 text-accent hover:text-accent/80 underline-offset-2 hover:underline"
                >
                  {installLink.storeName}
                  <ExternalLink size={10} />
                </a>
                {" "}(or see{" "}
                <a
                  href={KEPLR_LINKS.download}
                  target="_blank"
                  rel="noreferrer"
                  className="inline-flex items-center gap-1 text-accent hover:text-accent/80 underline-offset-2 hover:underline"
                >
                  keplr.app/get
                  <ExternalLink size={10} />
                </a>
                {" "}for Firefox / other browsers), <em>or</em> install Keplr
                Mobile and open this page in the Keplr in-app browser.
              </li>
            )}
            <li>Create a new wallet or import an existing one.</li>
            <li>Return to this page and click <strong>Connect Keplr</strong> below.</li>
            <li>
              If Keplr asks to add the Shielded-Vote chain, click{" "}
              <strong>Approve</strong>.
            </li>
          </ol>
          <div className="flex flex-wrap gap-2 pt-1">
            <a
              href={installLink.href}
              target="_blank"
              rel="noreferrer"
              className="inline-flex items-center gap-1.5 px-2.5 py-1.5 bg-accent/90 hover:bg-accent text-surface-0 rounded-md text-[11px] font-semibold transition-colors cursor-pointer"
            >
              <ExternalLink size={11} />
              {installLink.buttonLabel}
            </a>
          </div>
        </section>

        {/* Connect, sign, copy */}
        <section className="bg-surface-1 border border-border-subtle rounded-xl p-5 space-y-4">
          <h2 className="text-xs font-semibold text-text-primary">
            2 · Connect, sign, and copy
          </h2>

          <div className="flex flex-col gap-3 rounded-lg bg-surface-2 px-3 py-3">
            <div className="flex flex-col gap-2 md:flex-row md:items-center md:justify-between">
              <div className="min-w-0">
                <div className="flex items-center gap-1.5 text-[10px] text-text-muted">
                  <Wallet size={12} />
                  {keplrConnected ? "Keplr connected" : "Keplr not connected"}
                  {envLabel && chainId && (
                    <span className="ml-1 inline-flex items-center gap-1 rounded-full border border-accent/30 bg-accent/10 px-1.5 py-0.5 text-[9px] font-semibold text-accent">
                      {envLabel} · {chainId}
                    </span>
                  )}
                </div>
                <p className="text-[11px] text-text-primary font-mono break-all mt-1">
                  {wallet.address
                    ? `${wallet.address}${wallet.chainId ? ` (${wallet.chainId})` : ""}`
                    : "No wallet connected"}
                </p>
                {wallet.error && (
                  <p className="mt-1 text-[10px] text-danger">{wallet.error}</p>
                )}
              </div>
              <div className="flex gap-2 shrink-0">
                {wallet.address ? (
                  <button
                    onClick={wallet.disconnect}
                    className="px-3 py-2 bg-surface-3 hover:bg-surface-1 text-text-secondary rounded-lg text-[11px] font-semibold transition-colors cursor-pointer"
                  >
                    Disconnect
                  </button>
                ) : (
                  <button
                    onClick={wallet.connect}
                    disabled={wallet.connecting}
                    className="px-3 py-2 bg-accent/90 hover:bg-accent text-surface-0 rounded-lg text-[11px] font-semibold transition-colors cursor-pointer disabled:opacity-50"
                  >
                    {wallet.connecting ? "Connecting..." : "Connect Keplr"}
                  </button>
                )}
              </div>
            </div>
          </div>

          {keplrConnected && (
            <div className="flex flex-col gap-2 md:flex-row md:items-center md:justify-between rounded-lg bg-surface-2 px-3 py-3">
              <div className="min-w-0">
                <p className="text-[11px] text-text-primary font-semibold">
                  Sign challenge to derive Ed25519 trusted key
                </p>
                <p className="text-[10px] text-text-muted leading-relaxed mt-0.5">
                  Keplr will pop up asking you to sign a fixed challenge
                  string. The signature derives the Ed25519 public key in
                  memory and is discarded immediately — nothing about your
                  private key leaves Keplr.
                </p>
              </div>
              <button
                onClick={handleDerive}
                disabled={derivingKey}
                className="shrink-0 px-3 py-2 bg-accent/90 hover:bg-accent text-surface-0 rounded-lg text-[11px] font-semibold transition-colors cursor-pointer disabled:opacity-50"
              >
                {derivingKey
                  ? "Signing..."
                  : derivedKeyInfo
                    ? "Re-sign"
                    : "Sign with Keplr"}
              </button>
            </div>
          )}

          {deriveNotice && (
            <p className="text-[10px] text-success">{deriveNotice}</p>
          )}

          {derivedKeyInfo && (
            <div className="rounded-lg border border-accent/30 bg-accent/10 p-4 space-y-3">
              <div className="space-y-1">
                <p className="text-[10px] uppercase tracking-wider text-text-muted">
                  Send this to Valar Group
                </p>
                <p className="text-[10px] text-text-secondary leading-relaxed">
                  Paste this whole message into your trusted channel with
                  Valar Group. It contains your sv1 address (for the chain
                  coordinator policy) and the Ed25519{" "}
                  <a
                    href={staticConfigUrl}
                    target="_blank"
                    rel="noreferrer"
                    className="inline-flex items-center gap-1 text-accent hover:text-accent/80 underline-offset-2 hover:underline"
                  >
                    <code>trusted_keys[]</code>
                    <ExternalLink size={10} />
                  </a>{" "}
                  entry — both are public.
                </p>
              </div>
              <pre className="text-[11px] text-text-primary font-mono whitespace-pre-wrap break-all bg-surface-2 rounded-lg px-3 py-3 overflow-x-auto">
                {handoffMessage}
              </pre>
              <div className="flex flex-wrap items-center gap-2">
                <CopyButton
                  value={handoffMessage}
                  label="Copy handoff message"
                />
                <CopyButton
                  value={trustedKeyEntryJSON}
                  label="Copy trusted_keys entry only"
                />
              </div>
            </div>
          )}

          {error && (
            <div className="flex items-start gap-2 bg-danger/10 border border-danger/30 rounded-lg p-3">
              <AlertCircle size={14} className="text-danger mt-0.5 shrink-0" />
              <p className="text-[11px] text-danger">{error}</p>
            </div>
          )}
        </section>
      </div>
    </div>
  );
}
