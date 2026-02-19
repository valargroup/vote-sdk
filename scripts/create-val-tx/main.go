// Command create-val-tx constructs a MsgCreateValidatorWithPallasKey,
// signs it via `zallyd tx sign`, and broadcasts it via `zallyd tx broadcast`.
// Used by init_multi_ci.sh to register post-genesis validators that join an
// already-running chain.
//
// Usage:
//
//	create-val-tx \
//	  --home ~/.zallyd-val2 \
//	  --moniker val2 \
//	  --amount 10000000stake \
//	  --rpc-url tcp://localhost:26157
package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"cosmossdk.io/math"

	cmtjson "github.com/cometbft/cometbft/libs/json"
	"github.com/cosmos/cosmos-sdk/crypto/keys/ed25519"
	sdk "github.com/cosmos/cosmos-sdk/types"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
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

	// Read the validator account address written by the init script.
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

	// Marshal to gogoproto binary (the format the keeper expects).
	// The server unpacks the Any-wrapped pubkey itself via UnpackInterfaces.
	stakingMsgBytes, err := stakingMsg.Marshal()
	if err != nil {
		return fmt.Errorf("marshal MsgCreateValidator: %w", err)
	}

	// Read the Pallas public key.
	pallasPkBytes, err := os.ReadFile(filepath.Join(args.home, "pallas.pk"))
	if err != nil {
		return fmt.Errorf("read pallas.pk: %w", err)
	}

	fmt.Printf("Building MsgCreateValidatorWithPallasKey:\n")
	fmt.Printf("  moniker:    %s\n", args.moniker)
	fmt.Printf("  validator:  %s\n", valOperAddr)
	fmt.Printf("  amount:     %s\n", coin)
	fmt.Printf("  pallas_pk:  %s\n", base64.StdEncoding.EncodeToString(pallasPkBytes))

	// Build the unsigned Cosmos SDK tx JSON.
	unsignedTx := buildUnsignedTx(stakingMsgBytes, pallasPkBytes)
	unsignedJSON, err := json.MarshalIndent(unsignedTx, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal unsigned tx: %w", err)
	}

	// Write to temp file.
	ts := time.Now().UnixNano()
	tmpDir := os.TempDir()
	unsignedPath := filepath.Join(tmpDir, fmt.Sprintf("zally_unsigned_%d.json", ts))
	signedPath := filepath.Join(tmpDir, fmt.Sprintf("zally_signed_%d.json", ts))

	if err := os.WriteFile(unsignedPath, unsignedJSON, 0600); err != nil {
		return fmt.Errorf("write unsigned tx: %w", err)
	}
	defer os.Remove(unsignedPath) //nolint:errcheck

	// Sign via zallyd tx sign.
	fmt.Printf("Signing with key %q from %s ...\n", args.keyName, args.home)
	signCmd := exec.Command("zallyd",
		"tx", "sign", unsignedPath,
		"--from", args.keyName,
		"--keyring-backend", "test",
		"--chain-id", args.chainID,
		"--home", args.home,
		"--node", args.rpcURL,
		"--output-document", signedPath,
		"--yes",
	)
	signCmd.Stdout = os.Stdout
	signCmd.Stderr = os.Stderr
	if err := signCmd.Run(); err != nil {
		return fmt.Errorf("zallyd tx sign failed: %w", err)
	}
	defer os.Remove(signedPath) //nolint:errcheck

	// Broadcast via zallyd tx broadcast.
	fmt.Printf("Broadcasting to %s ...\n", args.rpcURL)
	broadcastCmd := exec.Command("zallyd",
		"tx", "broadcast", signedPath,
		"--node", args.rpcURL,
		"--output", "json",
	)
	broadcastOut, err := broadcastCmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return fmt.Errorf("zallyd tx broadcast failed: %s", string(exitErr.Stderr))
		}
		return fmt.Errorf("zallyd tx broadcast failed: %w", err)
	}

	fmt.Printf("Broadcast result: %s\n", strings.TrimSpace(string(broadcastOut)))
	return nil
}

// buildUnsignedTx returns the standard Cosmos SDK unsigned tx JSON envelope
// for a MsgCreateValidatorWithPallasKey.
func buildUnsignedTx(stakingMsgBytes, pallasPkBytes []byte) map[string]interface{} {
	return map[string]interface{}{
		"body": map[string]interface{}{
			"messages": []map[string]interface{}{
				{
					"@type":       "/zvote.v1.MsgCreateValidatorWithPallasKey",
					"staking_msg": base64.StdEncoding.EncodeToString(stakingMsgBytes),
					"pallas_pk":   base64.StdEncoding.EncodeToString(pallasPkBytes),
				},
			},
			"memo":                            "",
			"timeout_height":                  "0",
			"extension_options":               []interface{}{},
			"non_critical_extension_options":  []interface{}{},
		},
		"auth_info": map[string]interface{}{
			"signer_infos": []interface{}{},
			"fee": map[string]interface{}{
				"amount":    []interface{}{},
				"gas_limit": "200000",
				"payer":     "",
				"granter":   "",
			},
		},
		"signatures": []interface{}{},
	}
}

type cliArgs struct {
	home    string
	moniker string
	amount  string
	// apiURL is accepted but ignored (kept for backward compatibility).
	apiURL  string
	rpcURL  string
	chainID string
	keyName string
}

func parseArgs() cliArgs {
	args := cliArgs{
		amount:  "10000000stake",
		rpcURL:  "tcp://localhost:26157",
		chainID: "zvote-1",
		keyName: "validator",
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
		case "--rpc-url":
			i++
			args.rpcURL = os.Args[i]
		case "--chain-id":
			i++
			args.chainID = os.Args[i]
		case "--key-name":
			i++
			args.keyName = os.Args[i]
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
