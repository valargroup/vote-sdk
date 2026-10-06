"""Compact headline tables from results.jsonl: median (mean, p90) in %."""
import json
from collections import defaultdict
import numpy as np
from sim import SCENARIOS

g = defaultdict(list)
for l in open("results.jsonl"):
    r = json.loads(l)
    g[(r["scenario"], r["s"], r["variant"], r["turnout"])].append(r)

def f(rs, k):
    """median (mean, p90) of metric k, in %."""
    v = np.array([r[k] for r in rs if r[k] is not None], dtype=float) * 100
    return f"{np.median(v):.0f} ({v.mean():.1f}, {np.percentile(v, 90):.0f})"

print("| scenario (N/D/P) | s | pools det., no turnout eqs | pools det., with turnout eqs | lone-delegator pools det. | delegators exposed | rounds with a lone delegator exposed | direct voters exposed: lone, no pools | direct voters exposed: lone, D solvable |")
print("|---|---|---|---|---|---|---|---|---|")
for sc, p in SCENARIOS.items():
    for s in (0.0, 0.1, 0.3):
        a = g[(sc, s, "baseline", False)]; b = g[(sc, s, "baseline", True)]
        lab = f"{sc} ({p['N']}/{p['D']}/{p['P']}{', K=2' if 'K' in p else ''})"
        print(f"| {lab} | {s} | {f(a,'pools_det')} | {f(b,'pools_det')} | {f(b,'lone_pools_det')} | "
              f"{f(b,'delegators_exposed')} | {100*np.mean([r['any_lone_exposed'] for r in b]):.0f}% | "
              f"{f(b,'direct_exposed_simple')} | {f(b,'direct_exposed_full')} |")

V = ["baseline", "a_da_bucket", "b1_complete_routes", "b2_cover", "b1+b2", "b3_kfloor2"]
print("\nMitigations, turnout eqs on: mean % delegators exposed / % rounds with a lone delegator exposed\n")
print("| scenario | s | " + " | ".join(V) + " |")
print("|---|---|" + "---|" * len(V))
for sc in SCENARIOS:
    for s in (0.0, 0.1):
        cells = []
        for v in V:
            rs = g[(sc, s, v, True)]
            cells.append(f"{100*np.mean([r['delegators_exposed'] for r in rs]):.1f} / {100*np.mean([r['any_lone_exposed'] for r in rs]):.0f}%")
        print(f"| {sc} | {s} | " + " | ".join(cells) + " |")
