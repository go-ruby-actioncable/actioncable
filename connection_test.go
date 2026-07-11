package actioncable

import (
	"errors"
	"testing"
	"time"
)

// capture is a fake Transport that records the frames written to a connection.
type capture struct{ frames []string }

func (c *capture) transport(p []byte) { c.frames = append(c.frames, string(p)) }
func (c *capture) last() string {
	if len(c.frames) == 0 {
		return ""
	}
	return c.frames[len(c.frames)-1]
}

// failingAdapter fails Subscribe, to exercise StreamFrom's error path.
type failingAdapter struct{}

func (failingAdapter) Broadcast(string, []byte) error { return nil }
func (failingAdapter) Subscribe(string, func([]byte)) (func(), error) {
	return nil, errors.New("subscribe boom")
}

// recording channel action that logs the (action,data) invocations.
type recorder struct {
	calls []string
	datas []any
}

func (r *recorder) action(_, action string, data any) any {
	r.calls = append(r.calls, action)
	r.datas = append(r.datas, data)
	return "ok"
}

func TestConnection_ConnectWelcome(t *testing.T) {
	cap := &capture{}
	srv := NewServer(NewAsyncAdapter())
	conn := NewConnection(srv, cap.transport, nil)
	conn.Connect()
	if cap.last() != `{"type":"welcome"}` {
		t.Fatalf("welcome frame: %s", cap.last())
	}
}

func TestConnection_ConnectHookIdentifiesAndAuthorizes(t *testing.T) {
	cap := &capture{}
	srv := NewServer(NewAsyncAdapter())
	conn := NewConnection(srv, cap.transport, nil).OnConnect(func(c *Connection) error {
		c.IdentifiedBy("current_user", "alice")
		return nil
	})
	if err := conn.Connect(); err != nil {
		t.Fatalf("authorized connect returned error: %v", err)
	}
	if conn.Identifier("current_user") != "alice" {
		t.Fatal("connect hook did not identify the connection")
	}
	if cap.last() != `{"type":"welcome"}` {
		t.Fatalf("welcome not sent after authorized connect: %s", cap.last())
	}
	if conn.Closed() {
		t.Fatal("authorized connection must stay open")
	}
}

func TestConnection_RejectUnauthorized(t *testing.T) {
	cap := &capture{}
	srv := NewServer(NewAsyncAdapter())
	conn := NewConnection(srv, cap.transport, nil).OnConnect(func(c *Connection) error {
		return c.RejectUnauthorized()
	})
	err := conn.Connect()
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized, got %v", err)
	}
	// The gem closes with reason "unauthorized", reconnect=false, and no welcome.
	if cap.last() != `{"type":"disconnect","reason":"unauthorized","reconnect":false}` {
		t.Fatalf("unauthorized disconnect frame: %s", cap.last())
	}
	for _, f := range cap.frames {
		if f == `{"type":"welcome"}` {
			t.Fatal("welcome must not be sent to a rejected connection")
		}
	}
	if !conn.Closed() {
		t.Fatal("rejected connection must be closed")
	}
}

func TestConnection_ConnectHookNonAuthorizationError(t *testing.T) {
	cap := &capture{}
	srv := NewServer(NewAsyncAdapter())
	boom := errors.New("db down")
	conn := NewConnection(srv, cap.transport, nil).OnConnect(func(c *Connection) error {
		return boom
	})
	err := conn.Connect()
	if !errors.Is(err, boom) {
		t.Fatalf("expected the hook's error, got %v", err)
	}
	// A non-authorization error neither closes the connection nor transmits.
	if conn.Closed() {
		t.Fatal("non-authorization error must not close the connection")
	}
	if len(cap.frames) != 0 {
		t.Fatalf("non-authorization error must transmit nothing, got %v", cap.frames)
	}
}

