# x/liveness

`x/liveness` is a minimal downtime liveness module built as a comparison to
Cosmos SDK `x/slashing`.

The module keeps the consensus-relevant liveness state in the normal merklized
application store, but avoids the stock `x/slashing` write pattern where every
active validator dirties `ValidatorSigningInfo` on every block.

## Purpose

The chain currently needs only validator downtime handling:

- track whether active validators miss blocks;
- jail and optionally slash validators that miss too many blocks in a window;
- allow validators to unjail after the downtime jail duration;
- preserve deterministic, app-hash-visible state transitions.

This module intentionally does not implement the full SDK slashing feature set.

## What It Does Not Implement

- Double-sign evidence handling.
- Tombstoning as a production path.
- Consensus pubkey lookup storage.
- SDK missed-block bitmap chunks.
- SDK slashing migrations.
- Simulation support.
- A separate protobuf API.

For comparison convenience, this module reuses SDK slashing wire structs for
params, genesis, `MsgUnjail`, and signing-info queries. That keeps the branch
small without copying generated protobuf code.

## App Wiring

The app wires `x/liveness` where `x/slashing` used to run:

- `liveness` is in `BeginBlockers`, before `staking`.
- `liveness` is included in genesis init/export.
- `SvoteApp` receives `LivenessKeeper` instead of `SlashingKeeper`.
- The module provides staking hooks through depinject.

The module name and store key are:

```go
ModuleName = "liveness"
StoreKey   = "liveness"
```

This is not a drop-in external API replacement for the SDK `slashing` module.
It is a minimal comparison module.

## State Layout

All state is stored under the `liveness` KV store.

```text
0x00                                      -> slashingtypes.Params
0x01 | len(consAddr) | consAddr           -> slashingtypes.ValidatorSigningInfo
0x02 | len(consAddr) | consAddr | height  -> missed marker
0x03 | height | len(consAddr) | consAddr  -> missed marker pruning index
```

The module reuses `slashingtypes.ValidatorSigningInfo` for compatibility with
existing query/genesis structs, but it does not use `IndexOffset` as a moving
ring-buffer cursor.

## BeginBlock Flow

For each validator in `ctx.VoteInfos()`:

1. Convert the vote info address to `sdk.ConsAddress`.
2. Ask staking whether the validator is already jailed.
3. Load the validator signing info.
4. If the validator signed, return without writing liveness state.
5. If the validator missed:
   - write a sparse missed marker keyed by `(consAddr, height)`;
   - write a pruning index keyed by `(height, consAddr)`;
   - prune missed markers older than the active signed-block window;
   - count misses in the current window;
   - update `MissedBlocksCounter`;
   - emit the SDK liveness event;
   - if the miss count exceeds the downtime threshold, slash and jail through
     staking, set `JailedUntil`, reset the miss counter, and clear sparse misses.

The signed-block path is the core difference:

```go
if signed != comet.BlockIDFlagAbsent {
	return nil
}
```

No `IndexOffset++`, no signing-info write, and no missed-block write happen on
fully signed blocks.

## Downtime Punishment

The threshold logic is intentionally close to SDK `x/slashing`:

```text
minHeight = start_height + signed_blocks_window
maxMissed = signed_blocks_window - min_signed_per_window
punish if current_height > minHeight && missed_count > maxMissed
```

When punishment triggers, the module calls staking:

- `SlashWithInfractionReason(..., INFRACTION_DOWNTIME)`
- `Jail(consAddr)`

It also emits the SDK slash event keys so logs remain familiar.

## Unjail

`MsgUnjail` uses the SDK slashing message type and performs the same core
checks:

- validator exists;
- self-delegation exists;
- self-delegation meets minimum self-delegation;
- validator is currently jailed;
- current block time is at or after `JailedUntil`;
- validator is not tombstoned.

After a successful liveness-state check, the module resets:

- `StartHeight = current block height`;
- `MissedBlocksCounter = 0`;
- `IndexOffset = 0`;
- sparse missed markers for the validator.

Then it calls staking `Unjail(consAddr)`.

## Staking Hooks

The module implements only the hooks needed for liveness accounting:

- `AfterValidatorBonded`: create or reset signing info and clear old misses.
- `AfterValidatorRemoved`: delete signing info and sparse misses.
- all other hooks are no-ops.

Unlike SDK `x/slashing`, `AfterValidatorCreated` does not store a consensus
pubkey relation because this module does not process evidence.

## Genesis

Genesis uses SDK slashing genesis structs for convenience:

- params are imported/exported as `slashingtypes.Params`;
- signing infos are imported/exported as `slashingtypes.SigningInfo`;
- missed blocks are imported/exported as `slashingtypes.ValidatorMissedBlocks`.

On init, the module also seeds signing info for bonded genesis validators so
genesis validators have liveness state before the first block.

Missed block indexes in this module are absolute block heights, not SDK
ring-buffer indexes.

## Difference From SDK x/slashing

### SDK x/slashing

For every active validator on every block, SDK slashing:

1. loads `ValidatorSigningInfo`;
2. computes `index = IndexOffset % SignedBlocksWindow`;
3. increments `IndexOffset`;
4. reads/writes a bitmap bit if needed;
5. writes `ValidatorSigningInfo` back even when the validator signed.

That means a fully signed block still dirties one signing-info key per active
validator.

### x/liveness

For a signed vote:

1. loads the validator state;
2. returns without mutating liveness state.

For a missed vote:

1. writes sparse miss markers for the absolute height;
2. counts current-window misses by range scanning that validator's sparse miss
   keys;
3. mutates signing info only because a miss occurred, or because punishment
   state changes.

The write profile is therefore proportional to missed signatures, not active
validators times blocks.

## Tradeoffs

Advantages:

- very small policy surface;
- no non-merklized consensus state;
- no signed-block liveness writes;
- easy to audit for downtime-only behavior;
- no copied SDK generated protobuf files.

Costs:

- not a full SDK slashing replacement;
- no double-sign evidence support;
- external tooling expecting the `slashing` module name will need adapters or a
  drop-in variant;
- exported missed block indexes are absolute heights, not SDK bitmap indexes.

## Tests

The comparison branch includes app-level tests for:

- signed blocks do not change signing info;
- repeated missed blocks jail the validator.

The full Go suite passes with this module wired into the app:

```sh
go test ./x/liveness/... ./app
go test ./...
```
