package actioncable

import (
	"sort"
	"strings"
)

// Server is the pub-sub hub, the analogue of ActionCable.server. It owns an
// [Adapter] and turns ActionCable.server.broadcast(broadcasting, data) into an
// encode-then-fan-out over that adapter.
type Server struct {
	adapter Adapter
}

// NewServer returns a Server broadcasting through adapter.
func NewServer(adapter Adapter) *Server {
	return &Server{adapter: adapter}
}

// Adapter returns the underlying pub-sub adapter.
func (s *Server) Adapter() Adapter { return s.adapter }

// Broadcast JSON-encodes message and fans it out to every stream subscribed to
// broadcasting — the equivalent of ActionCable.server.broadcast. Subscribers
// receive the encoded payload; the default stream handler decodes it and
// transmits it to the client as a channel message.
func (s *Server) Broadcast(broadcasting string, message any) error {
	payload, err := encodeJSON(message)
	if err != nil {
		return err
	}
	return s.adapter.Broadcast(broadcasting, payload)
}

// RemoteConnections is the entry point for disconnecting connections identified
// elsewhere, the analogue of ActionCable.server.remote_connections.
func (s *Server) RemoteConnections() *RemoteConnections {
	return &RemoteConnections{server: s}
}

// RemoteConnections selects connections to act on by their identifiers.
type RemoteConnections struct {
	server *Server
}

// RemoteConnection is a handle to the connection(s) matching a set of
// identifiers, addressed via their shared internal pub-sub channel.
type RemoteConnection struct {
	server     *Server
	identifier string
}

// Where selects the connection(s) matching the given identified-by values,
// mirroring remote_connections.where(current_user: user).
func (rc *RemoteConnections) Where(identifiers map[string]any) *RemoteConnection {
	return &RemoteConnection{
		server:     rc.server,
		identifier: connectionIdentifierFrom(identifiers),
	}
}

// Disconnect asks the matching connection(s) to close, telling the client
// whether to reconnect. It publishes an internal disconnect message on the
// connection's internal channel; any live connection with that identity reacts
// by closing with the "remote" reason. This is the cross-server mechanism
// RemoteConnection#disconnect uses.
func (rc *RemoteConnection) Disconnect(reconnect bool) error {
	return rc.server.Broadcast(internalChannelFor(rc.identifier), map[string]any{
		"type":      "disconnect",
		"reconnect": reconnect,
	})
}

// connectionIdentifierFrom builds a stable identity string from a connection's
// identified-by values: each non-nil value's param, sorted, joined by ":".
// Mirrors Connection::Identification#connection_identifier. An empty set yields
// "" (an anonymous connection has no internal channel).
func connectionIdentifierFrom(identifiers map[string]any) string {
	if len(identifiers) == 0 {
		return ""
	}
	parts := make([]string, 0, len(identifiers))
	for _, v := range identifiers {
		if v == nil {
			continue
		}
		parts = append(parts, paramOf(v))
	}
	sort.Strings(parts)
	return strings.Join(parts, ":")
}

// internalChannelFor names the per-connection internal pub-sub channel, mirroring
// InternalChannel#internal_channel == "action_cable/#{connection_identifier}".
func internalChannelFor(identifier string) string {
	return "action_cable/" + identifier
}
