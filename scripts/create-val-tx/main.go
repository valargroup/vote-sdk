// Command create-val-tx constructs a MsgCreateValidatorWithPallasKey and
// POSTs it to the Zally REST API. Used by init_multi.sh to register
// post-genesis validators that join an already-running chain.
//
// Usage:
//
//	go run ./scripts/create-val-tx \
//	  --home ~/.zallyd-val2 \
//	  --moniker val2 \
//	  --amount 10000000stake \
//	  --api-url http://localhost:1318
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cosmossdk.io/math"

	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"

	cmtjson "github.com/cometbft/cometbft/libs/json"
)

func main() {
	// Configure bech32 prefixes to match the Zally chain.
	cfg := sdk.GetConfig()
	cfg.SetBech32PrefixForAccount("zvote", "zvotepub")
	cfg.SetBech32PrefixForValidator("zvotevaloper", "zvotevaloperpub")
	cfg.SetBech32PrefixForConsensusNode("zvotevalcons", "zvotevalconspub")

	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// privValidatorKey mirrors the CometBFT priv_validator_key.json structure.
type privValidatorKey struct {
	PubKey struct {
		Type  string `json:"type"`
		Value string `json:"value"` // base64-encoded ed25519 public key
	} `json:"pub_key"`
}

func run() error {
	args := parseArgs()

	// Load the consensus public key from priv_validator_key.json.
	pvKeyPath := filepath.Join(args.home, "config", "priv_validator_key.json")
	pvKeyRaw, err := os.ReadFile(pvKeyPath)
	if err != nil {
		return fmt.Errorf("read priv_validator_key.json: %w", err)
	}
	var pvKey privValidatorKey
	if err := cmtjson.Unmarshal(pvKeyRaw, &pvKey); err != nil {
		return fmt.Errorf("parse priv_validator_key.json: %w", err)
	}
	consPubKeyBytes, err := base64.StdEncoding.DecodeString(pvKey.PubKey.Value)
	if err != nil {
		return fmt.Errorf("decode consensus pubkey: %w", err)
	}

	edPk := &ed25519.PubKey{Key: consPubKeyBytes}

	// Load the validator account address from the keyring.
	// The keyring stores the address in <home>/keyring-test/<key_name>.info.
	// We use `zallyd keys show` output instead — but since this is a simple
	// helper, we read the address from a file the init script writes for us.
	valAccAddr, err := readFileString(filepath.Join(args.home, "validator_address.txt"))
	if err != nil {
		return fmt.Errorf("read validator_address.txt: %w", err)
	}
	valAccAddr = strings.TrimSpace(valAccAddr)

	// Derive the validator operator address (valoper) from the account address.
	accAddr, err := sdk.AccAddressFromBech32(valAccAddr)
	if err != nil {
		return fmt.Errorf("parse account address %q: %w", valAccAddr, err)
	}
	valOperAddr := sdk.ValAddress(accAddr).String()

	// Parse the self-delegation amount.
	coin, err := sdk.ParseCoinNormalized(args.amount)
	if err != nil {
		return fmt.Errorf("parse amount %q: %w", args.amount, err)
	}

	// Build the MsgCreateValidator using the SDK constructor.
	// DelegatorAddress is deprecated in v0.53+; the constructor omits it.
	description := stakingtypes.Description{Moniker: args.moniker}
	commission := stakingtypes.CommissionRates{
		Rate:          math.LegacyNewDecWithPrec(1, 1), // 10%
		MaxRate:       math.LegacyNewDecWithPrec(2, 1), // 20%
		MaxChangeRate: math.LegacyNewDecWithPrec(1, 2), // 1%
	}
	stakingMsg, err := stakingtypes.NewMsgCreateValidator(
		valOperAddr, edPk, coin, description, commission, math.NewInt(1),
	)
	if err != nil {
		return fmt.Errorf("build MsgCreateValidator: %w", err)
	}

	// Marshal to gogoproto binary (same format the keeper expects).
	stakingMsgBytes, err := stakingMsg.Marshal()
	if err != nil {
		return fmt.Errorf("marshal MsgCreateValidator: %w", err)
	}

	// Read the Pallas public key.
	pallasPkBytes, err := os.ReadFile(filepath.Join(args.home, "pallas.pk"))
	if err != nil {
		return fmt.Errorf("read pallas.pk: %w", err)
	}

	// Build the JSON payload. The REST handler uses encoding/json to decode,
	// so []byte fields are base64-encoded automatically.
	payload := struct {
		StakingMsg []byte `json:"staking_msg"`
		PallasPk   []byte `json:"pallas_pk"`
	}{
		StakingMsg: stakingMsgBytes,
		PallasPk:   pallasPkBytes,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal JSON payload: %w", err)
	}

	// POST to the REST API.
	url := args.apiURL + "/zally/v1/create-validator-with-pallas"
	fmt.Printf("POST %s\n", url)
	fmt.Printf("  moniker:    %s\n", args.moniker)
	fmt.Printf("  validator:  %s\n", valOperAddr)
	fmt.Printf("  amount:     %s\n", coin)
	fmt.Printf("  pallas_pk:  %s\n", base64.StdEncoding.EncodeToString(pallasPkBytes))

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("POST failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	fmt.Printf("  status:     %d\n", resp.StatusCode)
	fmt.Printf("  response:   %s\n", string(respBody))

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("API returned status %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

type cliArgs struct {
	home    string
	moniker string
	amount  string
	apiURL  string
}

func parseArgs() cliArgs {
	args := cliArgs{
		amount: "10000000stake",
		apiURL: "http://localhost:1318",
	}

	for i := 1; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--home":
			i++
			args.home = os.Args[i]
		case "--moniker":
			i++
			args.moniker = os.Args[i]
		case "--amount":
			i++
			args.amount = os.Args[i]
		case "--api-url":
			i++
			args.apiURL = os.Args[i]
		}
	}

	if args.home == "" {
		fmt.Fprintln(os.Stderr, "error: --home is required")
		os.Exit(1)
	}
	if args.moniker == "" {
		fmt.Fprintln(os.Stderr, "error: --moniker is required")
		os.Exit(1)
	}

	return args
}

func readFileString(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(b), nil
}
