package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/jiangfire/snapnotes/internal/domain"
	"github.com/jiangfire/snapnotes/internal/reminder"
)

type fakeStore struct {
	notes     []domain.Note
	reminders map[string]reminder.Schedule
	pending   int
	searched  int
}

func (s *fakeStore) ListNotes() ([]domain.Note, error) {
	return append([]domain.Note(nil), s.notes...), nil
}
func (s *fakeStore) GetReminder(noteID string) (reminder.Schedule, bool, error) {
	sc, ok := s.reminders[noteID]
	return sc, ok, nil
}
func (s *fakeStore) SaveReminder(noteID string, sc reminder.Schedule) error {
	if s.reminders == nil {
		s.reminders = make(map[string]reminder.Schedule)
	}
	s.reminders[noteID] = sc
	return nil
}
func (s *fakeStore) PendingOutboxCount() (int, error) { return s.pending, nil }

func (s *fakeStore) SearchNotes(query string, limit int, cursor string) (domain.SearchResult, error) {
	s.searched++
	return domain.SearchResult{Notes: append([]domain.Note(nil), s.notes...)}, nil
}
func (s *fakeStore) GetNote(id string) (domain.Note, error) {
	for _, n := range s.notes {
		if n.ID == id {
			return n, nil
		}
	}
	return domain.Note{}, errNotFound
}
func (s *fakeStore) NoteSyncStatus(id string) (string, error) { return "synced", nil }
func (s *fakeStore) ChainTip() (domain.ChainTip, bool, error) {
	return domain.ChainTip{}, false, nil
}

type fakeSubmitter struct {
	calls int
	body  string
	id    string
}

func (f *fakeSubmitter) Submit(body string, createdAt time.Time) (string, error) {
	f.calls++
	f.body = body
	if f.id == "" {
		f.id = "note-gen"
	}
	return f.id, nil
}

type fakeSyncer struct {
	calls int
	err   error
}

func (f *fakeSyncer) Sync(ctx context.Context) error {
	f.calls++
	return f.err
}

func press(code tea.Key) tea.Msg        { return tea.KeyPressMsg(code) }
func textKey(s string) tea.Key          { return tea.Key{Text: s} }

func TestHomeModelEnterSubmitsAndClearsInput(t *testing.T) {
	store := &fakeStore{}
	submitter := &fakeSubmitter{id: "note-1"}
	syncer := &fakeSyncer{}
	createdAt := time.Date(2026, 8, 20, 12, 34, 56, 0, time.UTC)
	model, err := NewHomeModel(store, submitter, syncer, func() time.Time { return createdAt }, func() string { return "local-1" })
	if err != nil {
		t.Fatalf("NewHomeModel: %v", err)
	}

	model, _ = update(model, press(textKey("capture an idea")))
	model, _ = update(model, press(tea.Key{Code: tea.KeyEnter}))

	if submitter.calls != 1 {
		t.Fatalf("Submit called %d times, want 1", submitter.calls)
	}
	if submitter.body != "capture an idea" {
		t.Fatalf("Submit body = %q, want %q", submitter.body, "capture an idea")
	}
	if len(model.notes) != 1 || model.notes[0].Body != "capture an idea" {
		t.Fatalf("model notes = %+v, want one 'capture an idea'", model.notes)
	}
	if model.input != "" {
		t.Fatalf("input after submit = %q, want empty", model.input)
	}
}

func TestNewHomeModelProvidesSafeDefaults(t *testing.T) {
	// A nil submitter/syncer (legacy offline config) must not panic on Enter.
	model, err := NewHomeModel(&fakeStore{}, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("NewHomeModel: %v", err)
	}
	model, _ = update(model, press(textKey("uses defaults")))
	model, _ = update(model, press(tea.Key{Code: tea.KeyEnter}))
	if len(model.notes) != 1 {
		t.Fatalf("notes = %d, want 1 (local-only fallback)", len(model.notes))
	}
	if !strings.Contains(model.View().Content, "sync: offline") {
		t.Fatalf("view = %q, want offline status", model.View().Content)
	}
}

func TestHomeModelViewShowsNewestNoteFirst(t *testing.T) {
	store := &fakeStore{notes: []domain.Note{
		{ID: "older", Body: "older note", CreatedAt: time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)},
		{ID: "newer", Body: "newer note", CreatedAt: time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)},
	}}
	model, err := NewHomeModel(store, nil, nil, time.Now, func() string { return "unused" })
	if err != nil {
		t.Fatalf("NewHomeModel: %v", err)
	}
	content := model.View().Content
	newerIndex := strings.Index(content, "newer note")
	olderIndex := strings.Index(content, "older note")
	if newerIndex < 0 || olderIndex < 0 {
		t.Fatalf("view = %q, want both notes", content)
	}
	if newerIndex >= olderIndex {
		t.Fatalf("newer note must sort before older note")
	}
}

