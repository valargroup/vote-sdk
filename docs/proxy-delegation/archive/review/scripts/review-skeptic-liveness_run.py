import sys
sys.path.insert(0, '.')
from sim import run, DAY
T = 7*DAY
mu0 = 10*2*0.58
def show(label, nd, k, nv, P, prof, dup):
    r = run(T, nd, k, nv, P, prof, mu0/dup)
    dcs = max(nd*k,1); vcs = max(nv*P,1)
    print(f"{label:38s} {prof:8s} dup={dup} shares={r['shares']:>9,} lostDC={100*r['lost_weight']['dc']/dcs:5.1f}% lostVC={100*r['lost_weight']['vc']/vcs:5.1f}% backlog={r['backlog_h']}")
for prof in ("uniform","deadline"):
  for dup in (1,2):
    show("baseline L1: 5k voters x10", 0,0,5000,10,prof,dup)
    show("L2: +5k deleg x2.5(~3)", 5000,3,5000,10,prof,dup)
    show("+10k deleg x3", 10000,3,5000,10,prof,dup)
    show("+20k deleg x3", 20000,3,5000,10,prof,dup)
    show("direct only: 10k voters x10", 0,0,10000,10,prof,dup)
    show("direct only: 5k voters x37", 0,0,5000,37,prof,dup)
    show("direct only: 2k voters x37", 0,0,2000,37,prof,dup)
