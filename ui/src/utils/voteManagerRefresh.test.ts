import { afterEach, describe, expect, it, vi } from "vitest";
import {
  startVoteManagerRefresh,
  VOTE_MANAGER_REFRESH_INTERVAL_MS,
  VOTE_MANAGER_RETRY_INTERVAL_MS,
} from "./voteManagerRefresh";

afterEach(() => {
  vi.useRealTimers();
});

describe("vote-manager refresh", () => {
  it("retries failures and periodically replaces successful snapshots", async () => {
    vi.useFakeTimers();
    const load = vi.fn()
      .mockRejectedValueOnce(new Error("unavailable"))
      .mockResolvedValueOnce(["sv1first"])
      .mockResolvedValueOnce(["sv1second"]);
    const onUpdate = vi.fn();
    const stop = startVoteManagerRefresh({
      endpoint: "https://prod.example",
      load,
      onUpdate,
    });

    await vi.advanceTimersByTimeAsync(0);
    expect(load).toHaveBeenCalledTimes(1);
    expect(onUpdate).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(VOTE_MANAGER_RETRY_INTERVAL_MS);
    expect(load).toHaveBeenCalledTimes(2);
    expect(onUpdate).toHaveBeenLastCalledWith({
      endpoint: "https://prod.example",
      addresses: ["sv1first"],
    });

    await vi.advanceTimersByTimeAsync(VOTE_MANAGER_REFRESH_INTERVAL_MS);
    expect(load).toHaveBeenCalledTimes(3);
    expect(onUpdate).toHaveBeenLastCalledWith({
      endpoint: "https://prod.example",
      addresses: ["sv1second"],
    });

    stop();
  });
});
