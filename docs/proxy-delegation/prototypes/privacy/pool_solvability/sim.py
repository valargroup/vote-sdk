"""Rev 4 pool-solvability simulation for proxy delegation (PLAN D4, §4.2 Routing, §5).

Question: from public data only, which delegate pools, lone-delegator amounts
and lone direct-voter amounts are EXACTLY determined?

Public data the attacker uses:
  * routes (which option each delegate routed on each proposal; abstain = no bucket),
  * per-bucket direct vote counts (each direct share reveals (p, o); 16 shares = 1 vote),
  * exact decrypted per-option totals T(p, o),
  * approximate per-delegate DC counts (pool reveals / 16), which flag lone pools.

Linear model (unknowns are integers but we only use linear algebra over Q):
  bucket (p, o):  D[p,o] + sum_{d routed to (p,o)} Pool[d] = T(p,o)
      D[p,o] is a fresh unknown iff the bucket shows >= 1 direct vote; it is
      absent (zero) iff the bucket shows zero direct shares.
  turnout (optional): for proposals p in a group g whose direct-voter SET is
      identical, sum_o D[p,o] = X_g. This is exactly the reviewer's
      T_total(p) - T_total(p') = routed(p) - routed(p') equation. Groups use
      the ground-truth voter sets, so this is an upper bound on the attacker.

A variable is determined iff its unit vector lies in the row space, i.e. iff
its column is a pivot whose RREF row has no other nonzero entry. RREF is done
with FLINT's fraction-free integer elimination (exact). Every determined
variable is additionally solved from the augmented system with the true
totals and asserted equal to the ground truth.

Generative model (DEFAULT_PARAMS):
  * K per proposal from {2,3,4,5} w.p. {.4,.25,.2,.15} (or fixed K); option
    popularity Zipf(gamma ~ U[1,2.5]) x lognormal noise, and each non-top
    option is "fringe" (x0.02) w.p. 0.15.
  * N direct voters, weight ceil(lognormal(ln 8, 1.2)) ballots, each votes each
    proposal w.p. 1-s for one option drawn by popularity (full weight per vote).
  * D delegates, delegator count n_d ~ k^-2.2 on 1..N/2 (about 2/3 lone pools);
    20% contrarian (options ~ pop^-0.5), else options ~ pop; 95% active; an
    active delegate routes each proposal w.p. r_d ~ U[0.8, 1.0].
  * Delegators pick 1/2/3 distinct delegates w.p. .7/.2/.1; 60% delegate all
    their weight (these supply b2 cover votes from their W=0 successor VAN).

Variants:
  baseline            sentinel abstain, not counted (PLAN §4.2).
  a_da_bucket         abstaining/unrouted pools go to a decrypted per-proposal
                      "delegated abstain" bucket that direct voters cannot use.
  b1_complete_routes  a pool counts only if its delegate routed every proposal;
                      otherwise it abstains on all of them.
  b2_cover            full delegators' W=0 successors cast zero-weight votes on
                      a uniform option, ~cover_per_bucket per bucket.
  b3_kfloor2          pools with < 2 DCs are not routed.
  b1+b2, a+b2         combinations (a+b2 = Abstain bucket open to direct votes,
                      receiving delegate abstentions and cover).
"""
import json
import sys
from multiprocessing import Pool

import flint
import numpy as np

SCENARIOS = {
    "small": dict(N=50, D=10, P=5),
    "medium": dict(N=500, D=30, P=10),
    "large": dict(N=5000, D=100, P=15),
    "many-proposals": dict(N=2000, D=50, P=37),
    "binary-only": dict(N=500, D=30, P=10, K=2),
    # Extra: delegates few relative to proposals (likely at launch).
    "early-few-delegates": dict(N=500, D=8, P=12),
    "few-delegates-37": dict(N=2000, D=15, P=37),
}
SKIPS = [0.0, 0.02, 0.1, 0.3]
VARIANTS = ["baseline", "a_da_bucket", "b1_complete_routes", "b2_cover", "b3_kfloor2",
            "b1+b2", "a+b2"]

