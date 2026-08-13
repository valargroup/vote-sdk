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

## Key rotation

Add the new public key to `static-voting-config-sample.json` under `trusted_keys`, sign new or updated round entries with the new `key_id`, and keep old trusted keys until every shipped wallet release has dropped them from its bundled trust anchor.
