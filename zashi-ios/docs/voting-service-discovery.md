# Voting Service Discovery

How Zashi discovers vote servers and PIR servers at runtime.

## Resolution order

1. **Local override** — `voting-config-local.json` bundled in the app (DEBUG builds only)
2. **CDN** — `https://zally-phi.vercel.app/api/voting-config` (served from Vercel Edge Config)
3. **Hardcoded fallback** — deployed dev server (`46.101.255.48`)

The first source that succeeds wins. This means a TestFlight build works out of the box (CDN or fallback), while a developer can drop a local file into the bundle to point at localhost.

## Config format

```json
{
  "version": 1,
  "vote_servers": [
    { "url": "https://46-101-255-48.sslip.io", "label": "Primary", "operator_address": "zvote1abc..." }
  ],
  "pir_servers": [
    { "url": "https://46-101-255-48.sslip.io/nullifier", "label": "PIR Server" }
  ]
}
```

**Important:** The JSON keys must be `vote_servers` and `pir_servers` — these map to `VotingServiceConfig.CodingKeys` in Swift. Using other key names (e.g. `nullifier_providers`) will cause silent decode failure, falling through to CDN/fallback.

The `operator_address` field is optional and used by the self-registration system to track which validator owns each entry. Swift `Codable` ignores unknown keys, so adding this field is backward-compatible with existing iOS builds.

`vote_servers` entries each serve the full set of endpoints — both chain API (`/zally/v1/*`) and helper API (`/api/v1/shares`). This is because the SDK and helper server are a single merged binary.

`pir_servers` serve the PIR nullifier exclusion proof protocol (port 3000 by default).

## Self-registration

Validators can register their URL with a single command via `join.sh`. The registration flow has two phases:

**Phase 1 (not yet bonded):** `join.sh` signs a registration payload with the validator's operator key and POSTs it to `/api/register-validator`. Since the validator isn't bonded yet, the entry goes into a `pending-registrations` queue (7-day expiry). The vote-manager sees pending registrations in the admin UI and clicks "Approve & Fund" to move the URL to `vote_servers` and send stake in one action.

**Phase 2 (bonded):** After `join.sh` registers the validator on-chain (via `create-val-tx`), it re-registers with the same endpoint. This time the edge function detects the validator is bonded and promotes the URL directly to `vote_servers` — no admin approval needed.

Both phases use the same endpoint (`POST /api/register-validator`) and the same ADR-036 amino signature format. The edge function decides the path based on on-chain bonding status. Both phases also write to `approved-servers` (see below).

The `zallyd sign-arbitrary` command provides the signature:
```bash
zallyd sign-arbitrary '{"operator_address":"...","url":"...","moniker":"...","timestamp":...}' \
  --from validator --keyring-backend test --home ~/.zallyd
```

## Server heartbeat (active vs approved)

`vote_servers` is split into two tiers:

- **`approved-servers`** (persistent) — once a server is approved (via admin approval or on-chain bonding), it stays in this list unless manually removed. Survives reboots and pulse gaps.
- **`vote_servers`** (active) — only contains servers that are approved AND actively pulsing. This is what iOS reads. Servers are added when they pulse and evicted if no pulse is received for >2 minutes.

### Startup and heartbeat flow

On every `zallyd start`, the helper performs two steps:

**Step 1 — Register** (`POST /api/register-validator`): Called once on startup. The edge function checks on-chain bonding status. If the validator is bonded, it upserts into both `approved-servers` and `vote_servers`. If not bonded, the entry goes to `pending-registrations` for admin approval. This is the same endpoint `join.sh` uses, so existing bonded validators automatically populate `approved-servers` on their first restart with the new binary — no manual re-registration needed.

**Step 2 — Pulse** (`POST /api/server-heartbeat`, every 30s): The edge function checks `approved-servers`:
- **Approved** → upsert into `vote_servers`, update `server-pulses[url] = now`, evict stale entries (>2 min), return `{ status: "active" }`.
- **Not approved** → upsert into `pending-registrations`, return `{ status: "pending" }`.

Both endpoints use the same payload and ADR-036 signature format (`{ operator_address, url, moniker, timestamp }`).

A safety-net cron (`/api/evict-stale-servers`, every 2 minutes) handles eviction when all servers are down and nobody is pulsing.

### Edge Config keys

| Key | Purpose |
|-----|---------|
| `voting-config` | Active config — `vote_servers` and `pir_servers`. iOS reads this. |
| `approved-servers` | Persistent list of `{ url, label, operator_address }`. Only removed manually. |
| `server-pulses` | Map `{ [url]: unix_timestamp }`. Updated every 30s by each server. |
| `pending-registrations` | Unapproved servers waiting for admin approval (7-day expiry). |

