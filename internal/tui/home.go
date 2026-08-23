package tui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/jiangfire/snapnotes/internal/domain"
	"github.com/jiangfire/snapnotes/internal/parser"
	"github.com/jiangfire/snapnotes/internal/reminder"
)

// NoteStore is the local read/status surface the home model needs. Note creation
// is delegated to a Submitter (which persists the note together with its outbound
// encrypted transaction), so SaveNote is intentionally not part of this surface.
type NoteStore interface {
	ListNotes() ([]domain.Note, error)
	GetReminder(string) (reminder.Schedule, bool, error)
	SaveReminder(string, reminder.Schedule) error
	PendingOutboxCount() (int, error)
	SearchNotes(query string, limit int, cursor string) (domain.SearchResult, error)
	GetNote(id string) (domain.Note, error)
	NoteSyncStatus(id string) (string, error)
	ChainTip() (domain.ChainTip, bool, error)
}

// Submitter persists a note locally and enqueues its encrypted transaction. It
// performs no network I/O, so a stopped server never blocks a local create.
type Submitter interface {
	Submit(body string, createdAt time.Time) (string, error)
}

// Syncer drains the outbox and catches up the local chain position. It may be
// nil in tests or in a purely offline configuration.
type Syncer interface {
	Sync(ctx context.Context) error
}

type syncResultMsg struct{ err error }

// SyncTickMsg is sent by the entry point on a timer to pull chain updates
// without user interaction (the server only notifies; the client pulls).
type SyncTickMsg struct{}

type HomeModel struct {
	store     NoteStore
	submitter Submitter
	syncer    Syncer
	now       func() time.Time
	nextID    func() string

	notes     []domain.Note
	reminders map[string]reminder.Schedule
	selected  int
	input     string
	err       error
	pending   int  // unsynced outbox items, for the status line
	syncing   bool // a Sync is currently in flight
}

func NewHomeModel(store NoteStore, submitter Submitter, syncer Syncer, now func() time.Time, nextID func() string) (*HomeModel, error) {
	if now == nil {
		now = time.Now
	}
	if nextID == nil {
		nextID = defaultNoteID
	}
	notes, err := store.ListNotes()
	if err != nil {
		return nil, err
	}

	model := &HomeModel{
		store:     store,
		submitter: submitter,
		syncer:    syncer,
		now:       now,
		nextID:    nextID,
		notes:     notes,
		reminders: make(map[string]reminder.Schedule),
	}
	for _, note := range notes {
		schedule, found, err := store.GetReminder(note.ID)
		if err != nil {
			return nil, err
		}
		if found {
			model.reminders[note.ID] = schedule
		}
	}
	model.sortNotes()
	model.refreshPending()
	return model, nil
}

func defaultNoteID() string {
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return time.Now().UTC().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(entropy[:])
}

func (m *HomeModel) Init() tea.Cmd {
	// Sync once on startup so a restarted client catches up before the user acts.
	if m.syncer != nil {
		return m.syncCmd()
	}
	return nil
}

func (m *HomeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case syncResultMsg:
		m.syncing = false
		m.err = msg.err
		m.refreshPending()
		return m, nil
	case SyncTickMsg:
		if m.syncer != nil {
			return m, m.syncCmd()
		}
		return m, nil
	case tea.KeyMsg:
		return m.updateKey(msg)
	}
	return m, nil
}

func (m *HomeModel) updateKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	if _, isRelease := key.(tea.KeyReleaseMsg); isRelease {
		return m, nil
	}

	k := key.Key()
	if k.Code == tea.KeyUp {
		if m.selected > 0 {
			m.selected--
		}
		return m, nil
	}
	if k.Code == tea.KeyDown {
		if m.selected+1 < len(m.notes) {
			m.selected++
		}
		return m, nil
	}
	if k.Code == 'r' && k.Mod&tea.ModCtrl != 0 {
		m.acknowledgeSelectedReminder()
		return m, nil
	}
	if k.Code == tea.KeyEnter || k.Code == tea.KeyReturn {
		if k.Mod&(tea.ModCtrl|tea.ModAlt) != 0 {
			m.input += "\n"
			return m, nil
		}
		return m.submit()
	}

	if k.Code == tea.KeyBackspace {
		m.input = strings.TrimSuffix(m.input, lastRune(m.input))
		return m, nil
	}

	if k.Text != "" {
		m.input += k.Text
	}
	return m, nil
}

