// Storix - modern web file manager for servers.
// Developed by X Project.
package events

import (
	"context"
	"testing"
	"time"
)

// recv waits for one event or fails the test.
func recv(t *testing.T, ch <-chan Event, within time.Duration) Event {
	t.Helper()
	select {
	case e, ok := <-ch:
		if !ok {
			t.Fatal("channel closed while waiting for an event")
		}
		return e
	case <-time.After(within):
		t.Fatal("timed out waiting for an event")
	}
	return Event{}
}

func TestSubscribeAndReceive(t *testing.T) {
	h := NewHub()
	defer h.Close()

	ch, cancel := h.Subscribe(7)
	defer cancel()

	if got := h.Subscribers(); got != 1 {
		t.Fatalf("Subscribers = %d, want 1", got)
	}

	h.Publish(7, New(EventJobCreated, map[string]string{"id": "abc"}))
	e := recv(t, ch, time.Second)
	if e.Type != EventJobCreated {
		t.Fatalf("Type = %q, want %q", e.Type, EventJobCreated)
	}
	if e.At.IsZero() {
		t.Fatal("event timestamp is zero")
	}
}

func TestPublishTargetsOneUser(t *testing.T) {
	h := NewHub()
	defer h.Close()

	mine, cancelMine := h.Subscribe(1)
	defer cancelMine()
	theirs, cancelTheirs := h.Subscribe(2)
	defer cancelTheirs()

	h.Publish(1, New(EventFSChanged, nil))
	if e := recv(t, mine, time.Second); e.Type != EventFSChanged {
		t.Fatalf("Type = %q, want %q", e.Type, EventFSChanged)
	}
	select {
	case e := <-theirs:
		t.Fatalf("unrelated subscriber received %q", e.Type)
	case <-time.After(100 * time.Millisecond):
	}
}

func TestUnsubscribeTwiceDoesNotPanic(t *testing.T) {
	h := NewHub()
	defer h.Close()

	ch, cancel := h.Subscribe(3)
	cancel()
	cancel()

	if got := h.Subscribers(); got != 0 {
		t.Fatalf("Subscribers = %d, want 0", got)
	}
	if _, ok := <-ch; ok {
		t.Fatal("channel should be closed after unsubscribe")
	}
	// Publishing after the last subscriber left must stay harmless.
	h.Publish(3, New(EventSystemNotice, "gone"))
}

func TestUnsubscribeAfterCloseDoesNotPanic(t *testing.T) {
	h := NewHub()
	ch, cancel := h.Subscribe(4)
	h.Close()
	h.Close()
	cancel()
	if _, ok := <-ch; ok {
		t.Fatal("channel should be closed after the hub is closed")
	}

	// A stream that arrives after shutdown gets a closed channel, not a stall.
	late, lateCancel := h.Subscribe(4)
	defer lateCancel()
	if _, ok := <-late; ok {
		t.Fatal("late subscriber should receive a closed channel")
	}
}

func TestSlowConsumerDoesNotBlockPublisher(t *testing.T) {
	h := NewHub()
	defer h.Close()

	// Subscribe and never read.
	_, cancel := h.Subscribe(5)
	defer cancel()

	const count = buffer * 50
	done := make(chan struct{})
	go func() {
		for i := 0; i < count; i++ {
			h.Publish(5, New(EventJobProgress, i))
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publisher blocked on a subscriber that is not reading")
	}
	if h.Dropped() == 0 {
		t.Fatal("expected the hub to report dropped events")
	}
}

func TestSlowConsumerKeepsNewestEvents(t *testing.T) {
	h := NewHub()
	defer h.Close()

	ch, cancel := h.Subscribe(6)
	defer cancel()

	for i := 0; i < buffer*3; i++ {
		h.Publish(6, New(EventJobProgress, i))
	}
	// The queue holds the newest events, so the first one read must not be
	// the very first one published.
	e := recv(t, ch, time.Second)
	if v, ok := e.Data.(int); ok && v == 0 {
		t.Fatal("oldest event survived, drop-oldest did not run")
	}
}

func TestBroadcastReachesEverySubscriber(t *testing.T) {
	h := NewHub()
	defer h.Close()

	a, cancelA := h.Subscribe(11)
	defer cancelA()
	b, cancelB := h.Subscribe(22)
	defer cancelB()

	h.Broadcast(New(EventSystemNotice, "maintenance"))

	for i, ch := range []<-chan Event{a, b} {
		e := recv(t, ch, time.Second)
		if e.Type != EventSystemNotice {
			t.Fatalf("subscriber %d got %q, want %q", i, e.Type, EventSystemNotice)
		}
	}
}

func TestHeartbeat(t *testing.T) {
	h := NewHub()
	defer h.Close()

	ctx, cancelCtx := context.WithCancel(context.Background())
	defer cancelCtx()

	ch, cancel := h.Subscribe(9)
	defer cancel()

	h.StartHeartbeat(ctx, 10*time.Millisecond)
	e := recv(t, ch, 2*time.Second)
	if e.Type != EventHeartbeat {
		t.Fatalf("Type = %q, want %q", e.Type, EventHeartbeat)
	}
}
