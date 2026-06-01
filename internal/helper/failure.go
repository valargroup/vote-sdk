package helper

import (
	"errors"
	"fmt"
	"strconv"
)

const (
	failureStagePanic            = "panic"
	failureStageRoundStatusCheck = "round_status_check"
	failureStageProcessShare     = "process_share"
	failureStageDecodeRoundID    = "decode_round_id"
	failureStageDecodePayload    = "decode_payload"
	failureStageTreeStatus       = "tree_status"
	failureStageMerklePath       = "merkle_path"
	failureStageProofGenerate    = "proof_generate"
	failureStageSubmitHTTP       = "submit_http"
	failureStageSubmitChain      = "submit_chain_reject"
)

type shareProcessingError struct {
	stage     string
	retriable bool
	chainCode *uint32
	err       error
}

func (e *shareProcessingError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *shareProcessingError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func wrapShareProcessingError(stage string, retriable bool, err error) error {
	if err == nil {
		return nil
	}
	var existing *shareProcessingError
	if errors.As(err, &existing) {
		return err
	}
	return &shareProcessingError{
		stage:     stage,
		retriable: retriable,
		err:       err,
	}
}

func newChainRejectError(code uint32, log string) error {
	err := fmt.Errorf("chain rejected tx (code %d): %s", code, log)
	return &shareProcessingError{
		stage:     failureStageSubmitChain,
		retriable: false,
		chainCode: &code,
		err:       err,
	}
}

type failureMetadata struct {
	stage     string
	retriable bool
	chainCode *uint32
}

func failureMetadataFromError(err error, defaultStage string, defaultRetriable bool) failureMetadata {
	md := failureMetadata{
		stage:     defaultStage,
		retriable: defaultRetriable,
	}
	var structured *shareProcessingError
	if errors.As(err, &structured) {
		if structured.stage != "" {
			md.stage = structured.stage
		}
		md.retriable = structured.retriable
		md.chainCode = structured.chainCode
	}
	return md
}

func retryAwareCaptureDecision(retriable bool, attempt, maxAttempts int) bool {
	if attempt <= 0 || maxAttempts <= 0 {
		return true
	}
	if !retriable {
		return attempt >= maxAttempts
	}
	return attempt == 1 || attempt >= maxAttempts
}

func buildFailureTags(share QueuedShare, md failureMetadata, attempt, maxAttempts int) map[string]string {
	tags := map[string]string{
		"round_id":     share.Payload.VoteRoundID,
		"share_index":  strconv.FormatUint(uint64(share.Payload.EncShare.ShareIndex), 10),
		"stage":        md.stage,
		"retriable":    strconv.FormatBool(md.retriable),
		"attempt":      strconv.Itoa(attempt),
		"max_attempts": strconv.Itoa(maxAttempts),
	}
	if md.chainCode != nil {
		tags["chain_code"] = strconv.FormatUint(uint64(*md.chainCode), 10)
	}
	return tags
}
