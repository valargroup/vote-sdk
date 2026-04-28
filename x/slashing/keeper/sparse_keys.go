package keeper

import (
	"encoding/binary"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/address"
	"github.com/cosmos/cosmos-sdk/types/kv"
)

var (
	missedBlockByValidatorKeyPrefix = []byte{0x04}
	missedBlockByHeightKeyPrefix    = []byte{0x05}
)

func missedBlockByValidatorPrefixKey(v sdk.ConsAddress) []byte {
	return append(missedBlockByValidatorKeyPrefix, address.MustLengthPrefix(v.Bytes())...)
}

func missedBlockByValidatorKey(v sdk.ConsAddress, height int64) []byte {
	bz := make([]byte, 8)
	binary.BigEndian.PutUint64(bz, uint64(height))

	return append(missedBlockByValidatorPrefixKey(v), bz...)
}

func missedBlockHeightFromByValidatorKey(key []byte) int64 {
	kv.AssertKeyAtLeastLength(key, 10)
	addrLen := int(key[1])
	heightOffset := 2 + addrLen
	kv.AssertKeyAtLeastLength(key, heightOffset+8)

	return int64(binary.BigEndian.Uint64(key[heightOffset : heightOffset+8]))
}

func missedBlockByHeightKey(height int64, v sdk.ConsAddress) []byte {
	bz := make([]byte, 8)
	binary.BigEndian.PutUint64(bz, uint64(height))

	key := append(append([]byte{}, missedBlockByHeightKeyPrefix...), bz...)
	return append(key, address.MustLengthPrefix(v.Bytes())...)
}

func missedBlockAddressFromByHeightKey(key []byte) sdk.ConsAddress {
	kv.AssertKeyAtLeastLength(key, 10)
	addrLen := int(key[9])
	kv.AssertKeyAtLeastLength(key, 10+addrLen)

	return sdk.ConsAddress(key[10 : 10+addrLen])
}

func missedBlockHeightFromByHeightKey(key []byte) int64 {
	kv.AssertKeyAtLeastLength(key, 9)

	return int64(binary.BigEndian.Uint64(key[1:9]))
}
