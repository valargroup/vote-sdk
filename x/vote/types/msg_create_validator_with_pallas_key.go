package types

// MsgCreateValidatorWithPallasKey wraps standard MsgCreateValidator with a
// Pallas public key. The handler creates the validator via the staking module,
// then registers the Pallas key in the ceremony state.
//
// This type is hand-written rather than protoc-generated because the zvote
// proto package does not import cosmos staking types. The message uses a
// simple two-field layout: encoded staking MsgCreateValidator bytes + pallas_pk.
//
// Wire format: field 1 (bytes) = staking_msg, field 2 (bytes) = pallas_pk.
// Compatible with protobuf binary encoding.
type MsgCreateValidatorWithPallasKey struct {
	// StakingMsg is the protobuf-encoded cosmos.staking.v1beta1.MsgCreateValidator.
	StakingMsg []byte `protobuf:"bytes,1,opt,name=staking_msg,json=stakingMsg,proto3" json:"staking_msg,omitempty"`
	// PallasPk is the compressed Pallas point (32 bytes).
	PallasPk []byte `protobuf:"bytes,2,opt,name=pallas_pk,json=pallasPk,proto3" json:"pallas_pk,omitempty"`
}

func (m *MsgCreateValidatorWithPallasKey) Reset()         { *m = MsgCreateValidatorWithPallasKey{} }
func (m *MsgCreateValidatorWithPallasKey) String() string { return "MsgCreateValidatorWithPallasKey" }
func (m *MsgCreateValidatorWithPallasKey) ProtoMessage()  {}

// XXX_MessageName returns the fully-qualified protobuf message name.
// This is required for gogoproto.MessageName to resolve the type URL
// used by the Cosmos SDK's MsgServiceRouter for handler lookup.
func (m *MsgCreateValidatorWithPallasKey) XXX_MessageName() string {
	return "zvote.v1.MsgCreateValidatorWithPallasKey"
}

func (m *MsgCreateValidatorWithPallasKey) GetStakingMsg() []byte {
	if m != nil {
		return m.StakingMsg
	}
	return nil
}

func (m *MsgCreateValidatorWithPallasKey) GetPallasPk() []byte {
	if m != nil {
		return m.PallasPk
	}
	return nil
}

// MsgCreateValidatorWithPallasKeyResponse is the response for MsgCreateValidatorWithPallasKey.
type MsgCreateValidatorWithPallasKeyResponse struct{}

func (m *MsgCreateValidatorWithPallasKeyResponse) Reset() {
	*m = MsgCreateValidatorWithPallasKeyResponse{}
}
func (m *MsgCreateValidatorWithPallasKeyResponse) String() string {
	return "MsgCreateValidatorWithPallasKeyResponse"
}
func (m *MsgCreateValidatorWithPallasKeyResponse) ProtoMessage() {}
