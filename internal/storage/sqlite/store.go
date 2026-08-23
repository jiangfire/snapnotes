package sqlite

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"strings"
	"time"

	"github.com/jiangfire/snapnotes/internal/domain"
	"github.com/jiangfire/snapnotes/internal/parser"
	"github.com/jiangfire/snapnotes/internal/reminder"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type SearchResult struct {
	Notes      []domain.Note
	NextCursor string
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS notes (
			id TEXT PRIMARY KEY,
			body TEXT NOT NULL,
			created_at INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS local_reminders (
			note_id TEXT PRIMARY KEY,
			next_fire_at INTEGER NOT NULL,
			repeat_rule TEXT NOT NULL,
			last_acknowledged_at INTEGER,
			timezone_name TEXT NOT NULL,
			timezone_offset_seconds INTEGER NOT NULL,
			dismissed INTEGER NOT NULL DEFAULT 0
		);
		CREATE VIRTUAL TABLE IF NOT EXISTS notes_fts USING fts5(
			note_id UNINDEXED,
			body,
			tags
		);
		CREATE TABLE IF NOT EXISTS outbox (
			operation_id   TEXT PRIMARY KEY,
			stream_id      BLOB NOT NULL,
			entity_id      TEXT NOT NULL,
			transaction_id BLOB NOT NULL,
			operation_type TEXT NOT NULL,
			payload        BLOB NOT NULL,
			created_at     INTEGER NOT NULL,
			sync_status    TEXT NOT NULL DEFAULT 'pending'
		);
		CREATE INDEX IF NOT EXISTS idx_outbox_status ON outbox(sync_status, created_at);
		CREATE TABLE IF NOT EXISTS sync_state (
			stream_id          BLOB PRIMARY KEY,
			last_block_height  INTEGER NOT NULL DEFAULT 0,
			last_block_hash    BLOB,
			last_mmr_root      BLOB,
			last_peaks         BLOB,
			last_chainwork     BLOB,
			genesis_block_hash BLOB,
			device_id         TEXT NOT NULL DEFAULT ''
		)
	`); err != nil {
		db.Close()
		return nil, err
	}

	return &Store{db: db}, nil
}

func (s *Store) SaveNote(note domain.Note) error {
	tags := strings.Join(parser.Parse(note.Body).Tags, " ")
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	_, err = tx.Exec(
		"INSERT INTO notes (id, body, created_at) VALUES (?, ?, ?)",
		note.ID,
		note.Body,
		note.CreatedAt.UTC().UnixMilli(),
	)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(
		"INSERT INTO notes_fts(note_id, body, tags) VALUES (?, ?, ?)",
		note.ID, note.Body, tags,
	); err != nil {
		return err
	}
	parsed := parser.Parse(note.Body)
	if parsed.Reminder != nil {
		schedule, err := reminder.NewSchedule(*parsed.Reminder, parsed.Repeat, time.UTC)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO local_reminders
			(note_id, next_fire_at, repeat_rule, timezone_name, timezone_offset_seconds, dismissed)
			VALUES (?, ?, ?, ?, ?, 0)
			ON CONFLICT(note_id) DO UPDATE SET next_fire_at=excluded.next_fire_at,
			repeat_rule=excluded.repeat_rule, timezone_name=excluded.timezone_name,
			timezone_offset_seconds=excluded.timezone_offset_seconds, dismissed=excluded.dismissed`,
			note.ID, schedule.NextFireAt.UnixMilli(), schedule.Repeat, schedule.Timezone.String(), 0); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SaveNoteWithOutbox persists a note and its outbound transaction in one local
// transaction, so an offline create never loses its pending sync work.
func (s *Store) SaveNoteWithOutbox(note domain.Note, item OutboxItem) error {
	tags := strings.Join(parser.Parse(note.Body).Tags, " ")
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err = tx.Exec(
		"INSERT INTO notes (id, body, created_at) VALUES (?, ?, ?)",
		note.ID, note.Body, note.CreatedAt.UTC().UnixMilli(),
	); err != nil {
		return err
	}
	if _, err = tx.Exec(
		"INSERT INTO notes_fts(note_id, body, tags) VALUES (?, ?, ?)",
		note.ID, note.Body, tags,
	); err != nil {
		return err
	}
	parsed := parser.Parse(note.Body)
	if parsed.Reminder != nil {
		schedule, err := reminder.NewSchedule(*parsed.Reminder, parsed.Repeat, time.UTC)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO local_reminders
			(note_id, next_fire_at, repeat_rule, timezone_name, timezone_offset_seconds, dismissed)
			VALUES (?, ?, ?, ?, ?, 0)
			ON CONFLICT(note_id) DO UPDATE SET next_fire_at=excluded.next_fire_at,
			repeat_rule=excluded.repeat_rule, timezone_name=excluded.timezone_name,
			timezone_offset_seconds=excluded.timezone_offset_seconds, dismissed=excluded.dismissed`,
			note.ID, schedule.NextFireAt.UnixMilli(), schedule.Repeat, schedule.Timezone.String(), 0); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(`INSERT INTO outbox
		(operation_id, stream_id, entity_id, transaction_id, operation_type, payload, created_at, sync_status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		item.OperationID, item.StreamID, item.EntityID, item.TransactionID,
		item.OperationType, item.Payload, item.CreatedAt.UTC().UnixMilli(), item.SyncStatus); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SaveReminder(noteID string, schedule reminder.Schedule) error {
	var acknowledged any
	if schedule.LastAcknowledgedAt != nil {
		acknowledged = schedule.LastAcknowledgedAt.UnixMilli()
	}
	_, offset := schedule.DisplayFireAt().Zone()
	_, err := s.db.Exec(`INSERT INTO local_reminders
		(note_id, next_fire_at, repeat_rule, last_acknowledged_at, timezone_name, timezone_offset_seconds, dismissed)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(note_id) DO UPDATE SET next_fire_at=excluded.next_fire_at,
		repeat_rule=excluded.repeat_rule, last_acknowledged_at=excluded.last_acknowledged_at,
		timezone_name=excluded.timezone_name, timezone_offset_seconds=excluded.timezone_offset_seconds,
		dismissed=excluded.dismissed`, noteID, schedule.NextFireAt.UnixMilli(), schedule.Repeat,
		acknowledged, schedule.Timezone.String(), offset, boolToInt(schedule.Dismissed()))
	return err
}

func (s *Store) GetReminder(noteID string) (reminder.Schedule, bool, error) {
	var nextFireAt, acknowledgedAt sql.NullInt64
	var repeat, timezoneName string
	var offset, dismissed int
	err := s.db.QueryRow(`SELECT next_fire_at, repeat_rule, last_acknowledged_at,
		timezone_name, timezone_offset_seconds, dismissed FROM local_reminders WHERE note_id = ?`, noteID).
		Scan(&nextFireAt, &repeat, &acknowledgedAt, &timezoneName, &offset, &dismissed)
	if err == sql.ErrNoRows {
		return reminder.Schedule{}, false, nil
	}
	if err != nil {
		return reminder.Schedule{}, false, err
	}
	var acknowledged *time.Time
	if acknowledgedAt.Valid {
		value := time.UnixMilli(acknowledgedAt.Int64).UTC()
		acknowledged = &value
	}
	location := time.FixedZone(timezoneName, offset)
	schedule, err := reminder.RestoreSchedule(time.UnixMilli(nextFireAt.Int64).UTC(), repeat, location, acknowledged, dismissed != 0)
	return schedule, true, err
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (s *Store) SearchNotes(query string, limit int, cursor string) (SearchResult, error) {
	if limit <= 0 {
		return SearchResult{}, errors.New("search limit must be positive")
	}
	if limit > 100 {
		limit = 100
	}

	matchQuery := strings.TrimSpace(query)
	if strings.HasPrefix(matchQuery, "tag:") {
		matchQuery = strings.TrimSpace(strings.TrimPrefix(matchQuery, "tag:"))
		if matchQuery == "" {
			return SearchResult{}, errors.New("tag query cannot be empty")
		}
		matchQuery = "tags : \"" + strings.ToLower(matchQuery) + "\""
	}
	if matchQuery == "" {
		return SearchResult{}, errors.New("search query cannot be empty")
	}

	lastCreatedAt, lastID, err := decodeCursor(cursor)
	if err != nil {
		return SearchResult{}, err
	}
	rows, err := s.db.Query(`
		SELECT n.id, n.body, n.created_at
		FROM notes_fts f
		JOIN notes n ON n.id = f.note_id
		WHERE notes_fts MATCH ?
		  AND (n.created_at < ? OR (n.created_at = ? AND n.id < ?))
		ORDER BY n.created_at DESC, n.id DESC
		LIMIT ?
	`, matchQuery, lastCreatedAt, lastCreatedAt, lastID, limit+1)
	if err != nil {
		return SearchResult{}, err
	}
	defer rows.Close()

	result := SearchResult{Notes: make([]domain.Note, 0, limit)}
	for rows.Next() {
		var note domain.Note
		var createdAt int64
		if err := rows.Scan(&note.ID, &note.Body, &createdAt); err != nil {
			return SearchResult{}, err
		}
		note.CreatedAt = time.UnixMilli(createdAt).UTC()
		result.Notes = append(result.Notes, note)
	}
	if err := rows.Err(); err != nil {
		return SearchResult{}, err
	}
	if len(result.Notes) > limit {
		last := result.Notes[limit-1]
		result.Notes = result.Notes[:limit]
		result.NextCursor = encodeCursor(last.CreatedAt.UnixMilli(), last.ID)
	}
	return result, nil
}

func encodeCursor(createdAt int64, id string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strings.Join([]string{time.UnixMilli(createdAt).UTC().Format(time.RFC3339Nano), id}, "\x00")))
}

func decodeCursor(cursor string) (int64, string, error) {
	if cursor == "" {
		return 1<<63 - 1, "\uffff", nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return 0, "", errors.New("invalid search cursor")
	}
	parts := strings.Split(string(decoded), "\x00")
	if len(parts) != 2 {
		return 0, "", errors.New("invalid search cursor")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, parts[0])
	if err != nil {
		return 0, "", errors.New("invalid search cursor")
	}
	return createdAt.UnixMilli(), parts[1], nil
}

func (s *Store) GetNote(id string) (domain.Note, error) {
	var note domain.Note
	var createdAt int64
	if err := s.db.QueryRow(
		"SELECT id, body, created_at FROM notes WHERE id = ?",
		id,
	).Scan(&note.ID, &note.Body, &createdAt); err != nil {
		return domain.Note{}, err
	}

	note.CreatedAt = time.UnixMilli(createdAt).UTC()
	return note, nil
}

func (s *Store) ListNotes() ([]domain.Note, error) {
	rows, err := s.db.Query("SELECT id, body, created_at FROM notes ORDER BY created_at DESC, id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var notes []domain.Note
	for rows.Next() {
		var note domain.Note
		var createdAt int64
		if err := rows.Scan(&note.ID, &note.Body, &createdAt); err != nil {
			return nil, err
		}
		note.CreatedAt = time.UnixMilli(createdAt).UTC()
		notes = append(notes, note)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if notes == nil {
		return []domain.Note{}, nil
	}
	return notes, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}
