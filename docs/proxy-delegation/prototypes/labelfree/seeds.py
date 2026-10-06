"""Min fleet workers (dup=1, r=0.58/s) for <=0.1% weight loss, 5 seeds."""
import sys, numpy as np, load_sim as ls
T = 7 * 86400.0
prof = sys.argv[1]
scen = [("a",2000,10,2000,1,50),("b",2000,10,2000,3,50),("c",2000,10,20000,3,200),
        ("a37",2000,37,2000,1,50),("b37",2000,37,2000,3,50),("c37",2000,37,20000,3,200)]
print("scen,design,route,median_W,min_W,max_W,infeasible_seeds,median_loss_at_cap_dc")
for name,nv,P,nd,k,D in scen:
    for design,rp in [("labeled","early"),("labelfree","early"),("labelfree","deadline"),("labelfree","late")]:
        Ws=[]; inf=0; capl=[]
        for seed in range(1,6):
            t,w,c = ls.build(T,nv,P,nd,k,D,design,prof,rp,seed=seed)
            W,_ = ls.min_workers(t,w,c,T,0.58,1)
            o,_ = ls.loss(t,w,c,T,1e9)
            capl.append(o[1])
            if W is None: inf+=1
            else: Ws.append(W)
            del t,w,c
        Ws.sort()
        med = Ws[len(Ws)//2] if Ws else None
        print(f"{name},{design},{rp if design=='labelfree' else '-'},{med},{min(Ws) if Ws else None},{max(Ws) if Ws else None},{inf},{100*float(np.median(capl)):.1f}%")
        sys.stdout.flush()
