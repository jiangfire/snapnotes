package sync

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"io"
	"time"

	"github.com/jiangfire/snapnotes/internal/domain"
	"github.com/jiangfire/snapnotes/internal/storage/sqlite"
)

// ClientKeys are the local device credentials for one stream.
type ClientKeys struct {
	StreamID        []byte
	StreamKey       []byte // key_epoch Stream Key used to wrap the DEK
	KeyEpoch        uint64
	AuthorPublicKey ed25519.PublicKey
	SigningKey      ed25519.PrivateKey
	PowTarget       []byte
	PowEpoch        uint64
}

// NoteStore is the persistence surface the NoteService needs.
type NoteStore interface {
	SaveNoteWithOutbox(note domain.Note, item sqlite.OutboxItem) error
}

// NoteService creates local notes and enqueues their encrypted transactions. It
// deliberately performs no network I/O, so a stopped server never blocks a
// local create (acceptance criterion for Task 3.2).
type NoteService struct {
	Store    NoteStore
	Keys     ClientKeys
	Random   io.Reader
	NextOpID func() string
}

// Submit saves a note locally and enqueues its outbound transaction in one local
// transaction. It returns the local note id (aligned with the chain note_id so a
// loopback re-ingest reuses the same row); the actual upload happens later via
// SyncClient.
func (s *NoteService) Submit(body string, createdAt time.Time) (string, error) {
	random := s.Random
	if random == nil {
		random = rand.Reader
	}

	tx, err := BuildCreate(CreateParams{
		StreamID:        s.Keys.StreamID,
		StreamKey:       s.Keys.StreamKey,
		KeyEpoch:        s.Keys.KeyEpoch,
		AuthorPublicKey: s.Keys.AuthorPublicKey,
		SigningKey:      s.Keys.SigningKey,
		Body:            body,
		ClientCreatedAt: createdAt,
		Randomness:      random,
		PowTarget:       s.Keys.PowTarget,
		PowEpoch:        s.Keys.PowEpoch,
	})
	if err != nil {
		return "", err
	}

	// Align the local note id with the chain's note_id so that a device's own
	// create reappearing via chain loopback reuses the same row (SaveSyncedNote
	// is INSERT OR IGNORE) instead of duplicating it. The outbox EntityID already
	// uses hex(tx.NoteID); keeping them equal makes the two paths converge.
	localID := hex.EncodeToString(tx.NoteID)
	note, err := domain.CreateNote(body, createdAt, func() string { return localID })
	if err != nil {
		return "", err
	}
	encoded, err := tx.Encode()
	if err != nil {
		return "", err
	}

	opID := s.NextOpID
	if opID == nil {
		opID = defaultOpID
	}
	item := sqlite.OutboxItem{
		OperationID:   opID(),
		StreamID:      tx.StreamID,
		EntityID:      hex.EncodeToString(tx.NoteID),
		TransactionID: tx.TransactionID,
		OperationType: "create",
		Payload:       encoded,
		CreatedAt:     createdAt,
		SyncStatus:    sqlite.OutboxPending,
	}
	if err := s.Store.SaveNoteWithOutbox(note, item); err != nil {
		return "", err
	}
	return note.ID, nil
}

func defaultLocalNoteID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func defaultOpID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return "op-" + hex.EncodeToString(b[:])
}
