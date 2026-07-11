<p align="center"><img src="https://raw.githubusercontent.com/go-ruby-actioncable/brand/main/social/go-ruby-actioncable-actioncable.png" alt="go-ruby-actioncable/actioncable" width="720"></p>

# actioncable — go-ruby-actioncable

[![Docs](https://img.shields.io/badge/docs-mkdocs--material-DC2626)](https://go-ruby-actioncable.github.io/docs/)
[![License](https://img.shields.io/badge/license-BSD--3--Clause-blue)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.26.4%2B-00ADD8)](https://go.dev/dl/)
[![Coverage](https://img.shields.io/badge/coverage-100%25-1a7f37)](#tests--coverage)

**A pure-Go (no cgo) reimplementation of the core of Rails'
[Action Cable](https://guides.rubyonrails.org/action_cable_overview.html)**
— the `Channel` / `Connection` / `Subscription` protocol machinery and pub-sub
broadcasting from MRI/Rails 4.0.5 — **faithful to the on-the-wire JSON
sub-protocol**, so a real Action Cable JavaScript client interoperates with it
unchanged. It models the wire envelopes, the per-connection subscription
registry, the channel lifecycle hooks, stream subscriptions and broadcasting
fan-out **without any Ruby runtime**.

It is the Action Cable backend for
[go-embedded-ruby](https://github.com/go-embedded-ruby/ruby), but is a
**standalone, reusable** module — a sibling of
[go-ruby-set](https://github.com/go-ruby-set/set),
[go-ruby-redis](https://github.com/go-ruby-redis/redis) and
[go-ruby-activesupport](https://github.com/go-ruby-activesupport/activesupport).

## The host-owned seams

Everything that is *not* protocol logic is left as an injectable seam, so the
host (the rbgo binding) owns it:

| Seam | Type | What the host plugs in |
|------|------|------------------------|
| **Transport** | `func(payload []byte)` | the WebSocket write — a `Connection` transmits a ready-to-send JSON frame through it. A **real pure-Go WebSocket transport** ([`Handler`](#websocket-transport) / [`Upgrade`](#websocket-transport)) ships here as the concrete fill; the host may still inject its own |
| **ConnectHook** | `func(*Connection) error` | the connection's `connect` method — identification (`identified_by`) and authorization; return [`RejectUnauthorized`](#connection-authorization) to reject |
| **ChannelAction** | `func(channel, action string, data any) any` | the channel's Ruby method bodies — `subscribed` / `unsubscribed` / `receive` / custom actions. The binding builds the closure capturing the `*Channel`, so a body can call `StreamFrom` / `Reject` / `Transmit` on it |
| **Adapter** | `interface{ Broadcast; Subscribe }` | the pub-sub backend — [`AsyncAdapter`](#pub-sub-adapters) (in-process) and [`RedisAdapter`](#pub-sub-adapters) ship here |

## Wire-protocol fidelity

The JSON envelopes are byte-faithful to Action Cable's `actioncable-v1-json`
protocol. Key order matters and is fixed per frame (dedicated structs), and
HTML-significant bytes are escaped exactly as `ActiveSupport::JSON` does under its
default `escape_html_entities_in_json = true` — `<`, `>`, `&` become `<`,
`>`, `&` and the JS line separators `U+2028` / `U+2029` become
` ` / ` ` — the mode a real Rails Action Cable server runs under. Every
frame below and every name derivation is checked **byte-for-byte against the real
MRI `actioncable` gem** by the differential [oracle test](#oracle-differential-test-vs-the-gem):

| Direction | Frame | Bytes |
|-----------|-------|-------|
| S→C | welcome | `{"type":"welcome"}` |
| S→C | ping | `{"type":"ping","message":1751800000}` |
| C→S | subscribe | `{"command":"subscribe","identifier":"{\"channel\":\"ChatChannel\"}"}` |
| S→C | confirm | `{"identifier":"…","type":"confirm_subscription"}` |
| S→C | reject | `{"identifier":"…","type":"reject_subscription"}` |
| C→S | message | `{"command":"message","identifier":"…","data":"{\"action\":\"speak\"}"}` |
| S→C | message | `{"identifier":"…","message":{…}}` |
| S→C | disconnect | `{"type":"disconnect","reason":"remote","reconnect":true}` |

The type / command / reason strings mirror `ActionCable::INTERNAL` exactly.

## Install

```sh
go get github.com/go-ruby-actioncable/actioncable
```

## Usage

```go
package main

import (
	"fmt"

	actioncable "github.com/go-ruby-actioncable/actioncable"
)

func main() {
	server := actioncable.NewServer(actioncable.NewAsyncAdapter())

	// The host owns the WebSocket; here we just print the frames.
	transport := func(payload []byte) { fmt.Println(string(payload)) }

	// The factory maps an identifier's "channel" to a Channel + its action body.
	factory := func(conn *actioncable.Connection, id string, p map[string]any) (*actioncable.Channel, bool) {
		var ch *actioncable.Channel
		action := func(_, act string, data any) any {
			switch act {
			case "subscribed":
				ch.StreamFrom("chat:1") // stream_from "chat:1"
			case "speak":
				ch.BroadcastTo("1", data) // broadcast_to room "1" -> "chat:1"
			}
			return nil
		}
		ch = actioncable.NewChannel(conn, actioncable.ChannelName(p["channel"].(string)), id, p, action)
		return ch, true
	}

	conn := actioncable.NewConnection(server, transport, factory)
	conn.Connect() // -> {"type":"welcome"}

	conn.Dispatch([]byte(`{"command":"subscribe","identifier":"{\"channel\":\"ChatChannel\"}"}`))
	// -> {"identifier":"{\"channel\":\"ChatChannel\"}","type":"confirm_subscription"}

	server.Broadcast("chat:1", map[string]any{"text": "hello"})
	// -> {"identifier":"{\"channel\":\"ChatChannel\"}","message":{"text":"hello"}}
}
```

## Connection authorization

The connection's `connect` method — where `identified_by` and authorization live —
is the `ConnectHook` seam, installed with `OnConnect`. It runs once, at
`Connect`, before the welcome frame. Returning `RejectUnauthorized()` (the analogue
of `reject_unauthorized_connection` raising `UnauthorizedError`) closes the
connection with the `unauthorized` reason and `reconnect:false` and sends **no**
welcome — exactly as the gem's `handle_open` rescue does:

```go
conn := actioncable.NewConnection(server, transport, factory).
	OnConnect(func(c *actioncable.Connection) error {
		user := authenticate(c) // your cookie / token logic
		if user == "" {
			return c.RejectUnauthorized()
		}
		c.IdentifiedBy("current_user", user) // identified_by :current_user
		return nil
	})

err := conn.Connect()
// authorized:   -> {"type":"welcome"}                                        (err == nil)
// unauthorized: -> {"type":"disconnect","reason":"unauthorized","reconnect":false}
//                  errors.Is(err, actioncable.ErrUnauthorized) == true
```

## WebSocket transport

A real, **pure-Go (no cgo, no third-party dependency)** WebSocket transport ships
for the `/cable` endpoint: `Handler` performs the RFC 6455 server handshake,
negotiates the `actioncable-v1-json` sub-protocol, and drives a `Connection` — a
real Action Cable JavaScript client interoperates with it unchanged. It is the
concrete fill for the `Transport` seam; a host wanting its own may keep using
`NewConnection` with a custom `Transport`.

```go
mux := http.NewServeMux()
mux.Handle("/cable", actioncable.Handler(server, factory, &actioncable.CableOptions{
	OnConnect:    authorize,          // the ConnectHook above
	PingInterval: 3 * time.Second,    // protocol-level heartbeat
}))
http.ListenAndServe(":28080", mux)
```

`Upgrade(w, r) (*WSConn, error)` is exposed for hosts that want the handshake
without the drive loop. Only the subset Action Cable needs is implemented
(unfragmented text frames, ping/pong, close); client frames are required to be
masked and server frames are sent unmasked, per RFC 6455.

## Pub-sub adapters

`Server.Broadcast(broadcasting, data)` JSON-encodes `data` and fans it out to
every stream subscribed to that broadcasting. Two adapters satisfy the `Adapter`
interface:

- **`AsyncAdapter`** — in-process (the `:async` adapter). Fan-out is synchronous
  and deterministic; it leaks no goroutines.
- **`RedisAdapter`** — cross-process (the `:redis` adapter). It carries no Redis
  code itself: it forwards to an injected **`RedisPubSub`** client seam
  (`Publish` / `Subscribe`), which [go-ruby-redis](https://github.com/go-ruby-redis/redis)
  satisfies. This keeps the module dependency-free and the adapter unit-testable
  with an in-memory fake.

`broadcasting` names are built by `BroadcastingName(channelName, model)` /
`SerializeBroadcasting(...)`, faithful to `serialize_broadcasting` (GlobalID
`to_gid_param` → `to_param` → string, joined by `:`), and `ChannelName` derives
the `channel_name` component (`ChatChannel` → `chat`, `Chat::RoomChannel` →
`chat:room`).

## API surface (v0.1)

```go
// Protocol
func EncodeWelcome() []byte
func EncodePing(epoch int64) []byte
func EncodeConfirmation(identifier string) []byte
func EncodeRejection(identifier string) []byte
func EncodeMessage(identifier string, message any) ([]byte, error)
func EncodeDisconnect(reason string, reconnect bool) []byte
func DecodeCommand(raw []byte) (Command, error)

// Pub-sub
type Adapter interface { Broadcast(...); Subscribe(...) }
type RedisPubSub interface { Publish(...); Subscribe(...) }
func NewAsyncAdapter() *AsyncAdapter
func NewRedisAdapter(client RedisPubSub) *RedisAdapter
func NewServer(adapter Adapter) *Server
func (s *Server) Broadcast(broadcasting string, message any) error
func (s *Server) RemoteConnections() *RemoteConnections

// Connection
type Transport func(payload []byte)
type ChannelFactory func(*Connection, string, map[string]any) (*Channel, bool)
type ConnectHook func(*Connection) error
var ErrUnauthorized error
func NewConnection(server *Server, transport Transport, factory ChannelFactory) *Connection
func (c *Connection) OnConnect(hook ConnectHook) *Connection  // connect method seam
func (c *Connection) RejectUnauthorized() error              // reject_unauthorized_connection
func (c *Connection) Connect() error                         // connect hook + welcome
func (c *Connection) Dispatch(raw []byte) error              // subscribe/unsubscribe/message
func (c *Connection) Beat(epoch int64)                       // ping
func (c *Connection) Advance(d time.Duration)                // fire due periodic timers
func (c *Connection) Disconnect(reason string, reconnect bool)
func (c *Connection) IdentifiedBy(key string, value any)     // identified_by

// WebSocket transport (pure-Go RFC 6455, /cable)
type CableOptions struct { OnConnect ConnectHook; PingInterval time.Duration }
func Handler(server *Server, factory ChannelFactory, opts *CableOptions) http.Handler
func Upgrade(w http.ResponseWriter, r *http.Request) (*WSConn, error)

// Channel
type ChannelAction func(channel, action string, data any) any
func NewChannel(conn *Connection, name, identifier string, params map[string]any, action ChannelAction) *Channel
func (ch *Channel) StreamFrom(broadcasting string) error
func (ch *Channel) StreamFor(model any) error
func (ch *Channel) BroadcastTo(model, message any) error
func (ch *Channel) Transmit(data any) error
func (ch *Channel) Reject()
func (ch *Channel) Periodically(interval time.Duration, fn func()) *PeriodicTimer

// Broadcasting names
func BroadcastingName(channelName string, model any) string
func SerializeBroadcasting(objects ...any) string
func ChannelName(className string) string
```

## Oracle: differential test vs the gem

`testdata/oracle.rb` runs the real MRI `actioncable` gem and emits the exact bytes
it produces for every wire frame and every name derivation; `oracle_test.go`
computes each case through the Go API and asserts **byte-for-byte** equality, so the
gem is the ground truth for the on-the-wire protocol. The oracle skips itself where
ruby or the gem is absent (Windows, qemu, no-gem machines), and the deterministic
ruby-free tests alone keep coverage at 100% on those lanes. Running it against
`actioncable 8.0.2` is what pinned the HTML-entity escaping (`ActiveSupport::JSON`'s
default `escape_html_entities_in_json = true`) and the acronym-boundary
`channel_name` rule (`HTTPServerChannel` → `http_server`).

## Roadmap (still deferred)

The protocol core, pub-sub, connection authorization and the `/cable` WebSocket
transport are implemented. The channel bodies stay a seam by design. Deferred, in
rough order:

- **Rails engine mount** — routing, `config.action_cable.*`, `cable.yml`.
- **Request-origin / CSRF checks** — `allow_request_origin?`,
  `disable_request_forgery_protection`, cookie / Warden / Devise plumbing (the
  `ConnectHook` seam is where a host wires these today).
- **Instrumentation** — `ActiveSupport::Notifications` events, logging tags.
- **Full RemoteConnections** — beyond the internal-channel disconnect modeled here.
- **go-ruby-redis wiring** — a ready-made `RedisPubSub` adapter over go-ruby-redis
  (the seam is in place; the concrete binding is deferred).
- **rbgo binding** — expose `Channel` / `Connection` / `ActionCable.server` to
  pure-Go Ruby.

## Tests & coverage

The suite is deterministic and Ruby-free: it asserts every wire envelope
byte-for-byte, the subscribe → confirm / reject flow, stream subscription and
broadcast fan-out, the async and redis adapters (the latter over an in-memory
fake client), the channel hooks via fake `ChannelAction` seams, periodic timers
on the virtual clock, and remote disconnect over the internal channel. It leaks
no goroutines.

```sh
COVERPKG=$(go list ./... | paste -sd, -)
go test -race -coverpkg="$COVERPKG" -coverprofile=cover.out ./...
go tool cover -func=cover.out | tail -1   # 100.0%
```

CGO-free, dependency-free, `gofmt` + `go vet` clean, race-clean, and green across
the six 64-bit Go targets (amd64, arm64, riscv64, loong64, ppc64le, s390x — the
big-endian s390x lane exercises the codec) and three OSes (Linux, macOS,
Windows).

## License

BSD-3-Clause — see [LICENSE](LICENSE). Copyright the go-ruby-actioncable/actioncable authors.

## WebAssembly

Being pure Go (CGO=0), this library also compiles to **WebAssembly** — both
`GOOS=js GOARCH=wasm` (browser / Node.js) and `GOOS=wasip1 GOARCH=wasm` (WASI).
CI builds both targets on every push, alongside the six 64-bit native/qemu arches.

```sh
GOOS=js     GOARCH=wasm go build ./...   # browser / Node
GOOS=wasip1 GOARCH=wasm go build ./...   # WASI (wasmtime, wasmer, wasmedge, …)
```
