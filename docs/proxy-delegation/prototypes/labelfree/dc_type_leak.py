"""Label-free with a DISTINGUISHABLE DC reveal type (separate ZKP3R message/VK).

Public: per-(p,o) count of DC reveals, C(p,o) = 16 * sum_{d: route(d,p)=o} n_d.
EA coalition: per-(p,o) sum of decrypted DC reveals, V(p,o) = sum_{d: route(d,p)=o} pool_d.
Both are the same linear system in per-delegate unknowns. A delegate's count/total is
exactly recoverable iff e_d is in the row space of the route incidence matrix.
Explicit-abstain/unrouted pools contribute nothing (sentinel), as in pool_inference.py."""
import random, sys
from pool_inference import row_reduce, in_span

def trial(rng, P, D, K=3, route_p=0.85):
    rows = {}
    for d in range(D):
        for p in range(P):
            if rng.random() >= route_p or rng.random() < 0.08:
                continue
            o = rng.choices(range(K), weights=[55, 35, 10][:K])[0]
            rows.setdefault((p, o), [0] * D)[d] = 1
    basis = row_reduce(list(rows.values()), D)
    return sum(1 for d in range(D) if in_span(basis, [1 if i == d else 0 for i in range(D)]))

rng = random.Random(3)
print("P,delegates_with_pools,frac_exactly_solvable")
for P in (3, 10, 37):
    for D in (5, 10, 20, 40, 80, 200):
        if D > 120 and P < 37: pass
        tr = 20 if D <= 80 else 5
        s = sum(trial(rng, P, D) for _ in range(tr)) / tr
        print(f"{P},{D},{s/D:.2f}"); sys.stdout.flush()
