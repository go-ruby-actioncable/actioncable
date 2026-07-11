package actioncable

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// This file provides a real, pure-Go (no cgo, no third-party dependency)
// WebSocket transport for the /cable endpoint: an RFC 6455 server handshake plus
// the minimal framing Action Cable uses, wired to drive a [Connection]. It is the
// concrete fill for the [Transport] seam — the host may still inject its own — and
// makes a real Action Cable JavaScript client interoperate over the same
// actioncable-v1-json sub-protocol this package speaks.
//
// Only the subset Action Cable needs is implemented: unfragmented text frames
// client<->server, ping/pong, and close. The protocol/state-machine above the
// socket (welcome, subscribe/confirm/reject, message, disconnect, heartbeat) is
// exactly the [Connection]/[Channel] logic the rest of the package models.

// wsGUID is the RFC 6455 magic value appended to Sec-WebSocket-Key to derive
// Sec-WebSocket-Accept.
const wsGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

// WebSocket opcodes (RFC 6455 §5.2).
const (
	opText  = 0x1
	opClose = 0x8
	opPing  = 0x9
	opPong  = 0xA
)

// maxFramePayload caps a single inbound frame (1 MiB) so a client cannot force
// unbounded allocation. Action Cable command frames are tiny.
const maxFramePayload = 1 << 20

// Errors returned by the handshake and framing layer.
var (
	errNotWebSocket    = errors.New("actioncable: not a WebSocket upgrade request")
	errNoHijack        = errors.New("actioncable: ResponseWriter does not support Hijack")
	errClientClosed    = errors.New("actioncable: client sent close frame")
	errUnmaskedClient  = errors.New("actioncable: client frame not masked")
	errFrameTooLarge   = errors.New("actioncable: frame payload too large")
	errBadSubprotocol  = errors.New("actioncable: no supported sub-protocol offered")
	errReservedOpcode  = errors.New("actioncable: reserved/unsupported opcode")
	errControlTooLarge = errors.New("actioncable: control frame payload too large")
)

