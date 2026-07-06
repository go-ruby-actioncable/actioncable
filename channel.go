package actioncable

import (
	"encoding/json"
	"time"
)

// ChannelAction is the seam that runs a channel's Ruby method bodies. The host
// (the rbgo binding) supplies it, capturing the *Channel in a closure so the body
// can call the channel's own methods (StreamFrom, Reject, Transmit, ...). The
// library invokes it as action(channelName, action, data):
//
//   - action "subscribed"   when the subscription is confirmed (data is nil);
//   - action "unsubscribed" when it is torn down (data is nil);
//   - action "receive" or a custom action name for an inbound "message" command,
//     with data being the decoded message payload.
//
// Its return value is the Ruby method's result (unused by the library itself).
type ChannelAction func(channel, action string, data any) any

// Channel is one subscription on a connection, the analogue of
// ActionCable::Channel::Base. It owns the streams it is subscribed to and its
// periodic timers.
type Channel struct {
	conn   *Connection
	server *Server

	// name is the channel_name component used in broadcasting names.
	name string
	// Identifier is the raw subscription identifier JSON string.
	Identifier string
	// Params are the parsed identifier params (including "channel").
	Params map[string]any
	// Action runs the channel's Ruby bodies. May be nil (no-op hooks).
	Action ChannelAction

	streams   map[string]func()
	timers    []*PeriodicTimer
	rejected  bool
	confirmed bool
}

// NewChannel builds a channel bound to conn. name is its channel_name component
// (see [ChannelName]); identifier is the raw identifier string; params are its
// parsed params; action runs its bodies (may be nil).
func NewChannel(conn *Connection, name, identifier string, params map[string]any, action ChannelAction) *Channel {
	return &Channel{
		conn:       conn,
		server:     conn.server,
		name:       name,
		Identifier: identifier,
		Params:     params,
		Action:     action,
		streams:    map[string]func(){},
	}
}

// Name returns the channel_name component.
func (ch *Channel) Name() string { return ch.name }

// Rejected reports whether the subscription was rejected in its subscribed hook.
func (ch *Channel) Rejected() bool { return ch.rejected }

// Confirmed reports whether the subscription was confirmed.
func (ch *Channel) Confirmed() bool { return ch.confirmed }

// Streams returns the broadcastings this channel is currently subscribed to.
func (ch *Channel) Streams() []string {
	names := make([]string, 0, len(ch.streams))
	for b := range ch.streams {
		names = append(names, b)
	}
	return names
}

func (ch *Channel) invokeSubscribed() {
	if ch.Action != nil {
		ch.Action(ch.name, "subscribed", nil)
	}
}

func (ch *Channel) invokeUnsubscribed() {
	if ch.Action != nil {
		ch.Action(ch.name, "unsubscribed", nil)
	}
}

// performAction dispatches an inbound message payload to the channel's action,
// mirroring Channel::Base#perform_action: the action name is data["action"], or
// "receive" when absent.
func (ch *Channel) performAction(data any) any {
	action := "receive"
	if m, ok := data.(map[string]any); ok {
		if a, ok := m["action"].(string); ok && a != "" {
			action = a
		}
	}
	if ch.Action == nil {
		return nil
	}
	return ch.Action(ch.name, action, data)
}

// StreamFrom subscribes the channel to broadcasting: every value broadcast there
// is decoded and transmitted to the client as a channel message, mirroring
// Channel::Streams#stream_from with the default handler. Subscribing to the same
// broadcasting twice is a no-op.
func (ch *Channel) StreamFrom(broadcasting string) error {
	if _, ok := ch.streams[broadcasting]; ok {
		return nil
	}
	unsub, err := ch.server.adapter.Subscribe(broadcasting, func(payload []byte) {
		var msg any
		if err := json.Unmarshal(payload, &msg); err != nil {
			return
		}
		_ = ch.Transmit(msg)
	})
	if err != nil {
		return err
	}
	ch.streams[broadcasting] = unsub
	return nil
}

// StreamFor subscribes to the broadcasting for model, mirroring stream_for.
func (ch *Channel) StreamFor(model any) error {
	return ch.StreamFrom(BroadcastingName(ch.name, model))
}

// BroadcastTo broadcasts message to the broadcasting for model, mirroring the
// class method Channel.broadcast_to(model, message).
func (ch *Channel) BroadcastTo(model any, message any) error {
	return ch.server.Broadcast(BroadcastingName(ch.name, model), message)
}

// Transmit sends data to this subscription's client as a channel message frame
// {"identifier":...,"message":data}, mirroring Channel::Base#transmit.
func (ch *Channel) Transmit(data any) error {
	payload, err := EncodeMessage(ch.Identifier, data)
	if err != nil {
		return err
	}
	ch.conn.transmit(payload)
	return nil
}

// Reject rejects the subscription from within the subscribed hook, mirroring
// Channel::Base#reject.
func (ch *Channel) Reject() { ch.rejected = true }

// Periodically registers fn to run once per interval on the connection's
// deterministic scheduler, mirroring Channel::PeriodicTimers#periodically.
func (ch *Channel) Periodically(interval time.Duration, fn func()) *PeriodicTimer {
	t := ch.conn.Scheduler.Every(interval, fn)
	ch.timers = append(ch.timers, t)
	return t
}

// unsubscribeStreams tears down all stream subscriptions and periodic timers,
// called on unsubscribe / connection close.
func (ch *Channel) unsubscribeStreams() {
	for b, unsub := range ch.streams {
		unsub()
		delete(ch.streams, b)
	}
	for _, t := range ch.timers {
		ch.conn.Scheduler.Remove(t)
	}
	ch.timers = nil
}
