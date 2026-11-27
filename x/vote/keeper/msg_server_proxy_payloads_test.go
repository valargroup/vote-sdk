package keeper_test

import (
	"fmt"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"google.golang.org/protobuf/proto"

	svtest "github.com/valargroup/vote-sdk/testutil"
	"github.com/valargroup/vote-sdk/testutil/proxytest"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// These tests cover the proxy delegation coordinator payloads and their
// approval floor (docs/proxy-delegation/contracts-v1.md §2.4, §5.5, §6, §7)
// with the dormancy gate open, through ProposeCoordinatorAction and
// ApproveCoordinatorAction.

// The vote managers of the payload tests.
var (
	proxyManagerA = svtest.TestAccAddr(0xA1)
	proxyManagerB = svtest.TestAccAddr(0xA2)
)

// setManagers installs vote managers addrs at the given threshold.
func (s *ProxyRegistryTestSuite) setManagers(threshold uint32, addrs ...string) {
	s.Require().NoError(s.keeper.SetVoteManagers(s.kv(), &types.VoteManagerSet{Addresses: addrs, Threshold: threshold}))
}

// coordinatorResult is the outcome of one coordinator tx.
type coordinatorResult struct {
	actionID uint64
	executed bool
	events   sdk.Events
	err      error
}

// propose runs creator's proposal of payload through the enabled handler as
// FinalizeBlock does: in a cache whose writes reach s.ctx only on success.
func (s *ProxyRegistryTestSuite) propose(creator string, payload proto.Message) coordinatorResult {
	cacheCtx, write := s.ctx.CacheContext()
	resp, err := s.msgServer.ProposeCoordinatorAction(cacheCtx, &types.MsgProposeCoordinatorAction{
		Creator: creator,
		Payload: coordinatorPayloadForMessage(s.T(), payload),
	})
	if err != nil {
		return coordinatorResult{err: err}
	}
	write()
	return coordinatorResult{actionID: resp.ActionId, executed: resp.Executed, events: cacheCtx.EventManager().Events()}
}

// approve runs approver's approval of action actionID as propose runs a
// proposal.
func (s *ProxyRegistryTestSuite) approve(approver string, actionID uint64) coordinatorResult {
	cacheCtx, write := s.ctx.CacheContext()
	resp, err := s.msgServer.ApproveCoordinatorAction(cacheCtx, &types.MsgApproveCoordinatorAction{Creator: approver, ActionId: actionID})
	if err != nil {
		return coordinatorResult{actionID: actionID, err: err}
	}
	write()
	return coordinatorResult{actionID: resp.ActionId, executed: resp.Executed, events: cacheCtx.EventManager().Events()}
}

// proposePending proposes payload as manager A under a threshold-1 policy of
// managers A and B, requires the approval floor to hold it, and returns its
// action id.
func (s *ProxyRegistryTestSuite) proposePending(payload proto.Message) uint64 {
	s.setManagers(1, proxyManagerA, proxyManagerB)
	proposed := s.propose(proxyManagerA, payload)
	s.Require().NoError(proposed.err)
	s.Require().False(proposed.executed, "the approval floor holds a proxy payload at one approval")
	return proposed.actionID
}

// execute proposes payload as manager A and approves it as manager B, the
// fewest approvals the floor allows, and returns the approval's outcome.
func (s *ProxyRegistryTestSuite) execute(payload proto.Message) coordinatorResult {
	return s.approve(proxyManagerB, s.proposePending(payload))
}

// mustExecute executes payload and returns the events of the approval that
// executed it.
func (s *ProxyRegistryTestSuite) mustExecute(payload proto.Message) sdk.Events {
	result := s.execute(payload)
	s.Require().NoError(result.err)
	s.Require().True(result.executed)
	return result.events
}

// actionStatus returns the stored status of action actionID.
func (s *ProxyRegistryTestSuite) actionStatus(actionID uint64) types.CoordinatorActionStatus {
	action, err := s.keeper.GetCoordinatorAction(s.kv(), actionID)
	s.Require().NoError(err)
	return action.Status
}

// params returns the stored registry params.
func (s *ProxyRegistryTestSuite) params() *types.ProxyDelegationParams {
	params, err := s.keeper.GetProxyDelegationParams(s.kv())
	s.Require().NoError(err)
	return params
}

// requireEntry fails unless stored entry want.Index equals want.
func (s *ProxyRegistryTestSuite) requireEntry(want *types.ProxyDelegateEntry) {
	s.Require().True(proto.Equal(want, s.entry(want.Index)), "delegate %d:\nwant %v\ngot  %v", want.Index, want, s.entry(want.Index))
}

// requireParams fails unless the stored params equal want.
func (s *ProxyRegistryTestSuite) requireParams(want *types.ProxyDelegationParams) {
	s.Require().True(proto.Equal(want, s.params()), "params:\nwant %v\ngot  %v", want, s.params())
}

// proxyVerifiers lists verifierIDs keyed by proxytest.Verifier.
func proxyVerifiers(verifierIDs ...uint32) []*types.ProxyVerifier {
	var verifiers []*types.ProxyVerifier
	for _, id := range verifierIDs {
		verifiers = append(verifiers, &types.ProxyVerifier{VerifierId: id, Pubkey: proxytest.Pub(proxytest.Verifier(id))})
	}
	return verifiers
}

// paramsPayload is manager A's params payload listing verifierIDs.
func paramsPayload(threshold, maxDelegates uint32, verifierIDs ...uint32) *types.MsgSetProxyDelegationParams {
	return &types.MsgSetProxyDelegationParams{
		Creator:           proxyManagerA,
		Verifiers:         proxyVerifiers(verifierIDs...),
		VerifierThreshold: threshold,
		MaxDelegates:      maxDelegates,
	}
}

// pausePayload is manager A's pause payload.
func pausePayload(paused bool) *types.MsgSetProxyDelegationPause {
	return &types.MsgSetProxyDelegationPause{Creator: proxyManagerA, Paused: paused}
}

// statusPayload is manager A's status payload over delegates.
func statusPayload(action, reason uint32, delegates ...uint32) *types.MsgSetDelegateStatus {
	return &types.MsgSetDelegateStatus{Creator: proxyManagerA, DelegateIndexes: delegates, Action: action, Reason: reason}
}

// eventsOfType returns the attributes of every event of type eventType, in
// emission order.
func eventsOfType(events sdk.Events, eventType string) []map[string]string {
	var found []map[string]string
	for _, event := range events {
		if event.Type != eventType {
			continue
		}
		attributes := make(map[string]string, len(event.Attributes))
		for _, attribute := range event.Attributes {
			attributes[attribute.Key] = attribute.Value
		}
		found = append(found, attributes)
	}
	return found
}

// statusEvent is the delegate_status event attributes of one delegate (§7).
func statusEvent(index, action, reason uint32) map[string]string {
	return map[string]string{
		types.AttributeKeyDelegateIndex: fmt.Sprint(index),
		types.AttributeKeyAction:        fmt.Sprint(action),
		types.AttributeKeyReason:        fmt.Sprint(reason),
	}
}

// TestProxyParamsPayload checks that the params payload replaces the verifier
// set, threshold and max_delegates, never touches the pause, and emits
// proxy_delegation_params (§5.5, §7).
func (s *ProxyRegistryTestSuite) TestProxyParamsPayload() {
	s.Require().NoError(s.keeper.SetProxyDelegationParams(s.kv(), &types.ProxyDelegationParams{
		Verifiers:         proxyVerifiers(1, 2),
		VerifierThreshold: 2,
		MaxDelegates:      100,
		ProxyDcPaused:     true,
	}))

	events := s.mustExecute(paramsPayload(1, 5, 3))
	s.requireParams(&types.ProxyDelegationParams{Verifiers: proxyVerifiers(3), VerifierThreshold: 1, MaxDelegates: 5, ProxyDcPaused: true})
	s.Require().Equal(map[string]string{"verifier_count": "1", "verifier_threshold": "1", "max_delegates": "5"},
		eventAttributes(s.T(), events, types.EventTypeProxyDelegationParams))
}

// TestProxyPausePayload checks that the pause payload sets proxy_dc_paused
// and nothing else, and emits proxy_delegation_pause (§5.5, §7).
func (s *ProxyRegistryTestSuite) TestProxyPausePayload() {
	// Before any params, the pause is all the payload writes.
	events := s.mustExecute(pausePayload(true))
	s.requireParams(&types.ProxyDelegationParams{ProxyDcPaused: true})
	s.Require().Equal(map[string]string{"paused": "true"}, eventAttributes(s.T(), events, types.EventTypeProxyDelegationPause))

	s.mustExecute(paramsPayload(1, 100, 1))
	want := &types.ProxyDelegationParams{Verifiers: proxyVerifiers(1), VerifierThreshold: 1, MaxDelegates: 100, ProxyDcPaused: true}
	s.requireParams(want)

	events = s.mustExecute(pausePayload(false))
	want.ProxyDcPaused = false
	s.requireParams(want)
	s.Require().Equal(map[string]string{"paused": "false"}, eventAttributes(s.T(), events, types.EventTypeProxyDelegationPause))
}

// The actions and two reasons of the status payload, by short names.
const (
	suspend = types.DelegateStatusActionSuspend
	lift    = types.DelegateStatusActionLift
	retire  = types.DelegateStatusActionRetire

	impersonation = types.DelegateStatusReasonImpersonation
	keyLost       = types.DelegateStatusReasonKeyLost
)

// TestDelegateStatusPayload_Transitions checks every action from every
// status (§5.5): suspend sets a live entry SUSPENDED with the reason, lift
// sets a SUSPENDED entry ACTIVE with no reason and leaves an ACTIVE one
// alone, and retire sets a live entry RETIRED with the reason and frees its
// handle. Suspending or lifting a RETIRED entry is ErrDelegateRetired (55),
// and retiring one changes nothing.
func (s *ProxyRegistryTestSuite) TestDelegateStatusPayload_Transitions() {
	const active, suspended, retired = types.DelegateStatusActive, types.DelegateStatusSuspended, types.DelegateStatusRetired
	for _, c := range []struct {
		name               string
		from, fromReason   uint32
		action             uint32
		want, wantReason   uint32
		wantRetiredRefusal bool
	}{
		{"suspend ACTIVE", active, 0, suspend, suspended, impersonation, false},
		{"suspend SUSPENDED replaces the reason", suspended, keyLost, suspend, suspended, impersonation, false},
		{"suspend RETIRED", retired, keyLost, suspend, retired, keyLost, true},
		{"lift ACTIVE", active, 0, lift, active, 0, false},
		{"lift SUSPENDED", suspended, keyLost, lift, active, 0, false},
		{"lift RETIRED", retired, keyLost, lift, retired, keyLost, true},
		{"retire ACTIVE", active, 0, retire, retired, impersonation, false},
		{"retire SUSPENDED", suspended, keyLost, retire, retired, impersonation, false},
		{"retire RETIRED keeps its reason", retired, keyLost, retire, retired, keyLost, false},
	} {
		s.Run(c.name, func() {
			s.SetupTest()
			s.seedEntries(1, 2)
			s.setStatus(1, c.from, c.fromReason)
			untouched := s.entry(2)

			result := s.execute(statusPayload(c.action, impersonation, 1))
			if c.wantRetiredRefusal {
				proxytest.RequireCode(s.T(), result.err, types.ErrDelegateRetired, 55)
				s.Require().Equal(types.CoordinatorActionStatus_COORDINATOR_ACTION_STATUS_PENDING, s.actionStatus(result.actionID))
			} else {
				s.Require().NoError(result.err)
				s.Require().Equal([]map[string]string{statusEvent(1, c.action, impersonation)}, eventsOfType(result.events, types.EventTypeDelegateStatus))
			}

			entry := s.entry(1)
			s.Require().Equal(c.want, entry.Status)
			s.Require().Equal(c.wantReason, entry.StatusReason)
			index, live, err := s.keeper.GetProxyDelegateByHandle(s.kv(), entry.Provider, entry.Handle)
			s.Require().NoError(err)
			s.Require().Equal(c.want != retired, live, "a handle record exactly while the entry is live")
			if live {
				s.Require().Equal(uint32(1), index)
			}
			s.requireEntry(untouched)
		})
	}
}

// TestDelegateStatusPayload_ManyDelegates applies one payload to several
// entries and checks one delegate_status event per entry in payload order
// (§7).
func (s *ProxyRegistryTestSuite) TestDelegateStatusPayload_ManyDelegates() {
	s.seedEntries(1, 3)
	events := s.mustExecute(statusPayload(suspend, types.DelegateStatusReasonLegal, 3, 1))
	for index, want := range map[uint32]uint32{1: types.DelegateStatusSuspended, 2: types.DelegateStatusActive, 3: types.DelegateStatusSuspended} {
		s.Require().Equal(want, s.entry(index).Status, "delegate %d", index)
	}
	s.Require().Equal([]map[string]string{statusEvent(3, suspend, 3), statusEvent(1, suspend, 3)}, eventsOfType(events, types.EventTypeDelegateStatus))
}

// TestDelegateStatusPayload_Atomic checks that one failing index leaves
// every entry unchanged: the handler itself writes nothing and emits no
// delegate_status event, whatever the tx's rollback would undo (§5.5).
func (s *ProxyRegistryTestSuite) TestDelegateStatusPayload_Atomic() {
	s.seedEntries(1, 2)
	before := []*types.ProxyDelegateEntry{s.entry(1), s.entry(2)}
	actionID := s.proposePending(statusPayload(suspend, types.DelegateStatusReasonLegal, 1, 2, 3))

	// Read back through the same context, never written to s.ctx.
	ctx, _ := s.ctx.CacheContext()
	_, err := s.msgServer.ApproveCoordinatorAction(ctx, &types.MsgApproveCoordinatorAction{Creator: proxyManagerB, ActionId: actionID})
	proxytest.RequireCode(s.T(), err, types.ErrDelegateNotFound, 53)
	for _, want := range before {
		got, err := s.keeper.GetProxyDelegateEntry(s.keeper.OpenKVStore(ctx), want.Index)
		s.Require().NoError(err)
		s.Require().True(proto.Equal(want, got), "delegate %d changed: %v", want.Index, got)
	}
	s.Require().Empty(eventsOfType(ctx.EventManager().Events(), types.EventTypeDelegateStatus))
}

// TestProxyPayloadsApprovalFloor checks the floor of §5.5 for each proxy
// payload at policy threshold 1: the proposal does not execute, a second
// approval by the proposer does not count, and the first approval by another
// manager executes the payload, which the coordinator events wrap (§7).
func (s *ProxyRegistryTestSuite) TestProxyPayloadsApprovalFloor() {
	s.seedEntries(1, 1)
	for _, c := range []struct {
		name      string
		payload   proto.Message
		eventType string
		applied   func() bool
	}{
		{"params", paramsPayload(1, 9, 1), types.EventTypeProxyDelegationParams, func() bool { return s.params().MaxDelegates == 9 }},
		{"pause", pausePayload(true), types.EventTypeProxyDelegationPause, func() bool { return s.params().ProxyDcPaused }},
		{"status", statusPayload(suspend, types.DelegateStatusReasonOther, 1), types.EventTypeDelegateStatus,
			func() bool { return s.entry(1).Status == types.DelegateStatusSuspended }},
	} {
		s.Run(c.name, func() {
			s.setManagers(1, proxyManagerA, proxyManagerB)
			proposed := s.propose(proxyManagerA, c.payload)
			s.Require().NoError(proposed.err)
			s.Require().False(proposed.executed)
			s.Require().False(c.applied())
			s.Require().Empty(eventsOfType(proposed.events, c.eventType))
			s.Require().Empty(eventsOfType(proposed.events, types.EventTypeExecuteCoordinatorAction))
			s.Require().Equal(types.CoordinatorActionStatus_COORDINATOR_ACTION_STATUS_PENDING, s.actionStatus(proposed.actionID))

			again := s.approve(proxyManagerA, proposed.actionID)
			s.Require().ErrorIs(again.err, types.ErrCoordinatorAlreadyApproved)
			s.Require().False(c.applied())
			s.Require().Equal(types.CoordinatorActionStatus_COORDINATOR_ACTION_STATUS_PENDING, s.actionStatus(proposed.actionID))

			approved := s.approve(proxyManagerB, proposed.actionID)
			s.Require().NoError(approved.err)
			s.Require().True(approved.executed)
			s.Require().True(c.applied())
			s.Require().Len(eventsOfType(approved.events, c.eventType), 1)
			executed := eventAttributes(s.T(), approved.events, types.EventTypeExecuteCoordinatorAction)
			s.Require().Equal("/"+string(proto.MessageName(c.payload)), executed[types.AttributeKeyActionType])
			s.Require().Equal("2", executed[types.AttributeKeyApprovalCount])
			s.Require().Equal(types.CoordinatorActionStatus_COORDINATOR_ACTION_STATUS_EXECUTED, s.actionStatus(proposed.actionID))
		})
	}
}

// TestProxyPayloadProposalErrors checks that a payload's stateless failure
// keeps its §6 code through ProposeCoordinatorAction and stores no action,
// and that a payload's creator must still be its proposer. The types tests
// cover each payload's codes.
func (s *ProxyRegistryTestSuite) TestProxyPayloadProposalErrors() {
	s.setManagers(1, proxyManagerA, proxyManagerB)
	next, err := s.keeper.GetNextCoordinatorActionID(s.kv())
	s.Require().NoError(err)
	proxytest.RequireCode(s.T(), s.propose(proxyManagerA, paramsPayload(2, 9, 1)).err, types.ErrInvalidProxyParams, 68)
	after, err := s.keeper.GetNextCoordinatorActionID(s.kv())
	s.Require().NoError(err)
	s.Require().Equal(next, after, "no action is stored")

	result := s.propose(proxyManagerA, &types.MsgSetProxyDelegationPause{Creator: proxyManagerB, Paused: true})
	s.Require().ErrorIs(result.err, types.ErrInvalidCoordinatorAction)
	s.Require().ErrorContains(result.err, "must match proposer")
}

// TestProxyPayloadExecutionRevalidates checks that execution re-runs a
// payload's ValidateBasic, so an invalid payload stored without a proposal,
// as a genesis import could hold, fails with its code and changes nothing.
func (s *ProxyRegistryTestSuite) TestProxyPayloadExecutionRevalidates() {
	s.setManagers(1, proxyManagerA, proxyManagerB)
	createdAt := s.blockTime()
	s.Require().NoError(s.keeper.SetCoordinatorAction(s.kv(), &types.CoordinatorAction{
		ActionId:  7,
		Payload:   coordinatorPayloadForMessage(s.T(), paramsPayload(3, 9, 1)),
		Proposer:  proxyManagerA,
		Approvals: []string{proxyManagerA},
		Status:    types.CoordinatorActionStatus_COORDINATOR_ACTION_STATUS_PENDING,
		CreatedAt: createdAt,
		ExpiresAt: createdAt + proxyTestDay,
	}))
	proxytest.RequireCode(s.T(), s.approve(proxyManagerB, 7).err, types.ErrInvalidProxyParams, 68)
	s.requireNoRegistryKeys()
}

// TestProxyRoundFlagApprovalFloor checks a MsgCreateVotingSession that sets
// proxy_delegation with the gate open: the floor holds it at one approval
// under threshold 1, a repeated approval does not count, and a second
// manager's approval executes it, creating a proxy round (§5.5).
func (s *MsgServerTestSuite) TestProxyRoundFlagApprovalFloor() {
	proxytest.SkipUnlessEnabled(s.T())
	s.seedEligibleValidators(2)
	manager, managerB := svtest.DefaultVoteManagerAddress, svtest.TestAccAddr(0xB2)
	s.seedVoteManagers(manager, managerB)
	msg := validSetupMsg()
	msg.ProxyDelegation = true
	roundID := computeExpectedRoundID(msg, uint64(s.ctx.BlockHeight()))

	resp, err := s.proposeCoordinatorAction(s.ctx, manager, msg)
	s.Require().NoError(err)
	s.Require().False(resp.Executed)
	_, err = s.keeper.GetVoteRound(s.keeper.OpenKVStore(s.ctx), roundID)
	s.Require().ErrorIs(err, types.ErrRoundNotFound)

	_, err = s.msgServer.ApproveCoordinatorAction(s.ctx, &types.MsgApproveCoordinatorAction{Creator: manager, ActionId: resp.ActionId})
	s.Require().ErrorIs(err, types.ErrCoordinatorAlreadyApproved)

	approved, err := s.msgServer.ApproveCoordinatorAction(s.ctx, &types.MsgApproveCoordinatorAction{Creator: managerB, ActionId: resp.ActionId})
	s.Require().NoError(err)
	s.Require().True(approved.Executed)
	round, err := s.keeper.GetVoteRound(s.keeper.OpenKVStore(s.ctx), roundID)
	s.Require().NoError(err)
	s.Require().True(round.GetProxyDelegation().GetEnabled())
}

// TestProxyRoundFlagWritesRoundState checks the round the flag creates with
// the gate open (§3.1, §5.5): field 31 records enabled, next_index at
// creation and pools_routed false, and the round id is the one the same
// payload gets without the flag, because field 31 is outside the round-id
// preimage. A round without the flag has no field 31.
func (s *MsgServerTestSuite) TestProxyRoundFlagWritesRoundState() {
	proxytest.SkipUnlessEnabled(s.T())
	create := func(flag bool) *types.VoteRound {
		s.SetupTest()
		s.seedEligibleValidators(2)
		manager, managerB := svtest.DefaultVoteManagerAddress, svtest.TestAccAddr(0xB2)
		s.seedVoteManagers(manager, managerB)
		kv := s.keeper.OpenKVStore(s.ctx)
		s.Require().NoError(s.keeper.SetProxyNextDelegateIndex(kv, 4))
		msg := validSetupMsg()
		msg.ProxyDelegation = flag

		resp, err := s.proposeCoordinatorAction(s.ctx, manager, msg)
		s.Require().NoError(err)
		if !resp.Executed {
			_, err = s.msgServer.ApproveCoordinatorAction(s.ctx, &types.MsgApproveCoordinatorAction{Creator: managerB, ActionId: resp.ActionId})
			s.Require().NoError(err)
		}
		var rounds []*types.VoteRound
		s.Require().NoError(s.keeper.IterateAllRounds(kv, func(round *types.VoteRound) bool {
			rounds = append(rounds, round)
			return false
		}))
		s.Require().Len(rounds, 1)
		return rounds[0]
	}

	flagged := create(true)
	s.Require().True(proto.Equal(&types.ProxyDelegationRoundState{Enabled: true, NextDelegateIndexAtCreation: 4}, flagged.ProxyDelegation))
	unflagged := create(false)
	s.Require().Nil(unflagged.ProxyDelegation)
	s.Require().Equal(unflagged.VoteRoundId, flagged.VoteRoundId)
	s.Require().Equal(computeExpectedRoundID(validSetupMsg(), uint64(s.ctx.BlockHeight())), flagged.VoteRoundId)
	// Each setup registers fresh random Pallas keys.
	flagged.ProxyDelegation, flagged.CeremonyValidators, unflagged.CeremonyValidators = nil, nil, nil
	s.Require().True(proto.Equal(unflagged, flagged), "the flag changes nothing else")
}

// TestCreateVotingSessionWithoutFlagKeepsThreshold checks that, with the gate
// open or closed, a create-session without the flag still executes in its
// proposing tx at threshold 1, as today.
func (s *MsgServerTestSuite) TestCreateVotingSessionWithoutFlagKeepsThreshold() {
	s.seedEligibleValidators(2)
	s.seedVoteManagers(svtest.DefaultVoteManagerAddress, svtest.TestAccAddr(0xB2))
	msg := validSetupMsg()

	resp, err := s.proposeCoordinatorAction(s.ctx, msg.Creator, msg)
	s.Require().NoError(err)
	s.Require().True(resp.Executed)
	round, err := s.keeper.GetVoteRound(s.keeper.OpenKVStore(s.ctx), computeExpectedRoundID(msg, uint64(s.ctx.BlockHeight())))
	s.Require().NoError(err)
	s.Require().Nil(round.ProxyDelegation)
}