// acceptKey computes the Sec-WebSocket-Accept value for a Sec-WebSocket-Key.
func acceptKey(key string) string {
	h := sha1.New()
	io.WriteString(h, key+wsGUID)
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

// negotiateSubprotocol picks the first sub-protocol the client offers that this
// server supports (the actioncable-v1-json protocol at the head of [Protocols]).
// It returns "" and false when none match.
func negotiateSubprotocol(header string) (string, bool) {
	offered := map[string]bool{}
	for _, p := range strings.Split(header, ",") {
		offered[strings.TrimSpace(p)] = true
	}
	for _, p := range Protocols {
		if offered[p] {
			return p, true
		}
	}
	return "", false
}

// isWebSocketUpgrade reports whether r is a well-formed WebSocket upgrade request
// (GET with the Connection: Upgrade / Upgrade: websocket token pair and a key).
func isWebSocketUpgrade(r *http.Request) bool {
	return r.Method == http.MethodGet &&
		tokenListContains(r.Header.Get("Connection"), "upgrade") &&
		strings.EqualFold(r.Header.Get("Upgrade"), "websocket") &&
		r.Header.Get("Sec-WebSocket-Key") != ""
}

// tokenListContains reports whether a comma-separated header value contains token
// (case-insensitively), e.g. Connection: keep-alive, Upgrade.
func tokenListContains(header, token string) bool {
	for _, t := range strings.Split(header, ",") {
		if strings.EqualFold(strings.TrimSpace(t), token) {
			return true
		}
	}
	return false
}

// WSConn is a single upgraded WebSocket connection: the hijacked net.Conn plus a
// buffered reader and a write mutex (the read loop and the heartbeat both write).
type WSConn struct {
	conn net.Conn
	br   *bufio.Reader
	mu   sync.Mutex // serialises writes
	// Subprotocol is the negotiated sub-protocol (e.g. "actioncable-v1-json").
	Subprotocol string
}

// Upgrade performs the RFC 6455 server handshake on r/w, negotiating one of the
// Action Cable [Protocols], and returns the upgraded [WSConn]. It writes the 101
// response itself. Errors: [errNotWebSocket] if r is not an upgrade request,
// [errBadSubprotocol] if the client offers no supported sub-protocol, or
// [errNoHijack] if w cannot be hijacked.
func Upgrade(w http.ResponseWriter, r *http.Request) (*WSConn, error) {
	if !isWebSocketUpgrade(r) {
		return nil, errNotWebSocket
	}
	proto, ok := negotiateSubprotocol(r.Header.Get("Sec-WebSocket-Protocol"))
	if !ok {
		return nil, errBadSubprotocol
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		return nil, errNoHijack
	}
	conn, brw, err := hj.Hijack()
	if err != nil {
		return nil, err
	}
	accept := acceptKey(r.Header.Get("Sec-WebSocket-Key"))
	resp := "HTTP/1.1 101 Switching Protocols\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Accept: " + accept + "\r\n" +
		"Sec-WebSocket-Protocol: " + proto + "\r\n\r\n"
	if _, err := io.WriteString(brw, resp); err != nil {
		conn.Close()
		return nil, err
	}
	if err := brw.Flush(); err != nil {
		conn.Close()
		return nil, err
	}
	return &WSConn{conn: conn, br: brw.Reader, Subprotocol: proto}, nil
}

// readMessage reads one data message from the peer, transparently answering ping
// with pong. It returns the text payload of a text frame, or an error: an
// [errClientClosed] on a close frame, or a framing/IO error. Only unfragmented
// frames are handled (Action Cable never fragments control/command frames).
func (c *WSConn) readMessage() ([]byte, error) {
	for {
		fin, opcode, payload, err := readFrame(c.br)
		if err != nil {
			return nil, err
		}
		if !fin {
			return nil, errReservedOpcode
		}
		switch opcode {
		case opText:
			return payload, nil
		case opPing:
			if err := c.writeFrame(opPong, payload); err != nil {
				return nil, err
			}
		case opPong:
			// unsolicited pong: ignore and keep reading
		case opClose:
			_ = c.writeFrame(opClose, nil)
			return nil, errClientClosed
		default:
			return nil, errReservedOpcode
		}
	}
}

// writeText sends payload as a single unfragmented text frame.
func (c *WSConn) writeText(payload []byte) error { return c.writeFrame(opText, payload) }

// writePing sends a ping control frame (the Action Cable heartbeat rides in the
// application-level text ping frame, but a protocol-level ping keeps intermediaries
// from idling the socket).
func (c *WSConn) writePing() error { return c.writeFrame(opPing, nil) }

// Close sends a close frame (best effort) and closes the underlying socket.
func (c *WSConn) Close() error {
	_ = c.writeFrame(opClose, nil)
	return c.conn.Close()
}

// writeFrame writes one server-to-client frame. Server frames are never masked
// (RFC 6455 §5.1); writes are serialised so the read loop and heartbeat do not
// interleave bytes.
func (c *WSConn) writeFrame(opcode byte, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var header [10]byte
	header[0] = 0x80 | opcode // FIN + opcode
	n := 2
	switch l := len(payload); {
	case l < 126:
		header[1] = byte(l)
	case l < 1<<16:
		header[1] = 126
		binary.BigEndian.PutUint16(header[2:4], uint16(l))
		n = 4
	default:
		header[1] = 127
		binary.BigEndian.PutUint64(header[2:10], uint64(l))
		n = 10
	}
	if _, err := c.conn.Write(header[:n]); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := c.conn.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

// readFrame reads and unmasks one frame from br. Client-to-server frames MUST be
// masked (RFC 6455 §5.1); an unmasked one is a protocol error.
func readFrame(br *bufio.Reader) (fin bool, opcode byte, payload []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(br, h[:]); err != nil {
		return
	}
	fin = h[0]&0x80 != 0
	opcode = h[0] & 0x0f
	masked := h[1]&0x80 != 0
	length := int(h[1] & 0x7f)
	switch length {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(br, ext[:]); err != nil {
			return
		}
		length = int(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(br, ext[:]); err != nil {
			return
		}
		length = int(binary.BigEndian.Uint64(ext[:]))
	}
	if length < 0 || length > maxFramePayload {
		err = errFrameTooLarge
		return
	}
	// Control frames must carry <=125 bytes (RFC 6455 §5.5).
	if opcode >= opClose && length > 125 {
		err = errControlTooLarge
		return
	}
	if !masked {
		err = errUnmaskedClient
		return
	}
	var mask [4]byte
	if _, err = io.ReadFull(br, mask[:]); err != nil {
		return
	}
	payload = make([]byte, length)
	if _, err = io.ReadFull(br, payload); err != nil {
		return
	}
	for i := range payload {
		payload[i] ^= mask[i%4]
	}
	return fin, opcode, payload, nil
}

// CableOptions configures [Handler].
type CableOptions struct {
	// OnConnect is the connect method run at open for identification/authorization
	// (see [ConnectHook]); may be nil.
	OnConnect ConnectHook
	// PingInterval is the protocol-level ping period. Zero disables the heartbeat.
	// The application-level ping frame Action Cable clients expect is a separate,
	// host-driven concern; this keeps idle intermediaries from dropping the socket.
	PingInterval time.Duration
}

// Handler returns an http.Handler for the /cable endpoint. It upgrades each
// request to WebSocket, builds a [Connection] whose [Transport] writes text frames
// to the socket and whose channels come from factory, runs the open handshake
// (connect hook + welcome), then pumps client frames into [Connection.Dispatch]
// until the socket closes. A failed handshake is answered with 400/426 and the
// socket is not driven. This is the concrete /cable transport; hosts wanting their
// own may keep using [NewConnection] with a custom [Transport] directly.
func Handler(server *Server, factory ChannelFactory, opts *CableOptions) http.Handler {
	if opts == nil {
		opts = &CableOptions{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := Upgrade(w, r)
		if err != nil {
			status := http.StatusBadRequest
			if err == errBadSubprotocol {
				status = http.StatusUpgradeRequired
			}
			http.Error(w, err.Error(), status)
			return
		}
		serveConn(ws, server, factory, opts)
	})
}

// serveConn drives a single upgraded connection: it wires the transport, runs the
// open handshake, starts the heartbeat, and pumps inbound frames until close.
func serveConn(ws *WSConn, server *Server, factory ChannelFactory, opts *CableOptions) {
	conn := NewConnection(server, func(payload []byte) { _ = ws.writeText(payload) }, factory)
	if opts.OnConnect != nil {
		conn.OnConnect(opts.OnConnect)
	}
	defer func() {
		conn.Disconnect(DisconnectServerRestart, true)
		ws.Close()
	}()

	if err := conn.Connect(); err != nil {
		// An unauthorized (or otherwise failed) connect: the disconnect frame, if
		// any, was already transmitted by Connect; stop driving the socket.
		return
	}

	done := make(chan struct{})
	defer close(done)
	if opts.PingInterval > 0 {
		go heartbeat(ws, opts.PingInterval, done)
	}

	for {
		msg, err := ws.readMessage()
		if err != nil {
			return
		}
		_ = conn.Dispatch(msg)
	}
}

// heartbeat sends a protocol ping every interval until done is closed.
func heartbeat(ws *WSConn, interval time.Duration, done <-chan struct{}) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			if err := ws.writePing(); err != nil {
				return
			}
		}
	}
}
