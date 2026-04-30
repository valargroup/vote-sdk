package main

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// decodeHex parses a hex-encoded field and (if expectedLen > 0) checks the
// decoded length. Returns a friendly error including the field name on failure.
func decodeHex(s string, expectedLen int, field string) ([]byte, error) {
	if s == "" {
		return nil, fmt.Errorf("--%s is required", field)
	}
	b, err := hex.DecodeString(strings.ToLower(s))
	if err != nil {
		return nil, fmt.Errorf("--%s: invalid hex: %w", field, err)
	}
	if expectedLen > 0 && len(b) != expectedLen {
		return nil, fmt.Errorf("--%s: decoded %d bytes, expected %d", field, len(b), expectedLen)
	}
	return b, nil
}

// decodeBase64 parses a standard base64-encoded field and (if expectedLen > 0)
// checks the decoded length.
func decodeBase64(s string, expectedLen int, field string) ([]byte, error) {
	if s == "" {
		return nil, fmt.Errorf("--%s is required", field)
	}
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, fmt.Errorf("--%s: invalid base64: %w", field, err)
	}
	if expectedLen > 0 && len(b) != expectedLen {
		return nil, fmt.Errorf("--%s: decoded %d bytes, expected %d", field, len(b), expectedLen)
	}
	return b, nil
}
