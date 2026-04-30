package main

// RoundSignaturesJSON is the JSON shape of `round_signatures` as it appears
// in token-holder-voting-config/voting-config.json. See the schema in
// vote-sdk/docs/config.md §"round_signatures schema".
type RoundSignaturesJSON struct {
	RoundID           string         `json:"round_id"`
	EaPK              string         `json:"ea_pk"`
	ValsetHash        string         `json:"valset_hash"`
	SignedPayloadHash string         `json:"signed_payload_hash"`
	Signatures        []SignatureRef `json:"signatures"`
}

// CheckpointJSON is the JSON shape of checkpoints/latest.json (and
// checkpoints/<height>.json archives). See vote-sdk/docs/config.md §"Signed
// checkpoint schema".
type CheckpointJSON struct {
	ChainID    string         `json:"chain_id"`
	Height     uint64         `json:"height"`
	HeaderHash string         `json:"header_hash"`
	ValsetHash string         `json:"valset_hash"`
	AppHash    string         `json:"app_hash"`
	IssuedAt   uint64         `json:"issued_at"`
	Signatures []SignatureRef `json:"signatures"`
}

// SignatureRef is one entry in a `signatures` array. `Signer` matches a
// `manifest_signers[].id` in the wallet bundle; `Alg` is currently always
// `"ed25519"`. `Signature` is base64-encoded.
type SignatureRef struct {
	Signer    string `json:"signer"`
	Alg       string `json:"alg"`
	Signature string `json:"signature"`
}
