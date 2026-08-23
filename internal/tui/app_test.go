package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/jiangfire/snapnotes/internal/domain"
	"github.com/jiangfire/snapnotes/internal/reminder"
)

func newApp(t *testing.T, store *fakeStore) *AppModel {
	t.Helper()
	home, err := NewHomeModel(store, nil, nil, time.Now, func() string { return "unused" })
	if err != nil {
		t.Fatalf("NewHomeModel: %v", err)
	}
	return NewAppModel(home, store, &fakeVerifier{})
}

func appUpdate(m *AppModel, msg tea.Msg) *AppModel {
	mm, _ := m.Update(msg)
	return mm.(*AppModel)
}

type fakeVerifier struct{ ok bool }

func (f *fakeVerifier) VerifyLeaf(index uint64) (bool, error) { return f.ok, nil }

func TestAppNavigatesBetweenScreensWithFKeys(t *testing.T) {
	app := newApp(t, &fakeStore{})
	if app.screen != ScreenHome {
		t.Fatalf("initial screen = %d, want Home", app.screen)
	}
	app = appUpdate(app, press(tea.Key{Code: tea.KeyF2}))
	if app.screen != ScreenSearch {
		t.Fatalf("F2 screen = %d, want Search", app.screen)
	}
	app = appUpdate(app, press(tea.Key{Code: tea.KeyF4}))
	if app.screen != ScreenAudit {
		t.Fatalf("F4 screen = %d, want Audit", app.screen)
	}
	app = appUpdate(app, press(tea.Key{Code: tea.KeyEsc}))
	if app.screen != ScreenHome {
		t.Fatalf("Esc screen = %d, want Home", app.screen)
	}
}

func TestAppCtrlOOpensDetailForSelectedNote(t *testing.T) {
	store := &fakeStore{notes: []domain.Note{
		{ID: "note-1", Body: "#idea detail me", CreatedAt: time.Now()},
	}}
	app := newApp(t, store)
	app = appUpdate(app, press(tea.Key{Code: 'o', Mod: tea.ModCtrl}))
	if app.screen != ScreenDetail {
		t.Fatalf("screen = %d, want Detail", app.screen)
	}
	if app.selectedID != "note-1" {
		t.Fatalf("selectedID = %q, want note-1", app.selectedID)
	}
	if !strings.Contains(app.View().Content, "#idea detail me") {
		t.Fatalf("detail view = %q, want note body", app.View().Content)
	}
	if !strings.Contains(app.View().Content, "author:") {
		t.Fatalf("detail view = %q, want author field", app.View().Content)
	}
}

func TestAppSearchRunsAndShowsResults(t *testing.T) {
	store := &fakeStore{notes: []domain.Note{
		{ID: "note-1", Body: "searchable idea", CreatedAt: time.Now()},
	}}
	app := newApp(t, store)
	app = appUpdate(app, press(tea.Key{Code: tea.KeyF2}))
	app = appUpdate(app, press(textKey("idea")))
	app = appUpdate(app, press(tea.Key{Code: tea.KeyEnter}))
	if store.searched == 0 {
		t.Fatal("search was not executed on Enter")
	}
	if !strings.Contains(app.View().Content, "searchable idea") {
		t.Fatalf("search view = %q, want result body", app.View().Content)
	}
}

func TestAppCalendarShowsDueReviews(t *testing.T) {
	location := time.FixedZone("CST", 8*60*60)
	dueTime := time.Date(2026, 8, 20, 1, 0, 0, 0, time.UTC)
	sched, err := reminder.NewSchedule(dueTime, "", location)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, location)
	store := &fakeStore{
		notes:     []domain.Note{{ID: "note-1", Body: "review me", CreatedAt: time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)}},
		reminders: map[string]reminder.Schedule{"note-1": sched},
	}
	app := newApp(t, store)
	app.home.now = func() time.Time { return now }
	app = appUpdate(app, press(tea.Key{Code: tea.KeyF3}))
	content := app.View().Content
	if !strings.Contains(content, "review me") {
		t.Fatalf("calendar view = %q, want due review body", content)
	}
}

func TestAppCalendarCtrlRAcknowledgesDueReminder(t *testing.T) {
	location := time.FixedZone("CST", 8*60*60)
	dueTime := time.Date(2026, 8, 20, 1, 0, 0, 0, time.UTC)
	sched, err := reminder.NewSchedule(dueTime, "", location)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 20, 10, 0, 0, 0, location)
	store := &fakeStore{
		notes:     []domain.Note{{ID: "note-1", Body: "review me", CreatedAt: time.Date(2026, 8, 19, 0, 0, 0, 0, time.UTC)}},
		reminders: map[string]reminder.Schedule{"note-1": sched},
	}
	app := newApp(t, store)
	app.home.now = func() time.Time { return now }
	app = appUpdate(app, press(tea.Key{Code: tea.KeyF3}))
	app = appUpdate(app, press(tea.Key{Code: 'r', Mod: tea.ModCtrl}))
	if !store.reminders["note-1"].Dismissed() {
		t.Fatal("due reminder not acknowledged after Ctrl+R")
	}
}

func TestAppAuditShowsChainTipAndVerifies(t *testing.T) {
	app := newApp(t, &fakeStore{})
	app.verifier = &fakeVerifier{ok: true}
	app = appUpdate(app, press(tea.Key{Code: tea.KeyF4}))
	content := app.View().Content
	if !strings.Contains(content, "Chain Audit") {
		t.Fatalf("audit view = %q, want header", content)
	}
	// No verified chain yet in the fake store -> graceful message.
	if !strings.Contains(content, "no verified chain") {
		t.Fatalf("audit view = %q, want no-chain message", content)
	}
	// Press v to verify a leaf.
	app = appUpdate(app, press(textKey("v")))
	if !strings.Contains(app.View().Content, "verified") {
		t.Fatalf("audit view = %q, want verify result", app.View().Content)
	}
}
