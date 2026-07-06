package actioncable

import (
	"encoding/json"
	"fmt"
	"time"
)

// Transport is the seam by which a [Connection] sends an encoded frame to the
// client. The host (e.g. the rbgo binding) owns the actual WebSocket and plugs
// its write here; the payload is a ready-to-send JSON frame.
type Transport func(payload []byte)

// ChannelFactory builds the [Channel] for a subscribe command. It receives the
// raw identifier string and its parsed params (which include "channel", the Ruby
// channel class name). It returns the channel and true, or false if no channel
// class matches the identifier — in which case the subscription is not confirmed,
// mirroring Rails logging "Subscription class not found".
type ChannelFactory func(conn *Connection, identifier string, params map[string]any) (*Channel, bool)

// Connection is a single client connection: its identified-by keys, its
// subscription registry, and the transport it transmits through. It is the
// analogue of ActionCable::Connection::Base.
type Connection struct {
	server    *Server
	transport Transport
	factory   ChannelFactory

	// Scheduler holds this connection's (and its channels') periodic timers,
	// advanced deterministically by the host's event loop.
	Scheduler *Scheduler

	identifiers   map[string]any
	subs          map[string]*Channel
	internalUnsub func()
	closed        bool
}

// NewConnection creates a connection that transmits through transport and builds
// channels through factory.
func NewConnection(server *Server, transport Transport, factory ChannelFactory) *Connection {
	return &Connection{
		server:      server,
		transport:   transport,
		factory:     factory,
		Scheduler:   &Scheduler{},
		identifiers: map[string]any{},
		subs:        map[string]*Channel{},
	}
}

// IdentifiedBy sets an identified-by key (e.g. current_user) for the connection.
func (c *Connection) IdentifiedBy(key string, value any) { c.identifiers[key] = value }

// Identifier returns a previously set identified-by value.
func (c *Connection) Identifier(key string) any { return c.identifiers[key] }

// Closed reports whether the connection has been closed.
func (c *Connection) Closed() bool { return c.closed }

func (c *Connection) transmit(payload []byte) { c.transport(payload) }

// Connect performs the open handshake: it transmits the welcome frame and
// subscribes to this connection's internal channel so RemoteConnections can
// reach it.
func (c *Connection) Connect() {
	c.transmit(EncodeWelcome())
	c.subscribeInternal()
}

func (c *Connection) subscribeInternal() {
	id := connectionIdentifierFrom(c.identifiers)
	if id == "" {
		return
	}
	unsub, _ := c.server.adapter.Subscribe(internalChannelFor(id), func(payload []byte) {
		var m map[string]any
		if err := json.Unmarshal(payload, &m); err != nil {
			return
		}
		if m["type"] == "disconnect" {
			reconnect, _ := m["reconnect"].(bool)
			c.close(DisconnectRemote, reconnect)
		}
	})
	c.internalUnsub = unsub
}

// Beat transmits a heartbeat ping frame carrying epoch, mirroring the periodic
// ping Action Cable sends. The host drives it on its own schedule.
func (c *Connection) Beat(epoch int64) { c.transmit(EncodePing(epoch)) }

// Advance moves this connection's timer scheduler forward by d, firing any due
// channel periodic timers.
func (c *Connection) Advance(d time.Duration) { c.Scheduler.Advance(d) }

// Dispatch routes a single client-to-server frame to the matching handler.
func (c *Connection) Dispatch(raw []byte) error {
	cmd, err := DecodeCommand(raw)
	if err != nil {
		return err
	}
	switch cmd.Command {
	case CommandSubscribe:
		return c.subscribe(cmd.Identifier)
	case CommandUnsubscribe:
		return c.unsubscribe(cmd.Identifier)
	case CommandMessage:
		return c.message(cmd.Identifier, cmd.Data)
	default:
		return fmt.Errorf("actioncable: unknown command %q", cmd.Command)
	}
}

// Subscriptions returns the number of active subscriptions (mostly for tests).
func (c *Connection) Subscriptions() int { return len(c.subs) }

// Subscription returns the channel for identifier, if subscribed.
func (c *Connection) Subscription(identifier string) (*Channel, bool) {
	ch, ok := c.subs[identifier]
	return ch, ok
}

func (c *Connection) subscribe(identifier string) error {
	if _, ok := c.subs[identifier]; ok {
		// Already subscribed; Rails logs and ignores.
		return nil
	}
	params, err := parseIdentifier(identifier)
	if err != nil {
		return err
	}
	ch, ok := c.factory(c, identifier, params)
	if !ok {
		return fmt.Errorf("actioncable: subscription class not found: %s", identifier)
	}
	c.subs[identifier] = ch
	ch.invokeSubscribed()
	if ch.rejected {
		delete(c.subs, identifier)
		ch.unsubscribeStreams()
		c.transmit(EncodeRejection(identifier))
		return nil
	}
	ch.confirmed = true
	c.transmit(EncodeConfirmation(identifier))
	return nil
}

func (c *Connection) unsubscribe(identifier string) error {
	ch, ok := c.subs[identifier]
	if !ok {
		return fmt.Errorf("actioncable: unknown subscription: %s", identifier)
	}
	ch.invokeUnsubscribed()
	ch.unsubscribeStreams()
	delete(c.subs, identifier)
	return nil
}

func (c *Connection) message(identifier, data string) error {
	ch, ok := c.subs[identifier]
	if !ok {
		return fmt.Errorf("actioncable: unknown subscription: %s", identifier)
	}
	var payload any
	if data != "" {
		if err := json.Unmarshal([]byte(data), &payload); err != nil {
			return err
		}
	}
	ch.performAction(payload)
	return nil
}

// Disconnect closes the connection, transmitting a disconnect frame with reason
// and telling the client whether to reconnect.
func (c *Connection) Disconnect(reason string, reconnect bool) { c.close(reason, reconnect) }

func (c *Connection) close(reason string, reconnect bool) {
	if c.closed {
		return
	}
	c.closed = true
	c.transmit(EncodeDisconnect(reason, reconnect))
	for id, ch := range c.subs {
		ch.unsubscribeStreams()
		delete(c.subs, id)
	}
	if c.internalUnsub != nil {
		c.internalUnsub()
	}
}
