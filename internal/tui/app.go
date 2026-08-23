package tui

import (
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/jiangfire/snapnotes/internal/domain"
	"github.com/jiangfire/snapnotes/internal/parser"
	"github.com/jiangfire/snapnotes/internal/reminder"
)

// Screen identifies which feature page is currently active in the AppModel.
type Screen int

const (
	ScreenHome Screen = iota
	ScreenSearch
	ScreenDetail
	ScreenCalendar
	ScreenAudit
)

// Verifier independently verifies an MMR inclusion proof for a chain leaf. The
// real implementation is *sync.SyncClient; a fake is used in tests.
type Verifier interface {
	VerifyLeaf(index uint64) (bool, error)
}

// AppModel is the top-level navigation shell. It owns the home timeline and the
// four Phase 5 feature pages (search, detail, calendar/reminders, chain audit)
// and routes function keys between them. Esc always returns to Home; the home
// screen remains the single place where notes are created.
type AppModel struct {
	home    *HomeModel
	store   NoteStore
	verifier Verifier

	screen      Screen
	selectedID  string // note id for the detail page
	searchQuery string
	searchInput string
	searchPage  domain.SearchResult
	searchErr   error
	verifyMsg   string
}

// NewAppModel builds the navigation shell around a home model. The store must
// satisfy the extended NoteStore surface (search, get, status, chain tip); the
// verifier may be nil if chain auditing is unavailable.
func NewAppModel(home *HomeModel, store NoteStore, verifier Verifier) *AppModel {
	return &AppModel{home: home, store: store, verifier: verifier, screen: ScreenHome}
}

func (m *AppModel) Init() tea.Cmd { return m.home.Init() }

func (m *AppModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case syncResultMsg:
		// A sync finished on any screen: refresh the home timeline so returned
		// notes become visible when the user navigates back.
		mm, cmd := m.home.Update(msg)
		m.home = mm.(*HomeModel)
		return m, cmd
	case SyncTickMsg:
		mm, cmd := m.home.Update(msg)
		m.home = mm.(*HomeModel)
		return m, cmd
	case tea.KeyMsg:
		// Global navigation keys work on every screen.
		if _, isRelease := msg.(tea.KeyReleaseMsg); isRelease {
			return m, nil
		}
		k := msg.Key()
		switch k.Code {
		case tea.KeyF1:
			m.screen = ScreenHome
			return m, nil
		case tea.KeyF2:
			m.screen = ScreenSearch
			return m, nil
		case tea.KeyF3:
			m.screen = ScreenCalendar
			return m, nil
		case tea.KeyF4:
			m.screen = ScreenAudit
			return m, nil
		case tea.KeyEsc:
			if m.screen != ScreenHome {
				m.screen = ScreenHome
				return m, nil
			}
		case 'o', 'O':
			if k.Mod&tea.ModCtrl != 0 {
				return m.openDetail()
			}
		}

		// Screen-local handling.
		switch m.screen {
		case ScreenHome:
			mm, cmd := m.home.Update(msg)
			m.home = mm.(*HomeModel)
			return m, cmd
		case ScreenSearch:
			return m.updateSearch(msg)
		case ScreenDetail:
			return m, nil
		case ScreenCalendar:
			return m.updateCalendar(msg)
		case ScreenAudit:
			return m.updateAudit(msg)
		}
	}
	return m, nil
}

func (m *AppModel) openDetail() (tea.Model, tea.Cmd) {
	if m.home.selected < 0 || m.home.selected >= len(m.home.notes) {
		return m, nil
	}
	m.selectedID = m.home.notes[m.home.selected].ID
	m.screen = ScreenDetail
	return m, nil
}

// --- Search page ---------------------------------------------------------

func (m *AppModel) runSearch() {
	res, err := m.store.SearchNotes(m.searchQuery, 20, "")
	if err != nil {
		m.searchErr = err
		m.searchPage = domain.SearchResult{}
		return
	}
	m.searchErr = nil
	m.searchPage = res
}

