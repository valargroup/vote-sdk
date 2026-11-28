package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/valargroup/vote-sdk/internal/votingconfig"
)

// newWalletListCmd builds `wallet-list`, the tooling for a wallet's signed
// list of curated delegates (proxy delegation contracts-v1 §12.3).
func newWalletListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wallet-list",
		Short: "Build, sign and verify a wallet's signed delegate list",
	}
	cmd.AddCommand(newWalletListPayloadCmd(), newWalletListSignCmd(), newWalletListVerifyCmd())
	return cmd
}

// readWalletListDraft reads a list file as tooling does before signing (see
// votingconfig.ParseWalletList) and returns it, the signatures it carries
// and its WalletListPayloadV1.
func readWalletListDraft(path string) (votingconfig.WalletList, []votingconfig.Signature, []byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return votingconfig.WalletList{}, nil, nil, fmt.Errorf("read wallet list: %w", err)
	}
	list, sigs, payload, err := votingconfig.ParseWalletList(raw)
	if err != nil {
		return list, nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	return list, sigs, payload, nil
}

// newWalletListPayloadCmd builds `wallet-list payload`, which prints the
// canonical payload a signer signs, for signers outside this CLI.
func newWalletListPayloadCmd() *cobra.Command {
	var listPath string
	cmd := &cobra.Command{
		Use:   "payload",
		Short: "Print a wallet list's WalletListPayloadV1 as base64",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if listPath == "" {
				return errors.New("--list is required")
			}
			_, _, payload, err := readWalletListDraft(listPath)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), base64.StdEncoding.EncodeToString(payload))
			hash := sha256.Sum256(payload)
			fmt.Fprintf(cmd.ErrOrStderr(), "signed_payload_hash: %s\n", hex.EncodeToString(hash[:]))
			return nil
		},
	}
	cmd.Flags().StringVar(&listPath, "list", "", "The wallet list file, signed or not")
	return cmd
}

// newWalletListSignCmd builds `wallet-list sign`, which signs a list with a
// key file, keeping signatures by other key ids.
func newWalletListSignCmd() *cobra.Command {
	var (
		listPath  string
		signerID  string
		privFile  string
		privStdin bool
		outPath   string
	)
	cmd := &cobra.Command{
		Use:   "sign",
		Short: "Sign a wallet list and write the signed document",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if listPath == "" || signerID == "" {
				return errors.New("--list and --signer-id are required")
			}
			if len(signerID) > 64 {
				return errors.New("--signer-id must be at most 64 bytes")
			}
			list, sigs, payload, err := readWalletListDraft(listPath)
			if err != nil {
				return err
			}
			priv, err := loadPrivateKey(privFile, privStdin, os.Stdin)
			if err != nil {
				return err
			}
			// Signatures cover only the payload, so the others stay valid.
			sigs = mergeSignatures(sigs, votingconfig.Signature{
				KeyID: signerID,
				Alg:   votingconfig.AlgEd25519,
				Sig:   base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload)),
			})
			doc, err := list.MarshalDocument(sigs)
			if err != nil {
				return err
			}
			// Write only a document that tooling and wallets read back.
			if _, _, _, err := votingconfig.ParseWalletList(doc); err != nil {
				return fmt.Errorf("the signed list would not parse: %w", err)
			}
			if outPath != "" {
				if err := os.WriteFile(outPath, doc, 0o644); err != nil {
					return err
				}
			} else if _, err := cmd.OutOrStdout().Write(doc); err != nil {
				return err
			}
			hash := sha256.Sum256(payload)
			fmt.Fprintf(cmd.ErrOrStderr(), "signed_payload_hash: %s\n", hex.EncodeToString(hash[:]))
			return nil
		},
	}
	cmd.Flags().StringVar(&listPath, "list", "", "The wallet list file, signed or not")
	cmd.Flags().StringVar(&signerID, "signer-id", "", "The key_id the wallet pins for this key")
	cmd.Flags().StringVar(&privFile, "privkey-file", "", "Read base64(seed) Ed25519 private key from this file")
	cmd.Flags().BoolVar(&privStdin, "privkey-stdin", false, "Read base64(seed) Ed25519 private key from stdin")
	cmd.Flags().StringVar(&outPath, "out", "", "Write the signed list here instead of stdout (may be --list)")
	return cmd
}

// walletListFinding is one reason a listed delegate needs a re-review.
type walletListFinding struct {
	Index  uint32 `json:"index"`
	Handle string `json:"handle"`
	// Kind is not_registered, suspended, retired, provider_mismatch,
	// handle_mismatch or key_mismatch.
	Kind   string `json:"kind"`
	Detail string `json:"detail"`
}

