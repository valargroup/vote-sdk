# Vote Coordinator Actions

Vote coordinator actions are the on-chain approval flow for privileged vote
chain operations. Instead of one vote manager acting alone, the chain stores a
coordinator policy and counts approvals from current coordinator members.

This is not Cosmos `x/gov`. It is the permission system for operating this vote
chain.

## Coordinator Policy

The coordinator policy has two parts:

- `vote_manager_addresses`: the account addresses that can propose and approve
  coordinator actions.
- `threshold`: how many current coordinators must approve before an action
  executes.

If `threshold` is omitted in genesis/config, it defaults to `1`.

The policy is only valid when:

- there is at least one coordinator address,
- every address is a valid account address,
- there are no duplicate addresses,
- `threshold` is at least `1`,
- `threshold` is less than or equal to the number of coordinator addresses.

With `threshold = 1`, the same action flow is still used. The proposal simply
executes immediately because the proposer counts as the first approval. The
proxy delegation actions are the exception: they have an approval floor (see
[Proxy Delegation Actions](#proxy-delegation-actions)).

## How An Action Works

1. A current coordinator proposes an action.
2. The proposed action is stored on-chain with:
   - an action ID,
   - the action payload,
   - the proposer,
   - the approvals,
   - status,
   - creation time,
   - expiry time.
3. The proposer is counted as the first approval.
4. Other current coordinators approve the action by action ID.
5. When enough distinct current coordinator approvals exist, the chain executes
   the action automatically. That is `threshold` approvals, or
   `max(2, threshold)` for an action under the proxy delegation approval floor.

Approvals are checked against the current coordinator policy when the action is
ready to execute. If membership changed after the action was proposed, only
approvals from current coordinators count.

Duplicate approvals are rejected. Approvals from non-coordinators are rejected.
Expired actions cannot be approved or executed.

## Actions That Require Coordinator Approval

These actions must go through the coordinator action flow:

- create a voting session,
- replace the coordinator members,
- change the coordinator threshold,
- schedule a software upgrade,
- cancel a software upgrade,
- set, rotate, or clear an endorser mapping,
- send funds from the shared `vote_funding` module account, including funding
  validator setup,
- set the proxy delegation params, pause or resume proxy delegation, and
  suspend, reinstate or retire delegates.

These coordinator-owned actions are payloads, not public `Msg` RPCs. The
external transaction path is `propose coordinator action` and `approve
coordinator action`.

## Module-Funded Sends

`MsgAuthorizedSend` is only a coordinator action payload. It has no caller
chosen source account or denom. The payload creator must match the coordinator
who proposes the action, and once the coordinator threshold is met the native
`usvote` funds are sent from the `vote_funding` module account to the recipient.

## Proxy Delegation Actions

Three payloads manage proxy delegation (`docs/proxy-delegation/contracts-v1.md`
sections 2.4 and 5.5), and `MsgCreateVotingSession` gains a flag. Until proxy
delegation is enabled at the v1.7.0 upgrade, the chain rejects all three
payloads as unsupported, and a flagged session with code 69
(`ErrProxyDelegationDisabled`).

**Approval floor.** The three payloads, and a `MsgCreateVotingSession` with
`proxy_delegation` set, execute only with at least `max(2, threshold)` distinct
current coordinator approvals. They never execute inside the proposing
transaction, even at threshold 1, so proxy delegation needs at least two vote
managers in the coordinator policy. Other actions keep their threshold.

`MsgSetProxyDelegationParams` replaces the verifier set, its threshold and
`max_delegates`, `MsgSetProxyDelegationPause` pauses new delegation batches,
and `MsgSetDelegateStatus` suspends, lifts or retires delegates. Their fields,
bounds, error codes and events are in contracts-v1 sections 2.4 and 5.5.

From the CLI:

```bash
svoted tx vote set-proxy-delegation-params --verifier 1:<pubkey-hex> --verifier-threshold 1 --max-delegates 2000 --from <key>
svoted tx vote set-proxy-delegation-pause true --from <key>
svoted tx vote set-delegate-status suspend --delegate 17 --delegate 18 --reason 1 --from <key>
svoted tx vote set-delegate-status lift --delegate 17 --reason 4 --from <key>
svoted tx vote set-delegate-status retire --delegate 18 --reason 2 --from <key>
svoted tx vote create-voting-session session.json --proxy-delegation --from <key>
```

Each command only proposes the action, which then waits for other
coordinators to approve it with `approve-coordinator-action`. The proxy flag is
set only with `--proxy-delegation`; a session file that names
`proxy_delegation` is refused.

## Actions Outside Coordinator Multisig

These remain outside the coordinator action flow:

- Pallas key registration,
- Pallas key rotation,
- validator creation through the validator join flow,
- ceremony participation,
- staking metadata edits,
- validator unjail,
- mapped endorser actions for endorsing or clearing a round endorsement.

EA/DKG key attestations also remain outside this coordinator multisig. A trusted
coordinator attestation key may attest an `ea_pk` individually for now. The
attestation signature is round-bound (`auth_version: 2`): it covers
`"zcash-shielded-vote:round-auth:v2" || round_id || ea_pk || pir_depth || tier0_layers || tier1_layers || poly_len`, so
one round's attestation cannot be reused for another round, and the published
`pir_layout` (including `poly_len`) cannot be swapped under attested rounds.

## How Operators Use It

The admin UI shows the current coordinator policy and pending coordinator
actions. A coordinator can propose supported actions from the existing admin
screens. Pending actions can be reviewed and approved from the coordinator
actions list.

The CLI commands for coordinator-owned actions create coordinator proposals:

- `svoted tx vote create-voting-session ... [--proxy-delegation]`
- `svoted tx vote update-vote-managers ... --threshold <n>`
- `svoted tx vote schedule-upgrade ...`
- `svoted tx vote cancel-upgrade`
- `svoted tx vote set-endorser ...`
- `svoted tx vote set-proxy-delegation-params ...`
- `svoted tx vote set-proxy-delegation-pause <true|false>`
- `svoted tx vote set-delegate-status <suspend|lift|retire> ...`

To approve a pending action from the CLI:

```bash
svoted tx vote approve-coordinator-action <action-id> --from <coordinator-key>
```

To inspect the coordinator policy and actions from the CLI:

```bash
svoted query vote vote-managers
svoted query vote pending-coordinator-actions
svoted query vote coordinator-action <action-id>
```

To inspect the policy and pending actions through the REST API:

```bash
curl http://localhost:1317/shielded-vote/v1/vote-managers
curl http://localhost:1317/shielded-vote/v1/coordinator-actions
curl http://localhost:1317/shielded-vote/v1/coordinator-actions/<action-id>
```
