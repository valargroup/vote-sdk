# Block explorer for svote-1 (systemd-native)

## Goal

Stand up a public, open-source block explorer for the `svote-1` chain that
shows **the real prod chain** (val1/val2/val3) with full history from
genesis, deployed and reset by the same CI workflows that manage the
validators today.

The explorer is a static ping-pub/explorer Vue bundle served by Caddy, backed
by a new non-validating svoted process ("archive") that runs alongside
val1/val2/val3 on the prod box as a systemd service. The archive shares
genesis with the validators, peers with val1 over p2p, and keeps
`pruning = "nothing"` so every block stays queryable forever.

## Non-goals

- Custom UI for `x/vote` messages. Vote txs render as raw JSON in ping-pub's
  generic tx view. A custom module page is a follow-up if needed.
- Multi-chain dropdown. Only `svote-1` is configured; a second chain entry
  can be added alongside later.
- Docker on prod. The prod box runs systemd. Nothing about this spec adds
  docker to prod; the only docker remaining would be the optional local-dev
  setup, which is out of scope here.
- Historical indexing beyond what the archive's RPC/LCD can serve directly.
  No SQL, no GraphQL, no Callisto/BDJuno. If a future query needs
  aggregations the daemon can't serve, that's a separate project.
- Gating / auth. The explorer is publicly readable. No login, no API keys.

## Architecture

Four svoted processes on the prod box, plus Caddy and a static directory:

```
sdk-chain-deploy.yml
 │                                  ┌──────────────────────────────────────┐
 │ (build-explorer job)             │       Caddy (already running)        │
 │ yarn install; yarn build;        │ ┌────────────────────────────────┐   │
 │ tar czf explorer-dist.tar.gz     │ │ val1.*.sslip.io   → :1418      │   │
 │                                  │ │ val2.*.sslip.io   → :1518      │   │
 │ (deploy job)                     │ │ val3.*.sslip.io   → :1618      │   │
 │ scp dist tarball + service unit ─┼─► explorer.*.sslip.io → file_server │
 │ systemctl restart svoted-archive │ │   /opt/shielded-vote/explorer/dist │
 │                                  │ │ explorer-api.* → localhost:1718    │
 │                                  │ │ explorer-rpc.* → localhost:26457   │
 │                                  │ └────────────────────────────────┘   │
 ▼                                  └──────────────────────────────────────┘
/opt/shielded-vote/
├── svoted
├── explorer/dist/                    (NEW — static Vue bundle)
├── .svoted-val1/   (systemd: svoted-val1.service)
├── .svoted-val2/   (systemd: svoted-val2.service)
├── .svoted-val3/   (systemd: svoted-val3.service)
└── .svoted-archive/                  (NEW — systemd: svoted-archive.service)
      config/genesis.json  ← copied from .svoted-val1/config/genesis.json
      config/config.toml   ← persistent_peers = <val1-node-id>@127.0.0.1:26156
      config/app.toml      ← pruning = "nothing", min-retain-blocks = 0
```

Explorer browser traffic lands on three sslip.io subdomains (`explorer.*`,
`explorer-api.*`, `explorer-rpc.*`) and never touches val1/val2/val3 — the
validators keep doing consensus, the archive answers all queries.

## Port allocation

Following the `+100` pattern already established by `init_multi.sh`:

|             | P2P     | RPC     | REST    | gRPC   | gRPC-web | pprof  |
|-------------|---------|---------|---------|--------|----------|--------|
| val1        | 26156   | 26157   | 1418    | 9390   | 9391     | 6160   |
| val2        | 26256   | 26257   | 1518    | 9490   | 9491     | 6260   |
| val3        | 26356   | 26357   | 1618    | 9590   | 9591     | 6360   |
| **archive** | **26456** | **26457** | **1718** | **9690** | **9691** | **6460** |

Only `1718` (REST) and `26457` (RPC) are reached by browser traffic (via
Caddy). The rest are internal to the box.

## Components

### 1. `scripts/init-archive.sh` (new, ~80 lines)

