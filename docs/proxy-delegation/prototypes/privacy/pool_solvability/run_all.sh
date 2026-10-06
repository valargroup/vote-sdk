#!/bin/sh
# Reproduce every output in this directory (about 2 minutes on 24 cores).
set -e
cd "$(dirname "$0")"
PY=.venv/bin/python
$PY validate.py | tee validate.out
$PY sim.py 100 results.jsonl
$PY summarize.py results.jsonl > results.md
$PY headline.py > headline.md
mkdir -p sens
$PY sim.py 100 sens/cover1.jsonl '{"cover_per_bucket": 1.0}'
$PY sim.py 100 sens/cover4.jsonl '{"cover_per_bucket": 4.0}'
$PY sim.py 100 sens/no_contrarian_no_fringe.jsonl '{"contrarian_p": 0.0, "fringe_p": 0.0}'
$PY sim.py 100 sens/no_contrarian.jsonl '{"contrarian_p": 0.0}'
$PY sim.py 100 sens/alpha1.6.jsonl '{"follow_alpha": 1.6}'
$PY sim.py 100 sens/r_all_1.jsonl '{"r_lo": 0.97, "r_hi": 1.0}'
for f in sens/*.jsonl; do $PY summarize.py "$f" > "${f%.jsonl}.md"; done
$PY sens_table.py baseline,b2_cover,b1+b2,a+b2,b1_complete_routes,a_da_bucket,b3_kfloor2 0.0,0.1 > sensitivity.md
$PY posterior.py > posterior.out
