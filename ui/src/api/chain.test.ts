import { describe, expect, it } from "vitest";
import {
  getPublishedSnapshotManifestUrl,
  type PublishedSnapshotManifest,
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
