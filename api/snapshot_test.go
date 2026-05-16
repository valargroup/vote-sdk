package api

import (
	"context"
	"errors"
	"testing"
)

func TestResolvePIRServiceURLPrefersVotingConfigResolver(t *testing.T) {
	cfg := SnapshotConfig{
		PIRServiceURLResolver: func(context.Context) (string, error) {
			return " https://stage.pir.valargroup.org ", nil
		},
	}

	got, err := resolvePIRServiceURL(context.Background(), cfg)
	if err != nil {
		t.Fatalf("resolvePIRServiceURL returned error: %v", err)
	}
	if got != "https://stage.pir.valargroup.org" {
		t.Fatalf("resolved PIR URL = %q, want stage endpoint", got)
	}
}

func TestResolvePIRServiceURLRejectsEmptyResolverResult(t *testing.T) {
	cfg := SnapshotConfig{
		PIRServiceURLResolver: func(context.Context) (string, error) {
			return " ", nil
		},
	}

	_, err := resolvePIRServiceURL(context.Background(), cfg)
	if err == nil {
		t.Fatalf("resolvePIRServiceURL returned nil error for empty resolver result")
	}
}

func TestResolvePIRServiceURLReturnsResolverError(t *testing.T) {
	want := errors.New("config unavailable")
	cfg := SnapshotConfig{
		PIRServiceURLResolver: func(context.Context) (string, error) {
			return "", want
		},
	}

	_, err := resolvePIRServiceURL(context.Background(), cfg)
	if !errors.Is(err, want) {
		t.Fatalf("resolvePIRServiceURL error = %v, want %v", err, want)
	}
}

func TestResolvePIRServiceURLRequiresResolver(t *testing.T) {
	_, err := resolvePIRServiceURL(context.Background(), SnapshotConfig{})
	if err == nil {
		t.Fatalf("resolvePIRServiceURL returned nil error without resolver")
	}
}
