# voting-config

`voting-config` signs and verifies `token-holder-voting-config` dynamic round entries.

`sign` emits `auth_version: 2` entries. The Ed25519 signature covers the domain-separated preimage `"zcash-shielded-vote:round-auth:v2" || round_id (32 raw bytes decoded from the 64-char lowercase-hex rounds key) || ea_pk (32 raw bytes decoded from base64) || pir_depth (u32 LE) || tier0_layers (u32 LE) || tier1_layers (u32 LE) || poly_len (u32 LE)`, binding each attestation to its round and the advertised `pir_layout` (including nested YPIR `poly_len`): a signed `ea_pk` cannot be replayed under a different round id, and `pir_layout` cannot be swapped under attested rounds. Pass the layout with `--pir-depth/--tier0-layers/--tier1-layers/--poly-len` (poly_len is 2048 or 4096); the full layout must match the target config (checked on `--merge`). Changing `pir_layout` therefore invalidates every round signature — re-sign all active rounds in the same change. `verify` still accepts legacy `auth_version: 1` entries (signature over only the raw `ea_pk`) in mixed files during migration, but new signatures are always v2 and wallets authenticate v2 only. Other wrapper fields such as vote servers and PIR endpoints are validated by CI, but are not signed by this scheme.

## Generate a key

```bash
voting-config keygen --signer-id valar-2026-q2 --out ./valar-2026-q2.seed
```

The private key file is `base64(seed)` for an Ed25519 keypair. Keep it out of git. The command also prints a JSON object ready to paste into `static-voting-config-sample.json` under `trusted_keys`.

If `--out` is omitted, the seed is printed to stdout for one-shot offline workflows.

## Sign a round entry

```bash
voting-config sign \
  --round-id 2771bf7f23f05ffee61d65b9fbd039b550033899e78a0b343f8928850cf7a305 \
  --ea-pk '<base64 32-byte ea_pk>' \
  --signer-id valar-2026-q2 \
  --privkey-file ./valar-2026-q2.seed \
  --pir-depth 19 \
  --tier0-layers 12 \
  --tier1-layers 7 \
  --poly-len 4096
```

The command prints a `rounds[round_id]` entry. To update a config file in place:

```bash
voting-config sign \
  --round-id 2771bf7f23f05ffee61d65b9fbd039b550033899e78a0b343f8928850cf7a305 \
  --ea-pk '<base64 32-byte ea_pk>' \
  --signer-id valar-2026-q2 \
  --privkey-file ./valar-2026-q2.seed \
  --pir-depth 19 \
  --tier0-layers 12 \
  --tier1-layers 7 \
  --poly-len 4096 \
  --merge ./dynamic-voting-config.json
```

Private-key input can come from exactly one of:

- `--privkey-file <path>`
- `--privkey-stdin`
- `VOTING_CONFIG_PRIVKEY`

## Verify a config

```bash
voting-config verify \
  --config dynamic-voting-config.json \
  --static-config static-voting-config-sample.json
```

Use `--json` for CI output.

When the config has `extensions.proxy_delegation_v1` (proxy delegation contracts-v1 §12.1), `verify` also checks its shape: only `verifier_api_urls` and `proof_rpc_urls`, each a list of distinct https URLs, with at least two `proof_rpc_urls`.

## Wallet lists

`wallet-list` handles a wallet's signed list of curated delegates (proxy delegation contracts-v1 §12.3), which the config repo hosts at `<env>/wallets/<wallet_id>/list.json`. Every signature covers `WalletListPayloadV1`, a binary encoding of every field but `signatures` under the domain `zcash-shielded-vote:wallet-list:v1`, so any JSON formatting of the same list verifies.

Print the payload of a list, signed or not, as base64, for a signer outside this CLI:

```bash
voting-config wallet-list payload --list prod/wallets/vizor/list.json
```

Sign it with a key file and write the published document, keeping signatures by other key ids:

```bash
voting-config wallet-list sign \
  --list draft.json \
  --signer-id valargroup \
  --privkey-file ./valargroup.seed \
  --out prod/wallets/vizor/list.json
```

The vote-manager keys that sign Vizor's lists are Keplr-derived, so they normally sign through the admin UI's Sign Wallet List page; `config-attestation-keygen` derives the same key offline from the Keplr mnemonic.

Verify a list as a wallet that pins the given keys does, plus config CI's checks:

```bash
voting-config wallet-list verify \
  --list prod/wallets/vizor/list.json \
  --wallet-id vizor \
  --chain-id zvote-1 \
  --allowed-key valargroup=8oQiUWq6QDGnAgRw6U3YhnXb6JLFSXauYnWHIFmJRcw= \
  --previous base-list.json
```

- Only a signature carrying a pinned `key_id` that verifies under that key's public key counts. Repeat `--allowed-key` to pin more than one.
- Every picture must decode to 128 x 128 pixels.
- `--previous` is the list this one replaces: if anything signed changed, `version` must be higher. A previous list that does not parse sets no version, since no wallet adopted it. A `--previous` file that can't be read fails the check. For a new list, leave the flag out.
- `--chain-check <REST endpoint>` also reads each row's registry entry (`GET /shielded-vote/v1/delegates/{index}`) from a node of the list's chain and reports, and fails on, every delegate that needs a re-review: not registered, suspended, retired, or registered with another provider, handle or key than the row's.
- `--json` writes `{ok, wallet_id, chain_id, version, signed_by, delegates, error, findings}`, also on failure.

## Key rotation

Add the new public key to `static-voting-config-sample.json` under `trusted_keys`, sign new or updated round entries with the new `key_id`, and keep old trusted keys until every shipped wallet release has dropped them from its bundled trust anchor.
