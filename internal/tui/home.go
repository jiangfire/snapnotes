package tui

import (
	"crypto/rand"
	"encoding/hex"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/jiangfire/snapnotes/internal/domain"
	"github.com/jiangfire/snapnotes/internal/parser"
	"github.com/jiangfire/snapnotes/internal/reminder"
)

type NoteStore interface {
	SaveNote(domain.Note) error
	ListNotes() ([]domain.Note, error)
	GetReminder(string) (reminder.Schedule, bool, error)
	SaveReminder(string, reminder.Schedule) error
}

type HomeModel struct {
	store  NoteStore
	now    func() time.Time
	nextID func() string

	notes     []domain.Note
	reminders map[string]reminder.Schedule
	selected  int
	input     string
	err       error
}

func NewHomeModel(store NoteStore, now func() time.Time, nextID func() string) (*HomeModel, error) {
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
	return nil
}

func (m *HomeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}

	if _, isRelease := msg.(tea.KeyReleaseMsg); isRelease {
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
		m.submit()
		return m, nil
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

func (m *HomeModel) submit() {
	if m.input == "" {
		return
	}

	note, err := domain.CreateNote(m.input, m.now(), m.nextID)
	if err != nil {
		m.err = err
		return
	}
	if err := m.store.SaveNote(note); err != nil {
		m.err = err
		return
	}

	if parsed := parser.Parse(note.Body); parsed.Reminder != nil {
		schedule, err := reminder.NewSchedule(*parsed.Reminder, parsed.Repeat, time.UTC)
		if err != nil {
			m.err = err
			return
		}
		if err := m.store.SaveReminder(note.ID, schedule); err != nil {
			m.err = err
			return
		}
		m.reminders[note.ID] = schedule
	}
	m.notes = append(m.notes, note)
	m.sortNotes()
	m.input = ""
	m.err = nil
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
