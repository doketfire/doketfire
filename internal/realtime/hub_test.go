package realtime

import (
	"testing"
	"time"

	"github.com/doketfire/doketfire/internal/collections"
)

func doc(id, owner string) collections.Document {
	return collections.Document{"id": id, "owner": owner}
}

func TestPublishSubscriptionsAndAuthorization(t *testing.T) {
	h := NewHub(10, 8)
	defer h.Close()
	h.SetAuthorizer(func(id Identity, op, col string, d collections.Document) bool {
		if id.Admin {
			return true
		}
		return id.Data["id"] == d["owner"]
	})

	c, err := h.Connect(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Subscribe(c.ID, Identity{Data: map[string]any{"id": "u1"}}, []string{"posts"}); err != nil {
		t.Fatal(err)
	}
	id1 := collections.NewID()
	id2 := collections.NewID()
	other, err := h.Connect(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Subscribe(other.ID, Identity{Data: map[string]any{"id": "u2"}}, []string{"posts/" + id2}); err != nil {
		t.Fatal(err)
	}

	h.Publish([]Event{
		{Action: "create", Collection: "posts", ID: id1, Record: doc(id1, "u1")},
		{Action: "create", Collection: "comments", ID: id2, Record: doc(id2, "u1")},
	})
	select {
	case ev := <-c.Events():
		if ev.ID != id1 || ev.Seq != 1 {
			t.Fatalf("unexpected event: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("expected event")
	}
	select {
	case ev := <-other.Events():
		t.Fatalf("other client should not receive: %+v", ev)
	case <-time.After(20 * time.Millisecond):
	}

	// Document subscription.
	h.Publish([]Event{{Action: "update", Collection: "posts", ID: id2, Record: doc(id2, "u2")}})
	select {
	case ev := <-other.Events():
		if ev.Action != "update" {
			t.Fatalf("unexpected: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("expected doc event")
	}

	// Unauthorized events are not delivered.
	h.Publish([]Event{{Action: "update", Collection: "posts", ID: id2, Record: doc(id2, "u1")}})
	select {
	case ev := <-other.Events():
		t.Fatalf("unauthorized event leaked: %+v", ev)
	case <-time.After(20 * time.Millisecond):
	}
}

func TestReplay(t *testing.T) {
	h := NewHub(10, 8)
	defer h.Close()
	h.SetAuthorizer(func(Identity, string, string, collections.Document) bool { return true })

	c1, _ := h.Connect(0)
	if err := h.Subscribe(c1.ID, Identity{}, []string{"posts"}); err != nil {
		t.Fatal(err)
	}
	id := collections.NewID()
	h.Publish([]Event{{Action: "create", Collection: "posts", ID: id, Record: doc(id, "")}})
	select {
	case ev := <-c1.Events():
		if ev.Seq != 1 {
			t.Fatalf("seq = %d", ev.Seq)
		}
	case <-time.After(time.Second):
		t.Fatal("no live event")
	}

	// A reconnecting client asks for events after seq 0 and gets the buffer
	// once it subscribes.
	c2, _ := h.Connect(0)
	if err := h.Subscribe(c2.ID, Identity{}, []string{"posts"}); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-c2.Events():
		if ev.Seq != 1 {
			t.Fatalf("replay seq = %d", ev.Seq)
		}
	case <-time.After(time.Second):
		t.Fatal("no replay event")
	}

	// A client that already saw seq 1 gets only newer events.
	h.Publish([]Event{{Action: "delete", Collection: "posts", ID: id, Old: doc(id, "")}})
	c3, _ := h.Connect(1)
	if err := h.Subscribe(c3.ID, Identity{}, []string{"posts"}); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-c3.Events():
		if ev.Seq != 2 || ev.Action != "delete" {
			t.Fatalf("unexpected replay: %+v", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("missing replay")
	}
}

func TestSlowClientDisconnected(t *testing.T) {
	h := NewHub(10, 1)
	defer h.Close()
	h.SetAuthorizer(func(Identity, string, string, collections.Document) bool { return true })
	c, _ := h.Connect(0)
	if err := h.Subscribe(c.ID, Identity{}, []string{"posts"}); err != nil {
		t.Fatal(err)
	}
	id := collections.NewID()
	h.Publish([]Event{
		{Action: "create", Collection: "posts", ID: id, Record: doc(id, "")},
		{Action: "update", Collection: "posts", ID: id, Record: doc(id, "")},
	})
	if h.ClientCount() != 0 {
		t.Fatalf("slow client should be disconnected, count=%d", h.ClientCount())
	}
	// The buffered event is still readable, then the channel is closed.
	<-c.Events()
	if _, ok := <-c.Events(); ok {
		t.Fatal("expected closed channel")
	}
}

func TestSubscriptionValidation(t *testing.T) {
	h := NewHub(10, 8)
	defer h.Close()
	c, _ := h.Connect(0)
	if err := h.Subscribe(c.ID, Identity{}, []string{"bad name"}); err == nil {
		t.Fatal("expected invalid collection error")
	}
	if err := h.Subscribe(c.ID, Identity{}, []string{"posts/not-a-ulid"}); err == nil {
		t.Fatal("expected invalid id error")
	}
	if err := h.Subscribe(c.ID, Identity{}, []string{"posts"}); err != nil {
		t.Fatalf("valid subscription rejected: %v", err)
	}
	if err := h.Subscribe(c.ID, Identity{}, nil); err != nil {
		t.Fatalf("empty subscription should clear: %v", err)
	}
	if err := h.Subscribe("missing", Identity{}, nil); err != ErrClientNotFound {
		t.Fatalf("expected not found, got %v", err)
	}
}