func (m *AppModel) updateSearch(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	k := msg.Key()
	switch k.Code {
	case tea.KeyEnter:
		m.searchQuery = strings.TrimSpace(m.searchInput)
		if m.searchQuery != "" {
			m.runSearch()
		}
		return m, nil
	case tea.KeyBackspace:
		m.searchInput = strings.TrimSuffix(m.searchInput, lastRune(m.searchInput))
		return m, nil
	case 'n':
		// Next page: fetch the next cursor if present.
		if m.searchPage.NextCursor != "" {
			res, err := m.store.SearchNotes(m.searchQuery, 20, m.searchPage.NextCursor)
			if err == nil {
				m.searchPage = res
			}
		}
		return m, nil
	case 'p':
		// Previous page is not cursor-tracked in the MVP; re-run from the start.
		m.runSearch()
		return m, nil
	}
	if k.Text != "" {
		m.searchInput += k.Text
	}
	return m, nil
}

// --- Calendar / reminders page -------------------------------------------

func (m *AppModel) updateCalendar(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	k := msg.Key()
	if k.Code == 'r' && k.Mod&tea.ModCtrl != 0 {
		return m, m.acknowledgeDueReminder()
	}
	return m, nil
}

// dueReview returns up to 5 notes whose reminder is actionable now (due or
// overdue, i.e. not still in the future and not dismissed), oldest fire time
// first — the daily "5 reviews" surfaced on the calendar page.
func (m *AppModel) dueReview() []domain.Note {
	notes, err := m.store.ListNotes()
	if err != nil {
		return nil
	}
	now := m.home.now()
	type due struct {
		note domain.Note
		at   time.Time
	}
	var dueList []due
	for _, n := range notes {
		sched, found, err := m.store.GetReminder(n.ID)
		if err != nil || !found {
			continue
		}
		switch sched.StatusAt(now) {
		case reminder.StatusActive, reminder.StatusDismissed:
			continue // future or already handled
		default:
			dueList = append(dueList, due{note: n, at: sched.DisplayFireAt()})
		}
	}
	sort.SliceStable(dueList, func(i, j int) bool {
		return dueList[i].at.Before(dueList[j].at)
	})
	if len(dueList) > 5 {
		dueList = dueList[:5]
	}
	out := make([]domain.Note, 0, len(dueList))
	for _, d := range dueList {
		out = append(out, d.note)
	}
	return out
}

func (m *AppModel) acknowledgeDueReminder() tea.Cmd {
	due := m.dueReview()
	if len(due) == 0 {
		return nil
	}
	note := due[0]
	sched, found, err := m.store.GetReminder(note.ID)
	if err != nil || !found {
		return nil
	}
	updated, err := sched.Acknowledge(m.home.now())
	if err != nil {
		return nil
	}
	if err := m.store.SaveReminder(note.ID, updated); err != nil {
		return nil
	}
	// Refresh the home model's in-memory reminder cache so it stays consistent.
	if s, ok := m.home.reminders[note.ID]; ok {
		_ = s
		m.home.reminders[note.ID] = updated
	}
	return nil
}

// --- Audit page ----------------------------------------------------------

func (m *AppModel) updateAudit(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	k := msg.Key()
	if k.Code == 'v' && m.verifier != nil {
		ok, err := m.verifier.VerifyLeaf(0)
		if err != nil {
			m.verifyMsg = "verify error: " + err.Error()
		} else if ok {
			m.verifyMsg = "leaf 0 verified ✓"
		} else {
			m.verifyMsg = "leaf 0 FAILED verification"
		}
	}
	return m, nil
}

// --- Views ---------------------------------------------------------------

func (m *AppModel) View() tea.View {
	var b strings.Builder
	b.WriteString(m.statusBar())
	b.WriteByte('\n')
	switch m.screen {
	case ScreenHome:
		b.WriteString(m.home.View().Content)
	case ScreenSearch:
		b.WriteString(m.searchView())
	case ScreenDetail:
		b.WriteString(m.detailView())
	case ScreenCalendar:
		b.WriteString(m.calendarView())
	case ScreenAudit:
		b.WriteString(m.auditView())
	}
	return tea.NewView(b.String())
}

func (m *AppModel) statusBar() string {
	names := map[Screen]string{
		ScreenHome:     "Home",
		ScreenSearch:   "Search",
		ScreenCalendar: "Calendar",
		ScreenAudit:    "Audit",
		ScreenDetail:   "Detail",
	}
	cur := names[m.screen]
	return fmt.Sprintf("[F1 Home] [F2 Search] [F3 Calendar] [F4 Audit]  |  %s", cur)
}

