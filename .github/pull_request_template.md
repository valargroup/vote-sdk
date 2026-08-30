## What changed

Describe the behavior change and why it is needed.

## Validation

List the tests or manual checks that cover the change.

## State compatibility

Apply exactly one label before merge:

- `V:state/compatible` when mixed-version validators preserve the same
  deterministic state for the same blocks.
- `V:state/breaking` when validators must switch through a coordinated upgrade.

Explain the classification for changes that can affect transaction decoding,
validation, execution, consensus parameters, stores, or state-relevant
dependencies.

## Backport

Apply `A:backport/v1.4.x` only when this state-compatible change should also be
released on the active `v1.4.x` maintenance line. Otherwise, leave the target
label unset.
