# Infrastructure

## Overview

All backend services run on a single DigitalOcean droplet (`46.101.255.48`) behind a
Caddy reverse proxy that provides automatic HTTPS via Let's Encrypt + sslip.io.

The frontend is deployed to Vercel.

## Network Discovery

Vercel Edge Config is the single entry point for network discovery. The `voting-config` key stores the list of public validator URLs and PIR servers. This is the same data iOS clients and `join.sh` use.

Edge Config is managed through the admin UI. When the bootstrap operator onboards a new validator, they can register its public URL in the admin UI (Validators → Register public URL), which writes to Edge Config. Registration is optional — validators participate in consensus and ceremonies regardless. They just won't be listed as public entry points.

Every validator node serves its own `genesis.json` at `/shielded-vote/v1/genesis`. Joining validators fetch genesis from the first discovered node. `network.json` is eliminated — CometBFT's peer exchange (PEX) handles peer discovery after the initial connection.

DO Spaces is used only for binary distribution (automated by `release.yml`).

## Services

| Service                        | Process              | Internal port | Systemd unit                     | Deploy path             |
| ------------------------------ | -------------------- | ------------- | -------------------------------- | ----------------------- |
| Shielded-Vote chain (REST API) | `svoted`             | 1318          | `svoted.service`                 | `/opt/shielded-vote`    |
| Helper server                  | embedded in `svoted` | 1318          | (same as above)                  | (same as above)         |
| Nullifier PIR server           | `nf-server`          | 3000          | `nullifier-query-server.service` | `/opt/nullifier-ingest` |

## External URLs

Caddy terminates TLS and routes by path:

| Service        | External URL                               | Example endpoint                    |
| -------------- | ------------------------------------------ | ----------------------------------- |
| Chain REST API | `https://46-101-255-48.sslip.io`           | `/shielded-vote/v1/rounds`          |
| Genesis        | `https://46-101-255-48.sslip.io`           | `/shielded-vote/v1/genesis`         |
| Helper server  | `https://46-101-255-48.sslip.io`           | `/api/v1/status`                    |
| Nullifier PIR  | `https://46-101-255-48.sslip.io/nullifier` | `/nullifier/` (Caddy strips prefix) |
| Frontend (UI)  | `https://shielded-vote.vercel.app`     | —                                   |
| Voting config  | `https://shielded-vote.vercel.app`     | `/api/voting-config`                |

## Frontend env vars

```bash
VITE_CHAIN_URL=https://46-101-255-48.sslip.io
VITE_NULLIFIER_URL=https://46-101-255-48.sslip.io/nullifier
```

### Edge Config env vars (Vercel project settings)

```bash
VERCEL_API_TOKEN=...     # Vercel REST API token with Edge Config write access
EDGE_CONFIG_ID=ecfg_...  # ID of the Edge Config store
CHAIN_API_URL=https://46-101-255-48.sslip.io  # For vote-manager verification
```

## Health checks

```bash
# Chain — list rounds
curl -sf https://46-101-255-48.sslip.io/shielded-vote/v1/rounds

# Genesis
curl -sf https://46-101-255-48.sslip.io/shielded-vote/v1/genesis | jq .chain_id

# Helper server — status
curl -sf https://46-101-255-48.sslip.io/api/v1/status

# Nullifier PIR server
curl -sf https://46-101-255-48.sslip.io/nullifier/health

# Voting config (Edge Config)
curl -sf https://shielded-vote.vercel.app/api/voting-config
```

## Ceremony

The EA key ceremony is automatic. When a voting round is created (via the admin UI
or `MsgCreateVotingSession`), the per-round ceremony runs via PrepareProposal:
auto-deal distributes ECIES-encrypted EA key shares to all validators, then
auto-ack confirms once enough validators have acknowledged. No manual bootstrap
step is needed — the round transitions from PENDING to ACTIVE on its own.

## CI / CD

