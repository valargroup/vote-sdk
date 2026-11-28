# Routing performance

Routing (contracts-v1 §5.6, `x/vote/keeper/keeper_proxy_routing.go`) runs in the EndBlock that moves a proxy round from ACTIVE to TALLYING, so its cost delays that one block. It adds each delegate's pool into the option its ballot chose on every proposal, one curve addition at a time, then re-randomizes each bucket that gained a pool.

## Benchmark

`BenchmarkEndBlockProxyRouting` (`x/vote/proxy_routing_test.go`) times that block on an IAVL store over an in-memory database, for a round of 50 proposals of 8 options with a direct vote in every bucket and delegates that each have a pool and a ballot. Run it with `-tags proxyenabled`; without the tag it fails, since EndBlock never routes. These are medians of `-benchmem -benchtime=3x -count=3` on an Apple M3 Ultra with Go 1.24.3, per transition, measured on 2026-11-16.

| Delegates | Time | Allocated | Allocations |
|---|---|---|---|
| 1,000 | 0.40 s | 419 MB | 10.5 M |
| 2,000 | 0.51 s | 572 MB | 14.3 M |

`max_delegates` is capped at 2,000 (`types.MaxDelegates`), so 2,000 is the worst case. Routing runs on one goroutine. It was not measured on the validator droplets. An earlier, optimized version took 1.8 to 1.9 times as long there as on the M3 Ultra, which would put 2,000 delegates at about 1 s. The node's `timeout_commit` is 950 ms and blocks with transactions commit about every 1.2 s (`docs/blocktimes.md`), so the transition block would land about 1 s late on every validator alike. That is a one-off delay, not a timeout risk, since every validator finishes routing before it starts the next height.

## Raising the cap

A faster routing was built and then removed to keep this code small: it summed pools in Jacobian coordinates without allocating, grouped them by the options their ballots chose on blocks of three proposals, and decoded them with a faster square root. It took 0.67 s at 20,000 delegates on the M3 Ultra. It computes the same group elements, so it writes identical bytes and can return without a state change. Raising `max_delegates` past 2,000 should come with it.
