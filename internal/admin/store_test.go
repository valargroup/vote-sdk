package admin

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
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
		RequestedAt:     now,
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

	// Upsert same operator — requested_at is preserved, metadata updates.
	r2 := r
	r2.RequestedAt = now + 10
	r2.Moniker = "val1-renamed"
	r2.URL = "https://v2.example.com"
	if err := s.UpsertPendingRegistration(r2); err != nil {
		t.Fatal(err)
	}
	list2, _ := s.ListPendingRegistrations()
	if len(list2) != 1 {
		t.Fatalf("want 1 after upsert, got %d", len(list2))
	}
	if list2[0].RequestedAt != now {
		t.Fatalf("requested_at should be preserved, got %d want %d", list2[0].RequestedAt, now)
	}
	if list2[0].Moniker != "val1-renamed" {
		t.Fatalf("moniker not updated: %q", list2[0].Moniker)
	}
	if list2[0].URL != "https://v2.example.com" {
		t.Fatalf("url not updated: %q", list2[0].URL)
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
		RequestedAt:     past,
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

func TestStoreDropsLegacyPendingSchema(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "legacy.db")

	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`
		CREATE TABLE pending_registrations (
			operator_address TEXT PRIMARY KEY,
			url              TEXT NOT NULL,
			moniker          TEXT NOT NULL,
			timestamp        INTEGER NOT NULL,
			signature        TEXT NOT NULL,
			pub_key          TEXT NOT NULL,
			first_seen_at    INTEGER NOT NULL,
			last_seen_at     INTEGER NOT NULL,
			expires_at       INTEGER NOT NULL
		);
		INSERT INTO pending_registrations (
			operator_address, url, moniker, timestamp, signature, pub_key,
			first_seen_at, last_seen_at, expires_at
		) VALUES ('sv1old', 'https://old.example', 'old', 1, 'sig', 'pk', 1, 1, 9999999999);
	`); err != nil {
		t.Fatal(err)
	}
	raw.Close()

	s, err := NewStore(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	list, err := s.ListPendingRegistrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("legacy pending rows should be dropped, got %+v", list)
	}

	now := time.Now().Unix()
	if err := s.UpsertPendingRegistration(PendingRegistration{
		OperatorAddress: "sv1new",
		URL:             "",
		Moniker:         "new",
		RequestedAt:     now,
		ExpiresAt:       now + 3600,
	}); err != nil {
		t.Fatal(err)
	}
}
