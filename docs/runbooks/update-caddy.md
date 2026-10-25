# Update Caddy on a validator

For Ubuntu/Debian hosts using our standard installer, update Caddy to **2.11.7
or newer (stable)**. Skip this if you're already up to date or use another proxy.
Caddy briefly restarts; your validator keeps running. **Do not rerun `join.sh`.**

SSH into your host, replace the URL below, and paste this block. Keep your
existing Caddyfile if prompted.

```bash
VALIDATOR_URL="https://validator.example.org"

sudo cp -a /etc/caddy/Caddyfile "/etc/caddy/Caddyfile.before-update.$(date -u +%Y%m%dT%H%M%SZ)" &&
sudo apt-get update &&
sudo apt-get install --only-upgrade caddy &&
/usr/bin/caddy version &&
dpkg --compare-versions "$(dpkg-query -W -f='${Version}' caddy)" ge 2.11.7 &&
sudo /usr/bin/caddy validate --config /etc/caddy/Caddyfile &&
sudo systemctl restart caddy &&
sudo systemctl is-active caddy &&
sudo "/proc/$(systemctl show --property MainPID --value caddy)/exe" version &&
curl -fsS --max-time 20 "$VALIDATOR_URL/shielded-vote/v1/genesis" > /dev/null && echo "HTTPS OK"
```

Expect `active`, version **2.11.7 or newer**, and `HTTPS OK`.
If the version check fails, configure [Caddy's official stable apt repository](https://caddyserver.com/docs/install#debian-ubuntu-raspbian)
and rerun the block. For other failures, check `sudo journalctl -u caddy -n 50 --no-pager`.