func TestConnection_SubscribeConfirmAndReceive(t *testing.T) {
	cap := &capture{}
	srv := NewServer(NewAsyncAdapter())
	rec := &recorder{}
	var created *Channel
	factory := func(conn *Connection, id string, p map[string]any) (*Channel, bool) {
		ch := NewChannel(conn, ChannelName(p["channel"].(string)), id, p, rec.action)
		created = ch
		return ch, true
	}
	conn := NewConnection(srv, cap.transport, factory)
	conn.Connect()

	id := `{"channel":"ChatChannel"}`
	if err := conn.Dispatch([]byte(`{"command":"subscribe","identifier":"{\"channel\":\"ChatChannel\"}"}`)); err != nil {
		t.Fatal(err)
	}
	if cap.last() != `{"identifier":"{\"channel\":\"ChatChannel\"}","type":"confirm_subscription"}` {
		t.Fatalf("confirm frame: %s", cap.last())
	}
	if !created.Confirmed() || created.Rejected() || created.Name() != "chat" {
		t.Fatalf("channel state: confirmed=%v rejected=%v name=%s", created.Confirmed(), created.Rejected(), created.Name())
	}
	if rec.calls[0] != "subscribed" {
		t.Fatalf("subscribed hook not run: %v", rec.calls)
	}
	if conn.Subscriptions() != 1 {
		t.Fatal("subscription not registered")
	}
	if _, ok := conn.Subscription(id); !ok {
		t.Fatal("subscription lookup failed")
	}

	// message with an explicit action
	if err := conn.Dispatch([]byte(`{"command":"message","identifier":"{\"channel\":\"ChatChannel\"}","data":"{\"action\":\"speak\",\"text\":\"hi\"}"}`)); err != nil {
		t.Fatal(err)
	}
	if rec.calls[len(rec.calls)-1] != "speak" {
		t.Fatalf("action dispatch failed: %v", rec.calls)
	}
	// message without an action -> "receive"
	if err := conn.Dispatch([]byte(`{"command":"message","identifier":"{\"channel\":\"ChatChannel\"}","data":"{\"text\":\"hi\"}"}`)); err != nil {
		t.Fatal(err)
	}
	if rec.calls[len(rec.calls)-1] != "receive" {
		t.Fatalf("receive default failed: %v", rec.calls)
	}
	// message with non-map data and with empty data both default to receive
	if err := conn.Dispatch([]byte(`{"command":"message","identifier":"{\"channel\":\"ChatChannel\"}","data":"\"scalar\""}`)); err != nil {
		t.Fatal(err)
	}
	if err := conn.Dispatch([]byte(`{"command":"message","identifier":"{\"channel\":\"ChatChannel\"}"}`)); err != nil {
		t.Fatal(err)
	}

	// unsubscribe
	if err := conn.Dispatch([]byte(`{"command":"unsubscribe","identifier":"{\"channel\":\"ChatChannel\"}"}`)); err != nil {
		t.Fatal(err)
	}
	if conn.Subscriptions() != 0 {
		t.Fatal("unsubscribe did not remove")
	}
	if rec.calls[len(rec.calls)-1] != "unsubscribed" {
		t.Fatalf("unsubscribed hook: %v", rec.calls)
	}
}

func TestConnection_AlreadySubscribed(t *testing.T) {
	cap := &capture{}
	srv := NewServer(NewAsyncAdapter())
	n := 0
	factory := func(conn *Connection, id string, p map[string]any) (*Channel, bool) {
		n++
		return NewChannel(conn, "chat", id, p, nil), true
	}
	conn := NewConnection(srv, cap.transport, factory)
	sub := []byte(`{"command":"subscribe","identifier":"{\"channel\":\"ChatChannel\"}"}`)
	_ = conn.Dispatch(sub)
	_ = conn.Dispatch(sub) // second is a no-op
	if n != 1 {
		t.Fatalf("factory called %d times, want 1", n)
	}
}

func TestConnection_Reject(t *testing.T) {
	cap := &capture{}
	srv := NewServer(NewAsyncAdapter())
	factory := func(conn *Connection, id string, p map[string]any) (*Channel, bool) {
		var ch *Channel
		action := func(_, act string, _ any) any {
			if act == "subscribed" {
				ch.Reject()
			}
			return nil
		}
		ch = NewChannel(conn, "chat", id, p, action)
		return ch, true
	}
	conn := NewConnection(srv, cap.transport, factory)
	if err := conn.Dispatch([]byte(`{"command":"subscribe","identifier":"{\"channel\":\"ChatChannel\"}"}`)); err != nil {
		t.Fatal(err)
	}
	if cap.last() != `{"identifier":"{\"channel\":\"ChatChannel\"}","type":"reject_subscription"}` {
		t.Fatalf("reject frame: %s", cap.last())
	}
	if conn.Subscriptions() != 0 {
		t.Fatal("rejected subscription must not be registered")
	}
}

