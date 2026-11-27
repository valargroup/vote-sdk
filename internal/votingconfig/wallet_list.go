package votingconfig

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"reflect"
	"strings"
	"unicode"
	"unicode/utf8"

	voied25519 "github.com/oasisprotocol/curve25519-voi/primitives/ed25519"
	"golang.org/x/image/vp8"
	"golang.org/x/image/vp8l"
	"golang.org/x/image/webp"
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
	// WalletListPictureSide is the width and height of every picture.
	WalletListPictureSide = 128

	maxWalletListRows          = 1000
	maxWalletListReservedNames = 1000
	maxWalletListSignatures    = 8
	maxWalletListNameChars     = 50
	maxWalletListStatement     = 280
	maxWalletListPictureBytes  = 12288
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
// zvdk1 form. Statement and Picture are nil when absent; Picture holds the
// WebP bytes.
type WalletListRow struct {
	Index     uint32
	Provider  uint32
	Handle    string
	DK        string
	Name      string
	Statement *string
	Picture   []byte
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

// walletListWire is the published document. Statement and Picture are
// pointers so an absent member stays absent; Signatures is omitted only for
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

// walletListRowWire is one published row; Picture is canonical base64.
type walletListRowWire struct {
	Index     uint32  `json:"index"`
	Provider  uint32  `json:"provider"`
	Handle    string  `json:"handle"`
	DK        string  `json:"dk"`
	Name      string  `json:"name"`
	Statement *string `json:"statement,omitempty"`
	Picture   *string `json:"picture,omitempty"`
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
	lp16 := func(s []byte) { out = append(binary.LittleEndian.AppendUint16(out, uint16(len(s))), s...) }
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
		var statement []byte
		if row.Statement != nil {
			statement = []byte(*row.Statement)
		}
		lp16(statement)
		lp16(row.Picture)
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
		switch {
		case !isDisplayText(row.Name, maxWalletListNameChars, false):
			return listError("InvalidName", "%s", at("name"))
		case row.Statement != nil && !isDisplayText(*row.Statement, maxWalletListStatement, true):
			return listError("InvalidStatement", "%s", at("statement"))
		}
		previous = row.Index
		if p := row.Picture; p != nil {
			if len(p) > maxWalletListPictureBytes {
				return listError("PictureTooLarge", "%s is %d bytes", at("picture"), len(p))
			}
			if len(p) < 12 || string(p[:4]) != "RIFF" || string(p[8:12]) != "WEBP" || uint64(binary.LittleEndian.Uint32(p[4:8]))+8 != uint64(len(p)) {
				return listError("InvalidPicture", "%s is not a WebP file", at("picture"))
			}
		}
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

// isDisplayText applies the name and statement rule: 1 to maxChars scalar
// values in NFC, with no control character other than a line feed where
// allowed, and no bidirectional formatting character.
func isDisplayText(s string, maxChars int, allowNewline bool) bool {
	n := utf8.RuneCountInString(s)
	if !utf8.ValidString(s) || n < 1 || n > maxChars || !norm.NFC.IsNormalString(s) {
		return false
	}
	for _, r := range s {
		switch {
		case r == 0x061C || r == 0x200E || r == 0x200F || (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069):
			return false
		case unicode.Is(unicode.Cc, r) && !(allowNewline && r == '\n'):
			return false
		}
	}
	return true
}

// ParseWalletList reads a list as config tooling does before signing it: the
// checks of VerifyWalletList up to and including the field rules, except
// that "signatures" may be absent or empty. It returns the list and the
// signatures it already carries.
func ParseWalletList(document []byte) (WalletList, []Signature, error) {
	return decodeWalletList(document, false)
}

// decodeWalletList applies verification steps 1 to 5 (contracts-v1 §12.3)
// and returns the list and its signatures. With signed false, "signatures"
// may be absent or empty.
func decodeWalletList(document []byte, signed bool) (WalletList, []Signature, error) {
	var none WalletList
	if len(document) > MaxWalletListBytes {
		return none, nil, listError("TooLarge", "%d bytes", len(document))
	}
	// Go's decoder replaces invalid UTF-8 and lone surrogate escapes with
	// U+FFFD where a wallet's parser refuses the document.
	if !utf8.Valid(document) || hasLoneSurrogate(document) {
		return none, nil, listError("Malformed", "not UTF-8 JSON")
	}
	members, err := decodeObjectMembers(document)
	if err != nil {
		return none, nil, listError("Malformed", "%v", err)
	}
	var format *string
	for _, m := range members {
		if m.Key == "format" {
			var s string
			if isJSONNull(m.Value) || json.Unmarshal(m.Value, &s) != nil {
				return none, nil, listError("Malformed", "format is not a string")
			}
			format = &s
		}
	}
	if format == nil {
		return none, nil, listError("Malformed", "no format")
	}
	if *format != WalletListFormatV1 {
		return none, nil, listError("UnsupportedFormat", "format %q", *format)
	}

	if err := checkStrictJSON(document, reflect.TypeOf(walletListWire{})); err != nil {
		return none, nil, listError("Malformed", "%v", err)
	}
	var tree any
	if err := json.Unmarshal(document, &tree); err != nil {
		return none, nil, listError("Malformed", "%v", err)
	}
	if hasNull(tree) {
		return none, nil, listError("Malformed", "null value")
	}
	top := tree.(map[string]any)
	required := []string{"wallet_id", "chain_id", "version", "issued_at", "reserved_names", "delegates"}
	if signed {
		required = append(required, "signatures")
	}
	if missing := missingMember(top, required...); missing != "" {
		return none, nil, listError("Malformed", "no %s", missing)
	}
	if rows, ok := top["delegates"].([]any); ok {
		for i, row := range rows {
			obj, ok := row.(map[string]any)
			if !ok {
				return none, nil, listError("Malformed", "delegates[%d] is not an object", i)
			}
			if missing := missingMember(obj, "index", "provider", "handle", "dk", "name"); missing != "" {
				return none, nil, listError("Malformed", "delegates[%d] has no %s", i, missing)
			}
		}
	}
	var wire walletListWire
	if err := json.Unmarshal(document, &wire); err != nil {
		return none, nil, listError("Malformed", "%v", err)
	}

	if n := len(wire.Signatures); n > maxWalletListSignatures || (signed && n < 1) {
		return none, nil, listError("Malformed", "%d signatures; a list carries 1 to %d", n, maxWalletListSignatures)
	}
	for i, s := range wire.Signatures {
		if len(s.KeyID) < 1 || len(s.KeyID) > 64 || s.Alg != AlgEd25519 {
			return none, nil, listError("Malformed", "signatures[%d] needs a key_id of 1 to 64 bytes and alg %s", i, AlgEd25519)
		}
		if sig, ok := canonicalBase64(s.Sig); !ok || len(sig) != ed25519.SignatureSize {
			return none, nil, listError("Malformed", "signatures[%d].sig is not canonical base64 of %d bytes", i, ed25519.SignatureSize)
		}
	}

	list := WalletList{
		WalletID: wire.WalletID, ChainID: wire.ChainID, Version: wire.Version,
		IssuedAt: wire.IssuedAt, ReservedNames: wire.ReservedNames,
	}
	for i, row := range wire.Delegates {
		r := WalletListRow{
			Index: row.Index, Provider: row.Provider, Handle: row.Handle, DK: row.DK, Name: row.Name,
			Statement: row.Statement,
		}
		if row.Picture != nil {
			picture, ok := canonicalBase64(*row.Picture)
			if !ok {
				return none, nil, listError("Malformed", "delegates[%d].picture is not canonical base64", i)
			}
			r.Picture = picture
		}
		list.Delegates = append(list.Delegates, r)
	}
	if _, err := list.Payload(); err != nil {
		return none, nil, err
	}
	return list, wire.Signatures, nil
}

// VerifiedWalletList is a list that passed VerifyWalletList.
type VerifiedWalletList struct {
	List WalletList
	// Payload is the list's WalletListPayloadV1.
	Payload []byte
	// Signatures are all of the document's signature objects.
	Signatures []Signature
	// SignedBy is the pinned key id whose signature verified.
	SignedBy string
}

// VerifyWalletList verifies a fetched list as a wallet does (contracts-v1
// §12.3, "Verification"), reporting the first problem as a *WalletListError
// in the contract's order. Only a signature whose key_id names a pinned key
// and that verifies under it counts; every other signature is ignored.
func VerifyWalletList(document []byte, walletID, chainID string, pinned []TrustedKey) (*VerifiedWalletList, error) {
	list, sigs, err := decodeWalletList(document, true)
	if err != nil {
		return nil, err
	}
	if list.WalletID != walletID {
		return nil, listError("WrongWallet", "the list is for wallet %q, not %q", list.WalletID, walletID)
	}
	if list.ChainID != chainID {
		return nil, listError("WrongChain", "the list is for chain %q, not %q", list.ChainID, chainID)
	}
	payload, _ := list.Payload() // decodeWalletList checked the fields
	for _, key := range pinned {
		pub, err := DecodeBase64Fixed(key.Pubkey, ed25519.PublicKeySize, "pinned key "+key.KeyID)
		if err != nil || (key.Alg != "" && key.Alg != AlgEd25519) {
			continue
		}
		for _, s := range sigs {
			sig, _ := canonicalBase64(s.Sig)
			if s.KeyID == key.KeyID && verifyZIP215(pub, payload, sig) {
				return &VerifiedWalletList{List: list, Payload: payload, Signatures: sigs, SignedBy: key.KeyID}, nil
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
		r := walletListRowWire{
			Index: row.Index, Provider: row.Provider, Handle: row.Handle, DK: row.DK, Name: row.Name,
			Statement: row.Statement,
		}
		if row.Picture != nil {
			picture := base64.StdEncoding.EncodeToString(row.Picture)
			r.Picture = &picture
		}
		wire.Delegates = append(wire.Delegates, r)
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

// CheckWalletListPictures decodes every picture and requires it to be
// WalletListPictureSide pixels square, which config CI checks so that
// wallets never fall back to the identicon for a signed picture.
func CheckWalletListPictures(l WalletList) error {
	for i, row := range l.Delegates {
		if row.Picture == nil {
			continue
		}
		// The headers' sizes come first, so a small file declaring a huge
		// image is refused before its pixels are allocated.
		sizes, err := webpDeclaredSizes(row.Picture)
		if err != nil {
			return fmt.Errorf("delegates[%d] (index %d): picture does not decode: %w", i, row.Index, err)
		}
		for _, size := range sizes {
			if size != (image.Point{X: WalletListPictureSide, Y: WalletListPictureSide}) {
				return fmt.Errorf("delegates[%d] (index %d): picture is %dx%d, not %dx%d",
					i, row.Index, size.X, size.Y, WalletListPictureSide, WalletListPictureSide)
			}
		}
		img, err := webp.Decode(bytes.NewReader(row.Picture))
		if err != nil {
			return fmt.Errorf("delegates[%d] (index %d): picture does not decode: %w", i, row.Index, err)
		}
		if b := img.Bounds(); b.Dx() != WalletListPictureSide || b.Dy() != WalletListPictureSide {
			return fmt.Errorf("delegates[%d] (index %d): picture decodes to %dx%d, not %dx%d",
				i, row.Index, b.Dx(), b.Dy(), WalletListPictureSide, WalletListPictureSide)
		}
	}
	return nil
}

// webpDeclaredSizes returns every image size a WebP file's headers declare,
// reading chunks as webp.Decode does: a VP8X chunk's canvas, then the frame
// size of the first VP8 or VP8L chunk, the one Decode allocates for. The
// canvas alone does not bound the frame.
func webpDeclaredSizes(p []byte) ([]image.Point, error) {
	if len(p) < 12 {
		return nil, errors.New("no RIFF header")
	}
	var sizes []image.Point
	for rest := p[12:]; ; {
		if len(rest) < 8 {
			return nil, errors.New("no VP8 or VP8L chunk")
		}
		id, n := string(rest[:4]), uint64(binary.LittleEndian.Uint32(rest[4:8]))
		if n > uint64(len(rest)-8) {
			return nil, fmt.Errorf("chunk %q overruns the file", id)
		}
		data := rest[8 : 8+n]
		switch id {
		case "VP8X":
			if len(data) != 10 {
				return nil, errors.New("VP8X chunk is not 10 bytes")
			}
			w := uint32(data[4]) | uint32(data[5])<<8 | uint32(data[6])<<16
			h := uint32(data[7]) | uint32(data[8])<<8 | uint32(data[9])<<16
			sizes = append(sizes, image.Point{X: int(w) + 1, Y: int(h) + 1})
		case "VP8 ":
			d := vp8.NewDecoder()
			d.Init(bytes.NewReader(data), len(data))
			frame, err := d.DecodeFrameHeader()
			if err != nil {
				return nil, err
			}
			return append(sizes, image.Point{X: frame.Width, Y: frame.Height}), nil
		case "VP8L":
			config, err := vp8l.DecodeConfig(bytes.NewReader(data))
			if err != nil {
				return nil, err
			}
			return append(sizes, image.Point{X: config.Width, Y: config.Height}), nil
		}
		padded := 8 + n + n%2
		if padded > uint64(len(rest)) {
			return nil, fmt.Errorf("chunk %q has no padding byte", id)
		}
		rest = rest[padded:]
	}
}

// CheckWalletListVersion applies the version rule between a wallet's
// previous list and its next one on the same chain: a list whose payload
// changed must carry a higher version.
func CheckWalletListVersion(previous, next WalletList) error {
	before, err := previous.Payload()
	if err != nil {
		return fmt.Errorf("previous list: %w", err)
	}
	after, err := next.Payload()
	if err != nil {
		return err
	}
	if !bytes.Equal(before, after) && next.Version <= previous.Version {
		return fmt.Errorf("the list changed but its version %d is not above the previous version %d", next.Version, previous.Version)
	}
	return nil
}

// CheckWalletListReplaces applies CheckWalletListVersion to next against
// previous, the document it replaces. A previous document that does not
// parse sets no version to rise above: no wallet adopted it, and it must not
// hold back its fix.
func CheckWalletListReplaces(previous []byte, next WalletList) error {
	old, _, err := ParseWalletList(previous)
	if err != nil {
		return nil
	}
	return CheckWalletListVersion(old, next)
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

// hasNull reports whether a decoded JSON value holds null at any depth.
// encoding/json would read null as a zero value.
func hasNull(v any) bool {
	switch v := v.(type) {
	case nil:
		return true
	case map[string]any:
		for _, item := range v {
			if hasNull(item) {
				return true
			}
		}
	case []any:
		for _, item := range v {
			if hasNull(item) {
				return true
			}
		}
	}
	return false
}

// missingMember returns the first of names that obj lacks, or "".
func missingMember(obj map[string]any, names ...string) string {
	for _, name := range names {
		if _, ok := obj[name]; !ok {
			return name
		}
	}
	return ""
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
