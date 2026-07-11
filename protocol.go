// Package actioncable is a pure-Go (no cgo) reimplementation of the core of
// Rails' Action Cable, faithful to MRI/Rails 4.0.5's on-the-wire protocol.
//
// It models the WebSocket sub-protocol (welcome/ping/subscribe/confirm/reject/
// unsubscribe/message/disconnect envelopes), the per-connection subscription
// registry, the channel lifecycle hooks, stream subscriptions and pub-sub
// broadcasting fan-out — everything that makes a real Action Cable JavaScript
// client interoperate — while leaving the two host-owned concerns as seams:
//
//   - the WebSocket transport, injected as a [Transport] func per [Connection];
//   - the channel action bodies (Ruby methods), injected as a [ChannelAction].
//
// The pub-sub backend is an [Adapter]; an in-process [AsyncAdapter] and a
// [RedisAdapter] (over a [RedisPubSub] client seam) ship here.
//
// The package is dependency-free, CGO-free, and has no dependency on any Ruby
// runtime. It is the Action Cable backend for go-embedded-ruby, but is a
// standalone, reusable module.
package actioncable

import (
	"bytes"
	"encoding/json"
)

// Message type strings the server sends to clients. These mirror
// ActionCable::INTERNAL[:message_types] exactly.
const (
	TypeWelcome      = "welcome"
	TypeDisconnect   = "disconnect"
	TypePing         = "ping"
	TypeConfirmation = "confirm_subscription"
	TypeRejection    = "reject_subscription"
)

// Command strings the client sends to the server. These mirror the commands
// accepted by ActionCable::Connection::Subscriptions.
const (
	CommandSubscribe   = "subscribe"
	CommandUnsubscribe = "unsubscribe"
	CommandMessage     = "message"
)

// Disconnect reasons, mirroring ActionCable::INTERNAL[:disconnect_reasons].
const (
	DisconnectUnauthorized   = "unauthorized"
	DisconnectInvalidRequest = "invalid_request"
	DisconnectServerRestart  = "server_restart"
	DisconnectRemote         = "remote"
)

// DefaultMountPath is Action Cable's default mount path.
const DefaultMountPath = "/cable"

// Protocols are the WebSocket sub-protocols Action Cable advertises, mirroring
// ActionCable::INTERNAL[:protocols]. The first is the JSON protocol this
// package implements.
var Protocols = []string{"actioncable-v1-json", "actioncable-unsupported"}

// Command is a client-to-server frame: {"command":...,"identifier":...,"data":...}.
// identifier is itself a JSON string (e.g. `{"channel":"ChatChannel"}`); data,
// present only for the "message" command, is a JSON string carrying the action
// payload.
type Command struct {
	Command    string `json:"command"`
	Identifier string `json:"identifier"`
	Data       string `json:"data"`
}

// Server-to-client frames. Each is its own struct so the JSON key order matches
// Rails byte-for-byte: welcome is {"type":...}; a confirmation is
// {"identifier":...,"type":...}; a channel message is {"identifier":...,"message":...}.

type welcomeFrame struct {
	Type string `json:"type"`
}

type pingFrame struct {
	Type    string `json:"type"`
	Message int64  `json:"message"`
}

type confirmFrame struct {
	Identifier string `json:"identifier"`
	Type       string `json:"type"`
}

type rejectFrame struct {
	Identifier string `json:"identifier"`
	Type       string `json:"type"`
}

type messageFrame struct {
	Identifier string `json:"identifier"`
	Message    any    `json:"message"`
}

type disconnectFrame struct {
	Type      string `json:"type"`
	Reason    string `json:"reason"`
	Reconnect bool   `json:"reconnect"`
}

// encodeJSON encodes v the way ActionCable's default coder (ActiveSupport::JSON)
// does with its default escape_html_entities_in_json = true: the HTML-significant
// bytes "<", ">" and "&" are emitted as the < / > / & escapes, and
// the JS line separators U+2028 / U+2029 as   /  . Go's encoder does
// exactly this with HTML escaping on (the default), so a real Action Cable
// JavaScript client receives byte-identical frames. The trailing newline the
// encoder appends is stripped.
//
// This matches ActiveSupport::JSON::Encoding::ESCAPED_CHARS with the global
// escape_html_entities_in_json flag, which defaults to true in Rails and in bare
// ActiveSupport alike — the mode a real Rails Action Cable server runs under.
func encodeJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// mustEncode encodes a fixed server frame that, by construction, never fails.
func mustEncode(v any) []byte {
	b, err := encodeJSON(v)
	if err != nil {
		panic(err)
	}
	return b
}

// EncodeWelcome returns the {"type":"welcome"} frame sent on connect.
func EncodeWelcome() []byte { return mustEncode(welcomeFrame{Type: TypeWelcome}) }

// EncodePing returns the {"type":"ping","message":<epoch>} heartbeat frame.
func EncodePing(epoch int64) []byte {
	return mustEncode(pingFrame{Type: TypePing, Message: epoch})
}

// EncodeConfirmation returns {"identifier":<id>,"type":"confirm_subscription"}.
func EncodeConfirmation(identifier string) []byte {
	return mustEncode(confirmFrame{Identifier: identifier, Type: TypeConfirmation})
}

// EncodeRejection returns {"identifier":<id>,"type":"reject_subscription"}.
func EncodeRejection(identifier string) []byte {
	return mustEncode(rejectFrame{Identifier: identifier, Type: TypeRejection})
}

// EncodeDisconnect returns {"type":"disconnect","reason":<reason>,"reconnect":<b>}.
func EncodeDisconnect(reason string, reconnect bool) []byte {
	return mustEncode(disconnectFrame{Type: TypeDisconnect, Reason: reason, Reconnect: reconnect})
}

// EncodeMessage returns {"identifier":<id>,"message":<message>}. Because message
// is arbitrary host data it may fail to encode, so an error is returned.
func EncodeMessage(identifier string, message any) ([]byte, error) {
	return encodeJSON(messageFrame{Identifier: identifier, Message: message})
}

// DecodeCommand parses a client-to-server frame.
func DecodeCommand(raw []byte) (Command, error) {
	var cmd Command
	if err := json.Unmarshal(raw, &cmd); err != nil {
		return Command{}, err
	}
	return cmd, nil
}

// parseIdentifier decodes a subscription identifier JSON string into its params
// (e.g. {"channel":"ChatChannel","room":"1"}). An empty identifier yields an
// empty map.
func parseIdentifier(identifier string) (map[string]any, error) {
	m := map[string]any{}
	if identifier == "" {
		return m, nil
	}
	if err := json.Unmarshal([]byte(identifier), &m); err != nil {
		return nil, err
	}
	return m, nil
}
