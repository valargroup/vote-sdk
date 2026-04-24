package admin

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStorePendingCRUD(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := NewStore(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	now := time.Now().Unix()
	r := PendingRegistration{
		OperatorAddress: "sv1abc",
		URL:             "https://v.example.com",
		Moniker:         "val1",
		Timestamp:       now,
		Signature:       "sig",
		PubKey:          "pk",
		FirstSeenAt:     now,
		LastSeenAt:      now,
		ExpiresAt:       now + 3600,
	}
	if err := s.UpsertPendingRegistration(r); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListPendingRegistrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("want 1 pending, got %d", len(list))
	}
	if list[0].Moniker != "val1" {
		t.Fatalf("moniker: %q", list[0].Moniker)
	}

	// Upsert same operator — first_seen preserved, last_seen updated.
	r2 := r
	r2.LastSeenAt = now + 10
	r2.Moniker = "val1-renamed"
	if err := s.UpsertPendingRegistration(r2); err != nil {
		t.Fatal(err)
	}
	list2, _ := s.ListPendingRegistrations()
	if len(list2) != 1 {
		t.Fatalf("want 1 after upsert, got %d", len(list2))
	}
	if list2[0].FirstSeenAt != now {
		t.Fatalf("first_seen_at should be preserved, got %d want %d", list2[0].FirstSeenAt, now)
	}
	if list2[0].Moniker != "val1-renamed" {
		t.Fatalf("moniker not updated: %q", list2[0].Moniker)
	}

	ok, err := s.RemovePendingRegistration("sv1abc")
	if err != nil || !ok {
		t.Fatalf("remove: ok=%v err=%v", ok, err)
	}
	list3, _ := s.ListPendingRegistrations()
	if len(list3) != 0 {
		t.Fatalf("want 0 after delete, got %d", len(list3))
	}
}

func TestStoreEvictExpiredPending(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s, err := NewStore(filepath.Join(dir, "evict.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	past := time.Now().Unix() - 100
	r := PendingRegistration{
		OperatorAddress: "sv1expired",
		URL:             "https://x.com",
		Moniker:         "gone",
		Timestamp:       past,
		Signature:       "s",
		PubKey:          "p",
		FirstSeenAt:     past,
		LastSeenAt:      past,
		ExpiresAt:       past + 1,
	}
	if err := s.UpsertPendingRegistration(r); err != nil {
		t.Fatal(err)
	}
	n, err := s.EvictExpiredPending()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 evicted, got %d", n)
	}
	list, _ := s.ListPendingRegistrations()
	if len(list) != 0 {
		t.Fatalf("want empty list, got %d", len(list))
	}
}
