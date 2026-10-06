"""How well does b2 cover hide zero-direct buckets from a guessing observer?

For buckets that hold routed pools and show c >= 1 votes (real + cover), report
P(no real direct vote | c). An observer who treats such a bucket as cover-only
recovers the baseline equation with this probability."""
from collections import defaultdict

import sim

for scen in sim.SCENARIOS:
    tally = defaultdict(lambda: [0, 0])  # c -> [cover-only, total]
    for i in range(100):
        w = sim.gen_world(sim.SCENARIOS[scen], 0.1, 1000 * i + 17, sim.DEFAULT_PARAMS)
        for b in sim.build_buckets(w, "b2_cover").values():
            c = len(b["real"]) + b["cover"]
            if b["pools"] and 1 <= c <= 4:
                tally[c][1] += 1
                tally[c][0] += not b["real"]
    cells = [f"c={c}: {100*tally[c][0]/tally[c][1]:.0f}% of {tally[c][1]}" if tally[c][1] else f"c={c}: -"
             for c in range(1, 5)]
    print(f"{scen:20s} " + " | ".join(cells))
