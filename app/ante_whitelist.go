package app

import (
	"fmt"
	"strings"

	sdk "github.com/cosmos/cosmos-sdk/types"
)

// MessageWhitelistDecorator rejects any transaction that contains a message
// whose proto type URL is not in the allowed set. This enforces the chain's
// restricted permission model at the ante handler level — before signature
// verification or state access.
//
// The whitelist is a positive-security model: only explicitly enumerated
// message types are permitted. Any new message type must be added here
// before it can be processed on the standard Cosmos tx path.
type MessageWhitelistDecorator struct {
	allowed map[string]bool
}

// NewMessageWhitelistDecorator creates a decorator that only allows the
// given proto type URLs. Type URLs must include the leading slash, e.g.
// "/cosmos.staking.v1beta1.MsgDelegate".
func NewMessageWhitelistDecorator(allowedTypeURLs []string) MessageWhitelistDecorator {
	m := make(map[string]bool, len(allowedTypeURLs))
	for _, url := range allowedTypeURLs {
		m[url] = true
	}
	return MessageWhitelistDecorator{allowed: m}
}

func (d MessageWhitelistDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
	for _, msg := range tx.GetMsgs() {
		typeURL := sdk.MsgTypeURL(msg)
		if !d.allowed[typeURL] {
			return ctx, fmt.Errorf("message type %s is not allowed on this chain", typeURL)
		}
	}
	return next(ctx, tx, simulate)
}

// DefaultAllowedMessages returns the canonical set of message type URLs
// permitted on the standard Cosmos tx path. Update this list when adding
// new message types to the chain.
func DefaultAllowedMessages() []string {
	return []string{
		// Vote module — standard Cosmos tx path only (ceremony/ZKP messages
		// use the custom VoteTxWrapper path and are never seen here).
		"/svote.v1.MsgCreateVotingSession",
		"/svote.v1.MsgRegisterPallasKey",
		"/svote.v1.MsgCreateValidatorWithPallasKey",
		"/svote.v1.MsgSetVoteManager",
		"/svote.v1.MsgAuthorizedSend",

		// Staking — only validator creation and metadata edits are allowed.
		// MsgDelegate/MsgUndelegate/MsgBeginRedelegate are excluded so
		// validators cannot reorganize stake without the vote manager.
		// Initial self-delegation is handled by MsgCreateValidatorWithPallasKey.
		// NOTE: MsgCreateValidator post-genesis is disallowed at the DualAnteHandler.
		"/cosmos.staking.v1beta1.MsgCreateValidator",
		"/cosmos.staking.v1beta1.MsgEditValidator",

		// Slashing
		"/cosmos.slashing.v1beta1.MsgUnjail",
	}
}

// AllowedMessagesSummary returns a human-readable list of allowed message
// types for error messages and logging.
func AllowedMessagesSummary() string {
	msgs := DefaultAllowedMessages()
	short := make([]string, len(msgs))
	for i, m := range msgs {
		parts := strings.Split(m, ".")
		short[i] = parts[len(parts)-1]
	}
	return strings.Join(short, ", ")
}
