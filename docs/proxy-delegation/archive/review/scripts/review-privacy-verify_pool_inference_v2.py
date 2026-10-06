"""Monte Carlo: how many per-delegate pool totals are exactly recoverable from
public data alone (routes + per-bucket direct ShareCount + published tallies).

A bucket (p,o) whose direct ShareCount is 0 (public: every direct reveal
carries (p,o)) has T[p][o] = sum of pools routed there -> exact linear equation.
Pool d is exactly recoverable iff e_d lies in the row space of those equations.

Variants:
  sentinel : unrouted / explicit-abstain pools are not counted anywhere (design as written)
  abstain_opt : every proposal has an Abstain option; explicit abstain AND unrouted
                pools are added to it (the integrated design's proposed mitigation,
                extended to unrouted)
"""
import random, sys

P_MOD = (1 << 61) - 1


def row_reduce(rows, n):
    basis = {}  # pivot -> row
    for r in rows:
        r = r[:]
        for piv in sorted(basis):
            if r[piv]:
                f = r[piv]
                br = basis[piv]
                r = [(a - f * b) % P_MOD for a, b in zip(r, br)]
        piv = next((i for i, v in enumerate(r) if v), None)
        if piv is None:
            continue
        inv = pow(r[piv], P_MOD - 2, P_MOD)
        r = [(v * inv) % P_MOD for v in r]
        # eliminate piv from existing basis rows
        for k in list(basis):
            br = basis[k]
            if br[piv]:
                f = br[piv]
                basis[k] = [(a - f * b) % P_MOD for a, b in zip(br, r)]
        basis[piv] = r
    return basis


def in_span(basis, vec):
    r = vec[:]
    for piv in sorted(basis):
        if r[piv]:
            f = r[piv]
            r = [(a - f * b) % P_MOD for a, b in zip(r, basis[piv])]
    return not any(r)


def trial(rng, P, n_direct, n_del, variant, direct_vote_p=0.85, route_p=0.85,
          direct_abstain=0.02, minor_option=0.0):
    # options: 0=Yes, 1=No, 2=Abstain (only meaningful for abstain_opt), 3=minor option
    opts = [0, 1, 3] if minor_option > 0 else [0, 1]
    if variant == "abstain_opt":
        opts = opts + [2]
    direct_nonzero = set()
    for _ in range(n_direct):
        for p in range(P):
            if rng.random() > direct_vote_p:
                continue
            u = rng.random()
            if variant == "abstain_opt" and u < direct_abstain:
                o = 2
            elif minor_option > 0 and u < direct_abstain + minor_option:
                o = 3
            else:
                o = 0 if rng.random() < 0.55 else 1
            direct_nonzero.add((p, o))
    buckets = {}
    for d in range(n_del):
        for p in range(P):
            if rng.random() < route_p:
                u = rng.random()
                if u < 0.08:
                    o = 2  # explicit abstain
                elif minor_option > 0 and u < 0.08 + minor_option * 3:
                    o = 3
                else:
                    o = 0 if rng.random() < 0.55 else 1
            else:
                o = 2  # unrouted
            if o == 2 and variant == "sentinel":
                continue  # not counted
            buckets.setdefault((p, o), []).append(d)
    rows = []
    for (p, o), ds in buckets.items():
        if (p, o) in direct_nonzero:
            continue
        row = [0] * n_del
        for d in ds:
            row[d] = 1
        rows.append(row)
    basis = row_reduce(rows, n_del)
    solved = sum(1 for d in range(n_del)
                 if in_span(basis, [1 if i == d else 0 for i in range(n_del)]))
    return solved, len(rows)


def main():
    rng = random.Random(7)
    trials = 200
    print("variant,P,direct_voters,delegates,minor_opt,mean_exact_pools,frac_exact,mean_eqs")
    for variant in ("sentinel", "abstain_opt"):
        for P in (5, 15):
            for nd in (30, 200, 1000):
                for ndel in (5, 20, 60):
                    for minor in (0.0, 0.01):
                        tot = 0
                        eqs = 0
                        for _ in range(trials):
                            s, e = trial(rng, P, nd, ndel, variant, minor_option=minor)
                            tot += s
                            eqs += e
                        print(f"{variant},{P},{nd},{ndel},{minor},{tot/trials:.2f},{tot/trials/ndel:.2f},{eqs/trials:.1f}")
                        sys.stdout.flush()


if __name__ == "__main__":
    main()