| Workflow                      | Trigger                                                               | What it does                                                                                               |
| ----------------------------- | --------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------- |
| `sdk-chain-deploy.yml`        | push to `main` (paths: `sdk/**`)                                      | Builds `svoted` with Rust FFI, deploys to droplet, restarts `svoted.service`, verifies health              |
| `nullifier-ingest-deploy.yml` | push to `main` (paths: `nullifier-ingest/**`, `sdk/deploy/Caddyfile`) | Builds `nf-server`, deploys to droplet, restarts `nullifier-query-server.service`, reloads Caddy           |
| `nullifier-ingest-resync.yml` | manual (`workflow_dispatch`)                                          | SSHes into droplet and runs the full `ingest → export → restart` pipeline to resync the nullifier snapshot |
| `release.yml`                 | push tag `v*`                                                         | Builds cross-platform binaries, uploads to DO Spaces along with `join.sh` and `version.txt`                |

All deploy workflows use `appleboy/ssh-action` + `appleboy/scp-action` with secrets
`DEPLOY_HOST`, `DEPLOY_USER`, and `SSH_PASSWORD`.

## External Contribution PR Notifier

A standalone Vercel project (`contrib-notifier/`) polls GitHub for PRs opened by
team members in upstream Zcash repos and posts notifications to a Slack channel.
When external contributors comment on or review those PRs, the notifier threads the
comment into the original Slack message so the team can respond quickly.

Deployed as its own Vercel project (separate from the admin UI) so the two concerns
stay independent and the UI can be open-sourced without leaking ops tooling.

### How it works

1. **Discovery** (every 5 min) — searches GitHub for open PRs by tracked authors
   (`czarcas7ic`, `p0mvn`, `greg0x`, `ValarDragon`) in tracked orgs
   (`ZcashFoundation`, `zcash`, `zodl-inc`). Posts one parent Slack message per
   new PR.
2. **Event fetch** — for each tracked open PR, fetches issue comments, review
   comments, and review submissions since the last poll. Events from anyone outside
   the tracked-author set are posted as Slack thread replies.
3. **Lifecycle** — when a PR is closed or merged the parent Slack message is
   updated to reflect the new state. A slower reconciliation pass (every 24 h)
   re-checks non-open PRs for reopen events.

### Full setup (start to finish)

#### 1. Create the Slack app

1. Go to <https://api.slack.com/apps> and click **Create New App** → **From scratch**.
2. Name it (e.g. "Contrib Notifier") and pick the Zcash workspace.
3. In the left sidebar go to **OAuth & Permissions**.
4. Under **Scopes → Bot Token Scopes**, add `chat:write`.
5. Scroll up and click **Install to Workspace**, then **Allow**.
6. Copy the **Bot User OAuth Token** (`xoxb-…`) — this is `SLACK_BOT_TOKEN`.

#### 2. Prepare the Slack channel

1. In Slack, create `#ext-zcash-contrib-notif` (or use an existing channel).
2. Invite the bot: type `/invite @Contrib Notifier` in the channel.
3. Get the channel ID: right-click the channel name → **View channel details** →
   the ID is at the bottom of the dialog (starts with `C`). This is
   `SLACK_NOTIFIER_CHANNEL_ID`.

#### 3. Create a GitHub PAT

1. Go to <https://github.com/settings/tokens?type=beta> (fine-grained tokens).
2. Create a token with **Public Repositories (read-only)** access — no org
   permissions needed since all target repos are public.
3. Copy the token — this is `GITHUB_TOKEN`.

#### 4. Create the Vercel project

```bash
cd contrib-notifier
npx vercel login                       # if not already logged in
npx vercel switch <your-pro-team>      # e.g. valar-53d3adec
npx vercel --yes                       # creates the project + first preview deploy
```

#### 5. Create an Edge Config store

1. Open the Vercel dashboard → **Storage** → **Create** → **Edge Config**.
2. Name it (e.g. "contrib-notifier-state") and connect it to the
   `contrib-notifier` project.
