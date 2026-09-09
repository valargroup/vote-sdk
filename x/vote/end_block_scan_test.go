package vote_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"cosmossdk.io/core/store"
	"cosmossdk.io/log"
	storetypes "cosmossdk.io/store/types"
	"github.com/cosmos/cosmos-sdk/runtime"
	"github.com/cosmos/cosmos-sdk/testutil"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	svtest "github.com/valargroup/vote-sdk/testutil"
	vote "github.com/valargroup/vote-sdk/x/vote"
	"github.com/valargroup/vote-sdk/x/vote/keeper"
	"github.com/valargroup/vote-sdk/x/vote/types"
)

// The state and ordered-event digests were captured from the five-scan EndBlock
// at c157ca61cda5a34bfb98d6d9eb0aa7f9b8b03a89. Keep phase ordering, including
// newly ACTIVE rounds waiting one block and newly TALLYING rounds being eligible
// for the final phase in the same block.
func TestEndBlockCommittedStateCompatibility(t *testing.T) {
	expected := map[string][][2]string{
		"transitions": {
			{"2f452aff9b02aebcb7a34abf36af5dc4749c8165175d41997f5057ec56820317", "a8430ad6bbf484ca91bb490382edc312f6521054387a03aab5417ff879181bcd"},
			{"4fc7d3480289be22b8d3f532613a49855742e78b99a4610f9554f20c7bd3e250", "19a47734dcc3f72cd65e75d5572a731de84521ef6d0501fbba0823acc53dd36b"},
			{"c14f9951827d38d76b2edd8192571180de2d9f8da037d5d91a551cbec9ddf824", "44a1294a78a08894f1074484e3bc6e88d55cc964bbd0060ff504fed6b2040f77"},
			{"2ee8df6528e4f8b34a2ae76d0aa7411cea57f87c3931d272b7d5d1d5dd452d1a", "553a5a25073b9079973fbd32a10333c7b43169dac71c53bc7993c9dcb5d228bf"},
			{"27de6b0347a3d3648018593c78a80fc5a27f1f73c37e1839b71cd967e5dd02ed", "926944819916bbc777613e5198be3dcce55822bb47284ab55f8c6a62162eb723"},
		},
		"overflow": {
			{"d9ddc990c7d4f8344486d86a8a05278675233491728e7914b46043a9103018d1", "004770fd53f60aadfd41f15095f6e8a236eadf3ca1a44be974e3a3d1a8ef65ee"},
		},
	}
	for _, scenario := range []struct {
		name  string
		times []int64
	}{
		{"transitions", []int64{999_999, 1_000_000, 1_000_001, 1_021_600, 1_021_601}},
		// Preserve existing uint64 deadline overflow behavior, even when a round
		// starts tallying and times out in the same block.
		{"overflow", []int64{-1}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			key := storetypes.NewKVStoreKey(types.StoreKey)
			testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient"))
			service := &roundScanStoreService{KVStoreService: runtime.NewKVStoreService(key)}
			k := keeper.NewKeeper(service, svtest.TestAuthority, log.NewNopLogger(), nil, nil)
			addrs := []string{svtest.TestValAddr(1), svtest.TestValAddr(2), svtest.TestValAddr(3), svtest.TestValAddr(4)}
			k.SetStakingKeeper(newModuleMockStakingKeeper(addrs...))
			slashing := newModuleMockSlashingKeeper(5 * time.Minute)
			k.SetSlashingKeeper(slashing)
			kv := k.OpenKVStore(testCtx.Ctx)

			// Interleave statuses in key order so grouping events by round instead
			// of by phase would change their order. Seed in reverse key order.
			for id := byte(18); id > 0; id-- {
				round := svtest.ActiveRoundFixture(bytes.Repeat([]byte{id}, 32))
				round.VoteEndTime = 1_000_000
				round.CeremonyPhaseStart = 999_400
				round.CeremonyPhaseTimeout = 600
				round.TallyPhaseStart = 999_400
				round.TallyPhaseTimeout = 600
				for i, addr := range addrs {
					round.CeremonyValidators = append(round.CeremonyValidators, &types.ValidatorPallasKey{
						ValidatorAddress: addr, PallasPk: bytes.Repeat([]byte{byte(i + 1)}, 32), ShamirIndex: uint32(i + 1),
					})
					if i < 3 {
						round.CeremonyAcks = append(round.CeremonyAcks, &types.AckEntry{ValidatorAddress: addr, AckHeight: 1})
						round.DkgContributions = append(round.DkgContributions, &types.DKGContribution{
							ValidatorAddress: addr, FeldmanCommitments: [][]byte{fpLE(uint64(i + 1))},
						})
					}
				}
				switch id {
				case 1, 8: // Expired DEALT with quorum; activation must wait to compute roots.
					round.Status = types.SessionStatus_SESSION_STATUS_PENDING
					round.CeremonyStatus = types.CeremonyStatus_CEREMONY_STATUS_DEALT
				case 2, 9: // Existing tally expires at the boundary.
					round.Status = types.SessionStatus_SESSION_STATUS_TALLYING
				case 3, 10: // Expired active round, with and without leaves.
				case 4, 11: // REGISTERING failure, including jailing and ceremony logs.
					round.Status = types.SessionStatus_SESSION_STATUS_PENDING
					round.CeremonyStatus = types.CeremonyStatus_CEREMONY_STATUS_REGISTERING
				case 5:
					round.Status = types.SessionStatus_SESSION_STATUS_FINALIZED
				case 6: // No tally deadline.
					round.Status = types.SessionStatus_SESSION_STATUS_TALLYING
					round.TallyPhaseTimeout = 0
				case 7: // Active until a later block.
					round.VoteEndTime++
				case 12: // No ceremony deadline.
					round.Status = types.SessionStatus_SESSION_STATUS_PENDING
					round.CeremonyStatus = types.CeremonyStatus_CEREMONY_STATUS_REGISTERING
					round.CeremonyPhaseTimeout = 0
				case 13: // DEALT with insufficient acknowledgments.
					round.Status = types.SessionStatus_SESSION_STATUS_PENDING
					round.CeremonyStatus = types.CeremonyStatus_CEREMONY_STATUS_DEALT
					round.CeremonyAcks = round.CeremonyAcks[:2]
				case 14: // DEALT with invalid validator count.
					round.Status = types.SessionStatus_SESSION_STATUS_PENDING
					round.CeremonyStatus = types.CeremonyStatus_CEREMONY_STATUS_DEALT
					round.CeremonyValidators = nil
				case 15:
					round.Status = types.SessionStatus_SESSION_STATUS_CEREMONY_FAILED
				case 16:
					round.Status = types.SessionStatus_SESSION_STATUS_UNSPECIFIED
				case 17:
					round.Status = types.SessionStatus(99)
				case 18: // PENDING with no actionable ceremony phase.
					round.Status = types.SessionStatus_SESSION_STATUS_PENDING
				}
				require.NoError(t, k.SetVoteRound(kv, round))
				if id == 1 || id == 3 || id == 7 || id == 8 {
					_, err := k.AppendCommitment(kv, round.VoteRoundId, fpLE(uint64(id)))
					require.NoError(t, err)
				}
			}

			for block, now := range scenario.times {
				service.scans, service.values = 0, 0
				ctx := testCtx.Ctx.WithBlockHeight(int64(block + 1)).WithBlockTime(time.Unix(now, 0).UTC()).
					WithEventManager(sdk.NewEventManager())
				require.NoError(t, vote.NewAppModule(k, nil).EndBlock(ctx))
				require.Equal(t, 1, service.scans)
				require.Equal(t, 18, service.values)
				require.Zero(t, service.open)
				events, err := json.Marshal(ctx.EventManager().Events())
				require.NoError(t, err)
				require.Equal(t, expected[scenario.name][block][0], fmt.Sprintf("%x", testCtx.CMS.Commit().Hash), "committed state at block %d", block+1)
				require.Equal(t, expected[scenario.name][block][1], fmt.Sprintf("%x", sha256.Sum256(events)), "ordered events at block %d: %s", block+1, events)
			}
		})
	}
}

