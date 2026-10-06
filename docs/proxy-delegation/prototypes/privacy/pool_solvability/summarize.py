"""Aggregate results.jsonl into Markdown tables (median / mean / p90 over seeds)."""
import json
import sys
from collections import defaultdict

import numpy as np

path = sys.argv[1] if len(sys.argv) > 1 else "results.jsonl"
rows = [json.loads(l) for l in open(path)]
grp = defaultdict(list)
for r in rows:
    grp[(r["scenario"], r["s"], r["variant"], r["turnout"])].append(r)

order = list(dict.fromkeys(r["scenario"] for r in sorted(rows, key=lambda r: r["seed"])))
from sim import SCENARIOS, SKIPS, VARIANTS  # noqa: E402
order = [s for s in SCENARIOS if s in order]


def stat(vals):
    """Median / mean / p90 of the non-null values, as percentages."""
    v = np.array([x for x in vals if x is not None], dtype=float)
    if not len(v):
        return "n/a"
    return f"{100*np.median(v):.1f} / {100*v.mean():.1f} / {100*np.percentile(v, 90):.1f}"


def pct(vals):
    """Mean of a 0/1 indicator, as a percentage."""
    v = np.array([x for x in vals if x is not None], dtype=float)
    return f"{100*v.mean():.0f}%"


n_seeds = len(grp[(order[0], 0.0, "baseline", False)])
print(f"# Results ({n_seeds} seeds per cell; cells are median / mean / p90, in %)\n")

print("## Baseline (sentinel abstain not counted)\n")
print("| scenario | s | turnout eqs | pools determined | lone-delegator pools determined | multi-delegator pool totals determined | delegators exposed | rounds with >=1 lone exposed | direct voters exposed (lone, no pools) | direct voters exposed (lone, D solvable) |")
print("|---|---|---|---|---|---|---|---|---|---|")
for sc in order:
    for s in SKIPS:
        for t in (False, True):
            g = grp[(sc, s, "baseline", t)]
            print(f"| {sc} | {s} | {'yes' if t else 'no'} | {stat([r['pools_det'] for r in g])} | "
                  f"{stat([r['lone_pools_det'] for r in g])} | {stat([r['multi_pools_det'] for r in g])} | "
                  f"{stat([r['delegators_exposed'] for r in g])} | {pct([r['any_lone_exposed'] for r in g])} | "
                  f"{stat([r['direct_exposed_simple'] for r in g])} | {stat([r['direct_exposed_full'] for r in g])} |")

print("\n## World statistics (baseline, medians)\n")
print("| scenario | s | zero-direct buckets | lone pools | delegators | full delegators | turnout noise / lone DC (s>0) |")
print("|---|---|---|---|---|---|---|")
for sc in order:
    for s in SKIPS:
        g = grp[(sc, s, "baseline", False)]
        tn = [r.get("turnout_noise_ratio") for r in g if r.get("turnout_noise_ratio") is not None]
        print(f"| {sc} | {s} | {100*np.median([r['zero_direct_buckets'] for r in g]):.1f}% | "
              f"{100*np.median([r['lone_pool_share'] for r in g]):.0f}% | "
              f"{np.median([r['n_delegators'] for r in g]):.0f} | {np.median([r['full_delegators'] for r in g]):.0f} | "
              f"{np.median(tn):.1f}x |" if tn else
              f"| {sc} | {s} | {100*np.median([r['zero_direct_buckets'] for r in g]):.1f}% | "
              f"{100*np.median([r['lone_pool_share'] for r in g]):.0f}% | "
              f"{np.median([r['n_delegators'] for r in g]):.0f} | {np.median([r['full_delegators'] for r in g]):.0f} | - |")

print("\n## Mitigations: delegators exposed (median / mean / p90 %), turnout equations on (worst case)\n")
hdr = " | ".join(VARIANTS)
print(f"| scenario | s | {hdr} |")
print("|---|---|" + "---|" * len(VARIANTS))
for sc in order:
    for s in SKIPS:
        cells = [stat([r["delegators_exposed"] for r in grp[(sc, s, v, True)]]) for v in VARIANTS]
        print(f"| {sc} | {s} | " + " | ".join(cells) + " |")

print("\n## Mitigations: lone-delegator pools determined, turnout on (median / mean / p90 %)\n")
print(f"| scenario | s | {hdr} |")
print("|---|---|" + "---|" * len(VARIANTS))
for sc in order:
    for s in SKIPS:
        cells = [stat([r["lone_pools_det"] for r in grp[(sc, s, v, True)]]) for v in VARIANTS]
        print(f"| {sc} | {s} | " + " | ".join(cells) + " |")

print("\n## Mitigations: all pools determined, turnout on (median / mean / p90 %)\n")
print(f"| scenario | s | {hdr} |")
print("|---|---|" + "---|" * len(VARIANTS))
for sc in order:
    for s in SKIPS:
        cells = [stat([r["pools_det"] for r in grp[(sc, s, v, True)]]) for v in VARIANTS]
        print(f"| {sc} | {s} | " + " | ".join(cells) + " |")

print("\n## Mitigations: rounds with >=1 lone delegator exposed, turnout on\n")
print(f"| scenario | s | {hdr} |")
print("|---|---|" + "---|" * len(VARIANTS))
for sc in order:
    for s in SKIPS:
        cells = [pct([r["any_lone_exposed"] for r in grp[(sc, s, v, True)]]) for v in VARIANTS]
        print(f"| {sc} | {s} | " + " | ".join(cells) + " |")

print("\n## Mitigations: direct voters exposed (lone, D solvable), turnout on (median / mean / p90 %)\n")
print(f"| scenario | s | {hdr} |")
print("|---|---|" + "---|" * len(VARIANTS))
for sc in order:
    for s in SKIPS:
        cells = [stat([r["direct_exposed_full"] for r in grp[(sc, s, v, True)]]) for v in VARIANTS]
        print(f"| {sc} | {s} | " + " | ".join(cells) + " |")

print("\n## b3 k-floor: delegated weight dropped (pools with one DC are not routed)\n")
print("| scenario | median | mean |")
print("|---|---|---|")
for sc in order:
    g = grp[(sc, 0.0, "b3_kfloor2", True)]
    v = [r["weight_lost"] for r in g]
    print(f"| {sc} | {100*np.median(v):.1f}% | {100*np.mean(v):.1f}% |")
