# Release branches and backports

`main` is the development line for the next feature or coordinated-upgrade
release. A branch named `vMAJOR.MINOR.x` is the maintenance line for an active
minor release, such as `v1.4.x` for `v1.4.2` and later `v1.4` patches.

Before the first release candidate for a new minor line, cut its maintenance
branch from the selected release commit. Release candidate and stable tags must
be reachable from that matching branch. For example, `v1.4.2-rc.3` and
`v1.4.2` must both be on `v1.4.x`.

## State compatibility

A change is state compatible when old and new validators produce the same
deterministic state and app hash from the same ordered blocks. This requires
more than preserving the database schema. Review changes to all of the
following before applying `V:state/compatible`:

- transaction and message decoding;
- ante, proposal, and message validation;
- deterministic message, begin-block, and end-block execution;
- store keys, values, indexes, and migrations;
- consensus parameters and state-derived defaults; and
- dependencies that can alter any of those paths.

Changes isolated to queries, operator tooling, monitoring, or user interfaces
are normally state compatible when they cannot alter deterministic execution.
A dormant feature is compatible only when mixed-version validators retain the
same accepted inputs and execution behavior.

Apply exactly one state label to every PR:

- `V:state/compatible` permits a rolling binary replacement.
- `V:state/breaking` requires a coordinated validator upgrade.

CI rejects state-breaking PRs against maintenance branches.

## Backport flow

Changes should merge to `main` first. Apply `A:backport/v1.4.x` when a
state-compatible change should also ship in the active maintenance line. After
the source PR merges, Mergify opens a separate PR against `v1.4.x` and assigns
it to the source author. The backport still requires normal CI and human review;
it is never merged automatically.

If the cherry-pick conflicts, resolve the generated PR rather than pushing
directly to the maintenance branch. Release-only metadata may target the
maintenance branch directly. An emergency fix made there must be forwarded to
`main` immediately afterward.

Applying a backport label never creates a tag, publishes artifacts, deploys a
binary, or schedules an upgrade. Releases remain explicit `vMAJOR.MINOR.PATCH`
or `vMAJOR.MINOR.PATCH-rc.N` tags.

When a maintenance line reaches end of life, remove its Mergify rule and target
label. Keep only the currently supported release lines active.
