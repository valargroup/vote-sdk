package app

import (
	"errors"
	"sync"
	"testing"

	voteapi "github.com/valargroup/vote-sdk/api"
)

func TestCryptoWarmupStatusDefaultsToNotStarted(t *testing.T) {
	app := &SvoteApp{}

	status := app.CryptoWarmupStatus()
	if status.Status != voteapi.CryptoReadinessStatusNotStarted {
		t.Fatalf("expected not_started, got %q", status.Status)
	}
}

func TestCryptoWarmupStateTransitions(t *testing.T) {
	app := &SvoteApp{}

	if !app.markCryptoWarmupStarted() {
		t.Fatal("expected first warm-up start to succeed")
	}
	if app.markCryptoWarmupStarted() {
		t.Fatal("expected duplicate warm-up start to be ignored")
	}

	warming := app.CryptoWarmupStatus()
	if warming.Status != voteapi.CryptoReadinessStatusWarming {
		t.Fatalf("expected warming, got %q", warming.Status)
	}
	if warming.StartedAt == nil || warming.StartedAt.IsZero() {
		t.Fatal("expected started_at to be set")
	}

	app.markCryptoWarmupReady()
	ready := app.CryptoWarmupStatus()
	if ready.Status != voteapi.CryptoReadinessStatusReady {
		t.Fatalf("expected ready, got %q", ready.Status)
	}
	if ready.CompletedAt == nil || ready.CompletedAt.IsZero() {
		t.Fatal("expected completed_at to be set")
	}
	if ready.Error != "" {
		t.Fatalf("expected empty error, got %q", ready.Error)
	}
}

func TestCryptoWarmupFailureIsReported(t *testing.T) {
	app := &SvoteApp{}

	if !app.markCryptoWarmupStarted() {
		t.Fatal("expected warm-up start to succeed")
	}
	app.markCryptoWarmupFailed(errors.New("boom"))

	status := app.CryptoWarmupStatus()
	if status.Status != voteapi.CryptoReadinessStatusFailed {
		t.Fatalf("expected failed, got %q", status.Status)
	}
	if status.Error != "boom" {
		t.Fatalf("expected error boom, got %q", status.Error)
	}
}

func TestCryptoWarmupStatusConcurrentReads(t *testing.T) {
	app := &SvoteApp{}
	if !app.markCryptoWarmupStarted() {
		t.Fatal("expected warm-up start to succeed")
	}

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status := app.CryptoWarmupStatus()
			if status.Status != voteapi.CryptoReadinessStatusWarming {
				t.Errorf("expected warming, got %q", status.Status)
			}
		}()
	}
	wg.Wait()
}