func TestHomeModelViewShowsTagsAndReminderStatus(t *testing.T) {
	location := time.FixedZone("CST", 8*60*60)
	schedule, err := reminder.NewSchedule(time.Date(2026, 8, 20, 1, 0, 0, 0, time.UTC), "", location)
	if err != nil {
		t.Fatalf("NewSchedule: %v", err)
	}
	store := &fakeStore{
		notes:     []domain.Note{{ID: "note-1", Body: "#idea remember this", CreatedAt: time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)}},
		reminders: map[string]reminder.Schedule{"note-1": schedule},
	}
	model, err := NewHomeModel(store, nil, nil, func() time.Time { return time.Date(2026, 8, 20, 10, 0, 0, 0, location) }, func() string { return "unused" })
	if err != nil {
		t.Fatalf("NewHomeModel: %v", err)
	}
	content := model.View().Content
	if !strings.Contains(content, "#idea") {
		t.Fatalf("view = %q, want tag #idea", content)
	}
	if !strings.Contains(content, "reminder: due") {
		t.Fatalf("view = %q, want reminder status", content)
	}
}

func TestHomeModelCtrlRAcknowledgesSelectedReminder(t *testing.T) {
	location := time.FixedZone("CST", 8*60*60)
	schedule, err := reminder.NewSchedule(time.Date(2026, 8, 20, 1, 0, 0, 0, time.UTC), "", location)
	if err != nil {
		t.Fatalf("NewSchedule: %v", err)
	}
	store := &fakeStore{
		notes:     []domain.Note{{ID: "note-1", Body: "remember this", CreatedAt: time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)}},
		reminders: map[string]reminder.Schedule{"note-1": schedule},
	}
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, location)
	model, err := NewHomeModel(store, nil, nil, func() time.Time { return now }, func() string { return "unused" })
	if err != nil {
		t.Fatalf("NewHomeModel: %v", err)
	}
	model, _ = update(model, press(tea.Key{Code: 'r', Mod: tea.ModCtrl}))
	if !store.reminders["note-1"].Dismissed() {
		t.Fatal("reminder remains active after Ctrl+R acknowledgement")
	}
	if !strings.Contains(model.View().Content, "reminder: dismissed") {
		t.Fatalf("view = %q, want dismissed status", model.View().Content)
	}
}

func TestHomeModelCtrlEnterInsertsNewlineWithoutSubmitting(t *testing.T) {
	store := &fakeStore{}
	submitter := &fakeSubmitter{id: "note-1"}
	createdAt := time.Date(2026, 8, 20, 12, 34, 56, 0, time.UTC)
	model, err := NewHomeModel(store, submitter, nil, func() time.Time { return createdAt }, func() string { return "local-1" })
	if err != nil {
		t.Fatalf("NewHomeModel: %v", err)
	}
	model, _ = update(model, press(textKey("first")))
	model, _ = update(model, press(tea.Key{Code: tea.KeyEnter, Mod: tea.ModCtrl}))
	model, _ = update(model, press(textKey("second")))

	if submitter.calls != 0 {
		t.Fatalf("Submit called %d times, want 0 before plain Enter", submitter.calls)
	}
	if model.input != "first\nsecond" {
		t.Fatalf("input = %q, want %q", model.input, "first\nsecond")
	}
}

func TestHomeModelSubmitTriggersSync(t *testing.T) {
	store := &fakeStore{}
	submitter := &fakeSubmitter{id: "note-1"}
	syncer := &fakeSyncer{}
	createdAt := time.Date(2026, 8, 20, 12, 34, 56, 0, time.UTC)
	model, err := NewHomeModel(store, submitter, syncer, func() time.Time { return createdAt }, func() string { return "local-1" })
	if err != nil {
		t.Fatalf("NewHomeModel: %v", err)
	}

	model, cmd := update(model, press(textKey("sync me")))
	model, cmd = update(model, press(tea.Key{Code: tea.KeyEnter}))

	if cmd == nil {
		t.Fatal("submit did not return a sync command")
	}
	// Running the command performs the Sync and returns a syncResultMsg.
	msg := cmd()
	if _, ok := msg.(syncResultMsg); !ok {
		t.Fatalf("sync command returned %T, want syncResultMsg", msg)
	}
	// Apply the result so the model updates its status.
	model, _ = update(model, msg)
	if syncer.calls != 1 {
		t.Fatalf("Sync called %d times, want 1", syncer.calls)
	}
	if !strings.Contains(model.View().Content, "✓ 已同步") {
		t.Fatalf("view = %q, want synced status", model.View().Content)
	}
}

func TestHomeModelViewShowsPendingCount(t *testing.T) {
	store := &fakeStore{pending: 3}
	syncer := &fakeSyncer{}
	model, err := NewHomeModel(store, nil, syncer, time.Now, func() string { return "unused" })
	if err != nil {
		t.Fatalf("NewHomeModel: %v", err)
	}
	if !strings.Contains(model.View().Content, "待同步 3") {
		t.Fatalf("view = %q, want pending count 3", model.View().Content)
	}
}

// update applies a message to the model, mirroring how Bubble Tea drives it, and
// returns the concrete *HomeModel for further assertions.
func update(m *HomeModel, msg tea.Msg) (*HomeModel, tea.Cmd) {
	mm, cmd := m.Update(msg)
	return mm.(*HomeModel), cmd
}

var errNotFound = errors.New("note not found")
