"""Helper-fleet queue model for proxy-delegation share reveals.

Models the existing submit_at policy (zcash_voting share_policy):
  - last-moment buffer L = min(40% of round, 6 h)            (timing.rs:19-45)
  - before the buffer: 16 shares, submit_at ~ U[t, min(t+100h, T-L)] (submission_schedule.rs:6)
  - inside the buffer: 1 share, submit_at = 0 (immediate)
Helpers: one global FIFO by eligible time (helper_submission_invariants.md
"earlier effective schedule time is selected first"), deterministic service
rate mu = operators * workers * 0.58 / dup (runbooks/software-upgrades.md:446-457).
A share is lost if it completes after T - 5 min (retry_schedule.go:10-16, 68-82).
"""
import random, sys

DAY = 86400.0
H = 3600.0

def run(T, n_deleg, dcs_per, n_voters, props, profile, mu, seed=1, vote_profile=None):
    rnd = random.Random(seed)
    L = min(0.4 * T, 6 * H)
    cutoff = T - 300.0
    late_block = T - 600.0  # Vizor blocks new delegation at end - 10 min
    ev = []  # (eligible_time, weight_fraction, kind)

    def arrival(prof):
        u = rnd.random()
        if prof == "uniform":
            return u * late_block
        # deadline-heavy: 50% over [0, T-24h], 35% over [T-24h, T-6h], 15% over [T-6h, T-10m]
        v = rnd.random()
        if v < 0.50:
            return u * (T - 24 * H)
        if v < 0.85:
            return (T - 24 * H) + u * (18 * H)
        return (T - 6 * H) + u * (6 * H - 600)

    def emit(t, kind):
        if t >= T - L:
            ev.append((t, 1.0, kind))
        else:
            win = min(100 * H, (T - L) - t)
            for _ in range(16):
                ev.append((t + rnd.random() * win, 1.0 / 16, kind))

    for _ in range(n_deleg):
        t = arrival(profile)
        for _ in range(dcs_per):
            emit(t, "dc")
    for _ in range(n_voters):
        t = arrival(vote_profile or profile)
        for _ in range(props):
            emit(t, "vc")
    ev.sort()
    c = 0.0
    lost_shares = {"dc": 0, "vc": 0}
    lost_weight = {"dc": 0.0, "vc": 0.0}
    tot = {"dc": 0, "vc": 0}
    max_wait = 0.0
    backlog_at = {}
    marks = [T - 24 * H, T - 6 * H, T - 1 * H]
    mi = 0
    svc = 1.0 / mu
    for (a, w, k) in ev:
        while mi < len(marks) and a >= marks[mi]:
            backlog_at[marks[mi]] = max(0.0, c - marks[mi])
            mi += 1
        start = a if a > c else c
        c = start + svc
        tot[k] += 1
        wait = c - a
        if wait > max_wait:
            max_wait = wait
        if c > cutoff:
            lost_shares[k] += 1
            lost_weight[k] += w
    return dict(shares=len(ev), tot=tot, lost_shares=lost_shares, lost_weight=lost_weight,
                max_wait_h=max_wait / H, backlog_h={round((T - m) / H, 1): round(v / H, 2) for m, v in backlog_at.items()})

if __name__ == "__main__":
    T = 7 * DAY
    base_mu = 10 * 2 * 0.58
    for profile in ("uniform", "deadline"):
        for dup in (1.0, 2.0, 5.0):
            r = run(T, 50_000, 3, 5_000, 10, profile, base_mu / dup)
            dcs = 50_000 * 3
            vcs = 5_000 * 10
            print(f"{profile:8s} dup={dup:.0f} mu={base_mu/dup:5.2f}/s shares={r['shares']:,} "
                  f"lostDCw={r['lost_weight']['dc']:.0f}/{dcs} ({100*r['lost_weight']['dc']/dcs:.1f}%) "
                  f"lostVCw={r['lost_weight']['vc']:.0f}/{vcs} ({100*r['lost_weight']['vc']/vcs:.1f}%) "
                  f"maxwait={r['max_wait_h']:.1f}h backlog(h before end->h)={r['backlog_h']}")
        sys.stdout.flush()