DEFAULT_PARAMS = dict(
    k_mix=([2, 3, 4, 5], [0.4, 0.25, 0.2, 0.15]),
    fringe_p=0.15,          # chance a non-top option is "fringe" (weight x0.02)
    contrarian_p=0.2,       # delegates choosing options ~ pop^-0.5
    active_p=0.95,          # delegates that route at all
    r_lo=0.8, r_hi=1.0,     # per-delegate per-proposal routing probability
    follow_alpha=2.2,       # delegator count per delegate ~ k^-alpha, k >= 1
    full_delegator_p=0.6,   # delegators who delegate all weight (W=0 successor)
    cover_per_bucket=2.0,   # b2: target mean zero-weight cover votes per bucket
)


def powerlaw_counts(rng, D, alpha, kmax):
    """Draw D delegator counts from P(k) proportional to k^-alpha on 1..kmax."""
    ks = np.arange(1, kmax + 1)
    w = ks.astype(float) ** -alpha
    return rng.choice(ks, size=D, p=w / w.sum())


def gen_world(scn, s, seed, prm):
    """Generate one round: options, direct votes, delegates, delegators, routes, cover."""
    rng = np.random.default_rng(seed)
    P, N, D = scn["P"], scn["N"], scn["D"]
    if "K" in scn:
        K = np.full(P, scn["K"])
    else:
        K = rng.choice(prm["k_mix"][0], size=P, p=prm["k_mix"][1])
    pop = []
    for p in range(P):
        k = int(K[p])
        g = rng.uniform(1.0, 2.5)
        w = np.arange(1, k + 1, dtype=float) ** -g * rng.lognormal(0, 0.5, k)
        fr = rng.random(k) < prm["fringe_p"]
        fr[0] = False
        w[fr] *= 0.02
        pop.append(rng.permutation(w / w.sum()))

    # Direct voters: weight in ballots, one option per proposal they vote on.
    W = np.ceil(rng.lognormal(np.log(8), 1.2, N)).astype(np.int64)
    vote = np.full((N, P), -1, dtype=np.int64)
    for p in range(P):
        part = rng.random(N) >= s
        opt = rng.choice(int(K[p]), size=N, p=pop[p])
        vote[part, p] = opt[part]

    # Delegates.
    n_d = powerlaw_counts(rng, D, prm["follow_alpha"], max(2, N // 2))
    contr = rng.random(D) < prm["contrarian_p"]
    active = rng.random(D) < prm["active_p"]
    r = rng.uniform(prm["r_lo"], prm["r_hi"], D)
    pref = np.zeros((D, P), dtype=np.int64)
    rmask = np.zeros((D, P), dtype=bool)
    for d in range(D):
        for p in range(P):
            q = pop[p] ** (-0.5 if contr[d] else 1.0)
            pref[d, p] = rng.choice(int(K[p]), p=q / q.sum())
            rmask[d, p] = active[d] and rng.random() < r[d]

    # Delegators: fill each delegate's n_d slots; a delegator picks 1-3 distinct delegates.
    slots = list(np.repeat(np.arange(D), n_d))
    rng.shuffle(slots)
    delegators = []
    while slots:
        m = int(rng.choice([1, 2, 3], p=[0.7, 0.2, 0.1]))
        chosen, i = [], 0
        while i < len(slots) and len(chosen) < m:
            if slots[i] in chosen:
                i += 1
            else:
                chosen.append(int(slots.pop(i)))
        delegators.append(chosen)
    pool_amt = np.zeros(D, dtype=np.int64)
    dc_amt = []
    full = rng.random(len(delegators)) < prm["full_delegator_p"]
    for chosen in delegators:
        wt = max(len(chosen), int(np.ceil(rng.lognormal(np.log(8), 1.2))))
        split = rng.dirichlet(np.ones(len(chosen))) * (wt - len(chosen))
        amts = 1 + np.floor(split).astype(np.int64)
        dc_amt.append(amts)
        for d, a in zip(chosen, amts):
            pool_amt[d] += a

    # b2 cover: each full delegator's zero-weight successor casts on proposal p with
    # probability q, on a uniformly random option, so a bucket gets ~cover_per_bucket.
    # a+b2 spreads cover over the K options plus the delegated-abstain bucket.
    F = int(full.sum())
    cover, cover_da = [], []
    for p in range(P):
        for extra, dst in ((0, cover), (1, cover_da)):
            k = int(K[p]) + extra
            q = min(1.0, prm["cover_per_bucket"] * k / max(F, 1))
            casts = rng.random(F) < q
            dst.append(np.bincount(rng.integers(0, k, size=int(casts.sum())), minlength=k))

    return dict(P=P, N=N, D=D, K=K, pop=pop, W=W, vote=vote, n_d=n_d, pref=pref,
                rmask=rmask, active=active, delegators=delegators, dc_amt=dc_amt,
                pool_amt=pool_amt, cover=cover, cover_da=cover_da, F=F)


def build_buckets(w, variant):
    """Apply a variant's routing/counting rule; return per-bucket public and true data."""
    P, D, K = w["P"], w["D"], w["K"]
    use_da = variant in ("a_da_bucket", "a+b2")
    complete = variant in ("b1_complete_routes", "b1+b2")
    use_cover = variant in ("b2_cover", "b1+b2", "a+b2")
    kfloor = 2 if variant == "b3_kfloor2" else 1
    counted = w["n_d"] >= kfloor  # pools excluded by the floor are never routed

    buckets = {}  # (p, o) -> dict(pools=[], real=[voters], cover=int)
    for p in range(P):
        for o in range(int(K[p]) + (1 if use_da else 0)):
            buckets[(p, o)] = dict(pools=[], real=[], cover=0)
        if use_cover:
            cov = w["cover_da"][p] if use_da else w["cover"][p]
            for o in range(len(cov)):
                buckets[(p, o)]["cover"] = int(cov[o])
    for v in range(w["N"]):
        for p in range(P):
            o = w["vote"][v, p]
            if o >= 0:
                buckets[(p, int(o))]["real"].append(v)
    for d in range(D):
        if not counted[d]:
            continue
        for p in range(P):
            routed = w["active"][d] if complete else w["rmask"][d, p]
            if routed:
                buckets[(p, int(w["pref"][d, p]))]["pools"].append(d)
            elif use_da:
                buckets[(p, int(K[p]))]["pools"].append(d)
    return buckets


def turnout_groups(w):
    """Group proposals whose direct-voter sets are identical (ground truth)."""
    groups = {}
    for p in range(w["P"]):
        key = (w["vote"][:, p] >= 0).tobytes()
        groups.setdefault(key, []).append(p)
    return list(groups.values())


def system(w, buckets, turnout):
    """Build the public linear system: column map, true values, sparse rows."""
    D = w["D"]
    col = {("pool", d): d for d in range(D)}
    for b, info in buckets.items():
        if len(info["real"]) + info["cover"] > 0:
            col[("D", b)] = len(col)
    groups = turnout_groups(w) if turnout else []
    for gi, g in enumerate(groups):
        if len(g) > 1:
            col[("X", gi)] = len(col)
    ncol = len(col)

    truth = np.zeros(ncol, dtype=object)
    for d in range(D):
        truth[d] = int(w["pool_amt"][d])
    for b, info in buckets.items():
        if ("D", b) in col:
            truth[col[("D", b)]] = int(w["W"][info["real"]].sum()) if info["real"] else 0
    for gi, g in enumerate(groups):
        if ("X", gi) in col:
            truth[col[("X", gi)]] = int(w["W"][w["vote"][:, g[0]] >= 0].sum())

    rows = []
    for b, info in buckets.items():
        idx = [col[("pool", d)] for d in info["pools"]]
        if ("D", b) in col:
            idx.append(col[("D", b)])
        if idx:
            rows.append({i: 1 for i in idx})
    for gi, g in enumerate(groups):
        if ("X", gi) not in col:
            continue
        for p in g:
            r = {col[("X", gi)]: -1}
            for b in buckets:
                if b[0] == p and ("D", b) in col:
                    r[col[("D", b)]] = 1
            rows.append(r)
    return col, truth, rows


def solve(w, buckets, turnout):
    """Find determined variables by exact RREF and verify each against the truth."""
    col, truth, rows = system(w, buckets, turnout)
    ncol = len(col)
    det = set()
    if rows:
        flat = []
        for r in rows:
            line = [0] * (ncol + 1)
            for i, v in r.items():
                line[i] = v
            line[ncol] = int(sum(v * truth[i] for i, v in r.items()))  # public total
            flat.extend(line)
        A = flint.fmpz_mat(len(rows), ncol + 1, flat)
        R, _den, rank = A.rref()
        for i in range(rank):
            nz = [j for j in range(ncol) if R[i, j] != 0]
            if len(nz) == 1:
                j = nz[0]
                val = flint.fmpq(R[i, ncol], R[i, j])
                assert val == truth[j], (j, val, truth[j])
                det.add(j)
    return col, det


def metrics(w, buckets, col, det):
    """Per-round exposure fractions."""
    D, n_d = w["D"], w["n_d"]
    pool_det = np.array([d in det for d in range(D)])
    lone = n_d == 1
    out = dict(
        pools_det=pool_det.mean(),
        lone_pools_det=pool_det[lone].mean() if lone.any() else np.nan,
        multi_pools_det=pool_det[~lone].mean() if (~lone).any() else np.nan,
    )
    exposed_deleg = [any(pool_det[d] and lone[d] for d in chosen) for chosen in w["delegators"]]
    out["delegators_exposed"] = float(np.mean(exposed_deleg))
    out["any_lone_exposed"] = float(bool((pool_det & lone).any()))
    simple, full = set(), set()
    for b, info in buckets.items():
        if len(info["real"]) == 1 and info["cover"] == 0:
            v = info["real"][0]
            if not info["pools"]:
                simple.add(v)
            if col.get(("D", b)) in det:
                full.add(v)
    out["direct_exposed_simple"] = len(simple) / w["N"]
    out["direct_exposed_full"] = len(full) / w["N"]
    return out


def run_one(args):
    """Evaluate every variant, with and without turnout equations, on one generated round."""
    scen, s, seed, prm = args
    w = gen_world(SCENARIOS[scen], s, seed, prm)
    res = []
    for variant in VARIANTS:
        buckets = build_buckets(w, variant)
        for turnout in (False, True):
            col, det = solve(w, buckets, turnout)
            m = metrics(w, buckets, col, det)
            m.update(scenario=scen, s=s, seed=seed, variant=variant, turnout=turnout)
            if variant == "b3_kfloor2":
                tot = w["pool_amt"].sum()
                m["weight_lost"] = float(w["pool_amt"][w["n_d"] < 2].sum() / tot)
            if variant == "baseline" and not turnout:
                m["zero_direct_buckets"] = sum(
                    1 for b in buckets.values() if not b["real"]) / len(buckets)
                m["lone_pool_share"] = float((w["n_d"] == 1).mean())
                m["n_delegators"] = len(w["delegators"])
                m["full_delegators"] = w["F"]
                if s > 0:
                    # Noise in an inexact turnout equation, in units of a lone DC amount.
                    X = np.array([w["W"][w["vote"][:, p] >= 0].sum() for p in range(w["P"])])
                    diffs = [X[i] - X[j] for i in range(len(X)) for j in range(i + 1, len(X))]
                    lone_amts = w["pool_amt"][w["n_d"] == 1]
                    if len(lone_amts):
                        m["turnout_noise_ratio"] = float(np.std(diffs) / np.median(lone_amts))
            res.append(m)
    return res


def main():
    seeds = int(sys.argv[1]) if len(sys.argv) > 1 else 100
    out = sys.argv[2] if len(sys.argv) > 2 else "results.jsonl"
    prm = dict(DEFAULT_PARAMS)
    if len(sys.argv) > 3:
        prm.update(json.loads(sys.argv[3]))
    scen_list = sys.argv[4].split(",") if len(sys.argv) > 4 else list(SCENARIOS)
    tasks = [(sc, s, 1000 * i + 17, prm) for sc in scen_list for s in SKIPS for i in range(seeds)]
    with Pool(24) as pool, open(out, "w") as f:
        for res in pool.imap_unordered(run_one, tasks, chunksize=2):
            for m in res:
                f.write(json.dumps({k: (None if isinstance(v, float) and np.isnan(v) else
                                        (v.item() if hasattr(v, "item") else v))
                                    for k, v in m.items()}) + "\n")


if __name__ == "__main__":
    main()