func TestConnection_UnknownChannelClass(t *testing.T) {
	srv := NewServer(NewAsyncAdapter())
	factory := func(conn *Connection, id string, p map[string]any) (*Channel, bool) {
		return nil, false
	}
	conn := NewConnection(srv, (&capture{}).transport, factory)
	err := conn.Dispatch([]byte(`{"command":"subscribe","identifier":"{\"channel\":\"MissingChannel\"}"}`))
	if err == nil {
		t.Fatal("expected error for unknown channel class")
	}
}

func TestConnection_DispatchErrors(t *testing.T) {
	srv := NewServer(NewAsyncAdapter())
	factory := func(conn *Connection, id string, p map[string]any) (*Channel, bool) {
		return NewChannel(conn, "chat", id, p, nil), true
	}
	conn := NewConnection(srv, (&capture{}).transport, factory)

	if err := conn.Dispatch([]byte(`not json`)); err == nil {
		t.Fatal("bad frame should error")
	}
	if err := conn.Dispatch([]byte(`{"command":"bogus"}`)); err == nil {
		t.Fatal("unknown command should error")
	}
	// subscribe with an unparseable identifier
	if err := conn.Dispatch([]byte(`{"command":"subscribe","identifier":"{bad"}`)); err == nil {
		t.Fatal("bad identifier should error")
	}
	// unsubscribe an unknown subscription
	if err := conn.Dispatch([]byte(`{"command":"unsubscribe","identifier":"nope"}`)); err == nil {
		t.Fatal("unknown unsubscribe should error")
	}
	// message to an unknown subscription
	if err := conn.Dispatch([]byte(`{"command":"message","identifier":"nope","data":"{}"}`)); err == nil {
		t.Fatal("unknown message should error")
	}
	// message with malformed data
	_ = conn.Dispatch([]byte(`{"command":"subscribe","identifier":"{\"channel\":\"ChatChannel\"}"}`))
	if err := conn.Dispatch([]byte(`{"command":"message","identifier":"{\"channel\":\"ChatChannel\"}","data":"{bad"}`)); err == nil {
		t.Fatal("malformed message data should error")
	}
}

func TestConnection_BeatAndAdvance(t *testing.T) {
	cap := &capture{}
	srv := NewServer(NewAsyncAdapter())
	factory := func(conn *Connection, id string, p map[string]any) (*Channel, bool) {
		ch := NewChannel(conn, "chat", id, p, nil)
		return ch, true
	}
	conn := NewConnection(srv, cap.transport, factory)
	conn.Beat(1751800000)
	if cap.last() != `{"type":"ping","message":1751800000}` {
		t.Fatalf("ping frame: %s", cap.last())
	}

	_ = conn.Dispatch([]byte(`{"command":"subscribe","identifier":"{\"channel\":\"ChatChannel\"}"}`))
	ch, _ := conn.Subscription(`{"channel":"ChatChannel"}`)
	ticks := 0
	ch.Periodically(10*time.Millisecond, func() { ticks++ })
	conn.Advance(25 * time.Millisecond)
	if ticks != 2 {
		t.Fatalf("periodic timer fired %d times, want 2", ticks)
	}
}

