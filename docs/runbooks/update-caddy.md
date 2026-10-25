# Update Caddy on a validator

Please update your validator's Caddy HTTPS proxy to **2.11.7 or newer (stable)**.
If you're already running that version or newer, no update is needed. Operators
using another HTTPS proxy can skip this guide.

This is a Caddy maintenance update. It needs no coordinated chain upgrade and
doesn't change validator keys or chain data. Restarting Caddy briefly interrupts
HTTPS requests; `svoted` keeps running. **Do not rerun `join.sh` for this update.**

## Ubuntu / Debian (standard installer)

1. SSH into the validator host and check the installed version:

   ```bash
   /usr/bin/caddy version
   ```

2. Back up the config and update the package:

   ```bash
   sudo cp -a /etc/caddy/Caddyfile "/etc/caddy/Caddyfile.before-update.$(date -u +%Y%m%dT%H%M%SZ)" &&
   sudo apt-get update &&
   sudo apt-get install --only-upgrade caddy &&
   /usr/bin/caddy version
   ```

   Keep your existing Caddyfile if the package manager asks. Confirm the version
   is **2.11.7 or newer** before continuing. If it is older, configure
   [Caddy's official stable apt repository](https://caddyserver.com/docs/install#debian-ubuntu-raspbian)
   and repeat the update. Resolve package holds or pinning if they prevent it.

3. Validate the config, restart Caddy, and verify the running binary and HTTPS:

   ```bash
   sudo /usr/bin/caddy validate --config /etc/caddy/Caddyfile &&
   sudo systemctl restart caddy &&
   sudo systemctl is-active caddy &&
   sudo "/proc/$(systemctl show --property MainPID --value caddy)/exe" version

   # Replace this with your validator's public URL.
   curl --fail --show-error --silent --max-time 20 \
     https://validator.example.org/shielded-vote/v1/genesis > /dev/null && echo "HTTPS OK"
   ```

   Expect `active`, a running version of **2.11.7 or newer**, and `HTTPS OK`.
   A config reload alone does not load the new binary. If validation fails, fix
   the reported error before restarting. If restart or HTTPS fails, inspect
   `sudo journalctl -u caddy -n 50 --no-pager`.

These commands assume the apt package and standard `caddy` systemd service.
For a custom binary, container, or service override, update through that deployment
method and verify the version of the running proxy, preserving any custom modules.

## macOS (development installs)

For the launchd service created by `join.sh`, run as the user who installed it:

```bash
cp -p "$HOME/.config/caddy/Caddyfile" "$HOME/.config/caddy/Caddyfile.before-update.$(date -u +%Y%m%dT%H%M%SZ)" &&
brew update && brew upgrade caddy && caddy version
```

Confirm **2.11.7 or newer**, then:

```bash
caddy validate --config "$HOME/.config/caddy/Caddyfile" &&
launchctl kickstart -k "gui/$(id -u)/com.shielded-vote.caddy" &&
launchctl print "gui/$(id -u)/com.shielded-vote.caddy"
```

Check that launchd reports `state = running` and repeat the HTTPS check above.

## Why we're recommending this

Reviewed on 2026-10-25. The installer previously reused any existing Caddy binary
without a minimum version check. The manual setup page also allowed installing
the distribution's default package.

The [October 13 upstream advisory](https://github.com/caddyserver/caddy/security/advisories/GHSA-8qx7-j2v2-x6q5)
affects versions through 2.11.4 and lists 2.11.5 as patched. It concerns headers
forwarded during WebSocket upgrades. Our standard generated Caddyfile proxies
the local REST API and doesn't configure authentication or FastCGI; we haven't
established an exploit against the standard validator setup. Customized proxies
may expose additional affected features.

We recommend [2.11.7](https://github.com/caddyserver/caddy/releases/tag/v2.11.7),
the current stable release, which includes the security fixes and corrects proxy
crashes and streaming regressions in 2.11.6. This recommendation does not mean
we've confirmed the Caddy version or exposure on any operator's host.
