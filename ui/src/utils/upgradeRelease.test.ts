import { describe, expect, it } from "vitest";
import {
  createScheduleUpgradeReview,
  ReleaseRequestGate,
  releaseBinariesMap,
  resolveCosmovisorReleaseBinaries,
  validateScheduleUpgradeReview,
  validateUpgradeInfoJson,
} from "./upgradeRelease";

const tag = "v1.1.0";
const amd64Digest = `sha256:${"a".repeat(64)}`;
const arm64Digest = `sha256:${"b".repeat(64)}`;
const assets = [
  {
    name: `shielded-vote-${tag}-cosmovisor-v1-linux-amd64.tar.gz`,
    browser_download_url: `https://github.com/valargroup/vote-sdk/releases/download/${tag}/amd64.tar.gz`,
    digest: amd64Digest,
  },
  {
    name: `shielded-vote-${tag}-cosmovisor-v1-linux-arm64.tar.gz`,
    browser_download_url: `https://github.com/valargroup/vote-sdk/releases/download/${tag}/arm64.tar.gz`,
    digest: arm64Digest,
  },
];
const validInfo = JSON.stringify({
  tag,
  binaries: releaseBinariesMap(resolveCosmovisorReleaseBinaries(tag, assets), [
    "linux/amd64",
    "linux/arm64",
  ]),
});

describe("resolveCosmovisorReleaseBinaries", () => {
  it("selects the two Cosmovisor archives and pins their SHA-256 digests", () => {
    const binaries = resolveCosmovisorReleaseBinaries(tag, assets);

    expect(binaries.map((binary) => binary.platform)).toEqual(["linux/amd64", "linux/arm64"]);
    expect(binaries[0]?.planUrl).toBe(
      `https://github.com/valargroup/vote-sdk/releases/download/${tag}/amd64.tar.gz?checksum=${amd64Digest}`,
    );
    expect(releaseBinariesMap(binaries, ["linux/arm64"])).toEqual({
      "linux/arm64": `https://github.com/valargroup/vote-sdk/releases/download/${tag}/arm64.tar.gz?checksum=${arm64Digest}`,
    });
  });

  it("rejects missing archives or digests", () => {
    expect(() => resolveCosmovisorReleaseBinaries(tag, assets.slice(0, 1))).toThrow(
      "missing shielded-vote-v1.1.0-cosmovisor-v1-linux-arm64.tar.gz",
    );
    expect(() =>
      resolveCosmovisorReleaseBinaries(tag, [
        { ...assets[0], digest: null },
        assets[1],
      ]),
    ).toThrow("missing a valid SHA-256 digest");
  });
});

describe("ReleaseRequestGate", () => {
  it("rejects responses invalidated by a tag change or newer request", () => {
    const gate = new ReleaseRequestGate();
    const firstRequest = gate.begin();

    gate.invalidate();
    expect(gate.isCurrent(firstRequest)).toBe(false);

    const secondRequest = gate.begin();
    const thirdRequest = gate.begin();
    expect(gate.isCurrent(secondRequest)).toBe(false);
    expect(gate.isCurrent(thirdRequest)).toBe(true);
  });
});

describe("validateUpgradeInfoJson", () => {
  it("accepts both checksum-pinned Linux binaries", () => {
    expect(validateUpgradeInfoJson(validInfo, tag)).toBe("");
  });

  it("rejects a stale tag, missing architecture, or unpinned URL", () => {
    expect(validateUpgradeInfoJson(validInfo, "v1.2.0")).toBe("Info JSON tag must be v1.2.0");
    expect(validateUpgradeInfoJson(JSON.stringify({ tag, binaries: {} }), tag)).toBe(
      "Info JSON is missing binaries.linux/amd64",
    );
    expect(validateUpgradeInfoJson(JSON.stringify({
      tag,
      binaries: {
        "linux/amd64": "https://example.com/amd64.tar.gz",
        "linux/arm64": "https://example.com/arm64.tar.gz",
      },
    }), tag)).toBe("binaries.linux/amd64 must include one checksum=sha256:<64 hex> value");
  });
});

describe("createScheduleUpgradeReview", () => {
  it("keeps the reviewed payload unchanged when the live form changes", () => {
    const form = {
      planName: " v1.1.0 ",
      height: 4_890_179,
      infoJson: validInfo,
      releaseTag: tag,
      replaceExisting: false,
      estimatedTimeMs: 1_786_377_600_000,
      requestedTimeMs: 1_786_377_600_000,
    };
    const review = createScheduleUpgradeReview(form);

    form.planName = "changed";
    form.height = 1;
    form.infoJson = JSON.stringify({ tag });

    expect(review.payload).toEqual({
      name: "v1.1.0",
      height: 4_890_179,
      info: validInfo,
      replaceExisting: false,
    });
    expect(Object.isFrozen(review)).toBe(true);
    expect(Object.isFrozen(review.payload)).toBe(true);
    expect(validateScheduleUpgradeReview(review, 4096)).toBe("");
  });
});
