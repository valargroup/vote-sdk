#!/usr/bin/env bash
set -euo pipefail
# Runs only inside the disposable systemd localnet container.
role="$1"
node_home="$2"
[ ! -e "$node_home/config/genesis.json" ] || { echo 'Refusing to reinitialize existing state' >&2; exit 1; }
install -d /usr/local/bin
install -m 0755 /artifacts/baseline/bin/svoted /usr/local/bin/svoted
install -m 0755 /artifacts/cosmovisor /usr/local/bin/cosmovisor
svoted init "$role" --chain-id upgrade-test-1 --home "$node_home" >/dev/null 2>&1
svoted keys add validator --keyring-backend test --home "$node_home" >/dev/null 2>&1
svoted pallas-keygen --home "$node_home" >/dev/null 2>&1
python3 - "$node_home" <<'PY'
import pathlib,sys,re
home=pathlib.Path(sys.argv[1])
p=home/'config/config.toml';s=p.read_text()
for key,value in {'addr_book_strict':'false','allow_duplicate_ip':'true','timeout_commit':'"1s"'}.items():
 s=re.sub(r'^'+key+r' = .*',key+' = '+value,s,flags=re.M)
s=s.replace('tcp://127.0.0.1:26657','tcp://0.0.0.0:26657');p.write_text(s)
p=home/'config/app.toml';s=p.read_text().replace('tcp://localhost:1317','tcp://0.0.0.0:1317')
s=re.sub(r'(\[api\][\s\S]*?\nenable = )false',r'\g<1>true',s)
for key,value in {'ea_sk_path':str(home/'ea.sk'),'pallas_sk_path':str(home/'pallas.sk'),'comet_rpc':'http://localhost:26657'}.items():
 s=re.sub(r'^'+key+r' = .*',key+' = "'+value+'"',s,flags=re.M)
p.write_text(s)
PY
