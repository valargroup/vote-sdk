package votingconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
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
// A repeated key is an error (see checkStrictJSON).
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

// encodeObjectMembers writes members as one JSON object, in order, copying
// each value's raw bytes.
func encodeObjectMembers(members []RawMember) []byte {
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, m := range members {
		if i > 0 {
			buf.WriteByte(',')
		}
		key, _ := json.Marshal(m.Key) // marshaling a string cannot fail
		buf.Write(key)
		buf.WriteByte(':')
		buf.Write(m.Value)
	}
	buf.WriteByte('}')
	return buf.Bytes()
}

// checkStrictJSON reports, at any depth of raw, a repeated key or an object
// key that does not exactly name a field of the struct type t describes
// there. encoding/json matches names without regard to case and keeps the
// last repeated key; wallets do neither, so a document that passes reads the
// same in both. Type mismatches are left to the decoder.
func checkStrictJSON(raw json.RawMessage, t reflect.Type) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil
	}
	switch {
	case t.Kind() == reflect.Struct && trimmed[0] == '{':
		fields := jsonFields(t)
		members, err := decodeObjectMembers(trimmed)
		if err != nil {
			return err
		}
		for _, m := range members {
			fieldType, ok := fields[m.Key]
			if !ok {
				return fmt.Errorf("unknown field %q", m.Key)
			}
			if err := checkStrictJSON(m.Value, fieldType); err != nil {
				return fmt.Errorf("%s: %w", m.Key, err)
			}
		}
	case t.Kind() == reflect.Map && trimmed[0] == '{':
		members, err := decodeObjectMembers(trimmed)
		if err != nil {
			return err
		}
		for _, m := range members {
			if err := checkStrictJSON(m.Value, t.Elem()); err != nil {
				return fmt.Errorf("%s: %w", m.Key, err)
			}
		}
	case t.Kind() == reflect.Slice && trimmed[0] == '[':
		var items []json.RawMessage
		if err := json.Unmarshal(trimmed, &items); err != nil {
			return err
		}
		for i, item := range items {
			if err := checkStrictJSON(item, t.Elem()); err != nil {
				return fmt.Errorf("[%d]: %w", i, err)
			}
		}
	}
	return nil
}

// jsonFields maps the JSON member name of each field of struct type t to the
// field's type, skipping fields tagged "-".
func jsonFields(t reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type, t.NumField())
	for i := range t.NumField() {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ","); name != "" && name != "-" {
			fields[name] = t.Field(i).Type
		}
	}
	return fields
}
