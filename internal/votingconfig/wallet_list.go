package votingconfig

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	voied25519 "github.com/oasisprotocol/curve25519-voi/primitives/ed25519"
	"golang.org/x/text/unicode/norm"
)

// A wallet list is one wallet's long-lived signed list of curated delegates
// (docs/proxy-delegation/contracts-v1.md §12.3).
const (
	// WalletListFormatV1 is the list's format member.
	WalletListFormatV1 = "zcash-vote-wallet-list/v1"
	// WalletListDomainV1 prefixes every WalletListPayloadV1.
	WalletListDomainV1 = "zcash-shielded-vote:wallet-list:v1"
	// MaxWalletListBytes is the largest document a wallet reads.
	MaxWalletListBytes = 4 << 20

	maxWalletListRows          = 1000
	maxWalletListReservedNames = 1000
	maxWalletListSignatures    = 8
	maxWalletListNameChars     = 50
)

// WalletList is a wallet list's signed content.
type WalletList struct {
	WalletID      string
	ChainID       string
	Version       uint64
	IssuedAt      uint64
	ReservedNames []string
	Delegates     []WalletListRow
}

// WalletListRow is one curated delegate. DK is the reviewed key in its
// zvdk1 form.
type WalletListRow struct {
	Index    uint32
	Provider uint32
	Handle   string
	DK       string
	Name     string
}

// WalletListError is a list's first problem. Name is the contract's bare
// error name, such as "InvalidName"; Detail says where, for people.
type WalletListError struct {
	Name   string
	Detail string
}

// Error returns the name, followed by the detail when there is one.
func (e *WalletListError) Error() string {
	if e.Detail == "" {
		return e.Name
	}
	return e.Name + ": " + e.Detail
}

// listError returns a *WalletListError.
func listError(name, format string, args ...any) error {
	return &WalletListError{Name: name, Detail: fmt.Sprintf(format, args...)}
}

// walletListWire is the published document. Signatures is omitted only for
// an unsigned draft.
type walletListWire struct {
	Format        string              `json:"format"`
	WalletID      string              `json:"wallet_id"`
	ChainID       string              `json:"chain_id"`
	Version       uint64              `json:"version"`
	IssuedAt      uint64              `json:"issued_at"`
	ReservedNames []string            `json:"reserved_names"`
	Delegates     []walletListRowWire `json:"delegates"`
	Signatures    []Signature         `json:"signatures,omitempty"`
}

// walletListRowWire is one published row.
type walletListRowWire struct {
	Index    uint32 `json:"index"`
	Provider uint32 `json:"provider"`
	Handle   string `json:"handle"`
	DK       string `json:"dk"`
	Name     string `json:"name"`
}

// Payload checks the list's field rules, in the contract's order, and returns
// its WalletListPayloadV1, the bytes every signature covers. A broken rule is
// a *WalletListError.
func (l WalletList) Payload() ([]byte, error) {
	if err := l.checkFields(); err != nil {
		return nil, err
	}
	out := []byte(WalletListDomainV1)
	lp8 := func(s []byte) { out = append(append(out, byte(len(s))), s...) }
	lp8([]byte(l.WalletID))
	lp8([]byte(l.ChainID))
	out = binary.LittleEndian.AppendUint64(out, l.Version)
	out = binary.LittleEndian.AppendUint64(out, l.IssuedAt)
	out = binary.LittleEndian.AppendUint16(out, uint16(len(l.ReservedNames)))
	for _, name := range l.ReservedNames {
		lp8([]byte(name))
	}
	out = binary.LittleEndian.AppendUint16(out, uint16(len(l.Delegates)))
	for _, row := range l.Delegates {
		out = binary.LittleEndian.AppendUint32(out, row.Index)
		out = binary.LittleEndian.AppendUint32(out, row.Provider)
		lp8([]byte(row.Handle))
		dk, _ := DecodeDelegateKey(row.DK) // checkFields decoded it
		out = append(out, dk[:]...)
		lp8([]byte(row.Name))
	}
	return out, nil
}

