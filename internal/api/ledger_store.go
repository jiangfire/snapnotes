package api

import (
	"database/sql"
	"fmt"
	"math/big"
	"os"
	"path/filepath"

	"github.com/jiangfire/snapnotes/internal/protocol"
)

// ledgerSchema is the on-disk layout for the server ledger. The chain itself is
// stored as one row per block (height, canonical header CBOR, block hash, and the
// canonical transaction CBOR). MMR peaks are NOT stored: they are recomputed on
// load by replaying the leaf hashes. Membership, the authorized-write set, and the
// dedup sets (tx/note ids) are mirrored so a restart rebuilds an identical state.
const ledgerSchema = `
CREATE TABLE IF NOT EXISTS streams (
	stream_id          BLOB PRIMARY KEY,
	target             BLOB NOT NULL,
	genesis_hash       BLOB NOT NULL,
	owner              BLOB NOT NULL,
	owner_enc          BLOB NOT NULL,
	current_key_epoch  INTEGER NOT NULL,
	height             INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS blocks (
	stream_id   BLOB NOT NULL,
	height      INTEGER NOT NULL,
	header_cbor BLOB NOT NULL,
	block_hash  BLOB NOT NULL,
	tx_cbor     BLOB NOT NULL,
	PRIMARY KEY (stream_id, height)
);
CREATE TABLE IF NOT EXISTS leaves (
	stream_id BLOB NOT NULL,
	pos       INTEGER NOT NULL,
	leaf_hash BLOB NOT NULL,
	PRIMARY KEY (stream_id, pos)
);
CREATE TABLE IF NOT EXISTS members (
	stream_id BLOB NOT NULL,
	device_id BLOB NOT NULL,
	signing   BLOB NOT NULL,
	enc       BLOB NOT NULL,
	label     TEXT NOT NULL,
	PRIMARY KEY (stream_id, device_id)
);
CREATE TABLE IF NOT EXISTS authorized (
	stream_id BLOB NOT NULL,
	pubkey_hex TEXT NOT NULL,
	PRIMARY KEY (stream_id, pubkey_hex)
);
CREATE TABLE IF NOT EXISTS tx_ids (
	stream_id BLOB NOT NULL,
	tx_id_hex TEXT NOT NULL,
	PRIMARY KEY (stream_id, tx_id_hex)
);
CREATE TABLE IF NOT EXISTS note_ids (
	stream_id  BLOB NOT NULL,
	note_id_hex TEXT NOT NULL,
	PRIMARY KEY (stream_id, note_id_hex)
);
CREATE INDEX IF NOT EXISTS idx_blocks_stream ON blocks(stream_id, height);
CREATE INDEX IF NOT EXISTS idx_leaves_stream ON leaves(stream_id, pos);
`

func openLedgerDB(dataDir string) (*sql.DB, error) {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "ledger.db"))
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(ledgerSchema); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// loadOrSeedStream rebuilds a stream from disk if it already has data, otherwise
// seeds it from the supplied genesis. This makes -genesis authoritative only on a
// fresh data directory; on restart the persisted chain wins.
func (l *Ledger) loadOrSeedStream(cfg StreamConfig) error {
	var cnt int
	if err := l.db.QueryRow("SELECT count(*) FROM streams WHERE stream_id = ?", cfg.StreamID).Scan(&cnt); err != nil {
		return err
	}
	if cnt > 0 {
		return l.rebuildStream(cfg.StreamID)
	}
	return l.seedStream(cfg)
}

