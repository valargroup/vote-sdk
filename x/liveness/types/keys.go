package types

import (
	"encoding/binary"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/address"
	"github.com/cosmos/cosmos-sdk/types/kv"
)

const (
	ModuleName = "liveness"
	StoreKey   = ModuleName
	RouterKey  = ModuleName
)

var (
	ParamsKey                     = []byte{0x00}
	ValidatorSigningInfoKeyPrefix = []byte{0x01}
	MissByValidatorKeyPrefix      = []byte{0x02}
	MissByHeightKeyPrefix         = []byte{0x03}
)

func ValidatorSigningInfoKey(v sdk.ConsAddress) []byte {
	return append(ValidatorSigningInfoKeyPrefix, address.MustLengthPrefix(v.Bytes())...)
}

func ValidatorSigningInfoAddress(key []byte) sdk.ConsAddress {
	kv.AssertKeyAtLeastLength(key, 3)
	return sdk.ConsAddress(key[2:])
}

func MissByValidatorPrefixKey(v sdk.ConsAddress) []byte {
	return append(MissByValidatorKeyPrefix, address.MustLengthPrefix(v.Bytes())...)
}

func MissByValidatorKey(v sdk.ConsAddress, height int64) []byte {
	bz := make([]byte, 8)
	binary.BigEndian.PutUint64(bz, uint64(height))
	return append(MissByValidatorPrefixKey(v), bz...)
}

func MissHeightFromByValidatorKey(key []byte) int64 {
	kv.AssertKeyAtLeastLength(key, 10)
	addrLen := int(key[1])
	heightOffset := 2 + addrLen
	kv.AssertKeyAtLeastLength(key, heightOffset+8)
	return int64(binary.BigEndian.Uint64(key[heightOffset : heightOffset+8]))
}

func MissByHeightKey(height int64, v sdk.ConsAddress) []byte {
	bz := make([]byte, 8)
	binary.BigEndian.PutUint64(bz, uint64(height))
	key := append(append([]byte{}, MissByHeightKeyPrefix...), bz...)
	return append(key, address.MustLengthPrefix(v.Bytes())...)
}

func MissAddressFromByHeightKey(key []byte) sdk.ConsAddress {
	kv.AssertKeyAtLeastLength(key, 10)
	addrLen := int(key[9])
	kv.AssertKeyAtLeastLength(key, 10+addrLen)
	return sdk.ConsAddress(key[10 : 10+addrLen])
}

func MissHeightFromByHeightKey(key []byte) int64 {
	kv.AssertKeyAtLeastLength(key, 9)
	return int64(binary.BigEndian.Uint64(key[1:9]))
}