Runs after `init_multi.sh`. Assumes `.svoted-val1/config/genesis.json` exists
and is finalized. Sequence:

1. `rm -rf $HOME_ARCHIVE`; `svoted init archive --chain-id svote-1 --home $HOME_ARCHIVE`.
2. `svoted pallas-keygen --home $HOME_ARCHIVE` (not used, but satisfies app.toml config-path references; matches the validator init pattern).
3. **Copy val1's finalized genesis into the archive's config dir** — this is
   the key line. `cp $HOME_VAL1/config/genesis.json $HOME_ARCHIVE/config/genesis.json`.
4. Read val1's node ID via `svoted comet show-node-id --home $HOME_VAL1`.
5. Patch `$HOME_ARCHIVE/config/config.toml`:
   - `laddr` P2P `0.0.0.0:26456`, RPC `127.0.0.1:26457`, pprof `localhost:6460`
   - `persistent_peers = "<val1_node_id>@127.0.0.1:26156"`
   - `addr_book_strict = false`, `allow_duplicate_ip = true`
6. Patch `$HOME_ARCHIVE/config/app.toml`:
   - `[api] enable = true`, `address = "tcp://0.0.0.0:1718"`, `enabled-unsafe-cors = true`
   - gRPC `localhost:9690`, gRPC-web `localhost:9691`
   - `[vote]` `ea_sk_path` / `pallas_sk_path` / `comet_rpc` pointing at the archive's own dirs/ports (not actually used since the archive isn't a validator, but cosmos-sdk refuses to start if these are unset)
   - `pruning = "nothing"`, `min-retain-blocks = 0`
   - No `[helper]`, `[admin]`, `[ui]`, `[pir]` sections — the archive doesn't run any of those features.

Implementation note: `init_multi.sh` already has two helper functions
(`configure_config_toml`, `configure_app_toml`) that do most of this work.
`init-archive.sh` can `source` `init_multi.sh`'s helpers, or duplicate the
two helper functions inline. Duplication is probably cleaner since sourcing
a 500-line script for two helpers executes a lot of unrelated code.
Decision for the plan: duplicate the two helpers, keep init-archive.sh
self-contained.

The archive is always created on every chain init. No flag gates it — if the
chain runs, the archive runs.

### 2. `docs/svoted-archive.service` (new, ~15 lines)

Mirror of `docs/svoted-val2.service` with archive-specific flags:

```ini
[Unit]
Description=Shielded-Vote SDK chain - Archive node (svoted-archive)
After=svoted-val1.service
Requires=svoted-val1.service

[Service]
Type=simple
User=root
WorkingDirectory=/opt/shielded-vote
ExecStart=/opt/shielded-vote/svoted start --home /opt/shielded-vote/.svoted-archive
Restart=on-failure

[Install]
WantedBy=multi-user.target
```

