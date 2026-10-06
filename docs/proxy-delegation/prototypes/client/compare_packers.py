"""Compare DC counts: plan packer vs sequential fill vs per-bundle Hamilton.

Imports the plan's prototype functions without running its self-test loop.
"""
import random, sys, importlib.util, io, contextlib

spec = importlib.util.spec_from_file_location("proto", __file__.replace("compare_packers.py", "planner.py"))
proto = importlib.util.module_from_spec(spec)
# The prototype runs a 6000-case self-test at import; silence it.
with contextlib.redirect_stdout(io.StringIO()):
    spec.loader.exec_module(proto)

BPS = 10_000


def seq_fill(W, targets):
    """Global targets, bundles largest first, delegates largest first, split at boundaries."""
    order_b = sorted(range(len(W)), key=lambda j: (-W[j], j))
    cap = {j: W[j] for j in order_b}
    slots = []
    bi = 0
    for d in sorted(range(len(targets)), key=lambda d: (-targets[d], d)):
        rem = targets[d]
        while rem > 0:
            j = order_b[bi]
            x = min(rem, cap[j])
            if x > 0:
                slots.append((j, d, x)); cap[j] -= x; rem -= x
            if cap[j] == 0:
                bi += 1
    return slots


def per_bundle(W, entries, keep):
    """Hamilton applied inside each bundle, then a global min-1 fix-up."""
    slots = []
    D = len(entries)
    tot = [0] * D
    for j, wb in enumerate(W):
        t, k, _ = hamilton_no_floor(wb, entries, keep)
        for d in range(D):
            if t[d] > 0:
                slots.append([j, d, t[d]]); tot[d] += t[d]
    # global floor: any delegate with 0 takes 1 ballot from the largest slot
    for d in range(D):
        if tot[d] == 0:
            s = max(slots, key=lambda s: s[2])
            s[2] -= 1
            slots.append([s[0], d, 1]); tot[d] += 1
    return [tuple(s) for s in slots if s[2] > 0]


def hamilton_no_floor(T, entries, keep):
    parties = [(bps, i) for i, bps in enumerate(entries)]
    s = sum(entries)
    if keep and BPS - s > 0:
        parties.append((BPS - s, len(entries)))
    alloc, rems = {}, []
    for bps, i in parties:
        num = T * bps
        alloc[i] = num // BPS
        rems.append((-(num % BPS), -bps, i))
    R = T - sum(alloc.values())
    for _, _, i in sorted(rems)[:R]:
        alloc[i] += 1
    return [alloc[i] for i in range(len(entries))], None, None


def draw():
    B = random.choices([1, 2, 3, 4, 6], weights=[55, 35, 5, 3, 2])[0]
    # ballots per bundle: log-uniform 8 .. 400k ballots (1 ZEC .. 50k ZEC)
    W = [int(10 ** random.uniform(0.9, 5.6)) for _ in range(B)]
    D = random.choices(range(1, 9), weights=[35, 25, 20, 8, 5, 3, 2, 2])[0]
    keep = random.random() < 0.5
    if keep:
        total = random.randint(1000, 9000)
        cuts = sorted(random.sample(range(1, total), D - 1)) if D > 1 else []
        entries = [b - a for a, b in zip([0] + cuts, cuts + [total])]
    else:
        cuts = sorted(random.sample(range(1, BPS), D - 1)) if D > 1 else []
        entries = [b - a for a, b in zip([0] + cuts, cuts + [BPS])]
    return W, entries, keep


def main(n=20000, seed=11):
    random.seed(seed)
    agg = {"plan": [0, 0], "seq": [0, 0], "perb": [0, 0]}
    extra_hist = {"plan": {}, "seq": {}, "perb": {}}
    worse_seq = 0
    cases = 0
    byB = {}
    for _ in range(n):
        W, entries, keep = draw()
        if sum(W) < len(entries) * 2:
            continue
        try:
            targets, k, adj, plan_slots = proto.plan(W, entries, keep)
        except ValueError:
            continue
        seq_slots = seq_fill(W, targets)
        pb_slots = per_bundle(W, entries, keep)
        D = len(entries)
        cases += 1
        for name, sl in (("plan", plan_slots), ("seq", seq_slots), ("perb", pb_slots)):
            agg[name][0] += len(sl)
            agg[name][1] += D
            e = len(sl) - D
            extra_hist[name][e] = extra_hist[name].get(e, 0) + 1
        if len(seq_slots) > len(plan_slots):
            worse_seq += 1
        B = len(W)
        b = byB.setdefault(B, [0, 0, 0, 0, 0])
        b[0] += 1; b[1] += len(plan_slots) - D; b[2] += len(seq_slots) - D; b[3] += len(pb_slots) - D; b[4] += D
        # sanity: seq fill conserves and respects caps
        per_b = [0] * B
        per_d = [0] * D
        for j, d, x in seq_slots:
            per_b[j] += x; per_d[d] += x
        assert per_d == targets and all(per_b[j] <= W[j] for j in range(B))
        assert len(seq_slots) <= D + B - 1
    print(f"cases={cases}")
    for name in agg:
        dcs, ds = agg[name]
        print(f"{name:5s} mean DCs per delegator = {dcs / cases:.3f}  (mean D = {ds / cases:.3f}); extra DCs over D per delegator = {(dcs - ds) / cases:.3f}")
    print(f"seq fill uses more DCs than the plan packer in {100 * worse_seq / cases:.1f}% of cases")
    for name in extra_hist:
        h = extra_hist[name]
        print(name, "extra-DC histogram:", dict(sorted(h.items())))
    print("by B: count, mean extra plan / seq / per-bundle, mean D")
    for B in sorted(byB):
        c, p, s, pb, d = byB[B]
        print(f"  B={B}: n={c} plan={p / c:.3f} seq={s / c:.3f} perb={pb / c:.3f} D={d / c:.2f}")


if __name__ == "__main__":
    main(int(sys.argv[1]) if len(sys.argv) > 1 else 20000)
