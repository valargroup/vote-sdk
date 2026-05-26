# Helper Queue Rescue

Use this only for local operations on a helper machine. Queue export and import
are intentionally not exposed over HTTP.

## Safety rules

- Stop `svoted` before export or import. The helper DB is locked while `svoted`
  is running, and rescue commands fail if the DB is already in use.
- Treat export files as sensitive. Processable rows contain encrypted share
  payloads, share commitments, and blind material.
- Move export files only over a trusted channel. Delete them after the rescue is
  complete.
- Processable queue rows are closed out after vote end time by the helper
  processor. Export before the vote closes if the queue may need rescue.
  Closeout keeps accounting rows, but it clears the witness material needed to
  retry submission.

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

The export includes every row for the round. `received` and `witnessed` rows
include full processable payloads. `submitted` and permanently `failed` rows are
included for debugging but should have witness material cleared already.

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
as `skipped_terminal` and are not scheduled.

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
