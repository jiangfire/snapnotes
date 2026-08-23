package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/jiangfire/snapnotes/internal/domain"
	"github.com/jiangfire/snapnotes/internal/reminder"
)

type memoryNoteStore struct {
	notes     []domain.Note
	reminders map[string]reminder.Schedule
}

func (s *memoryNoteStore) SaveNote(note domain.Note) error {
	s.notes = append(s.notes, note)
	return nil
}

func (s *memoryNoteStore) ListNotes() ([]domain.Note, error) {
	return append([]domain.Note(nil), s.notes...), nil
}

func (s *memoryNoteStore) GetReminder(noteID string) (reminder.Schedule, bool, error) {
	schedule, ok := s.reminders[noteID]
	return schedule, ok, nil
}

func (s *memoryNoteStore) SaveReminder(noteID string, schedule reminder.Schedule) error {
	if s.reminders == nil {
		s.reminders = make(map[string]reminder.Schedule)
	}
	s.reminders[noteID] = schedule
	return nil
}

func TestHomeModelEnterCreatesOneNoteAndClearsInput(t *testing.T) {
	store := &memoryNoteStore{}
	createdAt := time.Date(2026, 8, 20, 12, 34, 56, 0, time.UTC)
	model, err := NewHomeModel(store, func() time.Time { return createdAt }, func() string { return "note-1" })
	if err != nil {
		t.Fatalf("NewHomeModel returned error: %v", err)
	}

	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Text: "capture an idea"}))
	model = updated.(*HomeModel)
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	model = updated.(*HomeModel)

	if len(store.notes) != 1 {
		t.Fatalf("saved note count = %d, want 1", len(store.notes))
	}
	if store.notes[0].Body != "capture an idea" {
		t.Fatalf("saved note body = %q, want %q", store.notes[0].Body, "capture an idea")
	}
	if model.input != "" {
		t.Fatalf("input after submit = %q, want empty", model.input)
	}
}

func TestNewHomeModelProvidesSafeDefaults(t *testing.T) {
	model, err := NewHomeModel(&memoryNoteStore{}, nil, nil)
	if err != nil {
		t.Fatalf("NewHomeModel returned error: %v", err)
	}

	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Text: "uses defaults"}))
	model = updated.(*HomeModel)
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	_ = updated.(*HomeModel)
}

func TestHomeModelViewShowsNewestNoteFirst(t *testing.T) {
	store := &memoryNoteStore{notes: []domain.Note{
		{ID: "older", Body: "older note", CreatedAt: time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)},
		{ID: "newer", Body: "newer note", CreatedAt: time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)},
	}}
	model, err := NewHomeModel(store, time.Now, func() string { return "unused" })
	if err != nil {
		t.Fatalf("NewHomeModel returned error: %v", err)
	}

	content := model.View().Content
	newerIndex := indexOf(content, "newer note")
	olderIndex := indexOf(content, "older note")
	if newerIndex < 0 || olderIndex < 0 {
		t.Fatalf("view = %q, want both notes", content)
	}
	if newerIndex >= olderIndex {
		t.Fatalf("newer note index = %d, older note index = %d; want newer first", newerIndex, olderIndex)
	}
}

func TestHomeModelViewShowsReminderStatus(t *testing.T) {
	location := time.FixedZone("CST", 8*60*60)
	schedule, err := reminder.NewSchedule(time.Date(2026, 8, 20, 1, 0, 0, 0, time.UTC), "", location)
	if err != nil {
		t.Fatalf("NewSchedule returned error: %v", err)
	}
	store := &memoryNoteStore{
		notes:     []domain.Note{{ID: "note-1", Body: "remember this", CreatedAt: time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)}},
		reminders: map[string]reminder.Schedule{"note-1": schedule},
	}
	model, err := NewHomeModel(store, func() time.Time { return time.Date(2026, 8, 20, 10, 0, 0, 0, location) }, func() string { return "unused" })
	if err != nil {
		t.Fatalf("NewHomeModel returned error: %v", err)
	}
	if content := model.View().Content; !strings.Contains(content, "reminder: due") {
		t.Fatalf("view = %q, want reminder status", content)
	}
}

func TestHomeModelCtrlRAcknowledgesSelectedReminder(t *testing.T) {
	location := time.FixedZone("CST", 8*60*60)
	schedule, err := reminder.NewSchedule(time.Date(2026, 8, 20, 1, 0, 0, 0, time.UTC), "", location)
	if err != nil {
		t.Fatalf("NewSchedule returned error: %v", err)
	}
	store := &memoryNoteStore{
		notes:     []domain.Note{{ID: "note-1", Body: "remember this", CreatedAt: time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)}},
		reminders: map[string]reminder.Schedule{"note-1": schedule},
	}
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, location)
	model, err := NewHomeModel(store, func() time.Time { return now }, func() string { return "unused" })
	if err != nil {
		t.Fatalf("NewHomeModel returned error: %v", err)
	}

	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Code: 'r', Mod: tea.ModCtrl}))
	model = updated.(*HomeModel)
	if !store.reminders["note-1"].Dismissed() {
		t.Fatal("reminder remains active after Ctrl+R acknowledgement")
	}
	if !strings.Contains(model.View().Content, "reminder: dismissed") {
		t.Fatalf("view = %q, want dismissed status", model.View().Content)
	}
}

func TestHomeModelCtrlEnterInsertsNewlineWithoutSubmitting(t *testing.T) {
	store := &memoryNoteStore{}
	createdAt := time.Date(2026, 8, 20, 12, 34, 56, 0, time.UTC)
	model, err := NewHomeModel(store, func() time.Time { return createdAt }, func() string { return "note-1" })
	if err != nil {
		t.Fatalf("NewHomeModel returned error: %v", err)
	}

	updated, _ := model.Update(tea.KeyPressMsg(tea.Key{Text: "first"}))
	model = updated.(*HomeModel)
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModCtrl}))
	model = updated.(*HomeModel)
	updated, _ = model.Update(tea.KeyPressMsg(tea.Key{Text: "second"}))
	model = updated.(*HomeModel)

	if len(store.notes) != 0 {
		t.Fatalf("saved note count = %d, want 0 before plain Enter", len(store.notes))
	}
	if model.input != "first\nsecond" {
		t.Fatalf("input = %q, want %q", model.input, "first\nsecond")
	}
}

func indexOf(value, fragment string) int {
	for i := 0; i+len(fragment) <= len(value); i++ {
		if value[i:i+len(fragment)] == fragment {
			return i
		}
	}
	return -1
}
