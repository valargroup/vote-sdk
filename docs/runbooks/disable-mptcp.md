# Disable MPTCP on a Linux validator

Temporary mitigation for CVE-2026-98432 on Ubuntu/Debian hosts running native
`svoted` and Caddy services. No binary update is needed. **Do not rerun `join.sh`.**
Restart one validator at a time, outside ceremonies and tally windows, with
enough voting power remaining online.

## Apply

Run on the validator host. Both services must restart, even if MPTCP is already
disabled; a Caddy reload does not replace existing listeners.

```bash
(
  set -e
  printf '%s\n' 'net.mptcp.enabled = 0' | sudo tee /etc/sysctl.d/90-vote-disable-mptcp.conf
  sudo sysctl -p /etc/sysctl.d/90-vote-disable-mptcp.conf
  test "$(sysctl -n net.mptcp.enabled)" = 0
  sudo systemctl restart caddy
  sudo systemctl restart svoted
  sudo systemctl is-active --quiet caddy svoted
)
```

## Verify

Requires Python 3.9+ on Linux x86_64 or aarch64. Download the pinned verifier,
check its checksum, and inspect the running sockets:

```bash
(
  set -e
  MITIGATION_DIR=$(mktemp -d)
  cd "$MITIGATION_DIR"
  curl -fsSLo check-mptcp-listeners.py https://raw.githubusercontent.com/valargroup/vote-sdk/8afb0d77dd125755d5ab373f0eb8c8964b10b364/scripts/check-mptcp-listeners.py
  echo 'f4a8fbdae4d7822f24b5264559f6748f55f90dc50d7e307c0d0f5c29b975acfc  check-mptcp-listeners.py' | sha256sum -c -
  sudo python3 check-mptcp-listeners.py --require-process svoted --require-process caddy
)
```

Require exit **0**, `mptcp_listeners: 0`, and no errors. An empty `ss -M -l`
result is insufficient. Confirm your public helper endpoint
`/shielded-vote/v1/status` returns `status: ok`, peers reconnect, and blocks
advance before moving to another host. Check for conflicting sysctl settings
that could re-enable MPTCP after reboot.

For failures, inspect `sudo journalctl -u svoted -u caddy -n 100 --no-pager`.
Container deployments need separate network-namespace handling.

## Kernel fix

**TBD.** Package and reboot instructions will be added when the Ubuntu fix is
available. Disabling MPTCP is an interim mitigation, not the kernel fix.
See the [Ubuntu advisory](https://ubuntu.com/security/CVE-2026-98432).
