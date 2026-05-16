import { describe, expect, it } from "vitest";
import {
  getActiveRoundsFromList,
  getPrimaryActiveRoundFromList,
  getPublishedSnapshotManifestUrl,
  inferDefaultPirUrlFromHost,
  isActiveRoundStatus,
  LOCAL_PIR_URL,
  resolveDefaultPirUrl,
  shouldMigrateNullifierUrl,
  type ChainRound,
  type PublishedSnapshotManifest,
  type VotingConfig,
  validatePublishedSnapshotManifestShape,
} from "./chain";

const files = {
  "tier0.bin": { size: 1, sha256: "a".repeat(64) },
  "tier1.bin": { size: 2, sha256: "b".repeat(64) },
  "tier2.bin": { size: 3, sha256: "c".repeat(64) },
  "pir_root.json": { size: 4, sha256: "d".repeat(64) },
};

function manifest(patch: Partial<PublishedSnapshotManifest> = {}): PublishedSnapshotManifest {
  return {
    schema_version: 1,
    height: 2_800_000,
    created_at: "2026-05-14T00:00:00Z",
    files,
    ...patch,
  };
}

describe("default PIR URL resolution", () => {
  const stageConfig: VotingConfig = {
    version: 1,
    vote_servers: [],
    pir_endpoints: [
      { url: "https://stage.pir.valargroup.org", label: "PIR primary" },
      { url: "https://stage.pir-backup.valargroup.org", label: "PIR backup" },
    ],
  };

  it("prefers the first pir_endpoints entry from voting-config", () => {
    expect(resolveDefaultPirUrl(stageConfig, [])).toBe("https://stage.pir.valargroup.org");
  });

  it("infers staging from stage.* hostnames", () => {
    expect(inferDefaultPirUrlFromHost("https://stage.vote-chain-primary.valargroup.org")).toBe(
      "https://stage.pir.valargroup.org",
    );
  });

  it("infers production from prod.* hostnames", () => {
    expect(inferDefaultPirUrlFromHost("prod.vote-chain-primary.valargroup.org")).toBe(
      "https://prod.pir.valargroup.org",
    );
  });

  it("uses local PIR on localhost", () => {
    expect(inferDefaultPirUrlFromHost("http://localhost:5173")).toBe(LOCAL_PIR_URL);
    expect(resolveDefaultPirUrl(null, ["http://localhost:5173"])).toBe(LOCAL_PIR_URL);
  });

  it("returns null when config and host are unknown", () => {
    expect(resolveDefaultPirUrl(null, [])).toBeNull();
  });

  it("migrates away from deprecated fleet URLs once config resolves", () => {
    expect(
      shouldMigrateNullifierUrl(
        "https://pir.valargroup.org",
        "https://prod.pir.valargroup.org",
      ),
    ).toBe(true);
    expect(
      shouldMigrateNullifierUrl(
        "https://prod.pir.valargroup.org",
        "https://prod.pir.valargroup.org",
      ),
    ).toBe(false);
  });
});

describe("published snapshot validation", () => {
  it("builds canonical manifest URLs from the bucket base", () => {
    expect(getPublishedSnapshotManifestUrl(
      "https://vote.fra1.digitaloceanspaces.com/",
      2_800_000
    )).toBe("https://vote.fra1.cdn.digitaloceanspaces.com/snapshots/2800000/manifest.json");
  });

  it("leaves non-production precomputed bases unchanged", () => {
    expect(getPublishedSnapshotManifestUrl(
      "https://staging.example.com",
      2_800_000
    )).toBe("https://staging.example.com/snapshots/2800000/manifest.json");
  });

  it("accepts a valid manifest for the requested height", () => {
    expect(validatePublishedSnapshotManifestShape(manifest(), 2_800_000)).toEqual([]);
  });

  it("rejects non-object manifest payloads", () => {
    expect(validatePublishedSnapshotManifestShape(null, 2_800_000)).toEqual([
      "manifest must be an object",
    ]);
  });

  it("rejects manifests with the wrong schema or height", () => {
    expect(validatePublishedSnapshotManifestShape(
      manifest({ schema_version: 2, height: 2_800_010 }),
      2_800_000
    )).toEqual([
      "schema_version must be 1, got 2",
      "manifest height 2800010 does not match requested height 2800000",
    ]);
  });

  it("requires all files that nf-server bootstrap consumes", () => {
    const broken = manifest({
      files: {
        "tier0.bin": { size: 0, sha256: "not-a-sha" },
        "tier1.bin": files["tier1.bin"],
      },
    });

    expect(validatePublishedSnapshotManifestShape(broken, 2_800_000)).toEqual([
      "tier0.bin has invalid size",
      "tier0.bin has invalid sha256",
      "missing required file tier2.bin",
      "missing required file pir_root.json",
    ]);
  });
});

describe("active round helpers", () => {
  it("recognizes numeric and enum active statuses", () => {
    expect(isActiveRoundStatus(1)).toBe(true);
    expect(isActiveRoundStatus("1")).toBe(true);
    expect(isActiveRoundStatus("SESSION_STATUS_ACTIVE")).toBe(true);
    expect(isActiveRoundStatus("active")).toBe(true);
    expect(isActiveRoundStatus(3)).toBe(false);
    expect(isActiveRoundStatus("SESSION_STATUS_FINALIZED")).toBe(false);
  });

  it("returns all active rounds newest first and picks the newest primary", () => {
    const oldActive: ChainRound = {
      vote_round_id: "old",
      status: "SESSION_STATUS_ACTIVE",
      created_at_height: "10",
      vote_end_time: "100",
    };
    const newActive: ChainRound = {
      vote_round_id: "new",
      status: 1,
      created_at_height: "20",
      vote_end_time: "50",
    };
    const finalized: ChainRound = {
      vote_round_id: "done",
      status: "SESSION_STATUS_FINALIZED",
      created_at_height: "30",
      vote_end_time: "500",
    };

    expect(getActiveRoundsFromList([oldActive, finalized, newActive])).toEqual([
      newActive,
      oldActive,
    ]);
    expect(getPrimaryActiveRoundFromList([oldActive, finalized, newActive])).toBe(newActive);
  });
});
