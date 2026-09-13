#!/usr/bin/env python3
"""Rehearse a real v1.4.0 -> v1.6.0 cutover in an isolated systemd localnet.

No chain, signing key, snapshot, or voting-config from a live network is used.
Existing public release artifacts may be downloaded before starting the network.
"""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import re
import shlex
import shutil
import subprocess
import tarfile
import time
import uuid

REPO = Path(__file__).resolve().parents[1]
CHAIN = 'upgrade-test-1'
PLAN = 'v1.6.0'
BASE = 'http://artifacts:8080'
HOMES = {'primary': '/opt/shielded-vote/.svoted', 'secondary': '/root/.svoted'}


def sha(path):
    with open(path, 'rb') as f:
        return hashlib.file_digest(f, 'sha256').hexdigest()


def run(args, **kwargs):
    result = subprocess.run(args, text=True, capture_output=True, **kwargs)
    if result.returncode:
        raise RuntimeError(f'Command failed ({result.returncode}): {result.stderr[-6000:]}')
    return result.stdout.strip()


def download(url, path):
    run(['curl', '-fsSL', '--retry', '5', '--retry-all-errors', '--connect-timeout', '15', '--max-time', '300', url, '-o', str(path)])


def release(cache, tag, platform):
    name = f'shielded-vote-{tag}-{platform}.tar.gz'
    path = cache / name
    url = f'https://shielded-vote.nyc3.digitaloceanspaces.com/binaries/vote-sdk/{name}'
    if not Path(str(path)+'.sha256').exists():
        download(url+'.sha256', Path(str(path)+'.sha256'))
    expected = Path(str(path)+'.sha256').read_text().split()[0]
    if not path.exists() or sha(path) != expected:
        download(url, path)
    if sha(path) != expected:
        raise RuntimeError(f'Invalid release checksum: {name}')
    return path


def extract(path, dest):
    with tarfile.open(path) as archive:
        archive.extractall(dest, filter='data')