No `--serve-ui`, no `--serve-pir`, no `--serve-admin` — the archive just
answers queries. `Requires=svoted-val1.service` means if systemd stops val1,
it stops the archive too (and starting the archive automatically starts
val1 if it isn't already running).

### 3. Explorer static bundle — CI-built, scp'd to box

ping-pub's Vue app is a static SPA: `yarn build` produces a `dist/` with
HTML, JS, CSS. No backend. No runtime config — chain config is baked in at
build time.

**Source files in-repo:**
- `deploy/explorer/svote.json` — chain config for ping-pub. Points at
  `https://explorer-api.46-101-255-48.sslip.io` and
  `https://explorer-rpc.46-101-255-48.sslip.io`. Denom `usvote`, bech32 `sv`,
  chain_id `svote-1`, pretty_name `Zcash Vote`.
- No Dockerfile, no nginx.conf — Caddy serves the static files directly, CI
  runs `yarn` natively on the runner.

**CI build (new job in `sdk-chain-deploy.yml`):**
```yaml
build-explorer:
  steps:
    - uses: actions/checkout@v4
    - uses: actions/setup-node@v4
      with: { node-version: '20' }
    - name: Build ping-pub bundle
      run: |
        corepack enable
        git clone https://github.com/ping-pub/explorer.git /tmp/ping-pub
        cd /tmp/ping-pub
        git checkout ee1ca5ee54d7bd2dc4c5b4df8f3b93595440e9c8
        cp $GITHUB_WORKSPACE/deploy/explorer/svote.json chains/testnet/svote.json
        yarn install
        yarn build
        tar czf $GITHUB_WORKSPACE/explorer-dist.tar.gz -C dist .
    - uses: actions/upload-artifact@v4
      with:
        name: explorer-dist
        path: explorer-dist.tar.gz
```

PINGPUB_SHA pinned to `ee1ca5ee54d7bd2dc4c5b4df8f3b93595440e9c8` (master on
2026-04-17). Upgrades are explicit SHA bumps.

### 4. `sdk-chain-deploy.yml` extensions

The existing deploy job already scp's `svoted`, `init_multi.sh`, validator
service files to the box. Extensions:

- Add `build-explorer` job (above) running in parallel with the svoted build.
- Deploy job additions:
  - `needs: [..., build-explorer]`
  - `actions/download-artifact` to pull `explorer-dist.tar.gz`
  - scp `explorer-dist.tar.gz` + `scripts/init-archive.sh` + `docs/svoted-archive.service` + `deploy/Caddyfile`
  - Remote commands after current init + validator-start:
    ```
    mkdir -p /opt/shielded-vote/explorer/dist
    tar xzf /tmp/explorer-dist.tar.gz -C /opt/shielded-vote/explorer/dist
    cp svoted-archive.service /etc/systemd/system/
    systemctl daemon-reload
    systemctl enable --now svoted-archive
    ```
- **Caddy reload is handled out-of-band** by the existing `caddy-deploy.yml`
  workflow, which already fires on any change to `deploy/Caddyfile`. This
  workflow does NOT reload Caddy; on a merge that changes both Caddyfile
  and this workflow's files, the two workflows run independently and both
  eventually converge. The new Caddy routes go live as soon as
  `caddy-deploy.yml` finishes.

### 5. `sdk-chain-reset.yml` extensions

The reset workflow wipes `.svoted-val{1,2,3}` and re-runs init. Extensions:

- In the stop phase: add `systemctl stop svoted-archive`.
- In the wipe phase: add `rm -rf /opt/shielded-vote/.svoted-archive`.
- In the init phase: after `bash scripts/init_multi.sh --ci`, add
  `bash scripts/init-archive.sh`.
- In the start phase: after the validators come up, add
  `systemctl start svoted-archive`.

Because `Requires=svoted-val1.service`, an automatic restart of val1 would
also cycle the archive, which is the desired behavior.

### 6. `deploy/Caddyfile` — three new hostname blocks

```
explorer.46-101-255-48.sslip.io {
	root * /opt/shielded-vote/explorer/dist
	file_server
	try_files {path} /index.html
}

explorer-api.46-101-255-48.sslip.io {
	reverse_proxy localhost:1718
}

explorer-rpc.46-101-255-48.sslip.io {
	reverse_proxy localhost:26457
}
```

`try_files {path} /index.html` is Caddy's equivalent of the nginx SPA
fallback — unknown paths (like `/svote-testnet/blocks/42`) serve
`index.html` so the Vue router can take over.

No CORS-header injection: the archive's `app.toml` has
`enabled-unsafe-cors = true`, so LCD/RPC set their own
`Access-Control-*` headers. The existing Caddyfile comment explicitly warns
against duplicating them.

## Data flow

**Normal browsing:**
1. Browser → `https://explorer.46-101-255-48.sslip.io/` → Caddy → static files → SPA loads.
2. SPA JS → `https://explorer-api.46-101-255-48.sslip.io/cosmos/...` → Caddy → `localhost:1718` → svoted-archive LCD.
3. SPA JS → `https://explorer-rpc.46-101-255-48.sslip.io/...` → Caddy → `localhost:26457` → svoted-archive Tendermint RPC.

**Archive sync (continuous, in the background):**
- svoted-archive's Tendermint layer holds a p2p connection to val1 at
  `127.0.0.1:26156` using `val1_node_id`. Every block val1 commits gets
  gossiped to the archive, which verifies it (via the genesis-derived
  validator set) and writes it to disk. `pruning = "nothing"` means the
  block is kept forever.
- The archive never produces or signs blocks. It has no validator key.

## Reset behavior

When someone triggers `sdk-chain-reset.yml`, all four processes stop, their
data dirs get wiped, `init_multi.sh` generates a fresh genesis (val1's home
is authoritative), `init-archive.sh` copies that genesis into the archive's
home, and everyone starts back up together. The archive ends up on the new
chain with the new validator set, synced from height 1.

A browser refreshing during reset briefly sees a connection error (archive
stopped), then height 0 (chain just started), then climbs. The static SPA
bundle never needs to be rebuilt unless we change the chain ID or endpoint
hostnames.

If only the archive needs to restart (without a full chain reset — e.g.
svoted binary upgrade that affects the archive's config format), a plain
`systemctl restart svoted-archive` works and doesn't touch the validators.

## Scope of changes

Files touched:

**New (4 files):**
- `scripts/init-archive.sh` — ~80 lines
- `docs/svoted-archive.service` — ~15 lines
- `deploy/explorer/svote.json` — ~25 lines (ping-pub chain config)
- (nothing else)

**Modified (3 files):**
- `.github/workflows/sdk-chain-deploy.yml` — add `build-explorer` job; extend
  deploy job to scp + install archive/explorer artifacts. ~60 lines.
- `.github/workflows/sdk-chain-reset.yml` — add archive stop/wipe/init/start
  lines. ~10 lines.
- `deploy/Caddyfile` — 3 new hostname blocks. ~15 lines.

**Unchanged:**
- `scripts/init_multi.sh` — archive init lives in a separate script.
- Any `x/`, `app/`, `cmd/` code — no chain-daemon changes.
- Existing validator service units.

Total: ~200 lines of config + shell + one YAML workflow diff. No Go code.

## Testing

- **Local dev:** unchanged. `mise run multi:start` still works. The archive is
  a CI/prod-only concern for this spec. (An optional follow-up could add a
  local-archive helper target; out of scope here.)
- **CI:** `sdk-chain-reset.yml` already runs the full init + start sequence on
  the real prod box. Adding the archive to it makes every reset-chain run a
  full end-to-end test of the explorer stack. If `init-archive.sh` or the
  service unit is broken, reset fails and someone gets paged.
- **Post-merge smoke tests:** after the first deploy lands, manual verification:
  ```
  curl -sI https://explorer.46-101-255-48.sslip.io/                               # expect 200
  curl -s  https://explorer-api.46-101-255-48.sslip.io/cosmos/base/tendermint/v1beta1/blocks/latest | jq '.block.header.height'
  curl -s  https://explorer-rpc.46-101-255-48.sslip.io/status | jq '.result.node_info.moniker'   # "archive"
  ```
  and visually: browser open, blocks/validators/accounts pages render, recent
  tx from val1 (e.g. an auto-deal) appears.

## Open questions / follow-ups

- **Custom `x/vote` UI pages:** out of scope. Raw-JSON rendering is acceptable
  for pre-prod. A future fork of ping-pub with vote-specific pages is a
  separate spec.
- **Mainnet chain config:** when mainnet launches, add a second
  `deploy/explorer/zcash-vote.json`, a second archive service, and a second
  Caddy route set. Not in this spec.
- **Monitoring / sync lag:** out of scope. The only symptom of archive drift
  is stale explorer data (visible at a glance). If that becomes an
  operational concern, follow up with a Prometheus exporter or similar.
- **State-sync for faster archive bootstrap:** not worth it while the chain
  is young — genesis replay finishes in minutes. Revisit if the chain grows
  large enough that a full archive replay takes longer than people's
  patience during a chain reset.
