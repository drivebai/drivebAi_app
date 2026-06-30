package ws

import (
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/google/uuid"
)

// Event represents a WebSocket event to be sent to clients.
type Event struct {
	Type    string      `json:"type"`
	Payload interface{} `json:"payload"`
	// TargetUserIDs specifies which users should receive this event.
	// If empty, event is dropped (no broadcast to all).
	TargetUserIDs []uuid.UUID `json:"-"`
}

// Hub manages all active WebSocket connections grouped by user ID.
type Hub struct {
	// connections maps userID → set of active connections
	connections map[uuid.UUID]map[*Conn]bool

	register   chan *Conn
	unregister chan *Conn
	broadcast  chan *Event

	mu     sync.RWMutex
	logger *slog.Logger
}

// NewHub creates a new Hub.
func NewHub(logger *slog.Logger) *Hub {
	return &Hub{
		connections: make(map[uuid.UUID]map[*Conn]bool),
		register:    make(chan *Conn),
		unregister:  make(chan *Conn),
		broadcast:   make(chan *Event, 256),
		logger:      logger,
	}
}

// Run starts the hub's event loop. Should be called in a goroutine.
func (h *Hub) Run() {
	for {
		select {
		case conn := <-h.register:
			h.mu.Lock()
			if h.connections[conn.UserID] == nil {
				h.connections[conn.UserID] = make(map[*Conn]bool)
			}
			h.connections[conn.UserID][conn] = true
			h.mu.Unlock()
			h.logger.Debug("ws client registered", "user_id", conn.UserID)

		case conn := <-h.unregister:
			h.mu.Lock()
			if conns, ok := h.connections[conn.UserID]; ok {
				if _, exists := conns[conn]; exists {
					delete(conns, conn)
					close(conn.send)
					if len(conns) == 0 {
						delete(h.connections, conn.UserID)
					}
				}
			}
			h.mu.Unlock()
			h.logger.Debug("ws client unregistered", "user_id", conn.UserID)

		case event := <-h.broadcast:
			data, err := json.Marshal(event)
			if err != nil {
				h.logger.Error("ws marshal event failed", "error", err)
				continue
			}

			h.mu.RLock()
			for _, uid := range event.TargetUserIDs {
				if conns, ok := h.connections[uid]; ok {
					for conn := range conns {
						select {
						case conn.send <- data:
						default:
							// Slow client, skip
							h.logger.Warn("ws slow client, dropping message", "user_id", uid)
						}
					}
				}
			}
			h.mu.RUnlock()
		}
	}
}

// Broadcast sends an event to the target users.
func (h *Hub) Broadcast(event *Event) {
	if len(event.TargetUserIDs) == 0 {
		return
	}
	h.broadcast <- event
}

// Register adds a connection to the hub.
func (h *Hub) Register(conn *Conn) {
	h.register <- conn
}

// Unregister removes a connection from the hub.
func (h *Hub) Unregister(conn *Conn) {
	h.unregister <- conn
}

// IsUserOnline checks if a user has at least one active connection.
func (h *Hub) IsUserOnline(userID uuid.UUID) bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	conns, ok := h.connections[userID]
	return ok && len(conns) > 0
}

// IsSubscribedToChat reports whether the user is currently considered
// "actively present" for a chat — used by the push dispatcher to suppress
// a chat_message push when the recipient is already foregrounded on a WS
// connection and would otherwise get both an in-app banner AND a push.
//
// We don't (yet) track per-chat subscriptions explicitly — the iOS client
// receives all `new_message` events for chats it participates in via the
// user-scoped WS pipe. So treating "any active WS connection for the user"
// as "subscribed to all of their chats" is the right approximation for v1:
//   - False positives (suppressing a push the user wanted because they
//     have the app open on a different screen) are tolerable: the in-app
//     bell + WS still fire, and the recipient does see the message.
//   - False negatives (sending a push despite an open WS) would mean the
//     user gets a banner AND a push for the same message — annoying.
//
// chatID is accepted for the future when we plumb per-chat subscription
// tracking through the conn layer.
func (h *Hub) IsSubscribedToChat(userID uuid.UUID, _ uuid.UUID) bool {
	return h.IsUserOnline(userID)
}
