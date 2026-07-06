package actioncable

import "sync"

// Adapter is the pub-sub backend Action Cable fans broadcasts out through. A
// broadcasting is an opaque string channel; payloads are the already-encoded
// JSON bytes produced by [Server.Broadcast]. Both [AsyncAdapter] and
// [RedisAdapter] satisfy it.
//
// Subscribe registers handler for a broadcasting and returns a function that
// removes that subscription. Handlers are invoked with each broadcast payload.
type Adapter interface {
	Broadcast(broadcasting string, payload []byte) error
	Subscribe(broadcasting string, handler func(payload []byte)) (unsubscribe func(), err error)
}

// AsyncAdapter is the in-process pub-sub adapter — the analogue of Action
// Cable's :async adapter. Fan-out is synchronous and deterministic: Broadcast
// invokes every current subscriber's handler on the calling goroutine and
// returns when they have all run. It leaks no goroutines.
type AsyncAdapter struct {
	mu   sync.Mutex
	subs map[string][]*asyncSub
}

type asyncSub struct {
	handler func([]byte)
}

// NewAsyncAdapter returns an empty in-process adapter.
func NewAsyncAdapter() *AsyncAdapter {
	return &AsyncAdapter{subs: map[string][]*asyncSub{}}
}

// Subscribe registers handler for broadcasting.
func (a *AsyncAdapter) Subscribe(broadcasting string, handler func([]byte)) (func(), error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	sub := &asyncSub{handler: handler}
	a.subs[broadcasting] = append(a.subs[broadcasting], sub)
	return func() { a.unsubscribe(broadcasting, sub) }, nil
}

func (a *AsyncAdapter) unsubscribe(broadcasting string, sub *asyncSub) {
	a.mu.Lock()
	defer a.mu.Unlock()
	subs := a.subs[broadcasting]
	for i, s := range subs {
		if s == sub {
			a.subs[broadcasting] = append(subs[:i], subs[i+1:]...)
			break
		}
	}
	if len(a.subs[broadcasting]) == 0 {
		delete(a.subs, broadcasting)
	}
}

// Broadcast synchronously delivers payload to every current subscriber of
// broadcasting. Subscribers are snapshotted first so a handler may (un)subscribe
// during delivery without racing the iteration.
func (a *AsyncAdapter) Broadcast(broadcasting string, payload []byte) error {
	a.mu.Lock()
	subs := append([]*asyncSub(nil), a.subs[broadcasting]...)
	a.mu.Unlock()
	for _, s := range subs {
		s.handler(payload)
	}
	return nil
}
