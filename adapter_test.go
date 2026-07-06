package actioncable

import (
	"errors"
	"testing"
)

func TestAsyncAdapter_FanOutAndUnsubscribe(t *testing.T) {
	a := NewAsyncAdapter()
	var got1, got2 []string
	unsub1, _ := a.Subscribe("room", func(p []byte) { got1 = append(got1, string(p)) })
	_, _ = a.Subscribe("room", func(p []byte) { got2 = append(got2, string(p)) })
	_, _ = a.Subscribe("other", func(p []byte) { t.Fatal("wrong broadcasting delivered") })

	if err := a.Broadcast("room", []byte("m1")); err != nil {
		t.Fatal(err)
	}
	if len(got1) != 1 || len(got2) != 1 {
		t.Fatalf("fan-out failed: %v %v", got1, got2)
	}

	unsub1()
	_ = a.Broadcast("room", []byte("m2"))
	if len(got1) != 1 || len(got2) != 2 {
		t.Fatalf("unsubscribe failed: %v %v", got1, got2)
	}

	// Broadcast to a broadcasting with no subscribers is a no-op.
	if err := a.Broadcast("nobody", []byte("x")); err != nil {
		t.Fatal(err)
	}
}

func TestAsyncAdapter_UnsubscribeLastDeletesKeyAndDoubleUnsub(t *testing.T) {
	a := NewAsyncAdapter()
	unsub, _ := a.Subscribe("room", func([]byte) {})
	unsub() // removes last -> key deleted
	if _, ok := a.subs["room"]; ok {
		t.Fatal("expected key deleted when empty")
	}
	unsub() // double unsubscribe: not found, still safe
}

// failingClient exercises the RedisAdapter error paths.
type failingClient struct{ fail bool }

func (c *failingClient) Publish(string, []byte) error {
	if c.fail {
		return errors.New("publish boom")
	}
	return nil
}
func (c *failingClient) Subscribe(string, func([]byte)) (func(), error) {
	if c.fail {
		return nil, errors.New("subscribe boom")
	}
	return func() {}, nil
}

// memClient is a working in-memory RedisPubSub for the happy path.
type memClient struct {
	subs map[string][]func([]byte)
}

func newMemClient() *memClient { return &memClient{subs: map[string][]func([]byte){}} }
func (c *memClient) Publish(ch string, p []byte) error {
	for _, h := range c.subs[ch] {
		h(p)
	}
	return nil
}
func (c *memClient) Subscribe(ch string, h func([]byte)) (func(), error) {
	c.subs[ch] = append(c.subs[ch], h)
	return func() {}, nil
}

func TestRedisAdapter_Forwards(t *testing.T) {
	client := newMemClient()
	r := NewRedisAdapter(client)
	var got string
	if _, err := r.Subscribe("room", func(p []byte) { got = string(p) }); err != nil {
		t.Fatal(err)
	}
	if err := r.Broadcast("room", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if got != "hello" {
		t.Fatalf("got %q", got)
	}
}

func TestRedisAdapter_Errors(t *testing.T) {
	r := NewRedisAdapter(&failingClient{fail: true})
	if err := r.Broadcast("room", []byte("x")); err == nil {
		t.Fatal("expected publish error")
	}
	if _, err := r.Subscribe("room", func([]byte) {}); err == nil {
		t.Fatal("expected subscribe error")
	}
}

// RedisAdapter must satisfy Adapter.
var _ Adapter = (*RedisAdapter)(nil)
var _ Adapter = (*AsyncAdapter)(nil)
