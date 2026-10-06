"""Checks for sim.py: hand-built reviewer cases, and FLINT vs SymPy exact cross-check."""
import random

import numpy as np
import sympy

import sim


def world(D, P, K, votes, pool_amt, W):
    """Minimal world dict for sim.solve: votes is an (N, P) array of options, -1 = skipped."""
    return dict(D=D, P=P, K=np.array(K), N=len(W), W=np.array(W, dtype=np.int64),
                vote=np.array(votes, dtype=np.int64), pool_amt=np.array(pool_amt, dtype=np.int64))


def buckets_from(P, K, votes, routes):
    """routes: dict d -> {p: o}. Build sim-style bucket dicts."""
    b = {(p, o): dict(pools=[], real=[], cover=0) for p in range(P) for o in range(K[p])}
    for v, row in enumerate(votes):
        for p, o in enumerate(row):
            if o >= 0:
                b[(p, o)]["real"].append(v)
    for d, rt in routes.items():
        for p, o in rt.items():
            b[(p, o)]["pools"].append(d)
    return b


def check_hand_cases():
    """Reviewer cases: determined pools without any pool ever being alone in a bucket."""
    # Triangle: zero-direct buckets (p,2) hold {0,1}, {1,2}, {0,2}. All three determined.
    P, K = 3, [3, 3, 3]
    votes = [[0, 0, 0], [1, 1, 1], [0, 1, 0]]
    routes = {0: {0: 2, 2: 2}, 1: {0: 2, 1: 2}, 2: {1: 2, 2: 2}}
    w = world(3, P, K, votes, [5, 7, 11], [3, 4, 9])
    col, det = sim.solve(w, buckets_from(P, K, votes, routes), turnout=False)
    assert det >= {0, 1, 2}, det
    # Shared zero-direct option + one differing abstention + turnout (s = 0).
    P, K = 2, [3, 3]
    votes = [[0, 0], [1, 1], [0, 1]]
    routes = {0: {0: 2, 1: 0}, 1: {0: 2}}  # pool 1 abstains on proposal 1
    w = world(2, P, K, votes, [5, 7], [3, 4, 9])
    b = buckets_from(P, K, votes, routes)
    _, det_no = sim.solve(w, b, turnout=False)
    col, det_t = sim.solve(w, b, turnout=True)
    assert not ({0, 1} & det_no), det_no
    assert {0, 1} <= det_t, det_t
    # Lone direct voter in a bucket whose only pool is determined elsewhere.
    P, K = 2, [3, 3]
    votes = [[0, 0], [1, 1], [2, 1]]       # voter 2 is alone on (0,2)
    routes = {0: {0: 2, 1: 2}}             # pool 0 alone on zero-direct (1,2)
    w = world(1, P, K, votes, [6], [3, 4, 9])
    b = buckets_from(P, K, votes, routes)
    col, det = sim.solve(w, b, turnout=False)
    assert 0 in det and col[("D", (0, 2))] in det
    print("hand cases: OK")


def sympy_determined(rows, ncol):
    """Determined columns via SymPy rational RREF and, independently, the nullspace."""
    M = sympy.zeros(len(rows), ncol)
    for i, r in enumerate(rows):
        for j, v in r.items():
            M[i, j] = v
    R, piv = M.rref()
    det_rref = set()
    for i in range(len(piv)):
        nz = [j for j in range(ncol) if R[i, j] != 0]
        if len(nz) == 1:
            det_rref.add(nz[0])
    ns = M.nullspace()
    det_ns = {j for j in range(ncol) if all(v[j] == 0 for v in ns)}
    assert det_rref == det_ns, (det_rref ^ det_ns)
    return det_rref


def check_cross(n_cases):
    """Compare FLINT fraction-free RREF (sim.solve) with SymPy rationals on random rounds."""
    rnd = random.Random(1)
    scen = ["small", "medium", "binary-only", "early-few-delegates", "few-delegates-37",
            "many-proposals"]
    checked = 0
    nonempty = 0
    for i in range(n_cases):
        sc = scen[i % len(scen)]
        s = rnd.choice(sim.SKIPS)
        w = sim.gen_world(sim.SCENARIOS[sc], s, 9000 + i, sim.DEFAULT_PARAMS)
        for variant in sim.VARIANTS:
            b = sim.build_buckets(w, variant)
            for turnout in (False, True):
                col, truth, rows = sim.system(w, b, turnout)
                _, det = sim.solve(w, b, turnout)
                if rows:
                    ref = sympy_determined(rows, len(col))
                    assert ref == det, (sc, s, variant, turnout, ref ^ det)
                nonempty += bool(det)
                checked += 1
    print(f"cross-check: {checked} systems agree (FLINT vs SymPy RREF vs SymPy nullspace); "
          f"{nonempty} had >=1 determined variable")


if __name__ == "__main__":
    check_hand_cases()
    check_cross(36)