// walletListResult is `wallet-list verify --json`'s output.
type walletListResult struct {
	OK        bool                `json:"ok"`
	WalletID  string              `json:"wallet_id"`
	ChainID   string              `json:"chain_id"`
	Version   uint64              `json:"version,omitempty"`
	SignedBy  string              `json:"signed_by,omitempty"`
	Delegates int                 `json:"delegates"`
	Error     string              `json:"error,omitempty"`
	Findings  []walletListFinding `json:"findings,omitempty"`
}

// newWalletListVerifyCmd builds `wallet-list verify`, config CI's check of a
// list: a wallet's verification under the pinned keys, the picture size, the
// version rule against the list it replaces and, optionally, each row
// against the chain.
func newWalletListVerifyCmd() *cobra.Command {
	var (
		listPath     string
		walletID     string
		chainID      string
		allowedKeys  []string
		previousPath string
		chainURL     string
		jsonOut      bool
	)
	cmd := &cobra.Command{
		Use:   "verify",
		Short: "Verify a signed wallet list",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if listPath == "" || walletID == "" || chainID == "" || len(allowedKeys) == 0 {
				return errors.New("--list, --wallet-id, --chain-id and at least one --allowed-key are required")
			}
			result := walletListResult{WalletID: walletID, ChainID: chainID}
			err := verifyWalletListFile(cmd.Context(), &result, listPath, allowedKeys, previousPath, chainURL)
			if err != nil {
				result.Error = err.Error()
			} else if len(result.Findings) > 0 {
				err = fmt.Errorf("%d listed delegate(s) need a re-review", len(result.Findings))
			}
			result.OK = err == nil
			out := cmd.OutOrStdout()
			if jsonOut {
				data, merr := json.MarshalIndent(result, "", "  ")
				if merr != nil {
					return merr
				}
				if _, werr := out.Write(append(data, '\n')); werr != nil {
					return werr
				}
				return err
			}
			for _, f := range result.Findings {
				fmt.Fprintf(cmd.ErrOrStderr(), "delegate %d (@%s): %s: %s\n", f.Index, f.Handle, f.Kind, f.Detail)
			}
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "OK: %s list version %d for %s, %d delegates, signed by %s\n",
				walletID, result.Version, chainID, result.Delegates, result.SignedBy)
			return nil
		},
	}
	cmd.Flags().StringVar(&listPath, "list", "", "The signed wallet list file")
	cmd.Flags().StringVar(&walletID, "wallet-id", "", "The wallet_id the list must carry (its directory name in the config repo)")
	cmd.Flags().StringVar(&chainID, "chain-id", "", "The chain_id the list must carry")
	cmd.Flags().StringArrayVar(&allowedKeys, "allowed-key", nil, "A key the wallet pins, as key_id=<base64 Ed25519 public key>; repeatable")
	cmd.Flags().StringVar(&previousPath, "previous", "", "The list this one replaces (e.g. the PR base's); a changed list must raise its version unless the previous one does not parse")
	cmd.Flags().StringVar(&chainURL, "chain-check", "", "Chain REST endpoint: check each row's registry entry and report delegates that need a re-review")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Write a machine-readable result, also on failure")
	return cmd
}

// verifyWalletListFile runs every verify check on the list at path and fills
// result. Chain findings go to result.Findings; any other failure is the
// returned error.
func verifyWalletListFile(ctx context.Context, result *walletListResult, path string, allowedKeys []string, previousPath, chainURL string) error {
	pinned, err := parseAllowedKeys(allowedKeys)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read wallet list: %w", err)
	}
	verified, err := votingconfig.VerifyWalletList(raw, result.WalletID, result.ChainID, pinned)
	if err != nil {
		return err
	}
	list := verified.List
	result.Version, result.SignedBy, result.Delegates = list.Version, verified.SignedBy, len(list.Delegates)
	if err := votingconfig.CheckWalletListPictures(list); err != nil {
		return err
	}
	if previousPath != "" {
		previous, err := os.ReadFile(previousPath)
		if err != nil {
			return fmt.Errorf("read previous wallet list: %w", err)
		}
		if err := votingconfig.CheckWalletListReplaces(previous, list); err != nil {
			return err
		}
	}
	if chainURL == "" {
		return nil
	}
	chain := newChainClient(chainURL)
	if err := chain.requireChainID(ctx, result.ChainID); err != nil {
		return err
	}
	for _, row := range list.Delegates {
		findings, err := checkListedDelegate(ctx, chain, row)
		if err != nil {
			return fmt.Errorf("delegate %d: %w", row.Index, err)
		}
		result.Findings = append(result.Findings, findings...)
	}
	return nil
}

