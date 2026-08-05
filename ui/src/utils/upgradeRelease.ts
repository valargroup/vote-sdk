export const REQUIRED_UPGRADE_PLATFORMS = ["linux/amd64", "linux/arm64"] as const;

export type UpgradePlatform = (typeof REQUIRED_UPGRADE_PLATFORMS)[number];

export interface ReleaseBinary {
  platform: UpgradePlatform;
  assetName: string;
  downloadUrl: string;
  digest: string;
  planUrl: string;
}

interface GitHubReleaseAsset {
  name?: unknown;
  browser_download_url?: unknown;
  digest?: unknown;
}

interface GitHubRelease {
  assets?: unknown;
}

const RELEASE_API_BASE = "https://api.github.com/repos/valargroup/vote-sdk/releases/tags";
const SHA256_DIGEST = /^sha256:[0-9a-f]{64}$/i;

function expectedAssetName(tag: string, platform: UpgradePlatform): string {
  return `shielded-vote-${tag}-cosmovisor-v1-${platform.replace("/", "-")}.tar.gz`;
}

function checksumUrl(downloadUrl: string, digest: string): string {
  const parsed = new URL(downloadUrl);
  if (parsed.protocol !== "https:") {
    throw new Error(`release asset URL must use HTTPS: ${downloadUrl}`);
  }
  const separator = downloadUrl.includes("?") ? "&" : "?";
  return `${downloadUrl}${separator}checksum=${digest}`;
}

export function resolveCosmovisorReleaseBinaries(
  tag: string,
  assetsValue: unknown,
): ReleaseBinary[] {
  const normalizedTag = tag.trim();
  if (!normalizedTag) throw new Error("Enter a release tag before loading binaries");
  if (!Array.isArray(assetsValue)) throw new Error("GitHub release response is missing assets");

  const assets = assetsValue as GitHubReleaseAsset[];
  return REQUIRED_UPGRADE_PLATFORMS.map((platform) => {
    const assetName = expectedAssetName(normalizedTag, platform);
    const asset = assets.find((candidate) => candidate.name === assetName);
    if (!asset) throw new Error(`Release ${normalizedTag} is missing ${assetName}`);
    if (typeof asset.browser_download_url !== "string" || !asset.browser_download_url) {
      throw new Error(`${assetName} is missing its download URL`);
    }
    if (typeof asset.digest !== "string" || !SHA256_DIGEST.test(asset.digest)) {
      throw new Error(`${assetName} is missing a valid SHA-256 digest`);
    }

    return {
      platform,
      assetName,
      downloadUrl: asset.browser_download_url,
      digest: asset.digest.toLowerCase(),
      planUrl: checksumUrl(asset.browser_download_url, asset.digest.toLowerCase()),
    };
  });
}

export async function fetchCosmovisorReleaseBinaries(tag: string): Promise<ReleaseBinary[]> {
  const normalizedTag = tag.trim();
  if (!normalizedTag) throw new Error("Enter a release tag before loading binaries");

  const response = await fetch(`${RELEASE_API_BASE}/${encodeURIComponent(normalizedTag)}`, {
    headers: { Accept: "application/vnd.github+json" },
  });
  if (!response.ok) {
    throw new Error(`GitHub release ${normalizedTag} returned HTTP ${response.status}`);
  }
  const release = (await response.json()) as GitHubRelease;
  return resolveCosmovisorReleaseBinaries(normalizedTag, release.assets);
}

export function releaseBinariesMap(
  binaries: ReleaseBinary[],
  selectedPlatforms: readonly UpgradePlatform[],
): Record<string, string> {
  const selected = new Set(selectedPlatforms);
  return Object.fromEntries(
    binaries
      .filter((binary) => selected.has(binary.platform))
      .map((binary) => [binary.platform, binary.planUrl]),
  );
}

export function validateUpgradeInfoJson(infoJson: string, expectedTag: string): string {
  let value: unknown;
  try {
    value = JSON.parse(infoJson);
  } catch (err) {
    return err instanceof Error ? err.message : String(err);
  }
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    return "Info JSON must be an object";
  }

  const info = value as Record<string, unknown>;
  const normalizedTag = expectedTag.trim();
  if (!normalizedTag) return "Enter a release tag";
  if (info.tag !== normalizedTag) return `Info JSON tag must be ${normalizedTag}`;

  const binaries = info.binaries;
  if (!binaries || typeof binaries !== "object" || Array.isArray(binaries)) {
    return "Info JSON must include checksum-pinned binaries";
  }

  const binaryMap = binaries as Record<string, unknown>;
  for (const platform of REQUIRED_UPGRADE_PLATFORMS) {
    const rawUrl = binaryMap[platform];
    if (typeof rawUrl !== "string" || !rawUrl) {
      return `Info JSON is missing binaries.${platform}`;
    }
    let parsed: URL;
    try {
      parsed = new URL(rawUrl);
    } catch {
      return `binaries.${platform} must be a valid URL`;
    }
    if (parsed.protocol !== "https:") {
      return `binaries.${platform} must use HTTPS`;
    }
    const checksums = parsed.searchParams.getAll("checksum");
    if (checksums.length !== 1 || !SHA256_DIGEST.test(checksums[0] ?? "")) {
      return `binaries.${platform} must include one checksum=sha256:<64 hex> value`;
    }
  }

  return "";
}