func (l *Ledger) seedStream(cfg StreamConfig) error {
	gp, err := protocol.DecodeGenesisPayload(cfg.Genesis.Transaction.UnsignedBody.OperationPayload)
	if err != nil {
		return err
	}
	genesisCBOR, err := protocol.MarshalTransaction(
		cfg.Genesis.Transaction.UnsignedBody,
		cfg.Genesis.Transaction.TransactionID,
		cfg.Genesis.Transaction.Signature,
		cfg.Genesis.Transaction.PowEpoch,
		cfg.Genesis.Transaction.PowNonce,
	)
	if err != nil {
		return err
	}
	genesisTxHash, err := protocol.TransactionHash(
		cfg.Genesis.Transaction.UnsignedBody,
		cfg.Genesis.Transaction.TransactionID,
		cfg.Genesis.Transaction.Signature,
		cfg.Genesis.Transaction.PowEpoch,
		cfg.Genesis.Transaction.PowNonce,
	)
	if err != nil {
		return err
	}
	leaf := protocol.LeafHash(genesisTxHash)
	peaks := protocol.AddLeaf(nil, leaf, 0)
	owner := cfg.Genesis.Transaction.UnsignedBody.AuthorPublicKey

	auth := make(map[string]bool, len(cfg.AuthorizedKeys)+1)
	auth[hexKey(owner)] = true
	for _, k := range cfg.AuthorizedKeys {
		auth[hexKey(k)] = true
	}

	headerCBOR, err := protocol.MarshalBlockHeader(cfg.Genesis.Header)
	if err != nil {
		return err
	}

	tx, err := l.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`INSERT INTO streams (stream_id, target, genesis_hash, owner, owner_enc, current_key_epoch, height)
		 VALUES (?,?,?,?,?,?,?)`,
		cfg.StreamID, gp.InitialPowTarget, cfg.Genesis.BlockHash, owner, gp.OwnerEncryptionPublicKey, int64(gp.InitialKeyEpoch), 0,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO blocks (stream_id, height, header_cbor, block_hash, tx_cbor) VALUES (?,?,?,?,?)`,
		cfg.StreamID, 0, headerCBOR, cfg.Genesis.BlockHash, genesisCBOR,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO leaves (stream_id, pos, leaf_hash) VALUES (?,?,?)`,
		cfg.StreamID, 0, leaf,
	); err != nil {
		return err
	}
	for k := range auth {
		if _, err := tx.Exec(`INSERT INTO authorized (stream_id, pubkey_hex) VALUES (?,?)`, cfg.StreamID, k); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	l.streams[hexKey(cfg.StreamID)] = &streamState{
		target:          gp.InitialPowTarget,
		authorized:      auth,
		txIDs:           make(map[string]bool),
		noteIDs:         make(map[string]bool),
		count:           0,
		blocks:          []blockRecord{{header: cfg.Genesis.Header, hash: cfg.Genesis.BlockHash, txCBOR: genesisCBOR}},
		peaks:           peaks,
		leafHashes:      [][]byte{leaf},
		chainwork:       protocol.BlockWork(gp.InitialPowTarget),
		genesisHash:     cfg.Genesis.BlockHash,
		owner:           append([]byte(nil), owner...),
		ownerEnc:        append([]byte(nil), gp.OwnerEncryptionPublicKey...),
		members:         make(map[string]*memberRecord),
		currentKeyEpoch: gp.InitialKeyEpoch,
	}
	// Anchor the genesis tip so the external root log begins at height 0.
	_ = l.anchors.append(AnchorRecord{
		Height:    0,
		BlockHash: b64(cfg.Genesis.BlockHash),
		MMRRoot:   b64(cfg.Genesis.Header.MMRRoot),
	})
	return nil
}

