# Joining the Shielded-Vote chain as a validator

This runbook walks a new operator through bringing up a Shielded-Vote validator
on a fresh host. It wraps the one-line installer at
`https://vote.fra1.digitaloceanspaces.com/join.sh`, explains what each phase
does, and lists the most common failure modes.

> Use this page for **third-party operators** standing up a new validator. If
> you're rotating the primary/secondary nodes that ValarGroup runs, see
> [production-setup.md](../production-setup.md) instead.

## Overview

`join.sh` is a standalone bash script that:

1. Downloads the latest `svoted` + `create-val-tx` binaries from DigitalOcean
   Spaces (`s3://vote/binaries/vote-sdk/`)
2. Discovers an existing validator to peer with via the Vercel voting-config
   API
3. Downloads the canonical `genesis.json` from DO Spaces (the copy written by
   the most recent `sdk-chain-reset` CI run)
4. Initializes a fresh `~/.svoted` home and generates cryptographic keys
   (Cosmos secp256k1 validator key, Pallas key for the ceremony, node P2P key)
5. Configures `config.toml` / `app.toml`, sets up a Caddy TLS reverse proxy
   (auto-provisions Let's Encrypt via sslip.io)
6. Installs a persistent service (systemd on Linux, launchd on macOS), waits
   for the node to sync, and registers as a validator once a vote-manager has
   funded the account

End-to-end runtime on a clean droplet is typically **10–20 minutes**,
dominated by the initial chain sync.

## Prerequisites

**Hardware.** 2 vCPU / 4 GB RAM / 50 GB SSD is the practical minimum. A
primary/secondary-class box (4 vCPU / 16 GB) is recommended for long-term
operation.

**OS.** Linux (systemd, `apt`) or macOS (launchd, Homebrew). No Windows
support.

**Tools already on the host.** `curl` and `jq`. Everything else (`svoted`,
`create-val-tx`, Caddy) is installed by `join.sh`.

**Network.** Open the following ports on your firewall:

| Port    | Protocol | Purpose                                |
| ------- | -------- | -------------------------------------- |
| `26656` | TCP      | CometBFT P2P (peer sync)               |
| `443`   | TCP      | HTTPS — Caddy fronts REST API          |
| `80`    | TCP      | HTTP-01 challenge for Let's Encrypt    |

**Domain (optional).** If you have a DNS name pointing at the host, pass it as
`--domain <hostname>` or set `SVOTE_DOMAIN`. Otherwise `join.sh` auto-detects
the public IP and uses an `sslip.io` subdomain (e.g. `1-2-3-4.sslip.io`) so
TLS provisioning still works without DNS setup.

**Funding.** Before your validator can bond, an existing vote-manager must
send a small balance of `usvote` to your validator's address. Coordinate with
the ValarGroup ops team (see the Operations channel) — you'll share your
validator's bech32 address which `join.sh` prints during Phase 4.

## Install in one command

```bash
curl -fsSL https://vote.fra1.digitaloceanspaces.com/join.sh | bash
```

The script prompts once for a validator moniker (human-readable name). You
can bypass the prompt by exporting `SVOTE_MONIKER` first:

```bash
SVOTE_MONIKER=my-validator \
  curl -fsSL https://vote.fra1.digitaloceanspaces.com/join.sh | bash
```

### Environment overrides

| Variable                | Default                       | Purpose                                    |
| ----------------------- | ----------------------------- | ------------------------------------------ |
| `SVOTE_MONIKER`         | *(prompted)*                  | Validator display name                     |
| `SVOTE_DOMAIN`          | auto-detected via sslip.io    | TLS hostname                               |
| `SVOTE_HOME`            | `$HOME/.svoted`               | Chain data directory                       |
| `SVOTE_INSTALL_DIR`     | `$HOME/.local/bin`            | Binary install location                    |
| `SVOTE_LOCAL_BINARIES`  | `0`                           | Set to `1` to skip download and use local  |

## What each phase does

Phase boundaries in `join.sh` (file at `join.sh` in the vote-sdk repo):

| Phase | `join.sh` lines  | What happens                                                                |
| ----- | ---------------- | --------------------------------------------------------------------------- |
| 1     | 65–163           | Downloads + verifies + installs `svoted` and `create-val-tx`                |
| 2     | 171–205          | Fetches seed validator identity (`NODE_ID`, P2P host/port) from Vercel API  |
| 3     | 207–234          | `svoted init`, fetch genesis from DO, generate validator + Pallas keys     |
| 4     | 236–301          | Rewrites `config.toml` / `app.toml`, appends `[helper]` section             |
| 5     | 303–404          | Installs Caddy, writes Caddyfile, provisions TLS                            |
| 6     | 406–441          | Registers the validator URL with the Vercel voting-config (pending)         |
| 7     | 443–588          | Installs systemd/launchd service, starts svoted                             |
| 8     | 590–656          | Waits for sync, waits for funding, submits `MsgCreateValidatorWithPallasKey`|

If `join.sh` errors out, the echo line that ran just before the failure tells
you which phase you're in.

## Expected output (happy path)

```
=== Shielded-Vote validator join ===

=== Downloading binaries ===
Version: v1.2.3
Platform: linux-amd64
Checksum verified.
Installed: /home/ubuntu/.local/bin/svoted, /home/ubuntu/.local/bin/create-val-tx

=== Discovering network ===
Seed node: https://vote-chain-primary.example.com
Peers: <NODE_ID>@vote-chain-primary.example.com:26656

=== Initializing node ===
Fetching genesis.json from https://vote.fra1.digitaloceanspaces.com/genesis.json...
Genesis validated.

=== Generating cryptographic keys ===
(Pallas key + validator key generated)

=== Configuring node ===
Node configured.

=== Setting up TLS reverse proxy ===
Detected public IP: 203.0.113.42
Using sslip.io domain: 203-0-113-42.sslip.io
Caddy configured: https://203-0-113-42.sslip.io → localhost:1317

=== Registering with vote network ===
Registered (pending). The admin will see your request.

=== Installing systemd service ===
Service svoted started (survives SSH disconnect and reboots).

Waiting for node to sync...
  height: 12345, catching_up: true
  …
  height: 54321, catching_up: false
Node is synced.

Waiting for account cosmos1… to be funded...
  (ask the bootstrap operator to fund your address in the admin UI)
Account funded (10000000 usvote).

Registering as validator...
Verifying registration on-chain (waiting ~6s for block commit)...
Validator registered: cosmosvaloper1…

=============================================
  Validator is running
=============================================
```

## Common failures

### "ERROR: Unsupported OS" or "Unsupported architecture"
`join.sh` supports Linux and macOS on x86_64 or arm64. Anything else requires
a source build — see [README.md](../../README.md).

### Binary download 404s or hangs
Happens when a release hasn't published tarballs under
`s3://vote/binaries/vote-sdk/` for your platform yet. Check
`https://vote.fra1.digitaloceanspaces.com/version.txt` — the referenced tag
must have all four platform tarballs uploaded by `release.yml`'s `distribute`
job.

### "ERROR: No vote_servers found in voting-config"
The Vercel voting-config API has no registered validators yet. On a
brand-new chain, the bootstrap operator has to register the primary validator
via the admin UI before any joiner can discover the network. Ping the ops
team.

### "WARNING: Could not detect public IP. Skipping Caddy setup."
The host can't reach `https://ifconfig.me` (blocked egress, or the service is
down). Re-run with an explicit domain:

```bash
SVOTE_DOMAIN=my-validator.example.com \
  curl -fsSL https://vote.fra1.digitaloceanspaces.com/join.sh | bash
```

If you skip TLS entirely, your validator can still sync, but the admin UI
can't route traffic to it and other validators can't query its REST API.

### "WARNING: apt not found" / "Homebrew not found"
Install Caddy manually, then re-run:
- Linux: [caddyserver.com/docs/install](https://caddyserver.com/docs/install)
- macOS: `brew install caddy`

### "Waiting for account cosmos1… to be funded..." — hangs indefinitely
The bootstrap operator hasn't sent you `usvote` yet. Share your validator
address (printed in the script output) in the ops channel and wait for
confirmation. `join.sh` polls every 5 s; you can safely Ctrl-C and re-run
later — state is idempotent.

### "ERROR: create-val-tx exited with a non-zero status"
Usually means the node isn't fully synced when registration was attempted, or
the account has less than `10000000 usvote`. Check `journalctl -u svoted -f`
and the balance:

```bash
svoted query bank balances <your-address> --home ~/.svoted
```

### Validator registered but "Registration failed — '<moniker>' not found"
Rare — indicates the block containing `MsgCreateValidatorWithPallasKey`
didn't commit within 6 s. Wait a block, then re-check:

```bash
svoted query staking validators --home ~/.svoted --output json \
  | jq '.validators[] | select(.description.moniker == "<moniker>")'
```

If present, your validator is registered and you can ignore the error. If
absent after 30 s, the tx was rejected — check logs.

## Post-install

### Service management

**Linux (systemd):**
```bash
sudo systemctl status svoted
sudo systemctl restart svoted
sudo systemctl stop svoted
journalctl -u svoted -f
```

**macOS (launchd):**
```bash
launchctl print gui/$(id -u)/com.shielded-vote.validator
launchctl kickstart -k gui/$(id -u)/com.shielded-vote.validator
launchctl bootout gui/$(id -u)/com.shielded-vote.validator
tail -f ~/.svoted/node.log
```

### Useful queries

```bash
svoted status --home ~/.svoted | jq '.sync_info'
svoted query staking validators --home ~/.svoted --output json \
  | jq '.validators[].description.moniker'
curl -sf https://<your-domain>/shielded-vote/v1/rounds | jq
```

### Upgrading to a new release

Re-run the installer — `join.sh` will detect the existing service, stop it,
swap in the new binary, and restart:

```bash
curl -fsSL https://vote.fra1.digitaloceanspaces.com/join.sh | bash
```

Chain state (`~/.svoted/data/`) is preserved; only the binaries in
`~/.local/bin` are swapped.

## Offboarding

To stop validating without losing data:

```bash
# Linux
sudo systemctl stop svoted
sudo systemctl disable svoted

# macOS
launchctl bootout gui/$(id -u)/com.shielded-vote.validator
rm ~/Library/LaunchAgents/com.shielded-vote.validator.plist
```

To permanently leave the validator set, unbond via a standard Cosmos
transaction. Ping ops if you need help constructing it.

## Escalation

- Post in the Shielded-Vote ops channel (ask Adam or Roman for an invite)
- File a ticket in the Voting Blockers project on Linear
- Upstream issues: [github.com/valargroup/vote-sdk](https://github.com/valargroup/vote-sdk)
