package sqlite

import "errors"

// SyncState is the local device's progress along a stream's chain. It is the
// saved anchor the client uses to catch up after a restart or a dropped
// notification. GenesisBlockHash is the out-of-band trust anchor; LastBlockHash
// and LastBlockHeight pin the most recent block the device has verified.
type SyncState struct {
	StreamID         []byte
	LastBlockHeight  uint64
	LastBlockHash    []byte
	LastMMRRoot      []byte
	LastPeaks        []byte // concatenated 32-byte MMR peaks after the last verified block
	LastChainwork    []byte // 32-byte big-endian cumulative chainwork of the active tip
	GenesisBlockHash []byte
	DeviceID         string
}

// SaveSyncState records the device's verified chain position for a stream.
func (s *Store) SaveSyncState(state SyncState) error {
	if len(state.StreamID) != 32 {
		return errors.New("stream_id must be 32 bytes")
	}
	if len(state.GenesisBlockHash) != 32 {
		return errors.New("genesis_block_hash must be 32 bytes")
	}
	_, err := s.db.Exec(
		`INSERT INTO sync_state
			(stream_id, last_block_height, last_block_hash, last_mmr_root, last_peaks, last_chainwork, genesis_block_hash, device_id)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(stream_id) DO UPDATE SET
				last_block_height = excluded.last_block_height,
				last_block_hash = excluded.last_block_hash,
				last_mmr_root = excluded.last_mmr_root,
				last_peaks = excluded.last_peaks,
				last_chainwork = excluded.last_chainwork,
				genesis_block_hash = excluded.genesis_block_hash,
				device_id = excluded.device_id`,
		state.StreamID,
		int64(state.LastBlockHeight),
		state.LastBlockHash,
		state.LastMMRRoot,
		state.LastPeaks,
		state.LastChainwork,
		state.GenesisBlockHash,
		state.DeviceID,
	)
	return err
}

// GetSyncState returns the saved chain position for a stream. The boolean is false
// when the device has never synced that stream.
func (s *Store) GetSyncState(streamID []byte) (SyncState, bool, error) {
	rows, err := s.db.Query(
		`SELECT stream_id, last_block_height, last_block_hash, last_mmr_root, last_peaks, last_chainwork, genesis_block_hash, device_id
		 FROM sync_state WHERE stream_id = ?`,
		streamID,
	)
	if err != nil {
		return SyncState{}, false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return SyncState{}, false, nil
	}
	var st SyncState
	var height int64
	if err := rows.Scan(&st.StreamID, &height, &st.LastBlockHash, &st.LastMMRRoot, &st.LastPeaks, &st.LastChainwork, &st.GenesisBlockHash, &st.DeviceID); err != nil {
		return SyncState{}, false, err
	}
	st.LastBlockHeight = uint64(height)
	if err := rows.Err(); err != nil {
		return SyncState{}, false, err
	}
	return st, true, nil
}
