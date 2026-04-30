// Package main contains the manifest-signer CLI used by the round-signing
// operator and the checkpoint publisher to produce ed25519 attestations that
// wallets verify offline.
//
// The encoding here is the contract between the signer (this binary) and
// every wallet that verifies signatures — tests in canonical_test.go pin
// known-answer vectors so any change here would require a coordinated
// wallet release. Change the domain separator to bump the schema version.
package main

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// Domain separators tag what is being signed. Including them in the canonical
// payload prevents a signature over a "round manifest" from being replayed as
// a valid "checkpoint" signature (or vice-versa) by an attacker who could
// otherwise pick a forged payload that happens to share a byte prefix.
//
// Bumping these to /v2 is a breaking change requiring coordinated wallet +
// signer releases — see vote-sdk/docs/config.md §"Versioning".
const (
	roundManifestDomainSep = "shielded-vote/round-manifest/v1"
	checkpointDomainSep    = "shielded-vote/checkpoint/v1"
)

// RoundManifestPayload is the structured input to EncodeRoundManifest.
type RoundManifestPayload struct {
	ChainID    string // e.g. "svote-1"
	RoundID    []byte // 32 bytes (raw, not hex)
	EaPK       []byte // 32 bytes (Pallas compressed point)
	ValsetHash []byte // 32 bytes (CometBFT validators_hash)
}

// EncodeRoundManifest produces the canonical bytes that ed25519 signs over for
// a round-manifest attestation. See vote-sdk/docs/config.md §"round_signatures".
//
// Each variable-length field is prefixed with a uint16 big-endian length so
// the encoding is unambiguous even if a future schema lengthens a field.
// Callers that pre-validated lengths (e.g. EncodeRoundManifestStrict) get the
// same bytes regardless.
func EncodeRoundManifest(p RoundManifestPayload) ([]byte, error) {
	if p.ChainID == "" {
		return nil, errors.New("encode round manifest: chain_id is empty")
	}
	if len(p.RoundID) == 0 {
		return nil, errors.New("encode round manifest: round_id is empty")
	}
	if len(p.EaPK) == 0 {
		return nil, errors.New("encode round manifest: ea_pk is empty")
	}
	if len(p.ValsetHash) == 0 {
		return nil, errors.New("encode round manifest: valset_hash is empty")
	}
	for _, field := range [][]byte{[]byte(roundManifestDomainSep), []byte(p.ChainID), p.RoundID, p.EaPK, p.ValsetHash} {
		if len(field) > 0xFFFF {
			return nil, fmt.Errorf("encode round manifest: field exceeds u16 length cap (%d > 65535)", len(field))
		}
	}
	out := make([]byte, 0, 2*5+len(roundManifestDomainSep)+len(p.ChainID)+len(p.RoundID)+len(p.EaPK)+len(p.ValsetHash))
	out = appendLenPrefixed(out, []byte(roundManifestDomainSep))
	out = appendLenPrefixed(out, []byte(p.ChainID))
	out = appendLenPrefixed(out, p.RoundID)
	out = appendLenPrefixed(out, p.EaPK)
	out = appendLenPrefixed(out, p.ValsetHash)
	return out, nil
}

// CheckpointPayload is the structured input to EncodeCheckpoint.
type CheckpointPayload struct {
	ChainID    string
	Height     uint64
	HeaderHash []byte // 32 bytes
	ValsetHash []byte // 32 bytes
	AppHash    []byte // 32 bytes
	IssuedAt   uint64 // unix seconds
}

// EncodeCheckpoint produces the canonical bytes that ed25519 signs over for a
// checkpoint attestation. See vote-sdk/docs/config.md §"Signed checkpoint
// schema".
//
// Layout:
//
//	u16+domain_sep || u16+chain_id || u64_be(height)
//	  || u16+header_hash || u16+valset_hash || u16+app_hash || u64_be(issued_at)
func EncodeCheckpoint(p CheckpointPayload) ([]byte, error) {
	if p.ChainID == "" {
		return nil, errors.New("encode checkpoint: chain_id is empty")
	}
	if p.Height == 0 {
		return nil, errors.New("encode checkpoint: height is zero")
	}
	if p.IssuedAt == 0 {
		return nil, errors.New("encode checkpoint: issued_at is zero")
	}
	for _, field := range [][]byte{[]byte(checkpointDomainSep), []byte(p.ChainID), p.HeaderHash, p.ValsetHash, p.AppHash} {
		if len(field) == 0 {
			return nil, errors.New("encode checkpoint: empty header_hash / valset_hash / app_hash")
		}
		if len(field) > 0xFFFF {
			return nil, fmt.Errorf("encode checkpoint: field exceeds u16 length cap (%d > 65535)", len(field))
		}
	}
	const fixed = 8 /*height*/ + 8 /*issued_at*/ + 2*5 /*length prefixes*/
	out := make([]byte, 0, fixed+len(checkpointDomainSep)+len(p.ChainID)+len(p.HeaderHash)+len(p.ValsetHash)+len(p.AppHash))
	out = appendLenPrefixed(out, []byte(checkpointDomainSep))
	out = appendLenPrefixed(out, []byte(p.ChainID))
	out = binary.BigEndian.AppendUint64(out, p.Height)
	out = appendLenPrefixed(out, p.HeaderHash)
	out = appendLenPrefixed(out, p.ValsetHash)
	out = appendLenPrefixed(out, p.AppHash)
	out = binary.BigEndian.AppendUint64(out, p.IssuedAt)
	return out, nil
}

func appendLenPrefixed(dst, b []byte) []byte {
	dst = binary.BigEndian.AppendUint16(dst, uint16(len(b)))
	return append(dst, b...)
}

// PayloadDigest returns sha256(payload). Wallets do not need to compute this
// (ed25519 verifies the raw payload bytes), but operators publish it as
// `signed_payload_hash` for transparency / log correlation.
func PayloadDigest(payload []byte) [32]byte {
	return sha256.Sum256(payload)
}
