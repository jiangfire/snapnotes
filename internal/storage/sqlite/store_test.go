package sqlite

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/jiangfire/snapnotes/internal/domain"
	"github.com/jiangfire/snapnotes/internal/reminder"
)

func TestStorePersistsNoteAcrossReopen(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "notes.db")
	createdAt := time.Date(2026, 8, 20, 12, 34, 56, 0, time.UTC)
	want := domain.Note{ID: "note-1", Body: "persist this note", CreatedAt: createdAt}

	store, err := Open(databasePath)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	if err := store.SaveNote(want); err != nil {
		t.Fatalf("SaveNote returned error: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	store, err = Open(databasePath)
	if err != nil {
		t.Fatalf("reopen returned error: %v", err)
	}
	defer store.Close()

	got, err := store.GetNote(want.ID)
	if err != nil {
		t.Fatalf("GetNote returned error: %v", err)
	}
	if got.ID != want.ID || got.Body != want.Body || !got.CreatedAt.Equal(want.CreatedAt) {
		t.Fatalf("reloaded note = %+v, want %+v", got, want)
	}
}

func TestStoreListsNotesNewestFirst(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "notes.db"))
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer store.Close()

	older := domain.Note{ID: "older", Body: "older note", CreatedAt: time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)}
	newer := domain.Note{ID: "newer", Body: "newer note", CreatedAt: time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)}
	if err := store.SaveNote(older); err != nil {
		t.Fatalf("SaveNote older returned error: %v", err)
	}
	if err := store.SaveNote(newer); err != nil {
		t.Fatalf("SaveNote newer returned error: %v", err)
	}

	notes, err := store.ListNotes()
	if err != nil {
		t.Fatalf("ListNotes returned error: %v", err)
	}
	if len(notes) != 2 || notes[0].ID != newer.ID || notes[1].ID != older.ID {
		t.Fatalf("listed notes = %+v, want newer then older", notes)
	}
}

func TestStoreSearchesBodyAndTagsWithCursorPagination(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "notes.db"))
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	defer store.Close()

	base := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 105; i++ {
		body := fmt.Sprintf("searchable note %03d", i)
		if i%2 == 0 {
			body += " #idea"
		}
		note := domain.Note{ID: fmt.Sprintf("note-%03d", i), Body: body, CreatedAt: base.Add(time.Duration(i) * time.Minute)}
		if err := store.SaveNote(note); err != nil {
			t.Fatalf("SaveNote(%d) returned error: %v", i, err)
		}
	}

	firstPage, err := store.SearchNotes("searchable", 100, "")
	if err != nil {
		t.Fatalf("SearchNotes first page returned error: %v", err)
	}
	if len(firstPage.Notes) != 100 {
		t.Fatalf("first page note count = %d, want 100", len(firstPage.Notes))
	}
	if firstPage.NextCursor == "" {
		t.Fatal("first page cursor is empty, want another page")
	}

	secondPage, err := store.SearchNotes("searchable", 100, firstPage.NextCursor)
	if err != nil {
		t.Fatalf("SearchNotes second page returned error: %v", err)
	}
	if len(secondPage.Notes) != 5 {
		t.Fatalf("second page note count = %d, want 5", len(secondPage.Notes))
	}
	if secondPage.NextCursor != "" {
		t.Fatalf("second page cursor = %q, want empty", secondPage.NextCursor)
	}

	tagPage, err := store.SearchNotes("tag:idea", 10, "")
	if err != nil {
		t.Fatalf("SearchNotes tag query returned error: %v", err)
	}
	if len(tagPage.Notes) != 10 {
		t.Fatalf("tag page note count = %d, want 10", len(tagPage.Notes))
	}
	if _, err := store.SearchNotes("tag:", 10, ""); err == nil {
		t.Fatal("empty tag query returned nil error")
	}
}

func TestStorePersistsReminderScheduleAcrossReopen(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "notes.db")
	note := domain.Note{
		ID:        "note-reminder",
		Body:      "review this\n@remind 2026-08-20T09:00:00+08:00\n@repeat daily",
		CreatedAt: time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC),
	}

	store, err := Open(databasePath)
	if err != nil {
		t.Fatalf("Open returned error: %v", err)
	}
	if err := store.SaveNote(note); err != nil {
		t.Fatalf("SaveNote returned error: %v", err)
	}
	schedule, found, err := store.GetReminder(note.ID)
	if err != nil || !found {
		t.Fatalf("GetReminder before reopen = (%v, %t, %v), want reminder", schedule, found, err)
	}
	acknowledged, err := schedule.Acknowledge(time.Date(2026, 8, 20, 10, 0, 0, 0, time.FixedZone("CST", 8*60*60)))
	if err != nil {
		t.Fatalf("Acknowledge returned error: %v", err)
	}
	if err := store.SaveReminder(note.ID, acknowledged); err != nil {
		t.Fatalf("SaveReminder returned error: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	store, err = Open(databasePath)
	if err != nil {
		t.Fatalf("reopen returned error: %v", err)
	}
	defer store.Close()

	reloaded, found, err := store.GetReminder(note.ID)
	if err != nil || !found {
		t.Fatalf("GetReminder after reopen = (%v, %t, %v), want reminder", reloaded, found, err)
	}
	if !reloaded.NextFireAt.Equal(time.Date(2026, 8, 21, 1, 0, 0, 0, time.UTC)) {
		t.Fatalf("reloaded next fire = %s, want UTC 2026-08-21 01:00", reloaded.NextFireAt)
	}
	if reloaded.StatusAt(time.Date(2026, 8, 20, 11, 0, 0, 0, time.UTC)) != reminder.StatusActive {
		t.Fatalf("reloaded status = %q, want active", reloaded.StatusAt(time.Date(2026, 8, 20, 11, 0, 0, 0, time.UTC)))
	}
}
