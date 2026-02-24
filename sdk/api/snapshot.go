package api

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/z-cale/zally/crypto/ncroot"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/encoding/protowire"
)

// SnapshotConfig holds service URLs for fetching Zcash snapshot data.
type SnapshotConfig struct {
	// PIRServiceURL is the URL of the PIR server that serves nullifier tree roots.
	// Default: "http://157.180.63.235:3001"
	PIRServiceURL string

	// LightwalletdURL is the gRPC address of a lightwalletd server.
	// Default: "https://zec.rocks:443"
	LightwalletdURL string
}

// SnapshotData holds the Zcash mainnet data needed for MsgCreateVotingSession.
type SnapshotData struct {
	NullifierIMTRoot  []byte // 32-byte Poseidon IMT root from the PIR service
	SnapshotBlockhash []byte // 32-byte block hash at snapshot height
	NcRoot            []byte // 32-byte note commitment tree root (see below)
}

// fetchSnapshotData fetches all required snapshot data for session creation.
//
// The nullifier IMT root is the real value from the running PIR service.
// The snapshot blockhash is the real block hash from lightwalletd.
// The nc_root is computed via Rust FFI (Sinsemilla hash of the orchard frontier).
func fetchSnapshotData(ctx context.Context, cfg SnapshotConfig, height uint64) (*SnapshotData, error) {
	// Apply defaults.
	if cfg.PIRServiceURL == "" {
		cfg.PIRServiceURL = "http://157.180.63.235:3001"
	}
	if cfg.LightwalletdURL == "" {
		cfg.LightwalletdURL = "https://us.zec.stardust.rest:443"
	}

	// Fetch PIR root and tree state in parallel.
	type pirResult struct {
		root []byte
		err  error
	}
	type tsResult struct {
		ts  *lwdTreeState
		err error
	}

	pirCh := make(chan pirResult, 1)
	tsCh := make(chan tsResult, 1)

	go func() {
		root, err := fetchNullifierRoot(ctx, cfg.PIRServiceURL, height)
		pirCh <- pirResult{root, err}
	}()
	go func() {
		ts, err := fetchTreeState(ctx, cfg.LightwalletdURL, height)
		tsCh <- tsResult{ts, err}
	}()

	pirRes := <-pirCh
	if pirRes.err != nil {
		return nil, fmt.Errorf("fetch PIR root: %w", pirRes.err)
	}

	tsRes := <-tsCh
	if tsRes.err != nil {
		return nil, fmt.Errorf("fetch tree state at height %d: %w", height, tsRes.err)
	}
	ts := tsRes.ts

	// Decode block hash (hex string → bytes).
	blockhash, err := hex.DecodeString(ts.Hash)
	if err != nil {
		return nil, fmt.Errorf("decode blockhash hex %q: %w", ts.Hash, err)
	}

	// nc_root: compute real Sinsemilla root via Rust FFI.
	ncRoot, err := ncroot.ExtractNcRoot(ts.OrchardTree)
	if err != nil {
		return nil, fmt.Errorf("compute nc_root from orchard frontier: %w", err)
	}

	log.Printf("[zally-api] snapshot data fetched: height=%d blockhash=%s pir_root=%x nc_root=%x",
		ts.Height, ts.Hash[:min(16, len(ts.Hash))]+"...", pirRes.root[:8], ncRoot[:8])

	return &SnapshotData{
		NullifierIMTRoot:  pirRes.root,
		SnapshotBlockhash: blockhash,
		NcRoot:            ncRoot[:],
	}, nil
}

// --- PIR server client ---

