"""Public-observer linking of a small pool's reveals to its DC transaction.

All 16 reveals of a DC carry the same public label (0, d). For a delegate with a
single delegator, the observer therefore sees the full set of 16 reveal times.
Delays follow zcash_voting share_policy: uniform in [0, W) with
W = min(100 h, (vote_end - buffer) - t_dc), buffer = min(40% of round, 6 h).
A candidate DC tx at time t is consistent iff every reveal lies in [t, t + W(t)].
We count consistent DC txs among all DC txs of the round (Poisson arrivals).
"""
import random, statistics

H = 3600.0


def run(rate_per_h, round_days=7.0, trials=400, seed=1):
    rng = random.Random(seed)
    dur = round_days * 24 * H
    buffer = min(0.4 * dur, 6 * H)
    deadline = dur - buffer
    sets = []
    for _ in range(trials):
        # DC tx times over the delayed-share period
        ts = []
        t = 0.0
        while True:
            t += rng.expovariate(rate_per_h / H)
            if t >= deadline:
                break
            ts.append(t)
        if len(ts) < 2:
            continue
        target = rng.choice(ts)
        w = min(100 * H, deadline - target)
        reveals = [target + rng.random() * w for _ in range(16)]
        lo, hi = min(reveals), max(reveals)
        cand = 0
        for c in ts:
            wc = min(100 * H, deadline - c)
            if c <= lo and hi <= c + wc:
                cand += 1
        sets.append(cand)
    return sets


def main():
    print("dc_tx_rate_per_hour,median_anonymity_set,p10,frac_unique")
    for rate in (0.1, 0.25, 1, 4):
        s = sorted(run(rate))
        p10 = s[len(s) // 10]
        uniq = sum(1 for x in s if x == 1) / len(s)
        print(f"{rate},{statistics.median(s)},{p10},{uniq:.2f}")


if __name__ == "__main__":
    main()
