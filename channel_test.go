package actioncable

import "testing"

// harness builds a connection with a single channel whose action is provided.
func harness(t *testing.T, adapter Adapter, action ChannelAction) (*Connection, *Channel, *capture) {
	t.Helper()
	cap := &capture{}
	srv := NewServer(adapter)
	var created *Channel
	factory := func(conn *Connection, id string, p map[string]any) (*Channel, bool) {
		created = NewChannel(conn, ChannelName(p["channel"].(string)), id, p, action)
		return created, true
	}
	conn := NewConnection(srv, cap.transport, factory)
	if err := conn.Dispatch([]byte(`{"command":"subscribe","identifier":"{\"channel\":\"ChatChannel\"}"}`)); err != nil {
		t.Fatal(err)
	}
	return conn, created, cap
}

func TestChannel_StreamFromDeliversMessages(t *testing.T) {
	_, ch, cap := harness(t, NewAsyncAdapter(), nil)
	if err := ch.StreamFrom("chat:1"); err != nil {
		t.Fatal(err)
	}
	if err := ch.StreamFrom("chat:1"); err != nil { // duplicate is a no-op
		t.Fatal(err)
	}
	if len(ch.Streams()) != 1 {
		t.Fatalf("streams: %v", ch.Streams())
	}

	before := len(cap.frames)
	if err := ch.server.Broadcast("chat:1", map[string]any{"text": "hello"}); err != nil {
		t.Fatal(err)
	}
	if len(cap.frames) != before+1 {
		t.Fatal("broadcast not delivered to stream")
	}
	want := `{"identifier":"{\"channel\":\"ChatChannel\"}","message":{"text":"hello"}}`
	if cap.last() != want {
		t.Fatalf("stream frame: %s", cap.last())
	}
}

func TestChannel_StreamFromDecodeErrorIgnored(t *testing.T) {
	adapter := NewAsyncAdapter()
	_, ch, cap := harness(t, adapter, nil)
	if err := ch.StreamFrom("chat:1"); err != nil {
		t.Fatal(err)
	}
	before := len(cap.frames)
	// Bypass Server.Broadcast to push a malformed payload straight through.
	_ = adapter.Broadcast("chat:1", []byte("not json"))
	if len(cap.frames) != before {
		t.Fatal("malformed broadcast should not transmit")
	}
}

func TestChannel_StreamFromSubscribeError(t *testing.T) {
	_, ch, _ := harness(t, failingAdapter{}, nil)
	if err := ch.StreamFrom("chat:1"); err == nil {
		t.Fatal("expected subscribe error to propagate")
	}
}

func TestChannel_StreamForAndBroadcastTo(t *testing.T) {
	_, ch, cap := harness(t, NewAsyncAdapter(), nil)
	if err := ch.StreamFor(paramModel{"7"}); err != nil {
		t.Fatal(err)
	}
	if ch.Streams()[0] != "chat:7" {
		t.Fatalf("stream_for name: %v", ch.Streams())
	}
	before := len(cap.frames)
	if err := ch.BroadcastTo(paramModel{"7"}, map[string]any{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	if len(cap.frames) != before+1 {
		t.Fatal("broadcast_to did not reach stream_for subscriber")
	}
}

func TestChannel_TransmitEncodeError(t *testing.T) {
	_, ch, _ := harness(t, NewAsyncAdapter(), nil)
	if err := ch.Transmit(make(chan int)); err == nil {
		t.Fatal("expected transmit encode error")
	}
}

func TestChannel_BroadcastToEncodeError(t *testing.T) {
	_, ch, _ := harness(t, NewAsyncAdapter(), nil)
	if err := ch.BroadcastTo(paramModel{"1"}, make(chan int)); err == nil {
		t.Fatal("expected broadcast_to encode error")
	}
}

func TestChannel_NilActionHooksAndPerform(t *testing.T) {
	conn, ch, _ := harness(t, NewAsyncAdapter(), nil)
	// subscribed hook already ran with nil Action (no panic). Now perform:
	if got := ch.performAction(map[string]any{"action": "speak"}); got != nil {
		t.Fatalf("nil action perform: %v", got)
	}
	// unsubscribe runs the nil unsubscribed hook without panic.
	if err := conn.Dispatch([]byte(`{"command":"unsubscribe","identifier":"{\"channel\":\"ChatChannel\"}"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestChannel_UnsubscribeTearsDownStreamsAndTimers(t *testing.T) {
	conn, ch, _ := harness(t, NewAsyncAdapter(), nil)
	if err := ch.StreamFrom("chat:1"); err != nil {
		t.Fatal(err)
	}
	ticks := 0
	ch.Periodically(1000000, func() { ticks++ }) // 1ms
	if err := conn.Dispatch([]byte(`{"command":"unsubscribe","identifier":"{\"channel\":\"ChatChannel\"}"}`)); err != nil {
		t.Fatal(err)
	}
	if len(ch.Streams()) != 0 {
		t.Fatal("streams not torn down")
	}
	// The timer was removed from the scheduler: advancing fires nothing.
	conn.Advance(1000000000) // 1s
	if ticks != 0 {
		t.Fatalf("timer still firing after unsubscribe: %d", ticks)
	}
}

func TestServer_BroadcastEncodeError(t *testing.T) {
	srv := NewServer(NewAsyncAdapter())
	if err := srv.Broadcast("x", make(chan int)); err == nil {
		t.Fatal("expected server broadcast encode error")
	}
}

func TestServer_AdapterAccessor(t *testing.T) {
	a := NewAsyncAdapter()
	if NewServer(a).Adapter() != a {
		t.Fatal("Adapter accessor mismatch")
	}
}
