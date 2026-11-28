package votingconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
)

// RawMember is one member of a JSON object, with its value kept as raw JSON.
type RawMember struct {
	Key   string
	Value json.RawMessage
}

// isJSONNull reports whether raw is the JSON literal null.
func isJSONNull(raw []byte) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// decodeObjectMembers splits a JSON object into its members, in input order.
// A repeated key is an error (see decodeStrictObject).
func decodeObjectMembers(data []byte) ([]RawMember, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	if tok, err := dec.Token(); err != nil {
		return nil, err
	} else if tok != json.Delim('{') {
		return nil, errors.New("expected a JSON object")
	}
	var members []RawMember
	seen := make(map[string]bool)
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		key, ok := tok.(string)
		if !ok {
			return nil, errors.New("expected an object key")
		}
		if seen[key] {
			return nil, fmt.Errorf("repeated key %q", key)
		}
		seen[key] = true
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return nil, err
		}
		members = append(members, RawMember{Key: key, Value: value})
	}
	if _, err := dec.Token(); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("unexpected data after the JSON object")
	}
	return members, nil
}

// decodeStrictObject decodes the JSON object raw as wallets read it: each
// member into the destination that fields maps its exact key to, refusing a
// repeated, unknown or null member, then a missing required one.
// encoding/json matches keys without regard to case, keeps the last repeated
// key and reads null as a zero value; wallets do none of these, so an object
// that passes reads the same in both.
func decodeStrictObject(raw []byte, fields map[string]any, required ...string) error {
	members, err := decodeObjectMembers(raw)
	if err != nil {
		return err
	}
	for _, m := range members {
		dst, ok := fields[m.Key]
		switch {
		case !ok:
			return fmt.Errorf("unknown field %q", m.Key)
		case isJSONNull(m.Value):
			return fmt.Errorf("%s is null", m.Key)
		}
		if err := json.Unmarshal(m.Value, dst); err != nil {
			return fmt.Errorf("%s: %w", m.Key, err)
		}
	}
	for _, name := range required {
		if !slices.ContainsFunc(members, func(m RawMember) bool { return m.Key == name }) {
			return fmt.Errorf("no %s", name)
		}
	}
	return nil
}
