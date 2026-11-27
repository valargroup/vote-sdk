# Isolated e2e localnet

`scripts/localnet-isolated/` runs the Rust e2e suite (`e2e-tests/`) against a chain that cannot collide with your own. Binaries, homes, logs and pid files live under one directory, every listener is on a non-default loopback port, and nothing is installed into `~/go/bin` or written to `~/.svoted*`. A chain from `make init` or `make init-multi` can run beside it.

## Quick start

```bash
make localnet-isolated-init     # build svoted into $LOCALNET_DIR/bin, write genesis
make localnet-isolated-start    # run in the background, wait for blocks
make localnet-isolated-test     # the ignored e2e tests, as make test-api runs them
make localnet-isolated-stop
```

- `PROXY=1 make localnet-isolated-init` builds svoted with `-tags proxyenabled`, which opens the proxy delegation gate (`x/vote/types/proxy_delegation_gate_enabled.go`), and opens delegate registration in genesis ([Proxy localnet](#proxy-localnet)). Production builds never set the tag.
- `LOCALNET_VALIDATORS=3 make localnet-isolated-init` builds a three-validator chain, which `round_activation` needs. As in CI, `start` registers validators 2 and 3 with `create-val-tx` and waits until all three are bonded with Pallas keys.
- To run one test by hand, load the environment the tests read: `eval "$(make -s localnet-isolated-env)"`, then `cargo test --release --manifest-path e2e-tests/Cargo.toml --test atomic_delegate_cast -- --ignored --nocapture`. `make localnet-isolated-test E2E_TEST_ARGS=voting_flow` filters the suite.

`init` refuses to run while the localnet runs and wipes everything under `LOCALNET_DIR` except `bin/`. `stop` signals only pids whose command line is this localnet's svoted for this localnet's home.

## Variables

`init` reads these and records them in `$LOCALNET_DIR/localnet.env`; `start`, `stop`, `test` and `env` act on the recorded values, so only `LOCALNET_DIR` has to be repeated.

| Variable | Default | Meaning |
|---|---|---|
| `LOCALNET_DIR` | `${TMPDIR:-/tmp}/svote-localnet-isolated` | Root for `bin/`, `val<N>/` homes, `logs/` and `pids/`. Must be absolute; `$HOME`, `~/.svoted*` and the repository are refused |
| `PROXY` | `0` | `1` builds with `-tags proxyenabled` and opens delegate registration in genesis |
| `LOCALNET_VALIDATORS` | `1` | Validator count, 1 to 9 |
| `LOCALNET_CHAIN_ID` | `svote-1` | Chain id; the e2e tests read it as `SVOTE_CHAIN_ID` |
| `LOCALNET_API_PORT` | `21317` | val1 REST API, which also serves the helper |
| `LOCALNET_RPC_PORT` | `36657` | val1 CometBFT RPC |
| `LOCALNET_P2P_PORT` | `36656` | val1 CometBFT P2P |
| `LOCALNET_ABCI_PORT` | `36658` | val1 CometBFT `proxy_app` |
| `LOCALNET_PROMETHEUS_PORT` | `36660` | val1 CometBFT Prometheus listener (off unless enabled) |
| `LOCALNET_GRPC_PORT` | `19090` | val1 gRPC |
| `LOCALNET_GRPC_WEB_PORT` | `19091` | val1 gRPC-web, used only if `app.toml` has a separate gRPC-web address |
| `LOCALNET_PPROF_PORT` | `16060` | val1 pprof |
| `LOCALNET_PORT_STRIDE` | `100` | Validator N uses each port plus (N − 1) × stride |
| `LOCALNET_SKIP_BUILD` | `0` | `1` reuses `bin/` from an earlier init of the same `PROXY` mode |
| `VM_PRIVKEYS` | generated | Vote-manager keys, as in `scripts/init.sh`. When unset, `init` generates one into `$LOCALNET_DIR/vm_privkeys`, or two with `PROXY=1`, since a proxy coordinator action needs two approvals |
| `SVOTE_HELPER_API_TOKEN` | empty | Helper `api_token`, as in `scripts/init.sh` |
| `SVOTE_HELPER_EXPOSE_QUEUE_STATUS` | `true` | Helper `expose_queue_status`. `scripts/init.sh` defaults to `false`; `voter_throughput` polls it |
| `SVOTE_HELPER_MAX_CONCURRENT_PROOFS_V3` | `2` | Helper proof workers, as in `scripts/init.sh` |
| `E2E_TEST_ARGS` | empty | `make localnet-isolated-test` only: extra libtest arguments |

`make localnet-isolated-env` prints the variables the tests read: `SVOTE_API_URL`, `HELPER_SERVER_URL`, `SVOTE_NODE_URL`, `SVOTE_HOME`, `SVOTE_PALLAS_PK_PATH`, `SVOTE_CHAIN_ID`, `VM_PRIVKEYS`, `HELPER_API_TOKEN`, and a `PATH` with `$LOCALNET_DIR/bin` first, because the tests run `svoted` to sign. On a `PROXY=1` localnet it also prints `SVOTE_PROXY_VERIFIER_SEED` (see [Proxy localnet](#proxy-localnet)).

## What init sets up

Genesis follows `scripts/init.sh` for one validator and `scripts/init_multi.sh` for several: vote managers from `VM_PRIVKEYS` as auth-only accounts, the `vote_funding` pool, the genesis validator's Pallas key in vote genesis, the staking and slashing patches, the per-round EA key path (`ea_sk_path`), and the `[helper]` and `[ui]` sections. Validator 1 stakes 10,000,000 usvote alone and 20,000,000 when there are joiners, as those scripts do.

On top of that, for isolation:

- every listener binds to 127.0.0.1 on the ports above, and `[vote] comet_rpc` and `[helper] chain_api_port` point at this node, since their defaults would reach a chain on 26657 and 1317;
- PEX is off and `persistent_peers` lists only this localnet's validators;
- `[admin]` stays disabled and its `config_url` points at a closed loopback port;
- `start` launches svoted with `SVOTE_PIR_URL=disabled` and without `SENTRY_DSN`, `SVOTE_LWD_URLS`, `SVOTE_ZCASH_NETWORK` or `SVOTE_CONFIG_URL`, so no live service is contacted.

## Tests the suite skips

- `create_round_for_zashi` reads a live lightwalletd (over `grpcurl`) and a live Ironwood PIR server, so `make localnet-isolated-test` always skips it.
- `round_activation` asserts a three-validator ceremony; it runs only when `LOCALNET_VALIDATORS` is 3 or more.

## Proxy localnet

`PROXY=1 make localnet-isolated-init` builds svoted with the proxy delegation gate open. Other repos' end-to-end tests run against it; this repo's own proxy flow is `make test-proxy-e2e`, which needs no localnet. A proxy round needs two vote-manager approvals, so `PROXY=1` generates two `VM_PRIVKEYS`.

**Genesis.** With `PROXY=1`, `init` sets `proxy_params` (contracts-v1 §9): verifier 1 with a development key, `verifier_threshold` 1 and `max_delegates` 2,000 (the default, 0, would leave the registry full). A `PROXY=0` genesis is unchanged.

**The development verifier key.** Its seed is public: the SHA-256 of `svote-localnet-isolated/dev-proxy-verifier/v1`, `4c3d8a9e62ae663a09864829d8fadfa14366db2513aac484ae56f1a16f590e88`, set in `scripts/localnet-isolated/common.sh`. Never put its key in a real chain's params. `make localnet-isolated-env` exports it as `SVOTE_PROXY_VERIFIER_SEED`.
