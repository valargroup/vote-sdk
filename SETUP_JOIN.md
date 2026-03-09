# Join the Shielded-Vote Network as a Validator

There are two paths to join: **binary** (no repo needed) or **source** (for developers with the repo).

## Path A — Binary (no repo, no mise)

### Requirements

- Linux or macOS (amd64 or arm64)
- `curl` and `jq` installed
- Funded validator account (see Step 2)
- **Pre-built binaries on DO Spaces** — the `release.yml` GitHub Action must have run at least once to upload `svoted` and `create-val-tx` to `vote.fra1.digitaloceanspaces.com`. If `join.sh` fails to download binaries, trigger a release first.
- **At least one validator registered in Edge Config** — the bootstrap operator must have registered a validator's public URL in the admin UI so that `join.sh` can discover the network.

### Step 1 — Run join.sh

```bash
curl -fsSL https://vote.fra1.digitaloceanspaces.com/join.sh | bash
```

You will be prompted for a **moniker** (a display name for your validator). To run non-interactively:

```bash
SVOTE_MONIKER=my-validator \
  curl -fsSL https://vote.fra1.digitaloceanspaces.com/join.sh | bash
```

#### What join.sh does

1. Downloads pre-built binaries from DO Spaces (or uses local if already in PATH)
2. Queries the Vercel voting-config API to discover a live validator
3. Fetches `genesis.json` from the discovered validator's `/shielded-vote/v1/genesis` endpoint
4. Fetches the validator's P2P node identity from `/cosmos/base/tendermint/v1beta1/node_info`
5. Initializes the node, generates Cosmos + Pallas cryptographic keys
6. Configures CometBFT with the discovered peer, sets up Caddy TLS reverse proxy
7. Registers as a pending validator with the Vercel API (self-registration — admin sees it in the UI)
8. Installs a systemd service and starts the node
9. Waits for sync, then waits for admin to approve and fund via the admin UI
10. Once funded, automatically creates the validator on-chain (`MsgCreateValidatorWithPallasKey`)
11. Re-registers with the Vercel API to promote the URL to `vote_servers` in Edge Config

#### Optional env vars

| Variable | Default | Purpose |
|---|---|---|
| `SVOTE_MONIKER` | *(prompted)* | Validator display name |
| `SVOTE_INSTALL_DIR` | `~/.local/bin` | Where to install `svoted` and `create-val-tx` |
| `SVOTE_HOME` | `~/.svoted` | Node home directory |
| `VOTING_CONFIG_URL` | `https://shielded-vote.vercel.app` | Vercel app URL for network discovery |

After initialization, `join.sh` starts the node, syncs, and waits for funding. Ask the bootstrap operator to fund your address using the **admin UI** (Validators → Fund validator). Once funded, the script automatically registers you as a validator.

---

## Path B — Source (has repo, uses mise)

```bash
cd shielded-vote
mise install              # pin Go/Rust/Node versions
mise run validator:join    # builds from source, discovers network, joins
```

This runs `mise run build:install` (builds `svoted` + `create-val-tx` from source), then `join.sh` which detects the local binaries, fetches the network config via Vercel, starts the node, and registers as a validator once funded.

---

## Verify

Once `join.sh` reports "Validator registered", confirm you appear in the validator set:

```bash
svoted query staking validators --node tcp://localhost:26657
```

## Ceremony Participation

The EA key ceremony is automatic. When a new voting round is created, your validator is included in the ceremony if it is bonded and has a registered Pallas key (done automatically by `join.sh`). The block proposer handles dealing and acking via `PrepareProposal` — no manual steps required.

## Jailing and Unjailing

Validators are jailed by the standard `x/slashing` module if they miss too many blocks (default: 50% of a 100-block window). There is no ceremony-miss jailing — block-miss detection covers liveness. Slash fractions are zeroed out (no token burning; jailing is for liveness signaling only).

To unjail, the jailed validator sends a standard `cosmos.slashing.v1beta1.MsgUnjail` after the 10-minute cooldown. This can be done via the admin UI (click "Unjail" on the validator's card) or the CLI:

```bash
svoted tx slashing unjail --from validator --keyring-backend test --home ~/.svoted --chain-id svote-1
```

Check ceremony status:

```bash
mise status
# or
curl -s localhost:1318/shielded-vote/v1/rounds | jq
```

## Useful commands

```bash
# Check sync status
svoted status --home ~/.svoted | jq '.sync_info'

# Follow node logs
tail -f ~/.svoted/node.log            # macOS
journalctl -u svoted -f               # Linux
```

### Service management (Linux — systemd)

```bash
sudo systemctl status svoted
sudo systemctl stop svoted
sudo systemctl restart svoted
```

### Service management (macOS — launchd)

```bash
launchctl print gui/$(id -u)/com.shielded-vote.validator
launchctl bootout gui/$(id -u)/com.shielded-vote.validator
launchctl kickstart -k gui/$(id -u)/com.shielded-vote.validator
```

## Chain info

| | |
|---|---|
| Chain ID | `svote-1` |
| P2P port | `26656` |
| RPC port | `26657` (localhost only) |
| REST API port | `1318` (localhost only) |
| Node home | `~/.svoted` |
