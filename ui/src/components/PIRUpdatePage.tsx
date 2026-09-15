import { useState } from 'react';
import { useWallet } from '../hooks/useWallet';
import { deriveEd25519FromKeplr } from '../api/votingKey';
import { getChainUrl } from '../api/chain';
import { signPIRProposal, validatePIRProposal, type PIRProposal } from '../utils/pirUpdate';

async function post<T>(path: string, body: unknown): Promise<T> {
  const response = await fetch(`${getChainUrl()}${path}`, {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify(body)});
  if (!response.ok) throw new Error(await response.text());
  return response.json();
}
export function PIRUpdatePage() {
  const wallet = useWallet();
  const [scope, setScope] = useState('prod');
  const [tag, setTag] = useState('');
  const [height, setHeight] = useState('');
  const [signingChain, setSigningChain] = useState('');
  const [proposal, setProposal] = useState<PIRProposal | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [pr, setPR] = useState('');
  async function prepare() {
    setBusy(true); setError(''); setPR(''); setProposal(null);
    try {
      const n = Number(height);
      if (!Number.isSafeInteger(n) || n <= 0 || n % 10 !== 0 || !/^v[0-9A-Za-z.+-]{1,127}$/.test(tag)) throw new Error('Enter an explicit release tag and a positive snapshot height divisible by 10.');
      const result = await post<PIRProposal>('/api/pir-update-proposal', {schema_version:1, binary_tag:tag, snapshot_height:n});
      await validatePIRProposal(result, scope, tag, n);
      setProposal(result);
    } catch(e) { setError(String(e)); } finally { setBusy(false); }
  }
  async function sign() {
    if (!proposal) return;
    setBusy(true); setError('');
    try {
      await validatePIRProposal(proposal, scope, tag, Number(height));
      if (!wallet.address || wallet.source !== 'keplr' || !wallet.chainId) throw new Error('Connect the coordinator Keplr wallet first.');
      // Coordinator key derivation is bound to its original chain ID. Allow the
      // same pinned key to authorize staging without deriving a different key.
      const keyChain = signingChain.trim() || wallet.chainId;
      let address = wallet.address;
      let signPayload: (data: string) => Promise<{signature: string}> = wallet.signKeplrPayload;
      if (keyChain !== wallet.chainId) {
        const keplr = window.keplr;
        if (!keplr) throw new Error('Keplr is unavailable');
        await keplr.enable(keyChain);
        const [account] = await keplr.getOfflineSigner(keyChain).getAccounts();
        if (!account) throw new Error('No signing account is available for that chain');
        address = account.address;
        signPayload = async (data: string) => {
          const signed = await keplr.signArbitrary(keyChain, address, data);
          return {signature: signed.signature};
        };
      }
      const key = await deriveEd25519FromKeplr(address, keyChain, signPayload);
      const attestations = await signPIRProposal(proposal, key);
      const result = await post<{html_url:string}>('/api/pir-update-prs', {base_sha:proposal.base_sha, config:proposal.config, attestations});
      setPR(result.html_url);
    } catch(e) {setError(String(e));} finally {setBusy(false);}
  }
  return <section className="max-w-3xl mx-auto p-6 space-y-5">
    <h1 className="text-xl font-semibold">Authorize PIR update</h1>
    <p>Select a published binary and snapshot. Your coordinator signature authorizes enrolled PIR hosts to install them after the pull request is merged.</p>
    <div className="grid gap-4">
      <label>Environment<select disabled={busy} value={scope} onChange={e=>{setScope(e.target.value);setProposal(null);setPR('');}} className="block border rounded p-2 bg-surface-1"><option value="prod">Production</option><option value="stage">Staging</option></select></label>
      <label>Binary release tag<input disabled={busy} value={tag} placeholder="v…" onChange={e=>{setTag(e.target.value);setProposal(null);setPR('');}} className="block border rounded p-2 bg-surface-1" /></label>
      <label>Published snapshot height<input disabled={busy} value={height} inputMode="numeric" onChange={e=>{setHeight(e.target.value);setProposal(null);setPR('');}} className="block border rounded p-2 bg-surface-1" /></label>
    </div>
    <button disabled={busy} onClick={prepare} className="border rounded px-4 py-2 disabled:opacity-50">{busy ? 'Working…' : 'Review update'}</button>
    {proposal && <div className="space-y-3 border rounded p-4">
      <h2 className="font-semibold">{proposal.scope === 'prod' ? 'Production' : 'Staging'} · Zcash {proposal.network}</h2>
      {proposal.current_config && <><h3>Current configuration</h3><pre className="overflow-auto text-sm">{proposal.current_config}</pre></>}
      <h3>Proposed configuration</h3><pre className="overflow-auto text-sm">{proposal.config}</pre>
      <details><summary>Authenticated artifact hashes</summary><pre className="overflow-auto text-xs">{JSON.stringify(proposal.payload,null,2)}</pre></details>
      <p>Confirm this snapshot is appropriate for the active voting round. Previously signed updates remain valid and can select older binaries or snapshots.</p>
      <label className="block">Signing key’s original chain ID
        <input disabled={busy} value={signingChain} placeholder={wallet.chainId || 'zvote-1'} onChange={e=>setSigningChain(e.target.value)} className="block border rounded p-2 bg-surface-1" />
      </label>
      <p className="text-sm">Leave blank to use the connected chain. For staging, use the chain where your pinned valargroup key was originally derived.</p>
      <button disabled={busy || !!pr} onClick={sign} className="border rounded px-4 py-2 disabled:opacity-50">Sign and create pull request</button>
    </div>}
    {error && <p role="alert" className="text-red-500 whitespace-pre-wrap">{error}</p>}
    {pr && <a href={pr} target="_blank" rel="noreferrer" className="underline">Review signed update pull request</a>}
  </section>;
}
