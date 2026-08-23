package sync_test

import (
	"context"
	"testing"
	"time"

	notesync "github.com/jiangfire/snapnotes/internal/sync"
)

// TestIngestWritesPulledNotesToLocalStore is the core P2.0 acceptance test. A
// first device (A) creates notes and pushes them to the server; an independent
// second device (B) with its own store and the same trust anchor syncs the
// chain. After B's Sync, the notes A created must be present in B's local notes
// table with the correct decrypted body, the author public key, and a "synced"
// status — proving multi-device notes become visible in the local UI.
func TestIngestWritesPulledNotesToLocalStore(t *testing.T) {
	gen := mustGenesis(t)
	_, ts := startAPI(t, gen)

	// Device A: creates two notes and pushes them to the server.
	storeA := newStore(t)
	svcA := newService(t, storeA, gen)
	bodyA1 := "idea from device A #alpha @remind 2026-09-01T08:00:00+08:00"
	bodyA2 := "second note from device A #beta"
	idA1, err := svcA.Submit(bodyA1, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svcA.Submit(bodyA2, time.Now()); err != nil {
		t.Fatal(err)
	}
	flushOutbox(t, ts, gen, storeA)

	// Device B: a separate store (different device) syncing from the server.
	storeB := newStore(t)
	clientB := &notesync.SyncClient{
		Repo:      storeB,
		Endpoint:  ts.URL,
		Anchor:    anchorFor(gen),
		StreamKey: gen.StreamKey,
		KeyEpoch:  0,
	}
	if err := clientB.Sync(context.Background()); err != nil {
		t.Fatalf("device B Sync error: %v", err)
	}

	// The note A created must now exist in B's local store, decrypted.
	got, err := storeB.GetNote(idA1)
	if err != nil {
		t.Fatalf("device B missing pulled note %s: %v", idA1, err)
	}
	if got.Body != bodyA1 {
		t.Fatalf("device B note body = %q, want %q", got.Body, bodyA1)
	}
	if !bytesEqual(got.AuthorPublicKey, gen.OwnerSigningPublicKey) {
		t.Fatalf("device B note author key = %x, want owner key %x", got.AuthorPublicKey, gen.OwnerSigningPublicKey)
	}

	// The companion note must also be present.
	notes, err := storeB.ListNotes()
	if err != nil {
		t.Fatal(err)
	}
	if len(notes) != 2 {
		t.Fatalf("device B listed %d notes, want 2", len(notes))
	}

	// Notes that arrived on the verified chain are "synced", never "pending".
	status, err := storeB.NoteSyncStatus(idA1)
	if err != nil {
		t.Fatal(err)
	}
	if status != "synced" {
		t.Fatalf("pulled note status = %q, want synced", status)
	}
}

// TestIngestIsIdempotentOnLoopback verifies the note-ID alignment decision: a
// device's own create reappears via chain loopback with the SAME local id
// (hex(note_id)) so SaveSyncedNote's INSERT OR IGNORE does not duplicate it.
// After a second Sync the local note count must be unchanged.
func TestIngestIsIdempotentOnLoopback(t *testing.T) {
	gen := mustGenesis(t)
	_, ts := startAPI(t, gen)

	store := newStore(t)
	svc := newService(t, store, gen)
	id, err := svc.Submit("loopback note #self", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	flushOutbox(t, ts, gen, store)

	client := &notesync.SyncClient{
		Repo:      store,
		Endpoint:  ts.URL,
		Anchor:    anchorFor(gen),
		StreamKey: gen.StreamKey,
		KeyEpoch:  0,
	}
	if err := client.Sync(context.Background()); err != nil {
		t.Fatalf("first Sync error: %v", err)
	}
	afterFirst, err := store.ListNotes()
	if err != nil {
		t.Fatal(err)
	}
	if len(afterFirst) != 1 {
		t.Fatalf("after first Sync listed %d notes, want 1", len(afterFirst))
	}

	// The looped-back note must reuse the existing local id (no second row).
	reloaded, err := store.GetNote(id)
	if err != nil {
		t.Fatalf("reload loopback note: %v", err)
	}
	if reloaded.Body != "loopback note #self" {
		t.Fatalf("loopback note body = %q", reloaded.Body)
	}

	// A second Sync must not create a duplicate.
	if err := client.Sync(context.Background()); err != nil {
		t.Fatalf("second Sync error: %v", err)
	}
	afterSecond, err := store.ListNotes()
	if err != nil {
		t.Fatal(err)
	}
	if len(afterSecond) != 1 {
		t.Fatalf("after second Sync listed %d notes, want 1 (no duplicate on loopback)", len(afterSecond))
	}
}

// TestIngestSkipsNotesWithoutStreamKey verifies the safe-degradation path: a
// client that has not been given the epoch Stream Key does not error and leaves
// the pulled note encrypted (absent from the local notes table) rather than
// aborting the whole sync.
func TestIngestSkipsNotesWithoutStreamKey(t *testing.T) {
	gen := mustGenesis(t)
	_, ts := startAPI(t, gen)

	storeA := newStore(t)
	svcA := newService(t, storeA, gen)
	idA, err := svcA.Submit("protected note #secret", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	flushOutbox(t, ts, gen, storeA)

	storeB := newStore(t)
	// No StreamKey supplied — must not panic / must not surface the note.
	clientB := &notesync.SyncClient{
		Repo:     storeB,
		Endpoint: ts.URL,
		Anchor:   anchorFor(gen),
	}
	if err := clientB.Sync(context.Background()); err != nil {
		t.Fatalf("Sync without key error: %v", err)
	}
	if _, err := storeB.GetNote(idA); err == nil {
		t.Fatal("note should not be ingested without the epoch Stream Key")
	}
}
