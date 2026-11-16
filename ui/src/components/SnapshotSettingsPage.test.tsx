// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { SnapshotSettingsPage } from "./SnapshotSettingsPage";
import * as chainApi from "../api/chain";
import { useUIConfig } from "../store/uiConfigContext";

vi.mock("../api/chain", () => ({
  getActiveRounds: vi.fn(async () => ({ rounds: [] })),
  getSnapshotStatus: vi.fn(async () => ({ phase: "serving", height: 3578540, num_ranges: 619172 })),
  getLocalSnapshotStatus: vi.fn(),
  validatePublishedSnapshotManifest: vi.fn(),
  getPublishedSnapshotManifestUrl: vi.fn(),
  prepareSnapshot: vi.fn(),
}));
vi.mock("../store/uiConfigContext", () => ({ useUIConfig: vi.fn() }));
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true });

let container: HTMLDivElement;
let root: Root;
const mockConfig = (publishedConfigLoaded: boolean) =>
  vi.mocked(useUIConfig).mockReturnValue({
    uiMode: "prod",
    devPIRControls: false,
    precomputedBaseURL: null,
    zcashNetwork: null,
    publishedConfig: null,
    uiConfigLoaded: true,
    publishedConfigLoaded,
    refreshPublishedConfig: async () => {},
  });

beforeEach(() => {
  vi.clearAllMocks();
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});
afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
});

it("waits for voting-config before reading PIR status", async () => {
  mockConfig(false);
  await act(async () => root.render(<SnapshotSettingsPage />));
  expect(chainApi.getSnapshotStatus).not.toHaveBeenCalled();

  mockConfig(true);
  await act(async () => root.render(<SnapshotSettingsPage />));
  // Once for the published snapshot card's served height, once for the status card.
  expect(chainApi.getSnapshotStatus).toHaveBeenCalledTimes(2);
  expect(container.textContent).toContain("Serving");
  expect(container.textContent).toContain("3,578,540");
});
