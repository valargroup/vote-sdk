# voting-config

`voting-config` signs and verifies `token-holder-voting-config` dynamic round entries.

For `auth_version: 1`, signatures cover only the raw 32-byte `ea_pk` decoded from base64. Wrapper fields such as vote servers and PIR endpoints are validated by CI, but are not signed by this version of the scheme.

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
  --privkey-file ./valar-2026-q2.seed
```

The command prints a `rounds[round_id]` entry. To update a config file in place:

```bash
voting-config sign \
  --round-id 2771bf7f23f05ffee61d65b9fbd039b550033899e78a0b343f8928850cf7a305 \
  --ea-pk '<base64 32-byte ea_pk>' \
  --signer-id valar-2026-q2 \
  --privkey-file ./valar-2026-q2.seed \
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
