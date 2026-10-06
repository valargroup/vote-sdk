from helper_load_sim import run, DAY
T = 7 * DAY
mu = 10 * 2 * 0.58
for (nd, k, nv, p, label) in [(0,0,5000,10,"votes only 5k x10"),(5000,3,5000,10,"L2-like 5k deleg x3"),(10000,3,5000,10,"10k deleg x3"),(20000,3,5000,10,"20k deleg x3"),(50000,3,5000,10,"50k deleg x3")]:
    for prof in ("uniform","deadline"):
        for dup in (1.0,2.0):
            r = run(T, nd, k, nv, p, prof, mu/dup)
            dcs = max(1, nd*k); vcs = nv*p
            print(f"{label:22s} {prof:8s} dup={dup:.0f} shares={r['shares']:>9,} lostDC={100*r['lost_weight']['dc']/dcs:5.1f}% lostVC={100*r['lost_weight']['vc']/vcs:5.1f}% backlog@T-6h={r['backlog_h'].get(6.0,0):.1f}h maxwait={r['max_wait_h']:.1f}h")