// rebuildStream reconstructs a stream's in-memory state verbatim from disk. Peaks
// are recomputed by replaying the leaf hashes; everything else is read directly.
func (l *Ledger) rebuildStream(streamID []byte) error {
	var target, genesisHash, owner, ownerEnc []byte
	var currentKeyEpoch, height int64
	if err := l.db.QueryRow(
		`SELECT target, genesis_hash, owner, owner_enc, current_key_epoch, height FROM streams WHERE stream_id = ?`,
		streamID,
	).Scan(&target, &genesisHash, &owner, &ownerEnc, &currentKeyEpoch, &height); err != nil {
		return err
	}

	blocks := make([]blockRecord, 0, height+1)
	leafHashes := make([][]byte, 0, height+1)
	peaks := [][]byte{}
	rows, err := l.db.Query(`SELECT header_cbor, block_hash, tx_cbor FROM blocks WHERE stream_id = ? ORDER BY height ASC`, streamID)
	if err != nil {
		return err
	}
	pos := 0
	for rows.Next() {
		var headerCBOR, h, txCBOR []byte
		if err := rows.Scan(&headerCBOR, &h, &txCBOR); err != nil {
			rows.Close()
			return err
		}
		hdr, err := protocol.DecodeBlockHeader(headerCBOR)
		if err != nil {
			rows.Close()
			return fmt.Errorf("decode stored block header: %w", err)
		}
		blocks = append(blocks, blockRecord{header: hdr, hash: h, txCBOR: txCBOR})
		var leafHash []byte
		if err := l.db.QueryRow(`SELECT leaf_hash FROM leaves WHERE stream_id = ? AND pos = ?`, streamID, pos).Scan(&leafHash); err != nil {
			rows.Close()
			return err
		}
		leafHashes = append(leafHashes, leafHash)
		peaks = protocol.AddLeaf(peaks, leafHash, uint64(pos))
		pos++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()

	members, err := loadMemberMap(l.db, streamID)
	if err != nil {
		return err
	}
	authorized, err := loadKeySet(l.db, "SELECT pubkey_hex FROM authorized WHERE stream_id = ?", streamID)
	if err != nil {
		return err
	}
	txIDs, err := loadKeySet(l.db, "SELECT tx_id_hex FROM tx_ids WHERE stream_id = ?", streamID)
	if err != nil {
		return err
	}
	noteIDs, err := loadKeySet(l.db, "SELECT note_id_hex FROM note_ids WHERE stream_id = ?", streamID)
	if err != nil {
		return err
	}

	// Accumulate real cumulative chainwork by replaying each stored block's
	// declared PoW target (genesis included). This matches the in-memory value a
	// freshly seeded stream starts with, so restart is bit-identical.
	cw := new(big.Int)
	for _, b := range blocks {
		cw.Add(cw, protocol.BlockWork(b.header.PowTarget))
	}

	l.streams[hexKey(streamID)] = &streamState{
		target:          append([]byte(nil), target...),
		authorized:      authorized,
		txIDs:           txIDs,
		noteIDs:         noteIDs,
		count:           int64(len(txIDs)),
		blocks:          blocks,
		peaks:           peaks,
		leafHashes:      leafHashes,
		chainwork:       cw,
		genesisHash:     append([]byte(nil), genesisHash...),
		owner:           append([]byte(nil), owner...),
		ownerEnc:        append([]byte(nil), ownerEnc...),
		members:         members,
		currentKeyEpoch: uint64(currentKeyEpoch),
	}
	return nil
}

func loadMemberMap(db *sql.DB, streamID []byte) (map[string]*memberRecord, error) {
	out := make(map[string]*memberRecord)
	rows, err := db.Query(`SELECT device_id, signing, enc, label FROM members WHERE stream_id = ?`, streamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var devID, signing, enc []byte
		var label string
		if err := rows.Scan(&devID, &signing, &enc, &label); err != nil {
			return nil, err
		}
		out[hexKey(devID)] = &memberRecord{
			signing: append([]byte(nil), signing...),
			enc:     append([]byte(nil), enc...),
			label:   label,
		}
	}
	return out, rows.Err()
}

func loadKeySet(db *sql.DB, query string, streamID []byte) (map[string]bool, error) {
	out := make(map[string]bool)
	rows, err := db.Query(query, streamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

// persistDelta carries the on-disk writes implied by a single accepted block. It
// is committed as one SQLite transaction so a crash can never leave the chain and
// its mirrors inconsistent.
type persistDelta struct {
	newBlock     blockRecord
	leafHash     []byte
	txIDHex      string
	noteIDHex    string
	newMember    *memberRecord
	memberID     []byte
	addAuth      []string
	delAuth      []string
	epochChanged bool
	newEpoch     uint64
}

func (l *Ledger) persistDelta(streamID []byte, d persistDelta) error {
	tx, err := l.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	headerCBOR, err := protocol.MarshalBlockHeader(d.newBlock.header)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO blocks (stream_id, height, header_cbor, block_hash, tx_cbor) VALUES (?,?,?,?,?)`,
		streamID, int64(d.newBlock.header.Height), headerCBOR, d.newBlock.hash, d.newBlock.txCBOR,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(
		`INSERT INTO leaves (stream_id, pos, leaf_hash) VALUES (?,?,?)`,
		streamID, int64(d.newBlock.header.Height), d.leafHash,
	); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO tx_ids (stream_id, tx_id_hex) VALUES (?,?)`, streamID, d.txIDHex); err != nil {
		return err
	}
	if d.noteIDHex != "" {
		if _, err := tx.Exec(`INSERT INTO note_ids (stream_id, note_id_hex) VALUES (?,?)`, streamID, d.noteIDHex); err != nil {
			return err
		}
	}
	if d.newMember != nil {
		if _, err := tx.Exec(
			`INSERT INTO members (stream_id, device_id, signing, enc, label) VALUES (?,?,?,?,?)`,
			streamID, d.memberID, d.newMember.signing, d.newMember.enc, d.newMember.label,
		); err != nil {
			return err
		}
	}
	// Delete revoked keys first, then (re-)insert granted ones. A key_rotation_bundle
	// may revoke and re-grant the same member device, in which case the in-memory
	// state keeps the key; deleting before inserting reproduces that exactly.
	for _, k := range d.delAuth {
		if _, err := tx.Exec(`DELETE FROM authorized WHERE stream_id = ? AND pubkey_hex = ?`, streamID, k); err != nil {
			return err
		}
	}
	for _, k := range d.addAuth {
		if _, err := tx.Exec(`INSERT OR IGNORE INTO authorized (stream_id, pubkey_hex) VALUES (?,?)`, streamID, k); err != nil {
			return err
		}
	}
	if d.epochChanged {
		if _, err := tx.Exec(`UPDATE streams SET current_key_epoch = ? WHERE stream_id = ?`, int64(d.newEpoch), streamID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE streams SET height = ? WHERE stream_id = ?`, int64(d.newBlock.header.Height), streamID); err != nil {
		return err
	}
	return tx.Commit()
}
