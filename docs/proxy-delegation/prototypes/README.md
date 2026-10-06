# Proxy delegation prototypes

These throwaway planning prototypes back the `[measured]` claims in [../PLAN.md](../PLAN.md). Use them as references, not production code. Timings were taken on a loaded M3 Ultra, so re-measure on validator droplets and phones before relying on absolute numbers.

| Path | What it is | How to run |
|---|---|---|
| `circuits/zkp4-prototype.patch` | ZKP4 (C1-C15) as a new `proxy_delegation` module. Includes the 8-slot nullifier cap (3-bit check), `delegate_index_bound`, the last-moment single-share test and the ZKP3 `(0, d)` reveal. Rev 6 drops C15 and `delegate_index_bound` (PLAN.md O1); the patch keeps them as the measured reference | Apply to a voting-circuits v0.12.2 tree with `git apply -p2`, then run `cargo test --release --lib proxy_delegation` |
| `circuits/test_suite.log`, `measure_rows.log`, `prove_timing_threads.log` | Rev 3 run: 20/20 tests, 2,015/2,048 rows at K=11, 11,008 B proof, prove times by thread count | |
| `circuits/single_share_test.log` | Earlier layout-1 test run cited in the plan | |
| `chain/route_test.go` | Routing with the `Enc(0; rho)` re-randomizer, and the identity-C1 griefing vector it blocks | Copy into vote-sdk and run `go test` against `crypto/elgamal` |
| `chain/dk_test.go` | Ed25519 key checks: canonical, small-order and torsion | As above |
| `client/planner.py` | Allocation planner as of rev 5: Hamilton largest remainder, minimum-1 bump, exact packing with `#DC ≤ D + B − 1`, up to 8 delegates | `python3 planner.py` |
| `client/compare_packers.py`, `compare_packers.out` | Rev 6 packer comparison over 19,961 simulated wallets: rev 5's exact packer (2.834 DCs per delegator), the adopted sequential fill (2.845) and per-bundle Hamilton (4.068, rejected) | `python3 compare_packers.py` (imports `planner.py`) |
| `privacy/pool_inference.py` | How often hidden pool totals are exactly solvable from public results, with and without an Abstain option | `python3 pool_inference.py` |
| `privacy/reveal_linking.py` | How far reveal timing narrows which DC transaction fed a small pool | `python3 reveal_linking.py` |
| `labelfree/MEMO.md` | Decision memo on the rejected label-free (route-proving reveal) alternative (decision 31) | |
| `labelfree/zkp3r-prototype-full.patch` | ZKP3R route-proving reveal circuit prototype | Apply to a voting-circuits v0.12.2 tree with `git apply -p2` |
| `labelfree/zkp3r_*.log` | ZKP3R rows, timing and test results | |
| `labelfree/*.py`, `*.out` | Load, queue and leak models behind the label-free numbers | `python3 <script>.py` |
