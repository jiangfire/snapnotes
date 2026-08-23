package api

import (
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

var wsUpgrader = websocket.Upgrader{
	// The MVP trusts any origin; a production deployment must restrict this.
	CheckOrigin: func(r *http.Request) bool { return true },
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
}

// wsHub tracks open event subscriptions and broadcasts block notifications.
// Notifications carry no payload: clients must re-pull blocks by height, so a
// dropped notification can never cause data loss.
type wsHub struct {
	mu    sync.Mutex
	conns map[*websocket.Conn]struct{}
}

func newWSHub() *wsHub {
	return &wsHub{conns: make(map[*websocket.Conn]struct{})}
}

func (h *wsHub) add(c *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.conns[c] = struct{}{}
}

func (h *wsHub) remove(c *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.conns, c)
}

func (h *wsHub) broadcast(height uint64) {
	h.mu.Lock()
	conns := make([]*websocket.Conn, 0, len(h.conns))
	for c := range h.conns {
		conns = append(conns, c)
	}
	h.mu.Unlock()
	msg := wsMessage{Type: "new_block", Height: height}
	for _, c := range conns {
		c.SetWriteDeadline(time.Now().Add(2 * time.Second))
		if err := c.WriteJSON(msg); err != nil {
			c.Close()
			h.remove(c)
		}
	}
}

type wsMessage struct {
	Type   string `json:"type"`
	Height uint64 `json:"height"`
}

// handleEvents upgrades to a notification-only WebSocket. It never pushes block
// data; clients re-fetch via /blocks after receiving a notification.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	streamID, err := decodeStreamID(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeInvalidEncoding, "invalid stream_id")
		return
	}
	// The stream must exist; otherwise there is nothing to subscribe to.
	if _, ok := s.ledger.tip(streamID); !ok {
		writeError(w, http.StatusNotFound, CodeStreamNotFound, "stream not found")
		return
	}
	c, err := wsUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	s.hub.add(c)
	defer func() {
		s.hub.remove(c)
		_ = c.Close()
	}()
	// Read loop: we only need it to detect client disconnect. Control frames
	// (ping/pong/close) are handled by the gorilla library automatically.
	for {
		if _, _, err := c.ReadMessage(); err != nil {
			return
		}
	}
}