// checkFields applies §12.3's field rules in the table's order, row by row.
func (l WalletList) checkFields() error {
	if !IsWalletID(l.WalletID) {
		return listError("InvalidWalletId", "wallet_id %q", l.WalletID)
	}
	if len(l.ChainID) < 1 || len(l.ChainID) > 64 || strings.IndexFunc(l.ChainID, func(r rune) bool { return r < 0x21 || r > 0x7e }) >= 0 {
		return listError("InvalidChainId", "chain_id %q", l.ChainID)
	}
	if l.Version == 0 {
		return listError("InvalidVersion", "version must be at least 1")
	}
	if l.IssuedAt == 0 {
		return listError("InvalidIssuedAt", "issued_at must be at least 1")
	}
	if len(l.ReservedNames) > maxWalletListReservedNames {
		return listError("TooManyReservedNames", "%d reserved names", len(l.ReservedNames))
	}
	seenNames := make(map[string]bool, len(l.ReservedNames))
	for i, name := range l.ReservedNames {
		if !isReservedName(name) {
			return listError("InvalidReservedName", "reserved_names[%d] %q", i, name)
		}
		if seenNames[name] {
			return listError("RepeatedReservedName", "reserved_names[%d] %q", i, name)
		}
		seenNames[name] = true
	}
	if len(l.Delegates) > maxWalletListRows {
		return listError("TooManyDelegates", "%d rows", len(l.Delegates))
	}
	var previous uint32
	for i, row := range l.Delegates {
		at := func(field string) string { return fmt.Sprintf("delegates[%d] (index %d) %s", i, row.Index, field) }
		switch {
		case row.Index == 0:
			return listError("InvalidIndex", "%s", at("index"))
		case row.Index <= previous:
			return listError("IndexNotAscending", "%s", at("index"))
		case row.Provider != 1 && row.Provider != 2:
			return listError("InvalidProvider", "%s", at("provider"))
		case !IsDelegateHandle(row.Provider, row.Handle):
			return listError("InvalidHandle", "%s", at("handle"))
		}
		if _, ok := DecodeDelegateKey(row.DK); !ok {
			return listError("InvalidKey", "%s %q", at("dk"), row.DK)
		}
		if !isDisplayText(row.Name, maxWalletListNameChars) {
			return listError("InvalidName", "%s", at("name"))
		}
		previous = row.Index
	}
	return nil
}

// isReservedName reports whether name is a reserved name: 1 to 64 lowercase
// ASCII letters, digits, _ or -.
func isReservedName(name string) bool {
	if len(name) < 1 || len(name) > 64 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}

