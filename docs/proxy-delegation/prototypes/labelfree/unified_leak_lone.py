"""Variant of unified_leak.py with a heavy-tailed following (many lone
delegators): n_d ~ P(k) proportional to k^-2.2, k in 1..2000."""
import random, sys
import unified_leak as U

def powerlaw_follow(D, total_ignored, rng):
    ks = list(range(1, 2001)); w = [k ** -2.2 for k in ks]; s = sum(w)
    out = []
    for _ in range(D):
        x = rng.random() * s; acc = 0
        for k, wk in zip(ks, w):
            acc += wk
            if x <= acc: out.append(k); break
    return out

U.zipf_follow = powerlaw_follow
print("D P K q  V   | sd_known(LF) | W_d solvable labeled / LF | lone delegators with amount public per round labeled / LF (of lone)")
for D in [5, 10, 20]:
    for P in [10, 15, 37]:
        for K in [2, 3]:
            for q in [0.8, 0.95]:
                for V in [30, 300]:
                    r = U.run(D, P, K, q, V, 0.9, 0, 60, hash((D, P, K, q, V, 7)) & 0xffff)
                    print(f"{D:3d} {P:2d} {K} {q:.2f} {V:4d} | {r['sd']*100:5.1f}% | {r['W_lab']*100:5.1f}% / {r['W_lf']*100:5.1f}% | {r['lone_lab']:.2f} / {r['lone_lf']:.2f} (of {r['lone']:.2f})")
                    sys.stdout.flush()
