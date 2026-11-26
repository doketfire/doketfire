// Package realtime implements an in-memory SSE hub with per-client
// subscriptions, authorization callbacks and a bounded replay buffer.
package realtime

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/doketfire/doketfire/internal/collections"
)

// Event is a committed document change.
type Event struct {
	Seq        uint64               `json:"seq"`
	Action     string               `json:"action"` // create | update | delete
	Collection string               `json:"collection"`
	ID         string               `json:"id"`
	Record     collections.Document `json:"record,omitempty"`
	Old        collections.Document `json:"old,omitempty"`
}

// Identity is the principal used to authorize deliveries.
type Identity struct {
	Data  map[string]any
	Admin bool
}

// Authorizer decides whether an identity may receive an event for a document.
// Operation is "list" for collection subscriptions and "view" for document
// subscriptions.
type Authorizer func(identity Identity, operation, collection string, doc collections.Document) bool

// Client is an SSE connection.
type Client struct {
	ID   string
	ch   chan Event
	hub  *Hub
	subs map[string]struct{}
	auth Identity
	last uint64
}

// Events returns the delivery channel. It is closed when the client is
// disconnected for being too slow or when the hub closes.
func (c *Client) Events() <-chan Event { return c.ch }

// ErrClientNotFound indicates an unknown or disconnected client.
var ErrClientNotFound = errors.New("realtime client not found")

// ErrInvalidSubscription indicates a malformed subscription string.
var ErrInvalidSubscription = errors.New("invalid realtime subscription")

// Hub fans out events to connected clients.
type Hub struct {
	mu         sync.Mutex
	clients    map[string]*Client
	ring       []Event
	ringLimit  int
	clientBuf  int
	seq        uint64
	authorizer Authorizer
	closed     bool
}

// NewHub creates a hub. ringLimit bounds replay memory; clientBuf bounds each
// client's queue.
func NewHub(ringLimit, clientBuf int) *Hub {
	if ringLimit <= 0 {
		ringLimit = 256
	}
	if clientBuf <= 0 {
		clientBuf = 64
	}
	return &Hub{
		clients:   map[string]*Client{},
		ringLimit: ringLimit,
		clientBuf: clientBuf,
	}
}

// SetAuthorizer installs the access-control callback.
func (h *Hub) SetAuthorizer(a Authorizer) {
	h.mu.Lock()
	h.authorizer = a
	h.mu.Unlock()
}

// Connect registers a client, optionally replaying events after lastEventID.
// Subscriptions are empty until Subscribe is called.
func (h *Hub) Connect(lastEventID uint64) (*Client, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, errors.New("realtime hub is closed")
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	c := &Client{
		ID:   hex.EncodeToString(b),
		ch:   make(chan Event, h.clientBuf),
		hub:  h,
		subs: map[string]struct{}{},
		last: lastEventID,
	}
	h.clients[c.ID] = c
	return c, nil
}

// Unregister disconnects a client. It is safe to call multiple times.
func (h *Hub) Unregister(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if c, ok := h.clients[id]; ok {
		delete(h.clients, id)
		close(c.ch)
	}
}

// Subscribe replaces a client's subscription set. It also replays buffered
// events newer than the client's last event id.
func (h *Hub) Subscribe(clientID string, identity Identity, subscriptions []string) error {
	subs, err := parseSubscriptions(subscriptions)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	c, ok := h.clients[clientID]
	if !ok {
		return ErrClientNotFound
	}
	c.subs = subs
	c.auth = identity
	for _, ev := range h.ring {
		if ev.Seq <= c.last {
			continue
		}
		if h.shouldDeliverLocked(c, ev) {
			if !deliverLocked(c, ev) {
				return nil
			}
		}
	}
	return nil
}

// Sequence returns the sequence number of the last published event. New SSE
// connections use it to start live without replaying old history.
func (h *Hub) Sequence() uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.seq
}

// Publish assigns sequence numbers and fans out committed events. It must be
// called after the storage transaction commits.
func (h *Hub) Publish(events []Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	for _, ev := range events {
		h.seq++
		ev.Seq = h.seq
		h.ring = append(h.ring, ev)
		if len(h.ring) > h.ringLimit {
			h.ring = h.ring[len(h.ring)-h.ringLimit:]
		}
		for _, c := range h.clients {
			if h.shouldDeliverLocked(c, ev) {
				deliverLocked(c, ev)
			}
		}
	}
}

func (h *Hub) shouldDeliverLocked(c *Client, ev Event) bool {
	if len(c.subs) == 0 {
		return false
	}
	doc := ev.Record
	if ev.Action == "delete" && ev.Old != nil {
		doc = ev.Old
	}
	if doc == nil {
		return false
	}
	// Collection subscription => "list" permission. Document subscription =>
	// "view" permission. An identity needs access through at least one of them.
	for sub := range c.subs {
		collection, docID, isDoc := splitSubscription(sub)
		if collection != ev.Collection {
			continue
		}
		eventID := ev.ID
		if eventID == "" {
			eventID = doc.ID()
		}
		if isDoc && docID != eventID {
			continue
		}
		if h.authorizer == nil {
			return true
		}
		op := "list"
		if isDoc {
			op = "view"
		}
		if h.authorizer(c.auth, op, collection, doc) {
			return true
		}
	}
	return false
}

// deliverLocked sends without blocking. Slow clients are disconnected.
func deliverLocked(c *Client, ev Event) bool {
	select {
	case c.ch <- ev:
		c.last = ev.Seq
		return true
	default:
		delete(c.hub.clients, c.ID)
		close(c.ch)
		return false
	}
}

// Close disconnects every client and prevents new connections.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for id, c := range h.clients {
		delete(h.clients, id)
		close(c.ch)
	}
}

// ClientCount returns the number of connected clients.
func (h *Hub) ClientCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.clients)
}

func parseSubscriptions(subs []string) (map[string]struct{}, error) {
	if len(subs) > 100 {
		return nil, fmt.Errorf("%w: too many subscriptions", ErrInvalidSubscription)
	}
	out := make(map[string]struct{}, len(subs))
	for _, raw := range subs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		collection, id, hasID := splitSubscription(raw)
		if !collections.ValidName(collection) {
			return nil, fmt.Errorf("%w: invalid collection %q", ErrInvalidSubscription, collection)
		}
		if hasID && !collections.ValidID(id) {
			return nil, fmt.Errorf("%w: invalid document id %q", ErrInvalidSubscription, id)
		}
		normalized := collection
		if hasID {
			normalized = collection + "/" + id
		}
		out[normalized] = struct{}{}
	}
	return out, nil
}

func splitSubscription(sub string) (collection, id string, isDoc bool) {
	if i := strings.IndexByte(sub, '/'); i >= 0 {
		return sub[:i], sub[i+1:], true
	}
	return sub, "", false
}
