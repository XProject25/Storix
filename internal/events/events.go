// Package events is the server sent events hub of Storix.
//
// One hub instance fans a message out to every browser that is currently
// streaming /api/v1/events. Delivery is best effort on purpose: a subscriber
// that stopped reading, because the tab is frozen or the network stalled,
// loses its oldest queued event instead of holding up the goroutine that
// produced the new one. Progress reporting must never be able to block a file
// operation.
//
// Storix - modern web file manager for servers.
// Developed by X Project.
package events

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// Event types published by Storix. The frontend switches on these strings.
const (
	// EventJobCreated is sent when a background operation is accepted.
	EventJobCreated = "job.created"
	// EventJobProgress carries the live counters of a running operation.
	EventJobProgress = "job.progress"
	// EventJobDone is sent when an operation finished, including cancelled.
	EventJobDone = "job.done"
	// EventJobFailed is sent when an operation ended with an error.
	EventJobFailed = "job.failed"
	// EventFSChanged tells the UI that a directory needs to be reloaded.
	EventFSChanged = "fs.changed"
	// EventUploadProgress reports resumable upload offsets.
	EventUploadProgress = "upload.progress"
	// EventUploadDone reports a finished resumable upload.
	EventUploadDone = "upload.done"
	// EventShareChanged reports a created, edited or revoked public link.
	EventShareChanged = "share.changed"
	// EventSystemNotice carries a message meant for a toast.
	EventSystemNotice = "system.notice"
	// EventHeartbeat is the keep alive emitted by StartHeartbeat. It exists so
	// idle connections survive proxy and load balancer read timeouts.
	EventHeartbeat = "heartbeat"
)

// buffer is how many events a single subscriber may fall behind before the
// hub starts discarding its oldest queued event.
const buffer = 64

// Event is one message delivered on a server sent event stream.
type Event struct {
	Type string    `json:"type"`
	Data any       `json:"data,omitempty"`
	At   time.Time `json:"at"`
}

// New builds an event stamped with the current time.
func New(typ string, data any) Event {
	return Event{Type: typ, Data: data, At: time.Now().UTC()}
}

// subscriber is one open stream.
type subscriber struct {
	userID int64
	ch     chan Event
	// closed is written only while the hub write lock is held, so a publisher
	// holding the read lock can never send on a closed channel.
	closed bool
}

// Hub fans events out to the connected streams.
//
// The zero value is not usable; call NewHub.
type Hub struct {
	mu      sync.RWMutex
	subs    map[*subscriber]struct{}
	closed  bool
	done    chan struct{}
	dropped atomic.Uint64
}

// NewHub creates an empty hub.
func NewHub() *Hub {
	return &Hub{
		subs: make(map[*subscriber]struct{}),
		done: make(chan struct{}),
	}
}

// Subscribe registers a stream for one user and returns the delivery channel
// together with the function that tears it down. The returned function
// unsubscribes and closes the channel exactly once, so a handler can defer it
// and still call it explicitly. Subscribing to a closed hub returns an already
// closed channel rather than one that never delivers.
func (h *Hub) Subscribe(userID int64) (<-chan Event, func()) {
	s := &subscriber{userID: userID, ch: make(chan Event, buffer)}

	h.mu.Lock()
	if h.closed {
		s.closed = true
		h.mu.Unlock()
		close(s.ch)
		return s.ch, func() {}
	}
	h.subs[s] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	return s.ch, func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			delete(h.subs, s)
			if !s.closed {
				s.closed = true
				close(s.ch)
			}
		})
	}
}

// Publish delivers an event to the streams of one user. A userID of 0 reaches
// every subscriber. The call never blocks: a subscriber that is not keeping up
// loses its oldest queued event to make room for this one.
func (h *Hub) Publish(userID int64, e Event) {
	if h == nil {
		return
	}
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.closed {
		return
	}
	for s := range h.subs {
		if userID != 0 && s.userID != userID {
			continue
		}
		h.deliver(s, e)
	}
}

// Broadcast delivers an event to every subscriber.
func (h *Hub) Broadcast(e Event) { h.Publish(0, e) }

// deliver queues one event for one subscriber. The caller holds at least the
// read lock, which is what keeps the channel from being closed underneath.
func (h *Hub) deliver(s *subscriber, e Event) {
	select {
	case s.ch <- e:
		return
	default:
	}
	// The stream is behind. Discard its oldest event and retry once. Receiving
	// here races with the subscriber's own receive, which is harmless: either
	// way one event is gone and the publisher keeps moving.
	select {
	case <-s.ch:
		h.dropped.Add(1)
	default:
	}
	select {
	case s.ch <- e:
	default:
		h.dropped.Add(1)
	}
}

// Subscribers reports how many streams are currently connected.
func (h *Hub) Subscribers() int {
	if h == nil {
		return 0
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.subs)
}

// Dropped reports how many events were discarded because a subscriber was too
// slow. It is exposed for the system status screen.
func (h *Hub) Dropped() uint64 {
	if h == nil {
		return 0
	}
	return h.dropped.Load()
}

// StartHeartbeat emits a keep alive to every subscriber on a fixed interval
// until the context is cancelled or the hub is closed. Reverse proxies drop
// connections that stay silent, so an idle event stream needs traffic of its
// own. A non positive interval disables the heartbeat.
func (h *Hub) StartHeartbeat(ctx context.Context, every time.Duration) {
	if h == nil || every <= 0 {
		return
	}
	go func() {
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-h.done:
				return
			case <-t.C:
				h.Broadcast(New(EventHeartbeat, nil))
			}
		}
	}()
}

// Close disconnects every subscriber and makes further publishing a no-op.
// It is safe to call more than once.
func (h *Hub) Close() {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return
	}
	h.closed = true
	close(h.done)
	for s := range h.subs {
		if !s.closed {
			s.closed = true
			close(s.ch)
		}
		delete(h.subs, s)
	}
}
