"""Reveal-load arithmetic: labeled pools vs label-free route-proving reveals.
Inputs: L2-like round from PLAN §9.1 (5k direct voters x 10 proposals, 10k delegators x 3 DCs),
route rate 0.85 x (1 - 0.08 abstain) per (delegate, proposal), direct participation 100% as in
the liveness review. Fleet 10 operators x {2,4} workers x 0.58 reveals/s (staging, dup = 1).
Tx bytes: ZKP3 reveal ~5.2 KB (4,992 B proof); ZKP3U ~8 KB [inference]."""
P = 10
pr = P * 0.85 * 0.92
direct = 5_000 * P * 16
dcs = 10_000 * 3
zero_keep = 0.68  # 1 - 0.32 from zero_shares.out (median 25 ZEC)
rows = [
    ("labeled (16 per DC)", dcs * 16, 5.2, 5.2),
    ("label-free, 16 shares per DC", dcs * 16 * pr, 8.0, 8.0),
    ("label-free, 16/DC, skip value zeros", dcs * 16 * pr * zero_keep, 8.0, 8.0),
    ("label-free, budget 16 per delegator (k=4 at 3 DCs)", dcs * 4 * pr, 8.0, 8.0),
    ("label-free, k=1 per DC", dcs * 1 * pr, 8.0, 8.0),
]
print(f"P={P}, routed proposals per DC={pr:.2f}, direct reveals={direct:,}")
print("design,DC_reveals,total_reveals,x_labeled_total,fleet_h_2w,fleet_h_4w,block_GB")
lab_total = direct + dcs * 16
for name, r, kb_dc, kb_direct in rows:
    tot = direct + r
    kb_d = 5.2 if name.startswith("labeled") else kb_direct
    gb = (direct * kb_d + r * kb_dc) / 1e6
    print(f"{name},{r:,.0f},{tot:,.0f},{tot/lab_total:.2f},{tot/11.6/3600:.0f},{tot/23.2/3600:.0f},{gb:.1f}")
print()
print("late route: hours of whole-fleet capacity needed to reveal one delegate's following for one route tx")
print("n_dc,proposals_in_route,reveals,h_at_11.6/s,h_at_23.2/s,min_at_chain_cap_213/s")
for n in (10, 100, 1000, 5000):
    for pp in (1, 10):
        rv = n * 16 * pp
        print(f"{n},{pp},{rv:,},{rv/11.6/3600:.2f},{rv/23.2/3600:.2f},{rv/213/60:.1f}")
