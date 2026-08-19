export const VOTE_MANAGER_REFRESH_INTERVAL_MS = 30_000;
export const VOTE_MANAGER_RETRY_INTERVAL_MS = 5_000;

export interface VoteManagerSnapshot {
  endpoint: string;
  addresses: string[];
}

/**
 * Refresh the current vote-manager set until stopped. Failed loads leave the
 * last successful snapshot unchanged and retry sooner than routine refreshes.
 */
export function startVoteManagerRefresh({
  endpoint,
  load,
  onUpdate,
}: {
  endpoint: string;
  load: () => Promise<string[]>;
  onUpdate: (snapshot: VoteManagerSnapshot) => void;
}): () => void {
  let stopped = false;
  let timer: ReturnType<typeof setTimeout> | null = null;

  const refresh = async () => {
    let delay = VOTE_MANAGER_RETRY_INTERVAL_MS;
    try {
      const addresses = await load();
      if (stopped) return;
      onUpdate({ endpoint, addresses });
      delay = VOTE_MANAGER_REFRESH_INTERVAL_MS;
    } catch {
      if (stopped) return;
    }
    timer = setTimeout(() => void refresh(), delay);
  };

  void refresh();
  return () => {
    stopped = true;
    if (timer !== null) clearTimeout(timer);
  };
}
