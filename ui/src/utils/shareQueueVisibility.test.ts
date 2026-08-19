import { describe, expect, it } from "vitest";
import { resolveShareQueueVisibility } from "./shareQueueVisibility";

const voteManager = "sv1votemanager";

describe("share queue visibility", () => {
  it("always shows the stage monitor", () => {
    expect(resolveShareQueueVisibility({
      chainId: "svote-1",
      walletAddress: null,
      voteManagerAddresses: null,
    })).toBe("visible");
  });

  it("shows non-production development chains", () => {
    expect(resolveShareQueueVisibility({
      chainId: "local-vote-1",
      walletAddress: null,
      voteManagerAddresses: null,
    })).toBe("visible");
  });

  it("waits until the chain is identified", () => {
    expect(resolveShareQueueVisibility({
      chainId: null,
      walletAddress: voteManager,
      voteManagerAddresses: [voteManager],
    })).toBe("loading");
  });

  it("hides the production monitor without a connected wallet", () => {
    expect(resolveShareQueueVisibility({
      chainId: "zvote-1",
      walletAddress: null,
      voteManagerAddresses: [voteManager],
    })).toBe("hidden");
  });

  it("waits for the production vote-manager query", () => {
    expect(resolveShareQueueVisibility({
      chainId: "zvote-1",
      walletAddress: voteManager,
      voteManagerAddresses: null,
    })).toBe("loading");
  });

  it("shows the production monitor to a current vote manager", () => {
    expect(resolveShareQueueVisibility({
      chainId: "zvote-1",
      walletAddress: voteManager,
      voteManagerAddresses: ["sv1other", voteManager],
    })).toBe("visible");
  });

  it("hides the production monitor from other wallets", () => {
    expect(resolveShareQueueVisibility({
      chainId: "zvote-1",
      walletAddress: "sv1viewer",
      voteManagerAddresses: [voteManager],
    })).toBe("hidden");
  });
});