// fetchNullifierRoot queries the PIR server GET /root endpoint and
// validates that the tree was built to exactly the expected snapshot height.
// Returns the 32-byte Poseidon tree root (depth-29, matching the circuit).
func fetchNullifierRoot(ctx context.Context, pirURL string, expectedHeight uint64) ([]byte, error) {
	url := strings.TrimRight(pirURL, "/") + "/root"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP GET %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("PIR service returned %d: %s", resp.StatusCode, string(body))
	}

	var result struct {
		Root29 string  `json:"root29"`
		Height *uint64 `json:"height"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode PIR response: %w", err)
	}

	// Validate that the PIR tree height matches the requested snapshot height.
	if result.Height == nil {
		return nil, fmt.Errorf("PIR service has no checkpoint height (tree not synced)")
	}
	if *result.Height != expectedHeight {
		return nil, fmt.Errorf("PIR service tree height %d does not match requested snapshot height %d", *result.Height, expectedHeight)
	}

	// The PIR service returns raw hex (no 0x prefix) of a Pallas Fp element (32 bytes LE).
	rootBytes, err := hex.DecodeString(result.Root29)
	if err != nil {
		return nil, fmt.Errorf("decode PIR root hex: %w", err)
	}
	if len(rootBytes) != 32 {
		return nil, fmt.Errorf("PIR root is %d bytes, expected 32", len(rootBytes))
	}

	return rootBytes, nil
}

// --- Lightwalletd gRPC client (manual proto encoding, no codegen) ---

// rawCodec is a gRPC codec that passes raw protobuf bytes through without
// requiring generated message types. Name() returns "proto" so the wire
// content-type stays application/grpc+proto.
type rawCodec struct{}

func (rawCodec) Marshal(v any) ([]byte, error) {
	b, ok := v.(*[]byte)
	if !ok {
		return nil, fmt.Errorf("rawCodec: expected *[]byte, got %T", v)
	}
	return *b, nil
}

func (rawCodec) Unmarshal(data []byte, v any) error {
	b, ok := v.(*[]byte)
	if !ok {
		return fmt.Errorf("rawCodec: expected *[]byte, got %T", v)
	}
	*b = append([]byte(nil), data...)
	return nil
}

func (rawCodec) Name() string { return "proto" }

// lwdTreeState holds the parsed response from lightwalletd's GetTreeState.
type lwdTreeState struct {
	Network     string
	Height      uint64
	Hash        string
	Time        uint32
	OrchardTree string
}

// encodeBlockID manually encodes a lightwalletd BlockID proto message.
// Proto schema: message BlockID { uint64 height = 1; bytes hash = 2; }
func encodeBlockID(height uint64) []byte {
	var buf []byte
	if height > 0 {
		buf = protowire.AppendTag(buf, 1, protowire.VarintType)
		buf = protowire.AppendVarint(buf, height)
	}
	return buf
}

// decodeLwdTreeState manually decodes a lightwalletd TreeState proto message.
// Proto schema:
//
//	message TreeState {
//	    string network     = 1;
//	    uint64 height      = 2;
//	    string hash        = 3;
//	    uint32 time        = 4;
//	    string saplingTree = 5;
//	    string orchardTree = 6;
//	}
func decodeLwdTreeState(b []byte) (*lwdTreeState, error) {
	ts := &lwdTreeState{}
	for len(b) > 0 {
		num, typ, n := protowire.ConsumeTag(b)
		if n < 0 {
			return nil, fmt.Errorf("invalid proto tag")
		}
		b = b[n:]

		switch {
		case num == 1 && typ == protowire.BytesType:
			v, vn := protowire.ConsumeString(b)
			if vn < 0 {
				return nil, fmt.Errorf("invalid string field 1 (network)")
			}
			ts.Network = v
			b = b[vn:]
		case num == 2 && typ == protowire.VarintType:
			v, vn := protowire.ConsumeVarint(b)
			if vn < 0 {
				return nil, fmt.Errorf("invalid varint field 2 (height)")
			}
			ts.Height = v
			b = b[vn:]
		case num == 3 && typ == protowire.BytesType:
			v, vn := protowire.ConsumeString(b)
			if vn < 0 {
				return nil, fmt.Errorf("invalid string field 3 (hash)")
			}
			ts.Hash = v
			b = b[vn:]
		case num == 4 && typ == protowire.VarintType:
			v, vn := protowire.ConsumeVarint(b)
			if vn < 0 {
				return nil, fmt.Errorf("invalid varint field 4 (time)")
			}
			ts.Time = uint32(v)
			b = b[vn:]
		case num == 6 && typ == protowire.BytesType:
			v, vn := protowire.ConsumeString(b)
			if vn < 0 {
				return nil, fmt.Errorf("invalid string field 6 (orchardTree)")
			}
			ts.OrchardTree = v
			b = b[vn:]
		default:
			// Skip unknown fields (including field 5 saplingTree).
			vn := protowire.ConsumeFieldValue(num, typ, b)
			if vn < 0 {
				return nil, fmt.Errorf("invalid field %d type %d", num, typ)
			}
			b = b[vn:]
		}
	}
	return ts, nil
}

// fetchTreeState calls lightwalletd's GetTreeState gRPC method at the given height.
func fetchTreeState(ctx context.Context, lwdURL string, height uint64) (*lwdTreeState, error) {
	// Parse URL: "https://host:port" → TLS, "http://host:port" → plaintext.
	var dialTarget string
	var opts []grpc.DialOption

	if strings.HasPrefix(lwdURL, "https://") {
		dialTarget = strings.TrimPrefix(lwdURL, "https://")
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{})))
	} else {
		dialTarget = strings.TrimPrefix(lwdURL, "http://")
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	conn, err := grpc.NewClient(dialTarget, opts...)
	if err != nil {
		return nil, fmt.Errorf("create gRPC client for %s: %w", dialTarget, err)
	}
	defer conn.Close()

	reqBytes := encodeBlockID(height)
	var respBytes []byte

	err = conn.Invoke(
		ctx,
		"/cash.z.wallet.sdk.rpc.CompactTxStreamer/GetTreeState",
		&reqBytes,
		&respBytes,
		grpc.ForceCodec(rawCodec{}),
	)
	if err != nil {
		return nil, fmt.Errorf("GetTreeState(height=%d): %w", height, err)
	}

	return decodeLwdTreeState(respBytes)
}
