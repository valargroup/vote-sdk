# Disable MPTCP on a Linux validator

**CVE-2026-98432 requires a Linux kernel fix.** Until a fixed kernel is
available for your distribution, disable MPTCP and restart the services that
hold MPTCP listeners. This reduces the demonstrated remote root attack
surface; it does not fix the underlying TCP vulnerability.

This procedure is for Ubuntu/Debian validators running native `svoted` and
Caddy systemd services. No new validator binary or coordinated chain upgrade
is needed. The host setting covers both services and preserves existing Go
runtime options. **Do not rerun `join.sh` or reset your validator.**

Arrange a brief maintenance window, avoiding active ceremonies and tally
windows. Restart one validator at a time and ensure enough voting power stays
online. Existing connections reconnect during the restarts.

## 1. Download and inspect the verifier

The verifier requires Python 3.9 or newer on Linux x86_64 or aarch64 and root
permission to inspect process sockets. It reads socket metadata without
modifying the running services. Download the pinned verifier:

```bash
MITIGATION_DIR=$(mktemp -d)
MITIGATION_BASE=https://raw.githubusercontent.com/valargroup/vote-sdk/8afb0d77dd125755d5ab373f0eb8c8964b10b364
curl -fsSLo "$MITIGATION_DIR/check-mptcp-listeners.py" \
  "$MITIGATION_BASE/scripts/check-mptcp-listeners.py"
(
  cd "$MITIGATION_DIR" &&
  printf '%s\n' \
    'f4a8fbdae4d7822f24b5264559f6748f55f90dc50d7e307c0d0f5c29b975acfc  check-mptcp-listeners.py' | sha256sum -c -
)
```

Stop if a download or checksum fails. Review the downloaded script, then run:

```bash
sudo python3 "$MITIGATION_DIR/check-mptcp-listeners.py" \
  --require-process svoted --require-process caddy
```

Exit **1** means MPTCP listeners remain; exit **2** means verification failed
or is incomplete. An empty `ss -M -l` result is not proof that MPTCP is absent.
If you use another proxy, replace `--require-process caddy` with its process
name and use its restart procedure below.

## 2. Disable MPTCP and restart

Record `uname -r`, current block height, peer count and helper health first.
Check for conflicting MPTCP settings in `/etc/sysctl.conf` and sysctl drop-ins.
Write and apply the persistent configuration:

```bash
printf '%s\n' 'net.mptcp.enabled = 0' | \
  sudo tee /etc/sysctl.d/90-vote-disable-mptcp.conf > /dev/null &&
sudo sysctl -p /etc/sysctl.d/90-vote-disable-mptcp.conf &&
test "$(sysctl -n net.mptcp.enabled)" = 0 &&
sudo systemctl restart caddy &&
sudo systemctl is-active --quiet caddy &&
sudo systemctl restart svoted &&
sudo systemctl is-active --quiet svoted
```

The setting prevents new MPTCP sockets. **Existing listeners require a
restart**, including when the setting was already zero. A Caddy reload is
insufficient. Other services on the host may also need restarting.

## 3. Verify recovery

Rerun the verifier from step 1. Require exit **0**, `mptcp_listeners: 0`, no
errors, and protocol **6 (TCP)** for `svoted` and your proxy. Protocol **262**
is MPTCP. Do not count a stopped or inaccessible service as a successful check.

Replace the URL and check the public API:

```bash
VALIDATOR_URL=https://validator.example.org
curl -fsS --max-time 20 "$VALIDATOR_URL/shielded-vote/v1/status"
curl -fsS --max-time 20 \
  "$VALIDATOR_URL/cosmos/base/tendermint/v1beta1/blocks/latest"
curl -fsS --max-time 20 http://127.0.0.1:26657/status
curl -fsS --max-time 20 http://127.0.0.1:26657/net_info
```

Require helper `status: ok`, the expected chain ID, `catching_up: false`,
connected peers and a block height that advances on a subsequent check.
If you also serve PIR, verify its public `/ready` and `/root` endpoints and
confirm the root/height match the values recorded before the restart.

For failures, inspect `sudo journalctl -u svoted -u caddy -n 100 --no-pager`.
Do not re-enable MPTCP as an automatic rollback on an exposed, unpatched host.
Isolate or restrict the host before any rollback.

The sysctl applies to one network namespace. Container deployments must apply
and verify the mitigation in the relevant namespaces as well.

## Install the kernel fix

Use your distribution's advisory to identify a fixed kernel package, install
it, reboot during a maintenance window and verify the **running** version with
`uname -r`. A package update without a reboot does not replace the running
kernel. Repeat the service and socket checks after reboot.

References: [Ubuntu CVE tracker](https://ubuntu.com/security/CVE-2026-98432),
[Linux MPTCP setting](https://docs.kernel.org/networking/mptcp-sysctl.html),
[Go MPTCP behavior](https://go.dev/doc/godebug#go-124).