3. Note the store ID (`ecfg_…`) — this is `EDGE_CONFIG_ID`.
4. Vercel automatically sets the `EDGE_CONFIG` connection string env var when the
   store is connected, so reads via `@vercel/edge-config` work with no extra
   config.

#### 6. Create a Vercel API token

1. Go to <https://vercel.com/account/tokens>.
2. Create a token scoped to the team. This is `VERCEL_API_TOKEN` (used for Edge
   Config writes).

#### 7. Set env vars

In the Vercel dashboard for `contrib-notifier` → **Settings** → **Environment
Variables**, add:

| Variable                    | Value                     | Notes                               |
| --------------------------- | ------------------------- | ----------------------------------- |
| `SLACK_BOT_TOKEN`           | `xoxb-…`                  | From step 1                         |
| `SLACK_NOTIFIER_CHANNEL_ID` | `C…`                      | From step 2                         |
| `GITHUB_TOKEN`              | `github_pat_…` or `ghp_…` | From step 3                         |
| `VERCEL_API_TOKEN`          | `…`                       | From step 6                         |
| `EDGE_CONFIG_ID`            | `ecfg_…`                  | From step 5                         |
| `SLACK_MENTION_IDS`         | `U12345,U67890`           | Slack user IDs to @-mention         |
| `NOTIFIER_DRY_RUN`          | `true`                    | Start in dry-run; remove when ready |

Optional overrides (have sensible defaults):

| Variable                 | Default                                                                                  |
| ------------------------ | ---------------------------------------------------------------------------------------- |
| `TRACKED_GITHUB_AUTHORS` | `czarcas7ic,p0mvn,greg0x,ValarDragon`                                                    |
| `TRACKED_REPO_OWNERS`    | `ZcashFoundation,zcash,zodl-inc`                                                         |
| `AUTHOR_SLACK_MAP`       | `czarcas7ic:U0A8B0NM744,p0mvn:U0A81KAPYMR,greg0x:U0A8L9SA4QH,ValarDragon:U0A7RS10AJ3`  |

#### 8. Deploy to production

```bash
cd contrib-notifier
npx vercel --prod
```

#### 9. Verify dry-run

Hit the endpoint manually and inspect the log — it should show discovered PRs
without actually posting to Slack:

```bash
curl -sf https://<your-contrib-notifier>.vercel.app/api/poll | jq .
```

Wait 5 minutes for the cron to fire, then check again via the Vercel dashboard
→ **Logs** to confirm it runs on schedule.

#### 10. Go live

Remove or set `NOTIFIER_DRY_RUN` to `false` in the Vercel env vars, then
redeploy (Edge runtime functions bake env vars at build time):

```bash
cd contrib-notifier
npx vercel --prod
```

The next cron invocation will start posting to Slack for real.

### Maintenance endpoints

#### Reset (`POST /api/reset`)

Deletes all bot messages from Slack and clears the tracking state. The next poll
re-discovers all PRs and posts fresh messages. Use this when you need a clean
slate (e.g. switching channels).

```bash
curl -X POST https://<your-contrib-notifier>.vercel.app/api/reset
```

#### Refresh (`POST /api/refresh`)

Re-renders every tracked parent Slack message with the current config (mention
targets, author Slack map, PR state) without deleting anything. Thread history
is preserved.

Normally you don't need to call this manually — the poll handler detects config
changes automatically (it stores a hash of the rendering-relevant config in Edge
Config and refreshes all parent messages on the first poll after a redeploy that
changed `SLACK_MENTION_IDS`, `AUTHOR_SLACK_MAP`, or `SLACK_NOTIFIER_CHANNEL_ID`).
The manual endpoint is still available if you want to force a refresh.

```bash
curl -X POST https://<your-contrib-notifier>.vercel.app/api/refresh
```

### State

Mutable state (tracked PRs, Slack thread mappings, poll timestamps) is stored in
Edge Config under the `notifier-state` key. To reset, delete that key via the
Vercel dashboard or API.
