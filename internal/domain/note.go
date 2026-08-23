package domain

import (
	"errors"
	"time"
)

type Note struct {
	ID        string
	Body      string
	CreatedAt time.Time
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
