package bridge

import (
	"sync"
	"sync/atomic"
)

// Event is a normalized update pushed to WebSocket subscribers.
type Event struct {
	Type    string `json:"type"` // "message", "read", "typing"
	ChatID  int64  `json:"chat_id"`
	MsgID   int    `json:"msg_id,omitempty"`
	From    string `json:"from,omitempty"`
	FromID  int64  `json:"from_id,omitempty"`
	Text    string `json:"text,omitempty"`
	Media   any    `json:"media,omitempty"`
	Ts      int64  `json:"ts,omitempty"`
	ReplyTo int    `json:"reply_to,omitempty"`
}

// broker fans out events to all subscribed WS clients.
type broker struct {
	mu      sync.RWMutex
	nextID  atomic.Uint64
	subs    map[uint64]chan Event
	bufSize int
}

func newBroker(bufSize int) *broker {
	return &broker{subs: map[uint64]chan Event{}, bufSize: bufSize}
}

// Subscribe returns a channel and an unsubscribe func.
func (b *broker) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, b.bufSize)
	id := b.nextID.Add(1)
	b.mu.Lock()
	b.subs[id] = ch
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		if c, ok := b.subs[id]; ok {
			delete(b.subs, id)
			close(c)
		}
		b.mu.Unlock()
	}
}

// Publish sends to all subscribers. Drops events for slow consumers rather
// than blocking the telegram update loop.
func (b *broker) Publish(e Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}