func (m *AppModel) searchView() string {
	var b strings.Builder
	b.WriteString("Search Notes\n")
	b.WriteString("------------\n")
	b.WriteString("query> ")
	b.WriteString(m.searchInput)
	b.WriteByte('\n')
	if m.searchErr != nil {
		b.WriteString("error: ")
		b.WriteString(m.searchErr.Error())
		b.WriteByte('\n')
	}
	for i, n := range m.searchPage.Notes {
		b.WriteString(strconv.Itoa(i+1))
		b.WriteString(". ")
		b.WriteString(n.Body)
		b.WriteByte('\n')
	}
	if len(m.searchPage.Notes) == 0 && m.searchQuery != "" {
		b.WriteString("(no matches)\n")
	}
	if m.searchPage.NextCursor != "" {
		b.WriteString("press n for next page\n")
	}
	b.WriteString("Enter to search • Esc/F1 Home\n")
	return b.String()
}

func (m *AppModel) detailView() string {
	var b strings.Builder
	note, err := m.store.GetNote(m.selectedID)
	if err != nil {
		b.WriteString("note not found\n")
		b.WriteString("Esc/F1 Home\n")
		return b.String()
	}
	b.WriteString("Note Detail\n")
	b.WriteString("-----------\n")
	b.WriteString(note.Body)
	b.WriteByte('\n')
	if parsed := parser.Parse(note.Body); len(parsed.Tags) > 0 {
		b.WriteString("tags: ")
		b.WriteString(strings.Join(parsed.Tags, " "))
		b.WriteByte('\n')
	}
	b.WriteString("created: ")
	b.WriteString(note.CreatedAt.Format(time.RFC3339))
	b.WriteByte('\n')
	if len(note.AuthorPublicKey) > 0 {
		b.WriteString("author: ")
		b.WriteString(hex.EncodeToString(note.AuthorPublicKey))
		b.WriteByte('\n')
	} else {
		b.WriteString("author: (local/legacy)\n")
	}
	if status, err := m.store.NoteSyncStatus(note.ID); err == nil {
		b.WriteString("sync: ")
		b.WriteString(status)
		b.WriteByte('\n')
	}
	b.WriteString("Esc/F1 Home\n")
	return b.String()
}

func (m *AppModel) calendarView() string {
	var b strings.Builder
	now := m.home.now()
	b.WriteString("Calendar & Reminders\n")
	b.WriteString("--------------------\n")
	b.WriteString("Today: ")
	b.WriteString(now.Format("2006-01-02"))
	b.WriteByte('\n')
	b.WriteString("Daily review (due, max 5):\n")
	due := m.dueReview()
	if len(due) == 0 {
		b.WriteString("  (nothing due)\n")
	}
	for i, n := range due {
		b.WriteString("  ")
		b.WriteString(strconv.Itoa(i+1))
		b.WriteString(". ")
		b.WriteString(n.Body)
		b.WriteByte('\n')
	}
	b.WriteString("Ctrl+R to acknowledge the next due reminder\n")
	b.WriteString("Esc/F1 Home\n")
	return b.String()
}

func (m *AppModel) auditView() string {
	var b strings.Builder
	b.WriteString("Chain Audit (Phase 5)\n")
	b.WriteString("--------------------\n")
	tip, ok, err := m.store.ChainTip()
	if err != nil {
		b.WriteString("error reading chain tip: ")
		b.WriteString(err.Error())
		b.WriteByte('\n')
	} else if !ok {
		b.WriteString("no verified chain yet — run a sync first\n")
	} else {
		b.WriteString("genesis:  ")
		b.WriteString(hex.EncodeToString(tip.GenesisBlockHash))
		b.WriteByte('\n')
		b.WriteString("height:   ")
		b.WriteString(strconv.FormatUint(tip.LastBlockHeight, 10))
		b.WriteByte('\n')
		b.WriteString("last blk: ")
		b.WriteString(hex.EncodeToString(tip.LastBlockHash))
		b.WriteByte('\n')
		b.WriteString("mmr root: ")
		b.WriteString(hex.EncodeToString(tip.LastMMRRoot))
		b.WriteByte('\n')
		b.WriteString("work:     ")
		b.WriteString(hex.EncodeToString(tip.LastChainwork))
		b.WriteByte('\n')
	}
	if m.verifyMsg != "" {
		b.WriteString(m.verifyMsg)
		b.WriteByte('\n')
	}
	b.WriteString("press v to verify leaf 0 • Esc/F1 Home\n")
	return b.String()
}
