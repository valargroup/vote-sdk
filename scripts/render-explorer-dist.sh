#!/usr/bin/env bash
#
# Render the ping-pub explorer artifact for one Shielded Vote environment.
#
# The release artifact intentionally carries deploy-time placeholders in both
# chains/mainnet/svote.json and the Vite JS bundle because the same tarball is
# deployed to staging and production. This script makes the artifact concrete on
# the target explorer host.

set -euo pipefail

: "${CHAIN_ID:?CHAIN_ID must be set}"
: "${DNS_PREFIX:?DNS_PREFIX must be set; use an empty string for root DNS}"
: "${DOMAIN:?DOMAIN must be set}"

EXPLORER_DIST="${EXPLORER_DIST:-/opt/shielded-vote/current/explorer/dist}"
CHAIN_CONFIG="${EXPLORER_DIST}/chains/mainnet/svote.json"
LOGO_DIR="${EXPLORER_DIST}/logos"

if [ ! -d "${EXPLORER_DIST}" ]; then
  echo "ERROR: explorer dist not found: ${EXPLORER_DIST}" >&2
  exit 1
fi

mkdir -p "$(dirname "${CHAIN_CONFIG}")" "${LOGO_DIR}"

cat > "${CHAIN_CONFIG}" <<EOF
{
  "chain_name": "svote",
  "registry_name": "svote",
  "api": ["https://${DNS_PREFIX}explorer-api.${DOMAIN}"],
  "rpc": ["https://${DNS_PREFIX}explorer-rpc.${DOMAIN}"],
  "sdk_version": "0.50.0",
  "coin_type": "118",
  "min_tx_fee": "0",
  "addr_prefix": "sv",
  "logo": "https://${DNS_PREFIX}explorer.${DOMAIN}/logos/zcash.svg",
  "theme_color": "#F4B728",
  "assets": [
    {
      "base": "usvote",
      "symbol": "SVOTE",
      "exponent": "6",
      "coingecko_id": "",
      "logo": "https://${DNS_PREFIX}explorer.${DOMAIN}/logos/zcash.svg"
    }
  ],
  "chain_id": "${CHAIN_ID}",
  "pretty_name": "Zcash Vote",
  "features": ["blocks", "tx", "staking", "governance", "supply", "parameters", "consensus"]
}
EOF

python3 - "${EXPLORER_DIST}" "${DNS_PREFIX}" "${DOMAIN}" "${CHAIN_ID}" <<'PY'
from pathlib import Path
import re
import sys

dist = Path(sys.argv[1])
dns_prefix = sys.argv[2]
domain = sys.argv[3]
chain_id = sys.argv[4]

for path in dist.glob("assets/*.js"):
    text = path.read_text()
    rendered = (
        text
        .replace("{{DNS_PREFIX}}", dns_prefix)
        .replace("{{DOMAIN}}", domain)
        .replace("{{CHAIN_ID}}", chain_id)
    )
    if rendered != text:
        path.write_text(rendered)

index = dist / "index.html"
text = index.read_text()
host = f"{dns_prefix}explorer-api.{domain}"

start = "    <!-- svote endpoint guard:start -->\n"
end = "    <!-- svote endpoint guard:end -->\n"
while start in text and end in text:
    before, rest = text.split(start, 1)
    _, after = rest.split(end, 1)
    text = before + after

guard = f'''{start}    <script>
      try {{
        const key = "endpoint-svote";
        const value = window.localStorage.getItem(key) || "";
        if (value && (value.includes("{{{{") || !value.includes("{host}"))) {{
          window.localStorage.removeItem(key);
        }}
      }} catch (_) {{}}
    </script>
{end}'''

marker = '<script type="module" crossorigin src="/assets/'
if marker not in text:
    raise SystemExit("module script tag not found in explorer index.html")

text = text.replace(marker, guard + marker, 1)

cache_bust = f"svote-{chain_id}-{dns_prefix or 'root'}".replace(".", "-")
text = re.sub(
    r'(<script type="module" crossorigin src="/assets/[^"]+?\.js)(?:\?v=[^"]*)?("></script>)',
    rf'\1?v={cache_bust}\2',
    text,
    count=1,
)
index.write_text(text)

unrendered = []
for path in [index, dist / "chains/mainnet/svote.json", *dist.glob("assets/*.js")]:
    contents = path.read_text()
    if any(token in contents for token in ("{{DNS_PREFIX}}", "{{DOMAIN}}", "{{CHAIN_ID}}")):
        unrendered.append(str(path))

if unrendered:
    raise SystemExit("unrendered explorer placeholders remain in:\n" + "\n".join(unrendered))
PY

# ping-pub's built menu references these default logos. Keep them local so
# missing image paths do not fall through to the SPA HTML fallback.
fetch_logo() {
  local name="$1"
  local url="https://ping.pub/logos/${name}"
  if [ ! -s "${LOGO_DIR}/${name}" ]; then
    curl -fsSL --retry 3 -o "${LOGO_DIR}/${name}" "${url}"
  fi
}

fetch_logo cosmos.svg
fetch_logo osmosis.jpg
fetch_logo celestia.png

chmod 0644 "${CHAIN_CONFIG}" "${LOGO_DIR}/cosmos.svg" "${LOGO_DIR}/osmosis.jpg" "${LOGO_DIR}/celestia.png"

echo "Rendered explorer dist for chain ${CHAIN_ID} at https://${DNS_PREFIX}explorer.${DOMAIN}"
