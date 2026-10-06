package main

import (
	"encoding/json"
	"fmt"

	"google.golang.org/protobuf/proto"

	"github.com/valargroup/vote-sdk/x/vote/types"
)

func b(n int) []byte { x := make([]byte, n); for i := range x { x[i] = byte(i*7 + 3) }; return x }

func main() {
	del := &types.MsgDelegateVote{Rk: b(32), SpendAuthSig: b(64), SignedNoteNullifier: b(32), CmxNew: b(32), VanCmx: b(32),
		GovNullifiers: [][]byte{b(32), b(32), b(32), b(32), b(32)}, Proof: b(11328), VoteRoundId: b(32), Tx1Effects: b(821)}
	var votes []*types.MsgCastVote
	for i := 0; i < 50; i++ {
		votes = append(votes, &types.MsgCastVote{VanNullifier: b(32), VoteAuthorityNoteNew: b(32), VoteCommitment: b(32), ProposalId: uint32(i + 1),
			Proof: b(11040), VoteRoundId: b(32), VoteCommTreeAnchorHeight: 0, VoteAuthSig: b(64), RVpk: b(32)})
	}
	m := &types.MsgDelegateAndCastVoteBatch{Delegation: del, Batch: &types.MsgCastVoteBatch{Votes: votes}}
	raw, _ := proto.MarshalOptions{Deterministic: true}.Marshal(m)
	js, _ := json.Marshal(m)
	rpc, _ := json.Marshal(map[string]interface{}{"jsonrpc": "2.0", "id": 1, "method": "broadcast_tx_sync", "params": map[string]interface{}{"tx": append([]byte{7}, raw...)}})
	fmt.Printf("0x07 ZKP1+50 casts: raw=%d restJSON=%d cometRPC=%d (limits: REST 1048576, Comet max_body 1000000, mempool 1048576)\n", len(raw)+1, len(js), len(rpc))
	// 0x09 estimate: replace casts by DCs adds dc_slot_nf(32)+recovery_hint(32)+~6B tags per DC; worst case 10 DCs
	extra := 10 * (64 + 6)
	fmt.Printf("0x09 ZKP1+10 DC+40 casts (est): raw~%d cometRPC~%d\n", len(raw)+1+extra, len(rpc)+extra*4/3)
	// headroom: how many 11.3KB actions fit under Comet RPC cap
	per := (len(rpc) - 2000) / 51
	fmt.Printf("approx per-proof RPC bytes=%d -> max proofs under 1,000,000 RPC body ~%d\n", per, 1000000/per)
	// V2 bundle 64.7 KiB
	v2 := 66253
	fmt.Printf("V2 cast bundle %d B -> per-tx (raw<=~749000) max ~%d casts; per 5MiB block ~%d casts\n", v2, 749000/(v2+200), (5<<20)/(v2+200))
}
