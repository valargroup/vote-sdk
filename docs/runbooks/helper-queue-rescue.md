# Helper Queue Rescue

Use this only for local operations on a helper machine. Queue export and import
are intentionally not exposed over HTTP.

## Safety rules

- Stop `svoted` before export or import. The helper DB is locked while `svoted`
  is running, and rescue commands fail if the DB is already in use.
- Treat export files as sensitive. Processable rows and failed rows can contain
  encrypted share payloads, share commitments, and blind material.
- Move export files only over a trusted channel. Delete them after the rescue is
  complete.
- Queue data is purged after vote end time by the helper processor. Export
  before the vote closes if the queue may need rescue. The purge also truncates
  the helper DB WAL after expired share rows are deleted.

## Retry failed shares in an active round

On vote-sdk v1.3.1 or later, preview every retained failed share for one round:

```bash
export SVOTE_HOME=/opt/shielded-vote/.svoted
scripts/retry-failed-helper-shares.py \
  --home "$SVOTE_HOME" \
  --round-id <64-char-round-id-hex> \
  --expected-chain-id <chain-id>
```

The command is a dry run by default. It requires the local chain API to report
the round as active, lists every failed row it would reset, and refuses the
whole batch if any row no longer has the witness required for processing. For
a non-default helper database or REST port, also pass `--db-path` or
`--chain-api-port`.

After reviewing the exact rows, apply the recovery:

```bash
sudo scripts/retry-failed-helper-shares.py \
  --home "$SVOTE_HOME" \
  --round-id <64-char-round-id-hex> \
  --expected-chain-id <chain-id> \
  --execute
```

The script stops `svoted`, resets all failed rows for the round to `received`
with zero attempts in one transaction, and restarts the service. It preserves
their witness and original submission time. The helper then checks committed
nullifiers before proof generation: already revealed shares become submitted
without another broadcast, while missing shares follow the normal proof and
submission path. A genuinely invalid share can exhaust its retry budget and
become failed again.

Do not stop `svoted` first. The script requires an active systemd service so it
can guarantee that every stopped service is restarted, including when the
database update fails.

## Export

On the overloaded helper:

```bash
export SVOTE_HOME=/opt/shielded-vote/.svoted # use the same home path as the svoted service
systemctl stop svoted
svoted helper export-queue \
  --home "$SVOTE_HOME" \
  --round-id <64-char-round-id-hex> \
  --out /tmp/helper-queue-<round>.json
systemctl start svoted
```

By default the command reads `[helper].db_path` from app.toml when set, then
falls back to `<home>/helper.db`. Use `--db-path` to override both.

The export includes every row for the round. `received`, `witnessed`, and
permanently `failed` rows can include full payload material until the helper
purges the round after vote end time. `submitted` rows should have witness
material cleared already.

## Import

On a rescue helper:

```bash
export SVOTE_HOME=/opt/shielded-vote/.svoted # use the same home path as the svoted service
systemctl stop svoted
svoted helper import-queue \
  --home "$SVOTE_HOME" \
  --in /tmp/helper-queue-<round>.json
systemctl start svoted
```

Import inserts only processable rows. Terminal rows from the export are counted
as `skipped_terminal` and are not scheduled. A preserved pending reveal must
match its row's round, proposal, vote decision, encrypted share, and derived
share nullifier.

Duplicates are safe. Conflicts mean the rescue helper already has a different
payload for the same `(round_id, share_index, proposal_id, tree_position)` key.
Investigate conflicts before relying on that helper.

## Force Ready

As a last resort, import processable rows and ignore their original `submit_at`
times:

```bash
export SVOTE_HOME=/opt/shielded-vote/.svoted # use the same home path as the svoted service
systemctl stop svoted
svoted helper import-queue \
  --home "$SVOTE_HOME" \
  --in /tmp/helper-queue-<round>.json \
  --force-ready
systemctl start svoted
```

`--force-ready` schedules imported processable rows immediately. The original
submit time is preserved in the local `original_submit_at` metadata column so a
later export still shows that the row was force-rescheduled.