func TestConnection_DisconnectClosesAndCleansUp(t *testing.T) {
	cap := &capture{}
	srv := NewServer(NewAsyncAdapter())
	factory := func(conn *Connection, id string, p map[string]any) (*Channel, bool) {
		var ch *Channel
		action := func(_, act string, _ any) any {
			if act == "subscribed" {
				_ = ch.StreamFrom("chat:1")
			}
			return nil
		}
		ch = NewChannel(conn, "chat", id, p, action)
		return ch, true
	}
	conn := NewConnection(srv, cap.transport, factory)
	conn.Connect()
	_ = conn.Dispatch([]byte(`{"command":"subscribe","identifier":"{\"channel\":\"ChatChannel\"}"}`))

	conn.Disconnect(DisconnectServerRestart, false)
	if cap.last() != `{"type":"disconnect","reason":"server_restart","reconnect":false}` {
		t.Fatalf("disconnect frame: %s", cap.last())
	}
	if !conn.Closed() || conn.Subscriptions() != 0 {
		t.Fatal("disconnect did not clean up")
	}
	// after close, the stream must be gone: a broadcast delivers nothing new
	before := len(cap.frames)
	_ = srv.Broadcast("chat:1", map[string]any{"x": 1})
	if len(cap.frames) != before {
		t.Fatal("stream not torn down on disconnect")
	}
	// double close is a no-op
	conn.Disconnect(DisconnectServerRestart, false)
	if len(cap.frames) != before {
		t.Fatal("double disconnect transmitted again")
	}
}

func TestRemoteConnections_Disconnect(t *testing.T) {
	adapter := NewAsyncAdapter()
	srv := NewServer(adapter)
	newConn := func() (*Connection, *capture) {
		cap := &capture{}
		conn := NewConnection(srv, cap.transport, func(c *Connection, id string, p map[string]any) (*Channel, bool) {
			return NewChannel(c, "chat", id, p, nil), true
		})
		conn.IdentifiedBy("current_user", paramModel{"alice"})
		conn.Connect()
		return conn, cap
	}
	c1, cap1 := newConn()
	c2, cap2 := newConn()

	// A non-disconnect internal message is ignored.
	_ = srv.Broadcast(internalChannelFor("alice"), map[string]any{"type": "ping"})
	if c1.Closed() || c2.Closed() {
		t.Fatal("non-disconnect internal message closed a connection")
	}
	// A malformed internal payload is ignored.
	_ = adapter.Broadcast(internalChannelFor("alice"), []byte("not json"))
	if c1.Closed() || c2.Closed() {
		t.Fatal("malformed internal message closed a connection")
	}

	// The real remote disconnect closes both connections with the same identity.
	if err := srv.RemoteConnections().Where(map[string]any{"current_user": paramModel{"alice"}}).Disconnect(true); err != nil {
		t.Fatal(err)
	}
	if !c1.Closed() || !c2.Closed() {
		t.Fatal("remote disconnect did not close both connections")
	}
	if cap1.last() != `{"type":"disconnect","reason":"remote","reconnect":true}` {
		t.Fatalf("c1 disconnect frame: %s", cap1.last())
	}
	if cap2.last() != `{"type":"disconnect","reason":"remote","reconnect":true}` {
		t.Fatalf("c2 disconnect frame: %s", cap2.last())
	}
}

func TestRemoteConnections_AnonymousHasNoInternalChannel(t *testing.T) {
	srv := NewServer(NewAsyncAdapter())
	conn := NewConnection(srv, (&capture{}).transport, nil)
	conn.Connect() // no identified_by -> subscribeInternal returns early
	// Disconnect an anonymous connection has no internal channel; it closes fine.
	conn.Disconnect(DisconnectRemote, false)
	if !conn.Closed() {
		t.Fatal("anonymous connection did not close")
	}
}

func TestConnection_IdentifiedByLookup(t *testing.T) {
	srv := NewServer(NewAsyncAdapter())
	conn := NewConnection(srv, (&capture{}).transport, nil)
	conn.IdentifiedBy("current_user", "alice")
	if conn.Identifier("current_user") != "alice" {
		t.Fatal("IdentifiedBy/Identifier round-trip failed")
	}
}

func TestConnectionIdentifierFrom_NilValueCompacted(t *testing.T) {
	if got := connectionIdentifierFrom(map[string]any{"a": "x", "b": nil}); got != "x" {
		t.Fatalf("nil not compacted: %q", got)
	}
	if got := connectionIdentifierFrom(nil); got != "" {
		t.Fatalf("empty identity: %q", got)
	}
}
