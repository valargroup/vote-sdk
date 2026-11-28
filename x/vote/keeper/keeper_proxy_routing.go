package keeper

import (
	"bytes"
	"cmp"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strconv"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/mikelodder7/curvey"

	"github.com/valargroup/vote-sdk/crypto/elgamal"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// routingRerandomizerDomain prefixes the preimage of a bucket's
// re-randomizer (docs/proxy-delegation/contracts-v1.md §5.6).
const routingRerandomizerDomain = "svote-route-rerand-v1"

// ErrRoutingRerandomizerDegenerate is returned when a bucket's re-randomizer
// is zero or leaves an identity C1 or C2, which has probability about
// 2^-250; routing then halts the chain (contracts-v1 §5.6).
var ErrRoutingRerandomizerDegenerate = errors.New("routing re-randomizer: zero randomness or an identity C1 or C2")

// ErrRoutingCorruptState is returned when routing reads a value that the
// chain's own checks never store; routing then halts the chain
// (contracts-v1 §5.6).
var ErrRoutingCorruptState = errors.New("routing: corrupt state")

// RerandomizeRoutedBucket returns final = combined + Enc(eaPK, 0, rho) and
// the rho used, where combined is the bucket's direct tally at (proposalID,
// decision) plus every pool routed into it. final decrypts to the same value
// but never has an identity C1 or C2, even when crafted pools cancel, since
// an identity C1 would fail every partial-decryption DLEQ. rho follows
// contracts-v1 §5.6. When rho is zero or final has an identity C1 or C2 it
// returns ErrRoutingRerandomizerDegenerate; malformed inputs return other
// errors.
func RerandomizeRoutedBucket(eaPK *elgamal.PublicKey, roundID []byte, proposalID, decision uint32, combined *elgamal.Ciphertext) (final *elgamal.Ciphertext, rho curvey.Scalar, err error) {
	return rerandomizeRoutedBucket(new(curvey.ScalarPallas).Hash, eaPK, roundID, proposalID, decision, combined)
}

// rerandomizeRoutedBucket is RerandomizeRoutedBucket with hashToScalar in
// place of ScalarPallas.Hash, so tests can force a degenerate rho.
func rerandomizeRoutedBucket(hashToScalar func([]byte) curvey.Scalar, eaPK *elgamal.PublicKey, roundID []byte, proposalID, decision uint32, combined *elgamal.Ciphertext) (*elgamal.Ciphertext, curvey.Scalar, error) {
	if err := types.ValidateRoundID(roundID); err != nil {
		return nil, nil, fmt.Errorf("routing re-randomizer: %w", err)
	}
	combinedBytes, err := elgamal.MarshalCiphertext(combined)
	if err != nil {
		return nil, nil, fmt.Errorf("routing re-randomizer: %w", err)
	}
	preimage := append([]byte(routingRerandomizerDomain), roundID...)
	preimage = binary.BigEndian.AppendUint32(preimage, proposalID)
	preimage = binary.BigEndian.AppendUint32(preimage, decision)
	preimage = append(preimage, combinedBytes...)
	rho := hashToScalar(preimage)
	if rho.IsZero() { // EncryptWithRandomness rejects zero randomness.
		return nil, nil, ErrRoutingRerandomizerDegenerate
	}
	zero, err := elgamal.EncryptWithRandomness(eaPK, 0, rho)
	if err != nil {
		return nil, nil, fmt.Errorf("routing re-randomizer: %w", err)
	}
	final := elgamal.HomomorphicAdd(combined, zero)
	if final.C1.IsIdentity() || final.C2.IsIdentity() {
		return nil, nil, ErrRoutingRerandomizerDegenerate
	}
	return final, rho, nil
}

// routedBucket names one option of one proposal: a tally bucket.
type routedBucket struct{ proposal, option uint32 }

// RouteProxyPools adds each delegate's pool, Pool[d], into the option its
// ballot chose on every proposal and re-randomizes each bucket that gained a
// pool (contracts-v1 §5.6). Pools stay in place. It does nothing unless round
// is enabled and not yet routed; otherwise it sets pools_routed on round, for
// the caller to store, and emits proxy_pools_routed. EndBlock runs it before
// storing the round as TALLYING and halts the chain on any error: a store
// failure, ErrRoutingRerandomizerDegenerate, or ErrRoutingCorruptState for a
// stored value the chain's checks never write. Buckets are written only after
// all are computed, so a failed run writes nothing.
func (k *Keeper) RouteProxyPools(ctx context.Context, round *types.VoteRound) error {
	return k.routeProxyPools(ctx, round, new(curvey.ScalarPallas).Hash)
}

// routeProxyPools is RouteProxyPools with hashToScalar in place of
// ScalarPallas.Hash, so tests can force a degenerate re-randomizer.
func (k *Keeper) routeProxyPools(ctx context.Context, round *types.VoteRound, hashToScalar func([]byte) curvey.Scalar) error {
	state := round.GetProxyDelegation()
	if !state.GetEnabled() || state.GetPoolsRouted() {
		return nil
	}
	kvStore := k.OpenKVStore(ctx)
	roundID := round.VoteRoundId
	prefix, err := types.DelegateBallotPrefixForRound(roundID)
	if err != nil {
		return err
	}

	// Read every ballot first, so the iterator is closed before other access.
	type storedBallot struct {
		d     uint32
		value []byte
	}
	var ballots []storedBallot
	iter, err := kvStore.Iterator(prefix, types.PrefixEndBytes(prefix))
	if err != nil {
		return err
	}
	for ; iter.Valid(); iter.Next() {
		key := iter.Key()
		if len(key) != len(prefix)+4 {
			iter.Close()
			return fmt.Errorf("%w: ballot key %x", ErrRoutingCorruptState, key)
		}
		ballots = append(ballots, storedBallot{binary.BigEndian.Uint32(key[len(prefix):]), bytes.Clone(iter.Value())})
	}
	if err := iter.Close(); err != nil {
		return err
	}

	// sums[b] is the sum of the pools routed into bucket b.
	sums := make(map[routedBucket]*elgamal.Ciphertext)
	routedPools := 0
	for _, b := range ballots {
		poolBytes, err := k.GetTally(kvStore, roundID, types.PoolProposalID, b.d)
		if err != nil {
			return err
		}
		if poolBytes == nil {
			continue
		}
		var ballot types.DelegateBallotRecord
		if err := unmarshal(b.value, &ballot); err != nil {
			return fmt.Errorf("%w: delegate %d's ballot does not decode: %v", ErrRoutingCorruptState, b.d, err)
		}
		if !bytes.Equal(ballot.VoteRoundId, roundID) || ballot.DelegateIndex != b.d {
			return fmt.Errorf("%w: the ballot stored for delegate %d names round %x and delegate %d",
				ErrRoutingCorruptState, b.d, ballot.VoteRoundId, ballot.DelegateIndex)
		}
		if err := types.ValidateDelegateBallotOptions(round, ballot.Options); err != nil {
			return fmt.Errorf("%w: delegate %d's ballot: %s", ErrRoutingCorruptState, b.d, err.Error())
		}
		pool, err := elgamal.UnmarshalCiphertext(poolBytes)
		if err != nil {
			return fmt.Errorf("%w: Pool[%d]: %v", ErrRoutingCorruptState, b.d, err)
		}
		for i, option := range ballot.Options {
			bucket := routedBucket{uint32(i + 1), option}
			if sum, ok := sums[bucket]; ok {
				sums[bucket] = elgamal.HomomorphicAdd(sum, pool)
			} else {
				sums[bucket] = pool
			}
		}
		routedPools++
	}

	buckets := slices.SortedFunc(maps.Keys(sums), func(a, b routedBucket) int {
		return cmp.Or(cmp.Compare(a.proposal, b.proposal), cmp.Compare(a.option, b.option))
	})
	var eaPK *elgamal.PublicKey
	if len(buckets) > 0 {
		if eaPK, err = elgamal.UnmarshalPublicKey(round.EaPk); err != nil {
			return fmt.Errorf("%w: ea_pk: %v", ErrRoutingCorruptState, err)
		}
	}
	finals := make([][]byte, len(buckets))
	for i, bucket := range buckets {
		combined := sums[bucket]
		direct, err := k.GetTally(kvStore, roundID, bucket.proposal, bucket.option)
		if err != nil {
			return err
		}
		if direct != nil {
			tally, err := elgamal.UnmarshalCiphertext(direct)
			if err != nil {
				return fmt.Errorf("%w: tally of option %d on proposal %d: %v", ErrRoutingCorruptState, bucket.option, bucket.proposal, err)
			}
			combined = elgamal.HomomorphicAdd(tally, combined)
		}
		final, _, err := rerandomizeRoutedBucket(hashToScalar, eaPK, roundID, bucket.proposal, bucket.option, combined)
		if err != nil {
			return fmt.Errorf("option %d on proposal %d: %w", bucket.option, bucket.proposal, err)
		}
		if finals[i], err = elgamal.MarshalCiphertext(final); err != nil {
			return err
		}
	}
	for i, bucket := range buckets {
		key, err := types.TallyKey(roundID, bucket.proposal, bucket.option)
		if err != nil {
			return err
		}
		if err := kvStore.Set(key, finals[i]); err != nil {
			return err
		}
	}

	state.PoolsRouted = true
	sdk.UnwrapSDKContext(ctx).EventManager().EmitEvent(sdk.NewEvent(
		types.EventTypeProxyPoolsRouted,
		sdk.NewAttribute(types.AttributeKeyRoundID, hex.EncodeToString(roundID)),
		sdk.NewAttribute(types.AttributeKeyBallots, strconv.Itoa(len(ballots))),
		sdk.NewAttribute(types.AttributeKeyRoutedPools, strconv.Itoa(routedPools)),
	))
	return nil
}
