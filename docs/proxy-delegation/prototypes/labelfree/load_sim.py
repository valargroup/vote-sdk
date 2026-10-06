"""Helper and chain load: labeled pools vs label-free (route-proving) reveals.

Policy inputs (zcash_voting share_policy, vote-sdk helper):
  last-moment window L = min(40% of round, 6 h)       timing.rs:19-24, 32-45
  delayed share: submit_at ~ U[t, min(t + 100 h, T - L)]  submission_schedule.rs:6, 113-134
  inside L: single share, submit_at = 0                 submission_schedule.rs:119-121
  helper: one FIFO by effective schedule time           helper_submission_invariants.md
  0.58 shares/s per worker, 2 workers per operator      software-upgrades.md:446-457
  256 reveals per block, ~1.2 s blocks                  vote_share_submission_proposal.go:13, blocktimes.md:27-28
  share lost if it completes after T - 5 min            retry_schedule.go:12 (retrySafetyBuffer)

Model: deterministic FIFO server of rate min(W*r/dup, 256/1.2). Exact completion
c_j = (j+1)/m + max_{i<=j}(e_i - i/m) for events sorted by eligible time e.

Label-free eligibility of DC share i on proposal p (route time tau_d):
  pre-window DC: s ~ U[t, min(t+100h, T-L)] per (i,p); if tau_d <= s: e = s;
                 else re-spread e = tau_d + U*(T-L-tau_d) if tau_d < T-L, else e = tau_d
  in-window DC (layout 1): one share per p, e = max(t, tau_d)
"""
import sys
import numpy as np

H = 3600.0
DAY = 86400.0
CHAIN_CAP = 256 / 1.2  # reveals per second


def arrivals(rng, n, T, profile):
    if profile == "uniform":
        return rng.random(n) * (T - 600)
    if profile == "deadline":
        v = rng.random(n)
        u = rng.random(n)
        out = np.where(v < 0.50, u * (T - 24 * H),
              np.where(v < 0.85, (T - 24 * H) + u * 18 * H,
                       (T - 6 * H) + u * (6 * H - 600)))
        return out
    raise ValueError(profile)


def route_times(rng, n, T, profile):
    if profile == "early":
        return rng.random(n) * 0.5 * T
    if profile == "deadline":
        return arrivals(rng, n, T, "deadline")
    if profile == "late":
        v = rng.random(n)
        u = rng.random(n)
        return np.where(v < 0.5, (T - 24 * H) + u * 18 * H, (T - 6 * H) + u * (6 * H - 600))
    raise ValueError(profile)


def zipf_choice(rng, D, size, k):
    w = 1.0 / np.arange(1, D + 1)
    w /= w.sum()
    out = np.empty((size, k), dtype=np.int64)
    for j in range(size):
        out[j] = rng.choice(D, size=k, replace=False, p=w)
    return out


def build(T, n_voters, P, n_deleg, k, D, design, vprof, rprof, seed=1):
    rng = np.random.default_rng(seed)
    L = min(0.4 * T, 6 * H)
    dl = T - L
    ev_t, ev_w, ev_c = [], [], []  # times, weight fraction, class (0 direct, 1 dc)

    # direct votes: one batch per voter, P casts
    tv = arrivals(rng, n_voters, T, vprof)
    pre = tv < dl
    tp = tv[pre]
    win = np.minimum(100 * H, dl - tp)
    n_pre = tp.size * P * 16
    s = np.repeat(tp, P * 16) + rng.random(n_pre) * np.repeat(win, P * 16)
    ev_t.append(s); ev_w.append(np.full(n_pre, 1 / 16)); ev_c.append(np.zeros(n_pre, np.int8))
    tl = np.repeat(tv[~pre], P)
    ev_t.append(tl); ev_w.append(np.ones(tl.size)); ev_c.append(np.zeros(tl.size, np.int8))

    if n_deleg:
        td = arrivals(rng, n_deleg, T, vprof)
        dels = zipf_choice(rng, D, n_deleg, k)  # (n_deleg, k)
        tau = route_times(rng, D, T, rprof)
        t_dc = np.repeat(td, k)
        d_dc = dels.reshape(-1)
        pre = t_dc < dl
        if design == "labeled":
            tp = t_dc[pre]
            win = np.minimum(100 * H, dl - tp)
            n = tp.size * 16
            s = np.repeat(tp, 16) + rng.random(n) * np.repeat(win, 16)
            ev_t.append(s); ev_w.append(np.full(n, 1 / 16)); ev_c.append(np.ones(n, np.int8))
            tl = t_dc[~pre]
            ev_t.append(tl); ev_w.append(np.ones(tl.size)); ev_c.append(np.ones(tl.size, np.int8))
        else:
            tp = t_dc[pre]
            taup = tau[d_dc[pre]]
            win = np.minimum(100 * H, dl - tp)
            n = tp.size * 16 * P
            s = np.repeat(tp, 16 * P) + rng.random(n) * np.repeat(win, 16 * P)
            tr = np.repeat(taup, 16 * P)
            late = tr > s
            respread = tr + rng.random(n) * np.maximum(0.0, dl - tr)
            e = np.where(late, np.where(tr < dl, respread, tr), s)
            ev_t.append(e); ev_w.append(np.full(n, 1 / 16)); ev_c.append(np.ones(n, np.int8))
            tl = t_dc[~pre]
            taul = tau[d_dc[~pre]]
            e = np.repeat(np.maximum(tl, taul), P)
            ev_t.append(e); ev_w.append(np.ones(e.size)); ev_c.append(np.ones(e.size, np.int8))
    t = np.concatenate(ev_t)
    w = np.concatenate(ev_w)
    c = np.concatenate(ev_c)
    order = np.argsort(t, kind="stable")
    return t[order], w[order], c[order]


