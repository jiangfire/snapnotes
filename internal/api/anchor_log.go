package api

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// AnchorRecord is one append-only external root anchor: a signed checkpoint of the
// active chain's tip at the moment a block was accepted. The log is append-only and
// tamper-evident by position — an operator can periodically copy it to an immutable
// external location (e.g. a write-once bucket, a public ledger, a notary) to gain a
// third-party-verifiable root. This is the minimal, dependency-free realisation of
// Phase 5 "external root anchoring"; a stronger scheme (Merkle-tree over anchors,
// signature by the owner key) can layer on top later.
type AnchorRecord struct {
	Height    uint64 `json:"height"`
	BlockHash string `json:"block_hash"`
	MMRRoot   string `json:"mmr_root"`
}

// anchorLog is an append-only JSON-lines log of chain tip checkpoints.
type anchorLog struct {
	mu   sync.Mutex
	path string
	f    *os.File
}

func openAnchorLog(dataDir string) (*anchorLog, error) {
	if dataDir == "" {
		return nil, nil
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return nil, err
	}
	p := filepath.Join(dataDir, "anchors.log")
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &anchorLog{path: p, f: f}, nil
}

// append writes one anchor record. It is safe to call concurrently (serialised by
// the mutex) and returns immediately if the log is disabled (nil receiver).
func (a *anchorLog) append(rec AnchorRecord) error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	line, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if _, err := a.f.Write(line); err != nil {
		return err
	}
	return nil
}

// ReadAnchors returns every anchor record in order. It reads the file fresh so it
// reflects disk state (e.g. after a restart). A nil log yields an empty slice.
func (a *anchorLog) ReadAnchors() ([]AnchorRecord, error) {
	if a == nil {
		return nil, nil
	}
	f, err := os.Open(a.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []AnchorRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var rec AnchorRecord
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, sc.Err()
}

func (a *anchorLog) Close() error {
	if a == nil {
		return nil
	}
	return a.f.Close()
}
