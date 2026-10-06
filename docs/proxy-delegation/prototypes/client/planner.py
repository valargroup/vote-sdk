"""Throwaway prototype of the proxy-delegation allocation planner (v1)."""
import random, itertools, sys

BPS = 10_000

def largest_remainder(T, entries, keep):
    # entries: list of bps (user order); keep: True => keep party gets 10000 - sum
    parties = [(bps, i) for i, bps in enumerate(entries)]
    s = sum(entries)
    if keep:
        assert s <= BPS
        if BPS - s > 0:
            parties.append((BPS - s, len(entries)))  # keep last
    else:
        assert s == BPS
    alloc, rems = {}, []
    for bps, i in parties:
        num = T * bps
        alloc[i] = num // BPS
        rems.append((-(num % BPS), -bps, i))
    R = T - sum(alloc.values())
    assert 0 <= R < len(parties)
    for _, _, i in sorted(rems)[:R]:
        alloc[i] += 1
    keep_idx = len(entries) if keep and BPS - s > 0 else None
    adjusted = set()
    for i in range(len(entries)):
        if alloc[i] == 0:
            if keep_idx is not None and alloc[keep_idx] >= 1:
                donor = keep_idx
            else:
                cands = [j for j in range(len(entries)) if alloc[j] > 1]
                if not cands:
                    raise ValueError("insufficient weight")
                donor = max(cands, key=lambda j: (alloc[j], -j))
            alloc[donor] -= 1
            alloc[i] = 1
            adjusted.add(i)
            if donor != keep_idx:
                adjusted.add(donor)
    targets = [alloc[i] for i in range(len(entries))]
    k = alloc[keep_idx] if keep_idx is not None else 0
    assert sum(targets) + k == T
    return targets, k, adjusted

def bfd(W, targets):
    cap = list(W); out = []; touched = set()
    for d in sorted(range(len(targets)), key=lambda d: (-targets[d], d)):
        c = [j for j in range(len(W)) if cap[j] >= targets[d]]
        if not c:
            return None
        j = min(c, key=lambda j: (cap[j] - targets[d], j not in touched, j))
        cap[j] -= targets[d]; touched.add(j); out.append((j, d, targets[d]))
    return out

def split_free(W, targets, budget=20_000):
    if len(W) > 4:
        return bfd(W, targets)
    order = sorted(range(len(targets)), key=lambda d: (-targets[d], d))
    best = None
    visits = [0]
    cap = list(W)
    assign = {}
    def dfs(pos):
        nonlocal best
        visits[0] += 1
        if visits[0] > budget:
            return
        if pos == len(order):
            touched = len(set(assign.values()))
            cand = (touched, tuple(sorted(assign.items())))
            if best is None or cand < best:
                best = cand
            return
        d = order[pos]
        seen_caps = set()
        for j in range(len(W)):
            if cap[j] >= targets[d] and (cap[j], j in assign.values()) not in seen_caps:
                seen_caps.add((cap[j], j in assign.values()))
                cap[j] -= targets[d]; assign[d] = j
                dfs(pos + 1)
                cap[j] += targets[d]; del assign[d]
    dfs(0)
    if best is None:
        return bfd(W, targets)
    return [(j, d, targets[d]) for d, j in best[1]]

def split_greedy(W, targets):
    cap = list(W)
    slots = []
    touched = set()
    for d in sorted(range(len(targets)), key=lambda d: (-targets[d], d)):
        rem = targets[d]
        while rem > 0:
            j = max(range(len(W)), key=lambda j: (cap[j], j in touched, -j))
            x = min(rem, cap[j])
            assert x > 0
            slots.append((j, d, x)); cap[j] -= x; rem -= x; touched.add(j)
    return slots

def plan(W, entries, keep):
    T = sum(W)
    targets, k, adj = largest_remainder(T, entries, keep)
    slots = split_free(W, targets) if len(W) > 1 else [(0, d, targets[d]) for d in range(len(targets))]
    if slots is None:
        slots = split_greedy(W, targets)
    return targets, k, adj, slots

def check(W, entries, keep):
    T = sum(W)
    targets, k, adj, slots = plan(W, entries, keep)
    D, B = len(entries), len(W)
    per_d = [0]*D; per_b = [0]*B
    for j, d, x in slots:
        assert x >= 1
        per_d[d] += x; per_b[j] += x
    assert per_d == targets, (per_d, targets)
    assert all(per_b[j] <= W[j] for j in range(B))
    assert D <= len(slots) <= D + B - 1, (len(slots), D, B)
    assert len({(j, d) for j, d, _ in slots}) == len(slots)
    if B == 1:
        assert len(slots) == D
    for i in range(D):
        if i not in adj:
            q = T * entries[i] / BPS
            assert abs(targets[i] - q) < 1 + 1e-9, (targets[i], q)
    return len(slots)

random.seed(int(sys.argv[1]) if len(sys.argv) > 1 else 7)
worst = 0
for it in range(6000):
    B = random.choice([1, 1, 2, 2, 2, 3, 4, 6, 12])
    W = [random.choice([1, 2, 5, 40, 800, 7960, 200000, 2**24]) + random.randint(0, 50) for _ in range(B)]
    D = random.randint(1, 8)
    if sum(W) < D:
        continue
    keep = random.random() < 0.6
    if keep:
        cuts = sorted(random.randint(1, BPS) for _ in range(D))
        entries = [max(1, random.randint(1, BPS // D)) for _ in range(D)]
    else:
        pts = sorted(random.sample(range(1, BPS), D - 1)) if D > 1 else []
        entries = [b - a for a, b in zip([0] + pts, pts + [BPS])]
    n = check(W, entries, keep)
    worst = max(worst, n - D)
print("ok; worst extra slots beyond D:", worst)
# Worked examples
for W, e in [([7960, 40], [5000, 3000]), ([5000, 3000], [5000, 3000]), ([4000, 4000], [6000, 4000])]:
    print(W, e, plan(W, e, keep=sum(e) < BPS))