// parseAllowedKeys parses --allowed-key values, key_id=<base64 public key>.
func parseAllowedKeys(values []string) ([]votingconfig.TrustedKey, error) {
	keys := make([]votingconfig.TrustedKey, 0, len(values))
	for _, value := range values {
		keyID, pub, ok := strings.Cut(value, "=")
		if !ok || keyID == "" {
			return nil, fmt.Errorf("--allowed-key %q: want key_id=<base64 public key>", value)
		}
		if _, err := votingconfig.DecodeBase64Fixed(pub, ed25519.PublicKeySize, "--allowed-key "+keyID); err != nil {
			return nil, err
		}
		keys = append(keys, votingconfig.TrustedKey{KeyID: keyID, Alg: votingconfig.AlgEd25519, Pubkey: pub})
	}
	return keys, nil
}

// checkListedDelegate compares one row with its registry entry and returns
// what a re-review should look at (contracts-v1 §12.3, "Re-review alert").
// A wallet shows the row as reviewed only while the entry is ACTIVE with the
// row's provider, handle and key, so every other case is a finding.
func checkListedDelegate(ctx context.Context, chain *chainClient, row votingconfig.WalletListRow) ([]walletListFinding, error) {
	var findings []walletListFinding
	add := func(kind, format string, args ...any) {
		findings = append(findings, walletListFinding{Index: row.Index, Handle: row.Handle, Kind: kind, Detail: fmt.Sprintf(format, args...)})
	}
	delegate, err := chain.delegate(ctx, row.Index)
	if err != nil {
		return nil, err
	}
	if delegate == nil {
		add("not_registered", "no registry entry has index %d", row.Index)
		return findings, nil
	}
	switch delegate.Status {
	case 0:
	case 1:
		add("suspended", "the entry is suspended, reason %d", delegate.StatusReason)
	default:
		add("retired", "the entry is retired")
	}
	if delegate.Provider != row.Provider {
		add("provider_mismatch", "the entry's provider is %d, the list's %d", delegate.Provider, row.Provider)
	}
	if delegate.Handle != row.Handle {
		add("handle_mismatch", "the entry's handle is %q", delegate.Handle)
	}
	if len(delegate.DK) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("the entry's key is %d bytes", len(delegate.DK))
	}
	if key := votingconfig.EncodeDelegateKey([32]byte(delegate.DK)); key != row.DK {
		add("key_mismatch", "the entry's key is %s, the list's %s", key, row.DK)
	}
	return findings, nil
}

// chainClient reads delegate registry entries from a vote chain's REST API.
type chainClient struct {
	baseURL string
	http    *http.Client
}

// newChainClient returns a client for the chain REST API at baseURL.
func newChainClient(baseURL string) *chainClient {
	return &chainClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 20 * time.Second},
	}
}

// requireChainID fails unless the endpoint's node reports chain id want, so
// a "not registered" answer comes from the intended chain.
func (c *chainClient) requireChainID(ctx context.Context, want string) error {
	status, body, err := c.get(ctx, "/cosmos/base/tendermint/v1beta1/node_info")
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("chain node_info: HTTP %d", status)
	}
	var info struct {
		DefaultNodeInfo struct {
			Network string `json:"network"`
		} `json:"default_node_info"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return fmt.Errorf("chain node_info: %w", err)
	}
	if got := info.DefaultNodeInfo.Network; got != want {
		return fmt.Errorf("the endpoint serves chain %q, not %q", got, want)
	}
	return nil
}

// chainDelegate is the part of a delegate's registry entry (contracts-v1
// §3.1) the chain check reads.
type chainDelegate struct {
	DK           []byte `json:"dk"`
	Provider     uint32 `json:"provider"`
	Handle       string `json:"handle"`
	Status       uint32 `json:"status"`
	StatusReason uint32 `json:"status_reason"`
}

// delegate returns a delegate's registry entry, or nil when the chain
// answers that the index is not registered. Any other failure, including a
// 404 from something other than the chain's delegate query, is an error.
func (c *chainClient) delegate(ctx context.Context, index uint32) (*chainDelegate, error) {
	status, body, err := c.get(ctx, fmt.Sprintf("/shielded-vote/v1/delegates/%d", index))
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound && bytes.Contains(body, fmt.Appendf(nil, "delegate %d not found", index)) {
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("chain delegate query: HTTP %d: %s", status, bytes.TrimSpace(body))
	}
	var resp struct {
		Delegate *chainDelegate `json:"delegate"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("chain delegate query: %w", err)
	}
	if resp.Delegate == nil {
		return nil, errors.New("chain delegate query: the response has no delegate")
	}
	return resp.Delegate, nil
}

// get fetches path from the chain and returns its status and up to 1 MiB of
// its body.
func (c *chainClient) get(ctx context.Context, path string) (int, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return 0, nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, body, nil
}
