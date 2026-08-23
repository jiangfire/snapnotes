package domain

import (
	"errors"
	"time"
)

type Note struct {
	ID             string
	Body           string
	CreatedAt      time.Time
	AuthorPublicKey []byte // epoch Stream Key holder who authored the note; nil for legacy local rows
}

// SearchResult is a page of notes plus the cursor for the next page.
type SearchResult struct {
	Notes      []Note
	NextCursor string
}

// ChainTip is the verified active chain position a client pinned during sync.
// It is what the audit page surfaces (genesis hash, last block, mmr root, work).
type ChainTip struct {
	GenesisBlockHash []byte
	LastBlockHeight  uint64
	LastBlockHash    []byte
	LastMMRRoot      []byte
	LastChainwork    []byte
}

func CreateNote(body string, createdAt time.Time, nextID func() string) (Note, error) {
	if body == "" {
		return Note{}, errors.New("note body cannot be empty")
	}

	return Note{
		ID:        nextID(),
		Body:      body,
		CreatedAt: createdAt,
	}, nil
}