### Helper configuration

The heartbeat is configured in `app.toml` under `[helper]`:

```toml
# Vercel base URL for the heartbeat endpoint.
pulse_url = "https://zally-phi.vercel.app"

# This server's public URL as seen by clients (the Caddy TLS URL).
helper_url = "https://1-2-3-4.sslip.io"
```

Both fields must be set for the heartbeat to activate. `join.sh` writes these automatically — `pulse_url` from `VOTING_CONFIG_URL` and `helper_url` from the detected `VALIDATOR_URL` after Caddy TLS setup. Local dev scripts (`init.sh`, `init_multi.sh`) leave both empty to disable the heartbeat.

### Lifecycle

1. `join.sh` runs → `register-validator` adds server to `approved-servers` + `vote_servers`
2. `zallyd start` → helper calls `register-validator` once (ensures `approved-servers` is populated for bonded validators)
3. Helper starts 30s pulse loop → calls `server-heartbeat` which keeps the server in `vote_servers`
4. If the server stops (crash, restart, network issue), it is evicted from `vote_servers` after 2 minutes but stays in `approved-servers`
5. On restart, step 2 re-registers, step 3 resumes pulsing — server re-appears in `vote_servers` automatically
6. Admin can see active/inactive status in the admin UI "Approved servers" panel

## Local testing

`mise start` and `mise run multi:start` automatically write `secant/Resources/voting-config-local.json` with the correct ports for the mode being started. The file is gitignored and only bundled in DEBUG builds (via an Xcode build phase), taking priority over CDN.

| Mode             | Chain REST port | Command                | Auto-written? |
| ---------------- | --------------- | ---------------------- | ------------- |
| Single validator | 1318            | `mise start`           | Yes           |
| Multi validator  | 1418            | `mise run multi:start` | Yes           |

Whichever mode you start last wins, which is correct since you can only test against one chain at a time.

To manually override, edit the file directly — it won't be overwritten until the next `mise start` or `multi:start`.

## Where the URLs flow

```
VotingStore.initialize
  → votingAPI.fetchServiceConfig()        // resolves config per order above
  → votingAPI.configureURLs(config)       // sets ZallyAPIConfigStore actor
  → all subsequent API calls use resolved URLs
```

The resolved config is also stored in `VotingStore.State.serviceConfig` so the store can read URLs for the IMT server and chain node directly (used by `votingCrypto.syncVoteTree` and delegation proof).

## Share distribution

When multiple vote servers are configured, encrypted shares are distributed across them instead of all going to one server. With N servers and 5 shares:

- N >= 5: one share per server (shuffled)
- 1 < N < 5: round-robin
- N == 1: all shares to that server

## Config deployment

The config is served from **Vercel Edge Config**, a key-value store that can be updated instantly without redeployment. The edge function at `shielded_vote_generator_ui/api/voting-config.ts` reads the `voting-config` key and returns it as JSON.

### Updating the config

1. **Admin UI** (primary): Register/remove validator URLs via the Validators panel, or approve pending self-registrations. The "Approved servers" panel shows which servers are actively pulsing.
2. **Self-registration**: `join.sh` auto-registers via `POST /api/register-validator` (admin approves in UI, or auto-promoted after bonding). The heartbeat (`/api/server-heartbeat`) keeps the server in `vote_servers` after registration.
3. **Vercel Dashboard**: Go to the project's Edge Config store → edit keys directly (`voting-config`, `approved-servers`, `server-pulses`, `pending-registrations`)
4. **Vercel CLI**: `vercel edge-config items update voting-config --value '{"version":1,...}'`

Changes take effect immediately — no git push or redeploy needed. This is useful for demos where you spin up new servers and want TestFlight builds to pick them up right away.

### Setup (one-time)

1. In the Vercel dashboard, go to **Storage** → **Create** → **Edge Config**
2. Connect it to the `zally` project
3. Add a key `voting-config` with the JSON value:
   ```json
   {
     "version": 1,
     "vote_servers": [
       { "url": "https://46-101-255-48.sslip.io", "label": "Primary", "operator_address": "zvote1..." }
     ],
     "pir_servers": [
       { "url": "https://46-101-255-48.sslip.io/nullifier", "label": "Primary" }
     ]
   }
   ```
4. Vercel auto-sets the `EDGE_CONFIG` env var on the project