func TestEndBlockRoundScanEmptyAndMalformed(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(fmt.Sprintf("malformed=%t", malformed), func(t *testing.T) {
			key := storetypes.NewKVStoreKey(types.StoreKey)
			testCtx := testutil.DefaultContextWithDB(t, key, storetypes.NewTransientStoreKey("transient"))
			service := &roundScanStoreService{KVStoreService: runtime.NewKVStoreService(key)}
			k := keeper.NewKeeper(service, svtest.TestAuthority, log.NewNopLogger(), nil, nil)
			if malformed {
				roundKey, err := types.VoteRoundKey(bytes.Repeat([]byte{1}, 32))
				require.NoError(t, err)
				require.NoError(t, k.OpenKVStore(testCtx.Ctx).Set(roundKey, []byte{0xff}))
			}
			err := vote.NewAppModule(k, nil).EndBlock(testCtx.Ctx)
			if malformed {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, 1, service.scans)
			require.Zero(t, service.open, "close the iterator on both success and decode failure")
			require.Empty(t, testCtx.Ctx.EventManager().Events())
		})
	}
}

type roundScanStoreService struct {
	store.KVStoreService
	scans, values, open int
}

func (s *roundScanStoreService) OpenKVStore(ctx context.Context) store.KVStore {
	return roundScanStore{s.KVStoreService.OpenKVStore(ctx), s}
}

