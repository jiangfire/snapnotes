package sqlite

import (
	"database/sql"
	"errors"
	"time"
)

// OutboxStatus values for an outbox row.
const (
	OutboxPending = "pending"
	OutboxSynced  = "synced"
	OutboxFailed  = "failed"
)

// OutboxItem is a durable, not-yet-confirmed protocol transaction. The payload
// is the canonical CBOR of the full transaction so it can be resubmitted
// byte-for-byte after a restart (idempotent retry).
type OutboxItem struct {
	OperationID   string
	StreamID      []byte
	EntityID      string
	TransactionID []byte
	OperationType string
	Payload       []byte
	CreatedAt     time.Time
	SyncStatus    string
}

// EnqueueOutbox inserts a pending transaction. It must be called inside the same
// local transaction as the note write so an offline create never loses its
// outbound work.
func (s *Store) EnqueueOutbox(item OutboxItem) error {
	if item.OperationID == "" {
		return errors.New("operation_id is required")
	}
	if len(item.StreamID) != 32 {
		return errors.New("stream_id must be 32 bytes")
	}
	if len(item.TransactionID) != 32 {
		return errors.New("transaction_id must be 32 bytes")
	}
	if len(item.Payload) == 0 {
		return errors.New("payload is required")
	}
	if item.SyncStatus == "" {
		item.SyncStatus = OutboxPending
	}
	_, err := s.db.Exec(
		`INSERT INTO outbox (operation_id, stream_id, entity_id, transaction_id, operation_type, payload, created_at, sync_status)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(operation_id) DO UPDATE SET sync_status=excluded.sync_status`,
		item.OperationID, item.StreamID, item.EntityID, item.TransactionID,
		item.OperationType, item.Payload, item.CreatedAt.UTC().UnixMilli(), item.SyncStatus,
	)
	return err
}

// ListPendingOutbox returns pending outbox items ordered by creation time.
func (s *Store) ListPendingOutbox() ([]OutboxItem, error) {
	rows, err := s.db.Query(
		`SELECT operation_id, stream_id, entity_id, transaction_id, operation_type, payload, created_at, sync_status
		 FROM outbox WHERE sync_status = ? ORDER BY created_at ASC, operation_id ASC`,
		OutboxPending,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanOutboxRows(rows)
}

// GetOutbox returns a single outbox item by operation id.
func (s *Store) GetOutbox(operationID string) (OutboxItem, bool, error) {
	rows, err := s.db.Query(
		`SELECT operation_id, stream_id, entity_id, transaction_id, operation_type, payload, created_at, sync_status
		 FROM outbox WHERE operation_id = ?`,
		operationID,
	)
	if err != nil {
		return OutboxItem{}, false, err
	}
	defer rows.Close()
	items, err := scanOutboxRows(rows)
	if err != nil {
		return OutboxItem{}, false, err
	}
	if len(items) == 0 {
		return OutboxItem{}, false, nil
	}
	return items[0], true, nil
}

// MarkOutboxSynced records a successful submission so the item is never retried.
func (s *Store) MarkOutboxSynced(operationID string) error {
	return s.setOutboxStatus(operationID, OutboxSynced)
}

// MarkOutboxFailed records a permanent submission failure.
func (s *Store) MarkOutboxFailed(operationID string) error {
	return s.setOutboxStatus(operationID, OutboxFailed)
}

func (s *Store) setOutboxStatus(operationID, status string) error {
	res, err := s.db.Exec(
		`UPDATE outbox SET sync_status = ? WHERE operation_id = ?`,
		status, operationID,
	)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return errors.New("outbox item not found")
	}
	return nil
}

func scanOutboxRows(rows *sql.Rows) ([]OutboxItem, error) {
	var items []OutboxItem
	for rows.Next() {
		var item OutboxItem
		var createdAt int64
		if err := rows.Scan(
			&item.OperationID, &item.StreamID, &item.EntityID, &item.TransactionID,
			&item.OperationType, &item.Payload, &createdAt, &item.SyncStatus,
		); err != nil {
			return nil, err
		}
		item.CreatedAt = time.UnixMilli(createdAt).UTC()
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if items == nil {
		return []OutboxItem{}, nil
	}
	return items, nil
}
