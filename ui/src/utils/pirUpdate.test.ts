import {describe,it,expect} from 'vitest';
import {ed25519} from '@noble/curves/ed25519.js';
import vector from '../../../internal/pirupdate/testdata/pir-update-vector.json';
import {pirSigningBytes,validatePIRProposal, type PIRProposal} from './pirUpdate';
import {base64ToBytes,bytesToBase64} from './attestEntry';
describe('PIR update authorization',()=>{
  it('matches independent shared signing vector',()=>{
    const message=pirSigningBytes(vector.scope,vector.payload);
    expect(bytesToBase64(message)).toBe(vector.message_base64);
    expect(ed25519.verify(base64ToBytes(vector.attestations.signatures[0].sig),message,base64ToBytes(vector.key.pubkey))).toBe(true);
    expect(ed25519.verify(base64ToBytes(vector.attestations.signatures[0].sig),pirSigningBytes('stage',vector.payload),base64ToBytes(vector.key.pubkey))).toBe(false);
  });
  it('refuses a server proposal changing the operator selection',async()=>{
    const proposal:PIRProposal={scope:'prod',network:'main',base_sha:'abc',config:vector.config,payload:vector.payload};
    await expect(validatePIRProposal(proposal,'prod','v1.2.3',3484440)).resolves.toBeUndefined();
    await expect(validatePIRProposal(proposal,'stage','v1.2.3',3484440)).rejects.toThrow();
    await expect(validatePIRProposal(proposal,'prod','v1.2.4',3484440)).rejects.toThrow();
    await expect(validatePIRProposal({...proposal,config:vector.config+' '},'prod','v1.2.3',3484440)).rejects.toThrow();
  });
});