type roundScanStore struct {
	store.KVStore
	service *roundScanStoreService
}

func (s roundScanStore) Iterator(start, end []byte) (store.Iterator, error) {
	iter, err := s.KVStore.Iterator(start, end)
	if err != nil || !bytes.Equal(start, types.VoteRoundPrefix) {
		return iter, err
	}
	s.service.scans++
	s.service.open++
	return roundScanIterator{iter, s.service}, nil
}

func (s roundScanStore) Set(key, value []byte) error {
	if s.service.open != 0 {
		return fmt.Errorf("write while round iterator is open")
	}
	return s.KVStore.Set(key, value)
}

type roundScanIterator struct {
	store.Iterator
	service *roundScanStoreService
}

func (i roundScanIterator) Value() []byte {
	i.service.values++
	return i.Iterator.Value()
}

func (i roundScanIterator) Close() error {
	i.service.open--
	return i.Iterator.Close()
}

// Isolate round scanning from cryptographic root computation: active trees are
// empty, while both active and finalized rounds carry ceremony payloads.
func BenchmarkEndBlockRoundScan(b *testing.B) {
	for _, count := range []int{100, 1304} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			key := storetypes.NewKVStoreKey(types.StoreKey)
			testCtx := testutil.DefaultContextWithDB(b, key, storetypes.NewTransientStoreKey("transient"))
			k := keeper.NewKeeper(runtime.NewKVStoreService(key), svtest.TestAuthority, log.NewNopLogger(), nil, nil)
			ctx := testCtx.Ctx.WithBlockHeight(1).WithBlockTime(time.Unix(1_000_000, 0))
			kv := k.OpenKVStore(ctx)
			for id := 0; id < count; id++ {
				roundID := make([]byte, 32)
				binary.BigEndian.PutUint64(roundID, uint64(id))
				round := svtest.ActiveRoundFixture(roundID)
				if id%2 == 0 {
					round.Status = types.SessionStatus_SESSION_STATUS_FINALIZED
				}
				for v := byte(1); v <= 4; v++ {
					addr := svtest.TestValAddr(v)
					round.CeremonyValidators = append(round.CeremonyValidators, &types.ValidatorPallasKey{
						ValidatorAddress: addr, PallasPk: fpLE(uint64(v)), ShamirIndex: uint32(v),
					})
					round.DkgContributions = append(round.DkgContributions, &types.DKGContribution{
						ValidatorAddress: addr, FeldmanCommitments: [][]byte{fpLE(1), fpLE(2), fpLE(3)},
					})
					round.CeremonyAcks = append(round.CeremonyAcks, &types.AckEntry{ValidatorAddress: addr, AckHeight: 1})
				}
				require.NoError(b, k.SetVoteRound(kv, round))
			}
			module := vote.NewAppModule(k, nil)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := module.EndBlock(ctx); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