func (m *HomeModel) View() tea.View {
	var view strings.Builder
	view.WriteString("Recent Notes\n")
	view.WriteString("------------\n")
	for index, note := range m.notes {
		if index == m.selected {
			view.WriteString("> ")
		} else {
			view.WriteString("  ")
		}
		view.WriteString(note.Body)
		if parsed := parser.Parse(note.Body); len(parsed.Tags) > 0 {
			view.WriteString("  ")
			view.WriteString(strings.Join(parsed.Tags, " "))
		}
		if unchecked := countUnchecked(note.Body); unchecked > 0 {
			view.WriteString("  [ ] ")
			view.WriteString(strconv.Itoa(unchecked))
		}
		if schedule, ok := m.reminders[note.ID]; ok {
			view.WriteString("\nreminder: ")
			view.WriteString(string(schedule.StatusAt(m.now())))
		}
		view.WriteByte('\n')
	}
	if len(m.notes) == 0 {
		view.WriteString("No notes yet.\n")
	}
	if m.err != nil {
		view.WriteString("Error: ")
		view.WriteString(m.err.Error())
		view.WriteByte('\n')
	}
	view.WriteString("\n")
	view.WriteString(m.syncStatusLine())
	view.WriteString("\n> ")
	view.WriteString(m.input)
	view.WriteString("\nEnter to save")
	return tea.NewView(view.String())
}

func (m *HomeModel) acknowledgeSelectedReminder() {
	if m.selected < 0 || m.selected >= len(m.notes) {
		return
	}
	note := m.notes[m.selected]
	schedule, ok := m.reminders[note.ID]
	if !ok {
		return
	}
	updated, err := schedule.Acknowledge(m.now())
	if err != nil {
		m.err = err
		return
	}
	if err := m.store.SaveReminder(note.ID, updated); err != nil {
		m.err = err
		return
	}
	m.reminders[note.ID] = updated
	m.err = nil
}

// submit persists the note and enqueues its transaction via the Submitter, then
// triggers a background sync. It returns a command that performs the sync so the
// UI stays responsive and redraws when the sync completes.
func (m *HomeModel) submit() (tea.Model, tea.Cmd) {
	if m.input == "" {
		return m, nil
	}

	id := m.nextID()
	if m.submitter != nil {
		got, err := m.submitter.Submit(m.input, m.now())
		if err != nil {
			m.err = err
			return m, nil
		}
		id = got
	}

	note := domain.Note{ID: id, Body: m.input, CreatedAt: m.now()}
	if parsed := parser.Parse(note.Body); parsed.Reminder != nil {
		if sched, err := reminder.NewSchedule(*parsed.Reminder, parsed.Repeat, time.UTC); err == nil {
			m.reminders[note.ID] = sched
		}
	}
	m.notes = append(m.notes, note)
	m.sortNotes()
	m.input = ""
	m.err = nil
	m.refreshPending()

	if m.syncer != nil {
		return m, m.syncCmd()
	}
	return m, nil
}

func (m *HomeModel) syncCmd() tea.Cmd {
	return func() tea.Msg {
		err := m.syncer.Sync(context.Background())
		return syncResultMsg{err: err}
	}
}

func (m *HomeModel) refreshPending() {
	if m.store == nil {
		return
	}
	n, err := m.store.PendingOutboxCount()
	if err != nil {
		m.pending = 0
		return
	}
	m.pending = n
}

func (m *HomeModel) syncStatusLine() string {
	if m.syncer == nil {
		return "sync: offline"
	}
	if m.pending > 0 {
		return "sync: ⏳ 待同步 " + strconv.Itoa(m.pending)
	}
	if m.syncing {
		return "sync: ⏳ 同步中…"
	}
	return "sync: ✓ 已同步"
}

func (m *HomeModel) sortNotes() {
	sort.SliceStable(m.notes, func(i, j int) bool {
		return m.notes[i].CreatedAt.After(m.notes[j].CreatedAt)
	})
}

func lastRune(value string) string {
	if value == "" {
		return ""
	}
	for i := len(value) - 1; i >= 0; i-- {
		if value[i]&0xC0 != 0x80 {
			return value[i:]
		}
	}
	return value[:1]
}

func countUnchecked(body string) int {
	n := 0
	for _, item := range parser.Parse(body).CheckItems {
		if !item.Checked {
			n++
		}
	}
	return n
}