class Rehearsal:
    def __init__(self, args):
        self.args = args
        self.output = Path(args.output).resolve()
        self.output.mkdir(parents=True, exist_ok=False)
        self.artifacts = self.output / 'artifacts'
        self.artifacts.mkdir()
        self.project = 'svote-upgrade-' + uuid.uuid4().hex[:10]
        runtime_source = self.output/'sdk-runtime'
        runtime_source.mkdir()
        for name in ['join.sh', 'join-full.sh']:
            shutil.copy2(REPO/name, runtime_source/name)
        shutil.copytree(REPO/'scripts', runtime_source/'scripts', ignore=shutil.ignore_patterns('__pycache__'))
        shutil.copytree(REPO/'docker/upgrade', runtime_source/'docker/upgrade')
        self.env = dict(os.environ, UPGRADE_REPO=str(runtime_source), UPGRADE_INFRA=str(Path(args.infra).resolve()),
                        UPGRADE_ARTIFACTS=str(self.artifacts), UPGRADE_PLATFORM=args.platform,
                        UPGRADE_IMAGE=args.image)
        self.compose = ['docker', 'compose', '-p', self.project, '-f', str(REPO/'docker/upgrade/compose.yml')]
        self.results = {'compose_project': self.project, 'chain_id': CHAIN, 'plan': PLAN, 'candidate': args.candidate_tag,
                        'platform': args.platform, 'scenario': args.scenario,
                        'sdk_commit': run(['git', '-C', str(REPO), 'rev-parse', 'HEAD']), 'checks': []}
        self.results['infra_commit'] = run(['git', '-C', str(Path(args.infra).resolve()), 'rev-parse', 'HEAD'])
        self.results['source_sha256'] = {str(path.relative_to(REPO)): sha(path) for path in
            [REPO/'scripts/_chain_upgrade_common.sh', REPO/'scripts/update_chain.sh.template',
             REPO/'join.sh', REPO/'join-full.sh', REPO/'scripts/test_upgrade_localnet.py']}
        self.started = False

    def dc(self, *args, **kwargs):
        return run(self.compose+list(args), env=self.env, **kwargs)

    def node(self, node, command, timeout=120):
        return self.dc('exec', '-T', node, 'bash', '-euo', 'pipefail', '-c', command, timeout=timeout)

    def write(self, node, path, text):
        self.dc('exec', '-T', node, 'sh', '-c', 'cat > '+shlex.quote(path), input=text)

    def check(self, name):
        self.results['checks'].append(name)
        print('PASS:', name, flush=True)

    def wait(self, name, fn, timeout=120):
        deadline = time.monotonic()+timeout
        error = None
        while time.monotonic() < deadline:
            try:
                value = fn()
                if value:
                    return value
            except (subprocess.SubprocessError, RuntimeError, ValueError, KeyError) as e:
                error = e
            time.sleep(1)
        raise RuntimeError(f'Timeout: {name}: {error}')

    def query(self, node, path):
        return json.loads(self.node(node, 'curl -fsS http://localhost:26657/'+shlex.quote(path)))['result']

    def height(self, node='primary'):
        return int(self.query(node, 'status')['sync_info']['latest_block_height'])

    def svoted(self, node, args):
        home = HOMES[node]
        return self.node(node, '/usr/local/bin/svoted '+args+' --home '+home)

    def artifacts_setup(self):
        cache = Path(self.args.cache).resolve()
        cache.mkdir(parents=True, exist_ok=True)
        platform = self.args.platform.replace('/', '-')
        binary_dir = self.artifacts/'binaries/vote-sdk'
        binary_dir.mkdir(parents=True)
        for tag in ['v1.4.0', self.args.candidate_tag]:
            if tag != 'v1.4.0' and self.args.candidate_binary:
                root = self.artifacts/f'shielded-vote-{tag}-{platform}'
                (root/'bin').mkdir(parents=True)
                shutil.copy2(self.args.candidate_binary, root/'bin/svoted')
                helper = Path(self.args.candidate_binary).with_name('create-val-tx')
                if helper.exists():
                    shutil.copy2(helper, root/'bin/create-val-tx')
                path = binary_dir/f'{root.name}.tar.gz'
                with tarfile.open(path, 'w:gz') as archive:
                    archive.add(root, arcname=root.name)
            else:
                path = release(cache, tag, platform)
                shutil.copy2(path, binary_dir/path.name)
                path = binary_dir/path.name
                extract(path, self.artifacts)
                root = self.artifacts/f'shielded-vote-{tag}-{platform}'
            Path(str(path)+'.sha256').write_text(sha(path)+'  '+path.name+'\n')
            if tag == 'v1.4.0':
                (self.artifacts/'baseline').symlink_to(root.name)
            cv_archive = run([str(REPO/'scripts/package-cosmovisor-archive.sh'), tag, platform,
                              str(root/'bin/svoted'), str(binary_dir)])
            if tag == self.args.candidate_tag:
                self.archive_sha = sha(cv_archive)
                self.results['candidate_binary_sha256'] = sha(root/'bin/svoted')
            scripts = self.artifacts/f'scripts/upgrade/{tag}'
            scripts.mkdir(parents=True)
            rendered = run([str(REPO/'scripts/render-update-chain.sh'), tag, BASE,
                            BASE+f'/scripts/upgrade/{tag}/_chain_upgrade_common.sh',
                            BASE+f'/scripts/upgrade/{tag}/update_chain.sh'])
            (scripts/'update_chain.sh').write_text(rendered+'\n')
            shutil.copy2(REPO/'scripts/_chain_upgrade_common.sh', scripts/'_chain_upgrade_common.sh')
        # Fetch and verify the upstream supervisor before isolating the network.
        asset = f'cosmovisor-v1.6.0-{platform}.tar.gz'
        url = 'https://github.com/cosmos/cosmos-sdk/releases/download/cosmovisor%2Fv1.6.0/'
        sums = cache/'cosmovisor-SHA256SUMS'
        if not sums.exists():
            download(url+'SHA256SUMS-cosmovisor-v1.6.0.txt', sums)
        expected = next(line.split()[0] for line in sums.read_text().splitlines() if line.split()[-1] == asset)
        cv = cache/asset
        if not cv.exists() or sha(cv) != expected:
            download(url+asset, cv)
        assert sha(cv) == expected
        cv_dir = self.output/'cosmovisor-extracted'
        extract(cv, cv_dir)
        shutil.copy2(next(cv_dir.rglob('cosmovisor')), self.artifacts/'cosmovisor')
        (self.artifacts/'voting-config.json').write_text(json.dumps({'vote_servers':[{'url':'http://primary:1317'}]}))
        self.check('release and supervisor checksums verified before network isolation')

    def initialize(self):
        self.dc('up', '-d')
        self.started = True
        for node, home in HOMES.items():
            self.wait(node+' systemd', lambda: self.node(node, 'systemctl show --property=SystemState --value') in ('running', 'degraded'))
            self.node(node, f'bash /sdk/docker/upgrade/init-node.sh {node} {home}')
        validators = []
        for node, home in HOMES.items():
            address = self.svoted(node, 'keys show validator -a --keyring-backend test')
            valoper = self.svoted(node, 'keys show validator -a --bech val --keyring-backend test')
            pallas = self.node(node, f'base64 -w0 {home}/pallas.pk')
            node_id = self.svoted(node, 'comet show-node-id')
            validators.append((node,address,valoper,pallas,node_id))
        primary_home = HOMES['primary']
        self.node('primary', "umask 077; python3 -c 'import secrets; print(secrets.token_hex(32))' > /run/coordinator-key")
        self.svoted('primary', 'keys import-hex coordinator "$(cat /run/coordinator-key)" --keyring-backend test >/dev/null 2>&1')
        coordinator = self.svoted('primary', 'keys show coordinator -a --keyring-backend test')
        for _,address,_,_,_ in validators:
            self.svoted('primary', f'genesis add-genesis-account {address} 10000000usvote')
        self.node('primary', f'''source /sdk/scripts/_genesis_accounts_lib.sh
BINARY=/usr/local/bin/svoted
genesis_add_auth_base_account {primary_home}/config/genesis.json {coordinator}
genesis_add_module_funding_account {primary_home}/config/genesis.json vote_funding usvote 1000000000usvote''')
        genesis = json.loads(self.node('primary', f'cat {primary_home}/config/genesis.json'))
        genesis['app_state']['vote']['vote_manager_addresses'] = [coordinator]
        genesis['app_state']['vote']['pallas_keys'] = [{'validator_address':v,'pallas_pk':p} for _,_,v,p,_ in validators]
        for node,home in HOMES.items():
            self.write(node, home+'/config/genesis.json', json.dumps(genesis))
            self.svoted(node, 'genesis gentx validator 10000000usvote --chain-id '+CHAIN+' --keyring-backend test >/dev/null 2>&1')
        gentx = self.node('secondary', 'cat '+HOMES['secondary']+'/config/gentx/*.json')
        self.write('primary', primary_home+'/config/gentx/secondary.json', gentx)
        self.svoted('primary', 'genesis collect-gentxs >/dev/null 2>&1')
        genesis = self.node('primary', f'cat {primary_home}/config/genesis.json')
        (self.artifacts/'genesis.json').write_text(genesis)
        for node,home in HOMES.items():
            self.write(node, home+'/config/genesis.json', genesis)
            peers = ','.join(i+'@'+n+':26656' for n,_,_,_,i in validators if n!=node)
            self.node(node, f'''sed -i 's|^persistent_peers = .*|persistent_peers = "{peers}"|' {home}/config/config.toml
mkdir -p /etc/systemd/system/svoted.service.d /opt/shielded-vote/bin
install -m 0755 /artifacts/cosmovisor /opt/shielded-vote/bin/cosmovisor''')
            if node == 'primary':
                self.node(node, 'BASE_URL='+BASE+'/binaries/vote-sdk bash /infra/modules/environment/scripts/install-release.sh --tag v1.4.0 --skip-restart')
                self.node(node, 'cp /infra/modules/environment/deploy/systemd/svoted-chain.service /etc/systemd/system/svoted.service')
            else:
                self.write(node, '/etc/systemd/system/svoted.service', f'''[Unit]
Description=Legacy local validator
After=network.target
[Service]
User=root
EnvironmentFile=-/etc/default/svoted
Environment="SVOTE_HOME={home}"
ExecStart=/usr/local/bin/svoted start --home {home}
Restart=on-failure
LimitNOFILE=65535
[Install]
WantedBy=multi-user.target
''')
            self.write(node, '/etc/default/svoted', 'SVOTE_PIR_URL=disabled\nUNSAFE_SKIP_BACKUP=true\nDAEMON_ALLOW_DOWNLOAD_BINARIES=false\nDAEMON_DOWNLOAD_MUST_HAVE_CHECKSUM=false\n')
            self.node(node, 'systemctl daemon-reload && systemctl start svoted')
        self.wait('initial blocks', lambda: self.height()>3)
        self.check('two equal-power validators produce blocks using v1.4.0')
        recipient = validators[1][1]
        tx = json.loads(self.svoted('primary', f'tx vote authorized-send {recipient} 123 --from coordinator --chain-id {CHAIN} --keyring-backend test --gas 500000 --yes --output json'))
        assert tx.get('code',0) == 0
        self.wait('pre-upgrade funded account', lambda: json.loads(self.svoted('primary', f'query bank balances {recipient} --output json')).get('balances') == [{'denom':'usvote','amount':'123'}])
        self.results['state_account'] = recipient
        self.check('coordinator transaction creates non-genesis state before the upgrade')

    def join_observer(self, node='observer', snapshot=False):
        self.wait(node+' systemd', lambda: self.node(node, 'systemctl show --property=SystemState --value') in ('running', 'degraded'))
        self.node(node, 'mkdir -p /root/.local/bin && install -m 0755 /artifacts/cosmovisor /root/.local/bin/cosmovisor')
        command = f'SVOTE_HOME=/root/.svoted-full SVOTE_INSTALL_DIR=/root/.local/bin SVOTE_SERVICE_NAME=svoted SVOTE_DO_SPACES_BASE={BASE} SVOTE_CHAIN_ID={CHAIN} VOTING_CONFIG_URL={BASE}/voting-config.json SNAPSHOT_BASE_URL={BASE} SVOTE_SKIP_SNAPSHOT={0 if snapshot else 1} bash /sdk/join-full.sh'
        (self.output/f'{node}-join.log').write_text(self.node(node, command, timeout=180))
        self.node(node, 'ln -s /root/.local/bin/svoted /usr/local/bin/svoted')
        HOMES[node] = '/root/.svoted-full'
        self.check(node+': actual full-node installer starts Cosmovisor and synchronizes')

    def updater(self, node, mode, *extra):
        tag = self.args.candidate_tag
        command = f'curl -fsSL {BASE}/scripts/upgrade/{tag}/update_chain.sh | SVOTE_ACK_SINGLE_SIGNER=1 bash -s -- --mode {mode} --plan-name {PLAN} --tag {tag} '
        output = self.node(node, command+shlex.join(extra), timeout=180)
        (self.output/f'{node}-{mode}-{len(self.results["checks"])}.log').write_text(output)
        return output

    def prepare(self):
        for node in HOMES:
            pid = self.node(node, 'systemctl show svoted -p MainPID --value')
            self.updater(node, 'prepare', '--allow-no-plan')
            self.updater(node, 'prepare', '--allow-no-plan')
            assert self.node(node, 'systemctl show svoted -p MainPID --value') == pid
            self.updater(node, 'verify-prestage', '--allow-no-plan', '--skip-cosmovisor-service')
            self.check(node+': repeat preparation does not restart the running validator')
        self.updater('secondary', 'migrate', '--allow-no-plan')
        self.updater('primary', 'configure-autodownload')
        for node in HOMES:
            self.updater(node, 'verify-prestage', '--allow-no-plan')
            if node != 'observer':
                assert self.node(node, 'systemctl show svoted -p LimitNOFILE --value') == '65535'
            assert self.node(node, 'tr "\\0" "\\n" </proc/$(systemctl show svoted -p MainPID --value)/environ | sed -n "s/^UNSAFE_SKIP_BACKUP=//p"') == 'true'
        self.check('migration preserves backup policy and resource limits; both nodes are ready')
        before = self.node('primary', 'readlink /opt/shielded-vote/current')
        self.node('primary', f'BASE_URL={BASE}/binaries/vote-sdk bash /infra/modules/environment/scripts/install-release.sh --tag {self.args.candidate_tag} --stage-only')
        assert before == self.node('primary', 'readlink /opt/shielded-vote/current')
        self.check('infrastructure stage-only leaves current release unchanged')

    def cutover(self):
        tag = self.args.candidate_tag
        platform = self.args.platform.replace('/', '-')
        url = f'{BASE}/binaries/vote-sdk/shielded-vote-{tag}-cosmovisor-v1-{platform}.tar.gz?checksum=sha256:{self.archive_sha}'
        info = json.dumps({'tag':tag,'binaries':{self.args.platform:url}})
        height = self.height()+60
        tx = self.svoted('primary', f'tx vote schedule-upgrade {PLAN} {height} --info '+shlex.quote(info)+f' --from coordinator --chain-id {CHAIN} --keyring-backend test --gas 500000 --yes --output json')
        response = json.loads(tx)
        assert response.get('code',0) == 0, response
        self.wait('scheduled plan', lambda: json.loads(self.svoted('primary', 'query upgrade plan --output json')).get('plan',{}).get('name') == PLAN)
        for node in HOMES:
            self.updater(node, 'verify-prestage')
        self.check('scheduled plan and exact prepared release pass strict verification')
        if self.args.scenario == 'autodownload':
            for node, home in HOMES.items():
                self.node(node, f'mv {home}/cosmovisor/upgrades/{PLAN} /tmp/prepared-{PLAN}')
        else:
            self.dc('stop', 'artifacts')
        self.wait('halt and resume', lambda: self.height() >= height+20, timeout=240)
        for node,home in HOMES.items():
            applied = json.loads(self.svoted(node, f'query upgrade applied {PLAN} --output json'))
            assert int(applied['height']) == height
            current = self.node(node, f'{home}/cosmovisor/current/bin/svoted version')
            assert current == tag, current
            digest = self.node(node, f'sha256sum {home}/cosmovisor/current/bin/svoted').split()[0]
            assert digest == self.results['candidate_binary_sha256']
        common = min(self.height(n) for n in HOMES)-1
        hashes = [self.query(n, f'block?height={common}')['block']['header']['app_hash'] for n in HOMES]
        assert len(set(hashes)) == 1
        for node in HOMES:
            self.node(node, 'systemctl restart svoted')
        self.wait('blocks after restart', lambda: self.height() > common+5)
        for node in HOMES:
            balance = json.loads(self.svoted(node, f'query bank balances {self.results["state_account"]} --output json'))
            assert balance['balances'] == [{'denom':'usvote','amount':'123'}]
        self.results['applied_height'] = height
        self.check('automatic cutover, applied height, executable hashes, app hashes, and restart recovery')
        self.dc('start', 'artifacts')

    def snapshot_join(self):
        home = HOMES['observer']
        self.node('observer', 'systemctl stop svoted')
        self.node('observer', f"tar --exclude='data/cs.wal' -C {home} -cf - data | lz4 -9 > /tmp/upgrade-snapshot.tar.lz4")
        cid = self.dc('ps', '-q', 'observer')
        snapshot = self.artifacts/'snapshot.tar.lz4'
        run(['docker','cp',cid+':/tmp/upgrade-snapshot.tar.lz4',str(snapshot)])
        version = self.node('observer', f'{home}/cosmovisor/current/bin/svoted version')
        (self.artifacts/'latest.json').write_text(json.dumps({'chain_id':CHAIN,'height':str(self.height()),
            'url':BASE+'/snapshot.tar.lz4','checksum':sha(snapshot),'version':version,'date':'localnet'}))
        self.node('observer', 'systemctl start svoted')
        self.join_observer('fresh', snapshot=True)
        self.wait('fresh snapshot join catches up', lambda: self.height('fresh') >= self.height()-3)
        applied = json.loads(self.svoted('fresh', f'query upgrade applied {PLAN} --output json'))
        assert int(applied['height']) == self.results['applied_height']
        self.check('post-upgrade snapshot restores into a new Cosmovisor full node')

    def proof_test(self):
        if not self.args.proof_binary:
            self.results['real_proof_test'] = 'not requested; run candidate acceptance with --proof-binary'
            return
        shutil.copy2(self.args.proof_binary, self.artifacts/'atomic-delegate-cast')
        command = f'export VM_PRIVKEYS="$(cat /run/coordinator-key)" SVOTE_CHAIN_ID={CHAIN} SVOTE_HOME={HOMES["primary"]} SVOTE_PALLAS_PK_PATH={HOMES["primary"]}/pallas.pk SVOTE_API_URL=http://localhost:1317 HELPER_SERVER_URL=http://localhost:1317; /artifacts/atomic-delegate-cast --ignored --nocapture --test-threads=1'
        (self.output/'real-proof-test.log').write_text(self.node('primary',command,timeout=900))
        self.results['real_proof_test'] = 'passed'
        self.check('real-proof atomic delegation, cast, helper processing, and tally')

    def validator_join(self):
        node = 'joiner'
        # The default application disables the admin queue on non-admin nodes.
        # Enable it on this local primary and keep config requests on the local server.
        self.node('primary', f"sed -i '/^\\[admin\\]/,/^\\[/s/^disable = true/disable = false/' {HOMES['primary']}/config/app.toml; printf '%s\\n' 'SVOTE_CONFIG_URL={BASE}/voting-config.json' >> /etc/default/svoted; systemctl restart svoted")
        self.wait('local admin queue', lambda: self.node('primary', 'curl -fsS http://localhost:1317/api/pending-validators'))
        self.wait('joiner systemd', lambda: self.node(node, 'systemctl show --property=SystemState --value') in ('running', 'degraded'))
        self.node(node, 'mkdir -p /root/.local/bin && install -m 0755 /artifacts/cosmovisor /root/.local/bin/cosmovisor')
        command = f'SVOTE_MONIKER=localnet-joiner SVOTE_HOME=/root/.svoted SVOTE_DO_SPACES_BASE={BASE} SVOTE_CHAIN_ID={CHAIN} VOTING_CONFIG_URL={BASE}/voting-config.json SVOTE_ADMIN_URL=http://primary:1317 SVOTE_SNAPSHOT_BASE_URL={BASE} SVOTE_SKIP_SNAPSHOT=0 bash /sdk/join.sh --env prod --tls-mode skip'
        (self.output/'validator-join.log').write_text(self.node(node, command, timeout=240))
        self.node(node, 'ln -s /root/.local/bin/svoted /usr/local/bin/svoted')
        HOMES[node] = '/root/.svoted'
        address = self.svoted(node, 'keys show validator -a --keyring-backend test')
        operator = self.svoted(node, 'keys show validator --bech val -a --keyring-backend test')
        pending = self.node('primary', 'curl -fsS http://localhost:1317/api/pending-validators')
        assert operator in pending or address in pending, pending
        tx = json.loads(self.svoted('primary', f'tx vote authorized-send {address} 10000000 --from coordinator --chain-id {CHAIN} --keyring-backend test --gas 500000 --yes --output json'))
        assert tx.get('code', 0) == 0
        self.wait('new validator bonds', lambda: json.loads(self.svoted('primary', f'query staking validator {operator} --output json')).get('validator', {}).get('status') == 'BOND_STATUS_BONDED', timeout=240)
        self.check('actual validator installer registers, synchronizes, receives coordinator funding, and bonds')

    def finish(self, success):
        self.results['success'] = success
        (self.output/'result.json').write_text(json.dumps(self.results, indent=2)+'\n')
        if self.started:
            for node in HOMES:
                try:
                    (self.output/f'{node}-journal.log').write_text(self.node(node, 'journalctl -u svoted --no-pager -n 600',timeout=15))
                except (subprocess.SubprocessError, RuntimeError):
                    pass
            if not self.args.keep:
                self.dc('down', '--volumes', '--remove-orphans', timeout=60)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--infra', required=True, help='vote-infrastructure checkout with matching changes')
    parser.add_argument('--output', required=True, help='new evidence directory')
    parser.add_argument('--cache', default='/tmp/svote-upgrade-artifacts')
    parser.add_argument('--platform', choices=['linux/arm64','linux/amd64'], default='linux/arm64')
    parser.add_argument('--image', default='svote-upgrade-systemd:local')
    parser.add_argument('--candidate-tag', default='v1.6.0-rc.5')
    parser.add_argument('--candidate-binary', help='locally built FFI svoted; packaged without publishing a tag')
    parser.add_argument('--proof-binary', help='native Linux atomic_delegate_cast test executable')
    parser.add_argument('--scenario', choices=['prestage','autodownload'], default='prestage')
    parser.add_argument('--keep', action='store_true', help='retain only this run’s containers for diagnosis')
    args = parser.parse_args()
    if not re.fullmatch(r'v1\.6\.0-rc\.[0-9]+', args.candidate_tag):
        parser.error('candidate must be a v1.6.0 prerelease')
    test = Rehearsal(args)
    success = False
    try:
        test.artifacts_setup()
        test.initialize()
        test.join_observer()
        test.prepare()
        test.cutover()
        test.snapshot_join()
        test.proof_test()
        test.validator_join()
        success = True
    finally:
        test.finish(success)
    print('Evidence:', test.output)


if __name__ == '__main__':
    main()
