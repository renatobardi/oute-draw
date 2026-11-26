// Package hub relays live collaboration messages between the people who have the
// same scene open. It keeps no scene state: persistence goes through the store, so
// losing a room loses nothing but cursors.
package hub

import (
	"encoding/json"
	"sync"
)

type Peer struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	CanEdit bool   `json:"canEdit"`
}

type Client struct {
	Peer
	// Send is drained by the connection's writer; a full buffer drops the client.
	Send chan []byte
	room *room
}

type room struct {
	clients map[*Client]struct{}
}

type Hub struct {
	mu    sync.Mutex
	rooms map[string]*room
}

func New() *Hub { return &Hub{rooms: map[string]*room{}} }

func marshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// Join adds the client to the scene's room, tells it who is there and tells the
// others it arrived.
func (h *Hub) Join(sceneID string, p Peer) *Client {
	c := &Client{Peer: p, Send: make(chan []byte, 256)}
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.rooms[sceneID]
	if r == nil {
		r = &room{clients: map[*Client]struct{}{}}
		h.rooms[sceneID] = r
	}
	peers := make([]Peer, 0, len(r.clients))
	for other := range r.clients {
		peers = append(peers, other.Peer)
	}
	h.fanout(r, c, marshal(map[string]any{"t": "join", "peer": p}))
	r.clients[c] = struct{}{}
	c.room = r
	c.Send <- marshal(map[string]any{"t": "hello", "id": p.ID, "peers": peers})
	return c
}

func (h *Hub) Leave(sceneID string, c *Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.rooms[sceneID]
	if r == nil {
		return
	}
	// A slow client may already have been dropped by fanout; the others still need
	// to hear that it left.
	if _, ok := r.clients[c]; ok {
		h.drop(r, c)
	}
	h.fanout(r, nil, marshal(map[string]any{"t": "leave", "id": c.ID}))
	if len(r.clients) == 0 {
		delete(h.rooms, sceneID)
	}
}

// drop must be called with the lock held.
func (h *Hub) drop(r *room, c *Client) {
	delete(r.clients, c)
	close(c.Send)
}

// fanout must be called with the lock held. A client that cannot keep up is
// dropped: its writer sees the closed channel and ends the connection, and the
// client reconnects and reloads the scene.
func (h *Hub) fanout(r *room, except *Client, msg []byte) {
	for c := range r.clients {
		if c == except {
			continue
		}
		select {
		case c.Send <- msg:
		default:
			h.drop(r, c)
		}
	}
}

// Relay forwards a client message to the room, stamped with its sender. When to is
// set only that peer gets it (WebRTC signalling).
func (h *Hub) Relay(sceneID string, from *Client, to string, raw json.RawMessage) {
	msg := marshal(map[string]any{"t": "relay", "from": from.ID, "m": raw})
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.rooms[sceneID]
	if r == nil {
		return
	}
	if to == "" {
		h.fanout(r, from, msg)
		return
	}
	for c := range r.clients {
		if c.ID == to {
			select {
			case c.Send <- msg:
			default:
				h.drop(r, c)
			}
			return
		}
	}
}

// Notify sends a server event to everyone in the room, e.g. "comments" or "reload".
func (h *Hub) Notify(sceneID string, event string, extra map[string]any) {
	m := map[string]any{"t": event}
	for k, v := range extra {
		m[k] = v
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if r := h.rooms[sceneID]; r != nil {
		h.fanout(r, nil, marshal(m))
	}
}

// Count is the number of people in a scene's room.
func (h *Hub) Count(sceneID string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if r := h.rooms[sceneID]; r != nil {
		return len(r.clients)
	}
	return 0
}
