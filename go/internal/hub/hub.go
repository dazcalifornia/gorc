// Package hub is a websocket fan-out: one broadcast reaches every connected
// client, and a client that cannot keep up is dropped rather than allowed to
// block the producer.
package hub

import (
	"encoding/json"
	"sync"
)

// clientBuffer is how many messages a slow client may fall behind before it is
// disconnected. A pulse oximeter produces ~60 waveform samples a second, so
// this is a few seconds of grace.
const clientBuffer = 256

// Client is one websocket subscriber.
type Client struct {
	send   chan []byte
	closed chan struct{}
	once   sync.Once
}

// Send is the channel the websocket writer pumps to the socket.
func (c *Client) Send() <-chan []byte { return c.send }

// Closed is signalled when the hub drops this client for falling behind.
func (c *Client) Closed() <-chan struct{} { return c.closed }

func (c *Client) kill() { c.once.Do(func() { close(c.closed) }) }

// Hub tracks subscribers and broadcasts to all of them.
type Hub struct {
	mu      sync.RWMutex
	clients map[*Client]struct{}
}

func New() *Hub {
	return &Hub{clients: map[*Client]struct{}{}}
}

// Subscribe registers a new client.
func (h *Hub) Subscribe() *Client {
	c := &Client{send: make(chan []byte, clientBuffer), closed: make(chan struct{})}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	return c
}

// Unsubscribe removes a client. Safe to call more than once.
func (h *Hub) Unsubscribe(c *Client) {
	h.mu.Lock()
	delete(h.clients, c)
	h.mu.Unlock()
	c.kill()
}

// Broadcast marshals v once and delivers it to every client. Clients whose
// buffer is full are dropped: a stalled browser tab must not be able to apply
// backpressure all the way down to the device read loop.
func (h *Hub) Broadcast(v any) {
	payload, err := json.Marshal(v)
	if err != nil {
		return
	}

	h.mu.RLock()
	targets := make([]*Client, 0, len(h.clients))
	for c := range h.clients {
		targets = append(targets, c)
	}
	h.mu.RUnlock()

	var stalled []*Client
	for _, c := range targets {
		select {
		case c.send <- payload:
		default:
			stalled = append(stalled, c)
		}
	}
	if len(stalled) == 0 {
		return
	}
	h.mu.Lock()
	for _, c := range stalled {
		delete(h.clients, c)
	}
	h.mu.Unlock()
	for _, c := range stalled {
		c.kill()
	}
}

// Count reports connected clients, for /api/health.
func (h *Hub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}
