"""Compact sensitivity table: mean delegators exposed and share of rounds with a lone exposure."""
import json, sys
from collections import defaultdict
import numpy as np
from sim import SCENARIOS

runs = [("default", "results.jsonl"), ("no contrarians", "sens/no_contrarian.jsonl"),
        ("no contrarians, no fringe options", "sens/no_contrarian_no_fringe.jsonl"),
        ("follow alpha 1.6 (fewer lone pools)", "sens/alpha1.6.jsonl"),
        ("routing prob 0.97-1.0", "sens/r_all_1.jsonl"),
        ("cover 1 per bucket", "sens/cover1.jsonl"), ("cover 4 per bucket", "sens/cover4.jsonl")]
variants = sys.argv[1].split(",") if len(sys.argv) > 1 else ["baseline"]
skips = [float(x) for x in sys.argv[2].split(",")] if len(sys.argv) > 2 else [0.0, 0.1]
for v in variants:
    print(f"\n### {v}, turnout on: mean delegators exposed % / rounds with >=1 lone exposed %\n")
    print("| run | s | " + " | ".join(SCENARIOS) + " |")
    print("|---|---|" + "---|" * len(SCENARIOS))
    for name, path in runs:
        g = defaultdict(list)
        for l in open(path):
            r = json.loads(l)
            if r["variant"] == v and r["turnout"]:
                g[(r["scenario"], r["s"])].append(r)
        for s in skips:
            cells = []
            for sc in SCENARIOS:
                rs = g[(sc, s)]
                cells.append(f"{100*np.mean([r['delegators_exposed'] for r in rs]):.1f} / "
                             f"{100*np.mean([r['any_lone_exposed'] for r in rs]):.0f}")
            print(f"| {name} | {s} | " + " | ".join(cells) + " |")
