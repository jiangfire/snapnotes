package domain

import (
	"testing"
	"time"
)

func TestCreateNoteAssignsIDAndCreationTime(t *testing.T) {
	createdAt := time.Date(2026, 8, 20, 12, 34, 56, 0, time.UTC)
	note, err := CreateNote("capture an idea", createdAt, func() string {
		return "note-1"
	})
	if err != nil {
		t.Fatalf("CreateNote returned error: %v", err)
	}

	if note.ID != "note-1" {
		t.Fatalf("note ID = %q, want %q", note.ID, "note-1")
	}
	if note.Body != "capture an idea" {
		t.Fatalf("note body = %q, want %q", note.Body, "capture an idea")
	}
	if !note.CreatedAt.Equal(createdAt) {
		t.Fatalf("note creation time = %s, want %s", note.CreatedAt, createdAt)
	}
}
