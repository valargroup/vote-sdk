# Software Upgrade Runbook

The chain uses `x/upgrade` for coordinated state-breaking upgrades. The module
is not exposed directly to operators: raw `cosmos.upgrade` tx messages are not
allowlisted, and upgrade tx CLI generation is disabled. Coordinators schedule
or cancel plans through `x/vote` coordinator actions.

Routine binary swaps that preserve state still use `sdk-chain-deploy.yml`.

## First rollout with x/upgrade

The first release that adds `x/upgrade` must use **Reset SDK Chain**, not a
state-preserving deploy. Adding the `upgrade` KV store is itself a store/state
change, and existing live state does not contain that store.

## Scheduling a state-breaking upgrade

To schedule the halt height, a current coordinator proposes a coordinator
action. It executes immediately when the threshold is 1, or after enough
current coordinators approve it:

```bash
svoted tx vote schedule-upgrade <name> <height> \
  --info '{"tag":"v1.2.3","notes":"state-breaking upgrade"}' \
  --from <vote-manager-key> \
  --chain-id svote-1
```

If another plan already exists, the tx is rejected unless the caller explicitly
allows replacement:

```bash
svoted tx vote schedule-upgrade <name> <height> \
  --info '{"tag":"v1.2.4"}' \
  --replace-existing \
  --from <vote-manager-key> \
  --chain-id svote-1
```

There is no extra lead-time guard in `x/vote`. The underlying `x/upgrade`
keeper rejects past heights. Scheduling for the current block is accepted by
the keeper but effectively halts on the next preblock because the current
preblock has already run.

Inspect the scheduled plan through the query path:

```bash
svoted query upgrade plan
```

Cancel the current plan through the same coordinator action flow:

```bash
svoted tx vote cancel-upgrade \
  --from <vote-manager-key> \
  --chain-id svote-1
```

## Implementing the future binary

For each future state-breaking release:

1. Add a named handler in `app.RegisterUpgradeHandlers`.
2. Use the exact same name in the vote-manager scheduled plan.
3. If stores are added, renamed, or deleted, read the dumped upgrade-info file
   before `app.Load` and install an `UpgradeStoreLoader` for that plan.
4. Release the new binary.
5. Schedule the plan from the old binary.
6. At the halt height, install and start the new binary. Nodes without the
   matching handler halt with the `UPGRADE "<name>" NEEDED` message.

The completed handler records the applied plan in `x/upgrade`, so later queries
can confirm the upgrade height.