// IsWalletID reports whether id is a valid wallet_id: a lowercase letter
// followed by up to 31 lowercase letters, digits or -. It also names the
// list's directory in the config repo.
func IsWalletID(id string) bool {
	if len(id) < 1 || len(id) > 32 || id[0] < 'a' || id[0] > 'z' {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// isDisplayText applies the name rule: 1 to maxChars scalar values in NFC,
// with no control character and no bidirectional formatting character.
func isDisplayText(s string, maxChars int) bool {
	n := utf8.RuneCountInString(s)
	if !utf8.ValidString(s) || n < 1 || n > maxChars || !norm.NFC.IsNormalString(s) {
		return false
	}
	for _, r := range s {
		switch {
		case r == 0x061C || r == 0x200E || r == 0x200F || (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069):
			return false
		case unicode.Is(unicode.Cc, r):
			return false
		}
	}
	return true
}

// ParseWalletList reads a list as config tooling does before signing it: the
// checks of VerifyWalletList up to and including the field rules, except
// that "signatures" may be absent or empty. It returns the list, the
// signatures it already carries and its WalletListPayloadV1.
func ParseWalletList(document []byte) (WalletList, []Signature, []byte, error) {
	return decodeWalletList(document, false)
}

// decodeWalletList applies verification steps 1 to 5 (contracts-v1 §12.3)
// and returns the list, its signatures and its payload. With signed false,
// "signatures" may be absent or empty.
func decodeWalletList(document []byte, signed bool) (WalletList, []Signature, []byte, error) {
	var none WalletList
	if len(document) > MaxWalletListBytes {
		return none, nil, nil, listError("TooLarge", "%d bytes", len(document))
	}
	// Go's decoder replaces invalid UTF-8 and lone surrogate escapes with
	// U+FFFD where a wallet's parser refuses the document.
	if !utf8.Valid(document) || hasLoneSurrogate(document) {
		return none, nil, nil, listError("Malformed", "not UTF-8 JSON")
	}
	members, err := decodeObjectMembers(document)
	if err != nil {
		return none, nil, nil, listError("Malformed", "%v", err)
	}
	var format *string
	for _, m := range members {
		if m.Key == "format" {
			var s string
			if isJSONNull(m.Value) || json.Unmarshal(m.Value, &s) != nil {
				return none, nil, nil, listError("Malformed", "format is not a string")
			}
			format = &s
		}
	}
	if format == nil {
		return none, nil, nil, listError("Malformed", "no format")
	}
	if *format != WalletListFormatV1 {
		return none, nil, nil, listError("UnsupportedFormat", "format %q", *format)
	}

	var (
		list                 WalletList
		names, rows, rawSigs []json.RawMessage
		required             = []string{"wallet_id", "chain_id", "version", "issued_at", "reserved_names", "delegates"}
	)
	if signed {
		required = append(required, "signatures")
	}
	if err := decodeStrictObject(document, map[string]any{
		"format": new(string), "wallet_id": &list.WalletID, "chain_id": &list.ChainID,
		"version": &list.Version, "issued_at": &list.IssuedAt,
		"reserved_names": &names, "delegates": &rows, "signatures": &rawSigs,
	}, required...); err != nil {
		return none, nil, nil, listError("Malformed", "%v", err)
	}

	if n := len(rawSigs); n > maxWalletListSignatures || (signed && n < 1) {
		return none, nil, nil, listError("Malformed", "%d signatures; a list carries 1 to %d", n, maxWalletListSignatures)
	}
	var sigs []Signature
	for i, raw := range rawSigs {
		var s Signature
		if err := decodeStrictObject(raw, map[string]any{"key_id": &s.KeyID, "alg": &s.Alg, "sig": &s.Sig}, "key_id", "alg", "sig"); err != nil {
			return none, nil, nil, listError("Malformed", "signatures[%d]: %v", i, err)
		}
		if len(s.KeyID) < 1 || len(s.KeyID) > 64 || s.Alg != AlgEd25519 {
			return none, nil, nil, listError("Malformed", "signatures[%d] needs a key_id of 1 to 64 bytes and alg %s", i, AlgEd25519)
		}
		if sig, ok := canonicalBase64(s.Sig); !ok || len(sig) != ed25519.SignatureSize {
			return none, nil, nil, listError("Malformed", "signatures[%d].sig is not canonical base64 of %d bytes", i, ed25519.SignatureSize)
		}
		sigs = append(sigs, s)
	}

	list.ReservedNames = make([]string, len(names))
	for i, raw := range names {
		if isJSONNull(raw) || json.Unmarshal(raw, &list.ReservedNames[i]) != nil {
			return none, nil, nil, listError("Malformed", "reserved_names[%d] is not a string", i)
		}
	}
	for i, raw := range rows {
		var row WalletListRow
		if err := decodeStrictObject(raw, map[string]any{
			"index": &row.Index, "provider": &row.Provider, "handle": &row.Handle, "dk": &row.DK, "name": &row.Name,
		}, "index", "provider", "handle", "dk", "name"); err != nil {
			return none, nil, nil, listError("Malformed", "delegates[%d]: %v", i, err)
		}
		list.Delegates = append(list.Delegates, row)
	}
	payload, err := list.Payload()
	if err != nil {
		return none, nil, nil, err
	}
	return list, sigs, payload, nil
}

// VerifiedWalletList is a list that passed VerifyWalletList.
type VerifiedWalletList struct {
	List WalletList
	// Payload is the list's WalletListPayloadV1.
	Payload []byte
	// SignedBy is the pinned key id whose signature verified.
	SignedBy string
}

// VerifyWalletList verifies a fetched list as a wallet does (contracts-v1
// §12.3, "Verification"), reporting the first problem as a *WalletListError
// in the contract's order. Only a signature whose key_id names a pinned key
// and that verifies under it counts; every other signature is ignored.
func VerifyWalletList(document []byte, walletID, chainID string, pinned []TrustedKey) (*VerifiedWalletList, error) {
	list, sigs, payload, err := decodeWalletList(document, true)
	if err != nil {
		return nil, err
	}
	if list.WalletID != walletID {
		return nil, listError("WrongWallet", "the list is for wallet %q, not %q", list.WalletID, walletID)
	}
	if list.ChainID != chainID {
		return nil, listError("WrongChain", "the list is for chain %q, not %q", list.ChainID, chainID)
	}
	for _, key := range pinned {
		pub, err := DecodeBase64Fixed(key.Pubkey, ed25519.PublicKeySize, "pinned key "+key.KeyID)
		if err != nil || (key.Alg != "" && key.Alg != AlgEd25519) {
			continue
		}
		for _, s := range sigs {
			sig, _ := canonicalBase64(s.Sig)
			if s.KeyID == key.KeyID && verifyZIP215(pub, payload, sig) {
				return &VerifiedWalletList{List: list, Payload: payload, SignedBy: key.KeyID}, nil
			}
		}
	}
	return nil, listError("NoPinnedSignature", "no signature by a pinned key verifies")
}

// zip215Options are the ZIP-215 rules that ERRATA 15 sets for list
// signatures, the ones CometBFT and x/vote/types.VerifyEd25519ZIP215 apply.
var zip215Options = &voied25519.Options{Verify: voied25519.VerifyOptionsZIP_215}

// verifyZIP215 reports whether sig is a ZIP-215 valid signature over msg
// under pub. Unlike crypto/ed25519 it accepts a torsion component in R or
// the key, which the wallets' verifier also accepts.
func verifyZIP215(pub, msg, sig []byte) bool {
	return len(pub) == voied25519.PublicKeySize && voied25519.VerifyWithOptions(voied25519.PublicKey(pub), msg, sig, zip215Options)
}

// MarshalDocument writes the published document for the list and sigs: the
// members in the contract's order, two-space indented, with a trailing
// newline. With no sigs it writes an unsigned draft.
func (l WalletList) MarshalDocument(sigs []Signature) ([]byte, error) {
	wire := walletListWire{
		Format: WalletListFormatV1, WalletID: l.WalletID, ChainID: l.ChainID,
		Version: l.Version, IssuedAt: l.IssuedAt,
		ReservedNames: append([]string{}, l.ReservedNames...),
		Delegates:     make([]walletListRowWire, 0, len(l.Delegates)),
		Signatures:    sigs,
	}
	for _, row := range l.Delegates {
		wire.Delegates = append(wire.Delegates, walletListRowWire{
			Index: row.Index, Provider: row.Provider, Handle: row.Handle, DK: row.DK, Name: row.Name,
		})
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(wire); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// CheckWalletListReplaces applies the version rule to next against
// previous, the document it replaces on the same chain: a list whose payload
// changed must carry a higher version. A previous document that does not
// parse sets no version to rise above: no wallet adopted it, and it must not
// hold back its fix.
func CheckWalletListReplaces(previous []byte, next WalletList) error {
	old, _, before, err := ParseWalletList(previous)
	if err != nil {
		return nil
	}
	after, err := next.Payload()
	if err != nil {
		return err
	}
	if !bytes.Equal(before, after) && next.Version <= old.Version {
		return fmt.Errorf("the list changed but its version %d is not above the previous version %d", next.Version, old.Version)
	}
	return nil
}

// canonicalBase64 decodes padded standard base64 that re-encodes to the same
// text, so equal bytes have one accepted encoding.
func canonicalBase64(s string) ([]byte, bool) {
	b, err := base64.StdEncoding.Strict().DecodeString(s)
	if err != nil || base64.StdEncoding.EncodeToString(b) != s {
		return nil, false
	}
	return b, true
}

// hasLoneSurrogate reports whether a JSON text escapes a UTF-16 surrogate
// that is not half of a high-low pair.
func hasLoneSurrogate(doc []byte) bool {
	hexAt := func(i int) (int, bool) {
		if i+4 > len(doc) {
			return 0, false
		}
		var v int
		for _, c := range doc[i : i+4] {
			d := strings.IndexByte("0123456789abcdef", byte(unicode.ToLower(rune(c))))
			if d < 0 {
				return 0, false
			}
			v = v<<4 | d
		}
		return v, true
	}
	inString := false
	for i := 0; i < len(doc); i++ {
		c := doc[i]
		switch {
		case !inString:
			inString = c == '"'
		case c == '"':
			inString = false
		case c == '\\' && i+1 < len(doc):
			i++
			if doc[i] != 'u' {
				continue
			}
			v, ok := hexAt(i + 1)
			if !ok {
				continue
			}
			i += 4
			switch {
			case v >= 0xDC00 && v <= 0xDFFF:
				return true
			case v >= 0xD800 && v <= 0xDBFF:
				if i+2 >= len(doc) || doc[i+1] != '\\' || doc[i+2] != 'u' {
					return true
				}
				low, ok := hexAt(i + 3)
				if !ok || low < 0xDC00 || low > 0xDFFF {
					return true
				}
				i += 6
			}
		}
	}
	return false
}