def loss(t, w, c, T, mu):
    m = min(mu, CHAIN_CAP)
    idx = np.arange(t.size, dtype=np.float64)
    run = np.maximum.accumulate(t - idx / m)
    comp = (idx + 1) / m + run
    lost = comp > T - 300
    out = {}
    for cls in (0, 1):
        sel = c == cls
        tot = w[sel].sum()
        out[cls] = (w[sel & lost].sum() / tot) if tot else 0.0
    return out, comp


def min_workers(t, w, c, T, r, dup, target=0.001):
    """Smallest fleet-wide worker count with weight loss <= target for both classes."""
    lo, hi = 1, 4096
    o, _ = loss(t, w, c, T, hi * r / dup)
    if max(o.values()) > target:
        return None, o
    while lo < hi:
        mid = (lo + hi) // 2
        o, _ = loss(t, w, c, T, mid * r / dup)
        if max(o.values()) <= target:
            hi = mid
        else:
            lo = mid + 1
    return lo, None


def peak_rate(t, window):
    """Max eligible reveals per second over any aligned window (s)."""
    b = np.floor(t / window).astype(np.int64)
    counts = np.bincount(b)
    return counts.max() / window


if __name__ == "__main__":
    T = 7 * DAY
    r = 0.58
    scenarios = [
        ("a", 2000, 10, 2000, 1, 50),
        ("b", 2000, 10, 2000, 3, 50),
        ("c", 2000, 10, 20000, 3, 200),
        ("a37", 2000, 37, 2000, 1, 50),
        ("b37", 2000, 37, 2000, 3, 50),
        ("c37", 2000, 37, 20000, 3, 200),
    ]
    vprof = sys.argv[1] if len(sys.argv) > 1 else "deadline"
    rprofs = sys.argv[2].split(",") if len(sys.argv) > 2 else ["early", "deadline", "late"]
    print(f"T=7d arrivals={vprof} r={r}/s/worker cap={CHAIN_CAP:.1f}/s")
    print("scen,design,route,reveals,dc_reveals,avg_rate_pre_window,peak_1h_rate,peak_10m_rate,"
          "Wmin_dup1,Wmin_dup2,loss_at_20w_dup1(direct/dc),loss_at_20w_dup2(direct/dc),loss_at_cap(direct/dc)")
    for name, nv, P, nd, k, D in scenarios:
        for design in ("labeled", "labelfree"):
            for rp in (rprofs if design == "labelfree" else ["-"]):
                t, w, c = build(T, nv, P, nd, k, D, design, vprof, rp if rp != "-" else "early")
                n = t.size
                ndc = int((c == 1).sum())
                L = min(0.4 * T, 6 * H)
                avg = (t < T - L).sum() / (T - L)
                p1h = peak_rate(t, 3600)
                p10 = peak_rate(t, 600)
                w1, _ = min_workers(t, w, c, T, r, 1)
                w2, _ = min_workers(t, w, c, T, r, 2)
                l20a, _ = loss(t, w, c, T, 20 * r)
                l20b, _ = loss(t, w, c, T, 20 * r / 2)
                lcap, _ = loss(t, w, c, T, 1e9)
                f = lambda o: f"{100*o[0]:.1f}%/{100*o[1]:.1f}%"
                print(f"{name},{design},{rp},{n},{ndc},{avg:.1f},{p1h:.1f},{p10:.1f},"
                      f"{w1 if w1 else 'infeasible'},{w2 if w2 else 'infeasible'},{f(l20a)},{f(l20b)},{f(lcap)}")
                sys.stdout.flush()
                del t, w, c
