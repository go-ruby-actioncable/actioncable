package actioncable

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---- test helpers: RFC 6455 client-side framing ----------------------------

// maskedFrame builds a client-to-server frame (always masked, per RFC 6455).
func maskedFrame(opcode byte, payload []byte) []byte {
	var buf bytes.Buffer
	buf.WriteByte(0x80 | opcode) // FIN + opcode
	mask := []byte{0xA1, 0xB2, 0xC3, 0xD4}
	switch l := len(payload); {
	case l < 126:
		buf.WriteByte(0x80 | byte(l))
	case l < 1<<16:
		buf.WriteByte(0x80 | 126)
		var ext [2]byte
		binary.BigEndian.PutUint16(ext[:], uint16(l))
		buf.Write(ext[:])
	default:
		buf.WriteByte(0x80 | 127)
		var ext [8]byte
		binary.BigEndian.PutUint64(ext[:], uint64(l))
		buf.Write(ext[:])
	}
	buf.Write(mask)
	for i, b := range payload {
		buf.WriteByte(b ^ mask[i%4])
	}
	return buf.Bytes()
}

// readServerFrame reads one unmasked server-to-client frame.
func readServerFrame(r *bufio.Reader) (opcode byte, payload []byte, err error) {
	var h [2]byte
	if _, err = io.ReadFull(r, h[:]); err != nil {
		return
	}
	opcode = h[0] & 0x0f
	l := int(h[1] & 0x7f)
	switch l {
	case 126:
		var ext [2]byte
		if _, err = io.ReadFull(r, ext[:]); err != nil {
			return
		}
		l = int(binary.BigEndian.Uint16(ext[:]))
	case 127:
		var ext [8]byte
		if _, err = io.ReadFull(r, ext[:]); err != nil {
			return
		}
		l = int(binary.BigEndian.Uint64(ext[:]))
	}
	payload = make([]byte, l)
	_, err = io.ReadFull(r, payload)
	return
}

// scriptConn is a net.Conn over separate read/write halves, optionally failing the
// nth Write, for driving the framing layer deterministically.
type scriptConn struct {
	r           io.Reader
	w           io.Writer
	failWriteAt int // 0 = never; else the nth (1-based) Write returns an error
	writeN      int
	closed      bool
}

func (s *scriptConn) Read(p []byte) (int, error) { return s.r.Read(p) }
func (s *scriptConn) Write(p []byte) (int, error) {
	s.writeN++
	if s.failWriteAt > 0 && s.writeN >= s.failWriteAt {
		return 0, errors.New("scriptConn: write failed")
	}
	if s.w == nil {
		return len(p), nil
	}
	return s.w.Write(p)
}
func (s *scriptConn) Close() error                     { s.closed = true; return nil }
func (s *scriptConn) LocalAddr() net.Addr              { return nil }
func (s *scriptConn) RemoteAddr() net.Addr             { return nil }
func (s *scriptConn) SetDeadline(time.Time) error      { return nil }
func (s *scriptConn) SetReadDeadline(time.Time) error  { return nil }
func (s *scriptConn) SetWriteDeadline(time.Time) error { return nil }

func newWSConn(in []byte, out io.Writer, failWriteAt int) *WSConn {
	sc := &scriptConn{r: bytes.NewReader(in), w: out, failWriteAt: failWriteAt}
	return &WSConn{conn: sc, br: bufio.NewReader(sc)}
}

// ---- handshake / negotiation ----------------------------------------------

func TestNegotiateAndUpgradeHelpers(t *testing.T) {
	if p, ok := negotiateSubprotocol("actioncable-v1-json, x"); !ok || p != "actioncable-v1-json" {
		t.Fatalf("negotiate: %q %v", p, ok)
	}
	if _, ok := negotiateSubprotocol("nope"); ok {
		t.Fatal("negotiate should fail on unsupported")
	}
	if !tokenListContains("keep-alive, Upgrade", "upgrade") {
		t.Fatal("tokenListContains upgrade")
	}
	if tokenListContains("keep-alive", "upgrade") {
		t.Fatal("tokenListContains false positive")
	}
	base := func() *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/cable", nil)
		r.Header.Set("Connection", "Upgrade")
		r.Header.Set("Upgrade", "websocket")
		r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
		return r
	}
	if !isWebSocketUpgrade(base()) {
		t.Fatal("valid upgrade rejected")
	}
	bad := base()
	bad.Method = http.MethodPost
	if isWebSocketUpgrade(bad) {
		t.Fatal("POST accepted as upgrade")
	}
	bad = base()
	bad.Header.Del("Sec-WebSocket-Key")
	if isWebSocketUpgrade(bad) {
		t.Fatal("missing key accepted")
	}
	if acceptKey("dGhlIHNhbXBsZSBub25jZQ==") != "s3pPLMBiTxaQ9kYGzzhZRbK+xOo=" {
		t.Fatalf("acceptKey RFC6455 vector wrong: %s", acceptKey("dGhlIHNhbXBsZSBub25jZQ=="))
	}
}

// fakeHijacker is a ResponseWriter that hijacks to a caller-provided conn/brw.
type fakeHijacker struct {
	hdr       http.Header
	hijackErr error
	conn      net.Conn
	brw       *bufio.ReadWriter
}

func (f *fakeHijacker) Header() http.Header         { return f.hdr }
func (f *fakeHijacker) Write(p []byte) (int, error) { return len(p), nil }
func (f *fakeHijacker) WriteHeader(int)             {}
func (f *fakeHijacker) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if f.hijackErr != nil {
		return nil, nil, f.hijackErr
	}
	return f.conn, f.brw, nil
}

func upgradeReq() *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/cable", nil)
	r.Header.Set("Connection", "Upgrade")
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	r.Header.Set("Sec-WebSocket-Protocol", "actioncable-v1-json")
	return r
}

func TestUpgrade_ErrorPaths(t *testing.T) {
	// not a websocket request
	if _, err := Upgrade(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/cable", nil)); err != errNotWebSocket {
		t.Fatalf("want errNotWebSocket, got %v", err)
	}
	// unsupported sub-protocol
	r := upgradeReq()
	r.Header.Set("Sec-WebSocket-Protocol", "nope")
	if _, err := Upgrade(httptest.NewRecorder(), r); err != errBadSubprotocol {
		t.Fatalf("want errBadSubprotocol, got %v", err)
	}
	// ResponseWriter without Hijack support
	if _, err := Upgrade(httptest.NewRecorder(), upgradeReq()); err != errNoHijack {
		t.Fatalf("want errNoHijack, got %v", err)
	}
	// Hijack itself errors
	fh := &fakeHijacker{hdr: http.Header{}, hijackErr: errors.New("no hijack")}
	if _, err := Upgrade(fh, upgradeReq()); err == nil || err.Error() != "no hijack" {
		t.Fatalf("want hijack error, got %v", err)
	}
	// WriteString error (tiny buffer forces a flush during the write)
	fc := &scriptConn{r: bytes.NewReader(nil), failWriteAt: 1}
	fh = &fakeHijacker{hdr: http.Header{}, conn: fc,
		brw: bufio.NewReadWriter(bufio.NewReader(fc), bufio.NewWriterSize(fc, 1))}
	if _, err := Upgrade(fh, upgradeReq()); err == nil {
		t.Fatal("want WriteString error")
	}
	if !fc.closed {
		t.Fatal("conn not closed after write error")
	}
	// Flush error (default buffer holds the response; the failing Write happens at Flush)
	fc = &scriptConn{r: bytes.NewReader(nil), failWriteAt: 1}
	fh = &fakeHijacker{hdr: http.Header{}, conn: fc,
		brw: bufio.NewReadWriter(bufio.NewReader(fc), bufio.NewWriter(fc))}
	if _, err := Upgrade(fh, upgradeReq()); err == nil {
		t.Fatal("want Flush error")
	}
	// Success
	fc = &scriptConn{r: bytes.NewReader(nil), w: io.Discard}
	fh = &fakeHijacker{hdr: http.Header{}, conn: fc,
		brw: bufio.NewReadWriter(bufio.NewReader(fc), bufio.NewWriter(fc))}
	ws, err := Upgrade(fh, upgradeReq())
	if err != nil || ws.Subprotocol != "actioncable-v1-json" {
		t.Fatalf("upgrade success: %v %q", err, ws.Subprotocol)
	}
}

// ---- framing ---------------------------------------------------------------

func TestWriteFrame_Lengths(t *testing.T) {
	for _, n := range []int{5, 200, 1 << 16} {
		var out bytes.Buffer
		ws := newWSConn(nil, &out, 0)
		if err := ws.writeText(bytes.Repeat([]byte("a"), n)); err != nil {
			t.Fatalf("writeText(%d): %v", n, err)
		}
		op, payload, err := readServerFrame(bufio.NewReader(&out))
		if err != nil || op != opText || len(payload) != n {
			t.Fatalf("roundtrip n=%d: op=%d len=%d err=%v", n, op, len(payload), err)
		}
	}
	// header write error
	ws := newWSConn(nil, io.Discard, 1)
	if err := ws.writeText([]byte("x")); err == nil {
		t.Fatal("want header write error")
	}
	// payload write error (header ok, second Write fails)
	ws = newWSConn(nil, io.Discard, 2)
	if err := ws.writeText([]byte("x")); err == nil {
		t.Fatal("want payload write error")
	}
	// writePing (empty payload path) and Close
	var out bytes.Buffer
	ws = newWSConn(nil, &out, 0)
	if err := ws.writePing(); err != nil {
		t.Fatal(err)
	}
	if err := ws.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReadFrame_ErrorPaths(t *testing.T) {
	rf := func(b []byte) error {
		_, _, _, err := readFrame(bufio.NewReader(bytes.NewReader(b)))
		return err
	}
	if err := rf(nil); err == nil {
		t.Fatal("empty: want header read error")
	}
	if err := rf([]byte{0x81, 0xFE}); err == nil {
		t.Fatal("126 ext: want read error")
	}
	if err := rf([]byte{0x81, 0xFF}); err == nil {
		t.Fatal("127 ext: want read error")
	}
	// too large (127 length beyond cap)
	big := []byte{0x81, 127}
	var ext [8]byte
	binary.BigEndian.PutUint64(ext[:], maxFramePayload+1)
	if err := rf(append(big, ext[:]...)); err != errFrameTooLarge {
		t.Fatalf("want errFrameTooLarge, got %v", err)
	}
	// control frame too large (opClose, 16-bit length 200)
	ctrl := []byte{0x88, 126, 0x00, 0xC8}
	if err := rf(ctrl); err != errControlTooLarge {
		t.Fatalf("want errControlTooLarge, got %v", err)
	}
	// unmasked client frame
	if err := rf([]byte{0x81, 0x05, 'h', 'e', 'l', 'l', 'o'}); err != errUnmaskedClient {
		t.Fatalf("want errUnmaskedClient, got %v", err)
	}
	// mask read truncated
	if err := rf([]byte{0x81, 0x85}); err == nil {
		t.Fatal("mask: want read error")
	}
	// payload read truncated
	if err := rf([]byte{0x81, 0x85, 0x01, 0x02, 0x03, 0x04}); err == nil {
		t.Fatal("payload: want read error")
	}
	// success + unmask
	fin, op, payload, err := readFrame(bufio.NewReader(bytes.NewReader(maskedFrame(opText, []byte("hi")))))
	if err != nil || !fin || op != opText || string(payload) != "hi" {
		t.Fatalf("read success: fin=%v op=%d payload=%q err=%v", fin, op, payload, err)
	}
}

func TestReadMessage_Control(t *testing.T) {
	// ping -> pong, then text returned
	in := append(maskedFrame(opPing, []byte("p")), maskedFrame(opText, []byte("hi"))...)
	var out bytes.Buffer
	ws := newWSConn(in, &out, 0)
	msg, err := ws.readMessage()
	if err != nil || string(msg) != "hi" {
		t.Fatalf("ping/text: %q %v", msg, err)
	}
	op, payload, _ := readServerFrame(bufio.NewReader(&out))
	if op != opPong || string(payload) != "p" {
		t.Fatalf("expected pong echo, got op=%d %q", op, payload)
	}
	// unsolicited pong ignored, then text
	in = append(maskedFrame(opPong, nil), maskedFrame(opText, []byte("yo"))...)
	ws = newWSConn(in, io.Discard, 0)
	if msg, err = ws.readMessage(); err != nil || string(msg) != "yo" {
		t.Fatalf("pong/text: %q %v", msg, err)
	}
	// close frame
	ws = newWSConn(maskedFrame(opClose, nil), io.Discard, 0)
	if _, err = ws.readMessage(); err != errClientClosed {
		t.Fatalf("want errClientClosed, got %v", err)
	}
	// non-final frame is rejected
	nonFinal := maskedFrame(opText, nil)
	nonFinal[0] &^= 0x80 // clear FIN
	ws = newWSConn(nonFinal, io.Discard, 0)
	if _, err = ws.readMessage(); err != errReservedOpcode {
		t.Fatalf("non-final: want errReservedOpcode, got %v", err)
	}
	// reserved data opcode
	ws = newWSConn(maskedFrame(0x3, nil), io.Discard, 0)
	if _, err = ws.readMessage(); err != errReservedOpcode {
		t.Fatalf("reserved opcode: got %v", err)
	}
	// readFrame error surfaces
	ws = newWSConn(nil, io.Discard, 0)
	if _, err = ws.readMessage(); err == nil {
		t.Fatal("want read error")
	}
	// pong write error on ping
	ws = newWSConn(maskedFrame(opPing, nil), io.Discard, 1)
	if _, err = ws.readMessage(); err == nil {
		t.Fatal("want pong write error")
	}
}

// ---- heartbeat -------------------------------------------------------------

func TestHeartbeat(t *testing.T) {
	// ticker fires -> writePing succeeds, then done closes -> return
	var out bytes.Buffer
	ws := newWSConn(nil, &out, 0)
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() { heartbeat(ws, time.Millisecond, done); close(stopped) }()
	// read at least one ping frame off the wire
	deadline := time.After(2 * time.Second)
	for {
		ws.mu.Lock()
		n := out.Len()
		ws.mu.Unlock()
		if n >= 2 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("no ping emitted")
		case <-time.After(time.Millisecond):
		}
	}
	close(done)
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeat did not stop on done")
	}
	// writePing error path: failing conn returns from the ticker branch
	ws = newWSConn(nil, io.Discard, 1)
	stopped = make(chan struct{})
	go func() { heartbeat(ws, time.Millisecond, make(chan struct{})); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeat did not return on write error")
	}
}

// ---- serveConn over an in-memory pipe --------------------------------------

func TestServeConn_Unauthorized(t *testing.T) {
	c1, c2 := net.Pipe()
	ws := &WSConn{conn: c1, br: bufio.NewReader(c1)}
	srv := NewServer(NewAsyncAdapter())
	factory := func(conn *Connection, id string, p map[string]any) (*Channel, bool) {
		return NewChannel(conn, "chat", id, p, nil), true
	}
	opts := &CableOptions{OnConnect: func(c *Connection) error { return c.RejectUnauthorized() }}
	go serveConn(ws, srv, factory, opts)

	cr := bufio.NewReader(c2)
	op, payload, err := readServerFrame(cr)
	if err != nil || op != opText {
		t.Fatalf("expected disconnect text frame: op=%d err=%v", op, err)
	}
	if !strings.Contains(string(payload), `"reason":"unauthorized"`) {
		t.Fatalf("unauthorized disconnect payload: %s", payload)
	}
	c2.Close()
}

func TestServeConn_HappyPath(t *testing.T) {
	c1, c2 := net.Pipe()
	ws := &WSConn{conn: c1, br: bufio.NewReader(c1)}
	srv := NewServer(NewAsyncAdapter())
	factory := func(conn *Connection, id string, p map[string]any) (*Channel, bool) {
		var ch *Channel
		action := func(_, act string, _ any) any {
			switch act {
			case "subscribed":
				_ = ch.StreamFrom("chat:1")
			case "speak":
				_ = srv.Broadcast("chat:1", map[string]any{"text": "hi"})
			}
			return nil
		}
		ch = NewChannel(conn, "chat", id, p, action)
		return ch, true
	}
	go serveConn(ws, srv, factory, &CableOptions{})

	cr := bufio.NewReader(c2)
	// welcome
	if _, p, err := readServerFrame(cr); err != nil || string(p) != `{"type":"welcome"}` {
		t.Fatalf("welcome: %q %v", p, err)
	}
	// subscribe -> confirm
	if _, err := c2.Write(maskedFrame(opText, []byte(`{"command":"subscribe","identifier":"{\"channel\":\"ChatChannel\"}"}`))); err != nil {
		t.Fatal(err)
	}
	if _, p, err := readServerFrame(cr); err != nil || !strings.Contains(string(p), "confirm_subscription") {
		t.Fatalf("confirm: %q %v", p, err)
	}
	// message speak -> broadcast fans back a channel message
	if _, err := c2.Write(maskedFrame(opText, []byte(`{"command":"message","identifier":"{\"channel\":\"ChatChannel\"}","data":"{\"action\":\"speak\"}"}`))); err != nil {
		t.Fatal(err)
	}
	if _, p, err := readServerFrame(cr); err != nil || !strings.Contains(string(p), `"message":{"text":"hi"}`) {
		t.Fatalf("broadcast message: %q %v", p, err)
	}
	c2.Close() // client goes away -> read loop errors -> serveConn returns
}

// ---- Handler over a real TCP socket ----------------------------------------

func TestHandler_EndToEnd(t *testing.T) {
	srv := NewServer(NewAsyncAdapter())
	factory := func(conn *Connection, id string, p map[string]any) (*Channel, bool) {
		return NewChannel(conn, "chat", id, p, nil), true
	}
	ts := httptest.NewServer(Handler(srv, factory, &CableOptions{PingInterval: time.Millisecond}))
	defer ts.Close()

	conn, err := net.Dial("tcp", strings.TrimPrefix(ts.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	key := "dGhlIHNhbXBsZSBub25jZQ=="
	req := "GET /cable HTTP/1.1\r\nHost: x\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + key + "\r\nSec-WebSocket-Version: 13\r\n" +
		"Sec-WebSocket-Protocol: actioncable-v1-json\r\n\r\n"
	if _, err := io.WriteString(conn, req); err != nil {
		t.Fatal(err)
	}
	br := bufio.NewReader(conn)
	// read the handshake response headers
	var accept, proto string
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("reading handshake: %v", err)
		}
		if line == "\r\n" {
			break
		}
		if h, v, ok := strings.Cut(line, ": "); ok {
			switch http.CanonicalHeaderKey(h) {
			case "Sec-Websocket-Accept":
				accept = strings.TrimSpace(v)
			case "Sec-Websocket-Protocol":
				proto = strings.TrimSpace(v)
			}
		}
	}
	if accept != acceptKey(key) {
		t.Fatalf("bad accept: %q want %q", accept, acceptKey(key))
	}
	if proto != "actioncable-v1-json" {
		t.Fatalf("bad negotiated protocol: %q", proto)
	}
	// welcome frame, then subscribe -> confirm
	if _, p, err := readServerFrame(br); err != nil || string(p) != `{"type":"welcome"}` {
		t.Fatalf("welcome over TCP: %q %v", p, err)
	}
	if _, err := conn.Write(maskedFrame(opText, []byte(`{"command":"subscribe","identifier":"{\"channel\":\"ChatChannel\"}"}`))); err != nil {
		t.Fatal(err)
	}
	// a heartbeat ping may arrive interleaved; skip control frames to find the confirm
	for {
		op, p, err := readServerFrame(br)
		if err != nil {
			t.Fatalf("confirm over TCP: %v", err)
		}
		if op == opPing {
			continue
		}
		if !strings.Contains(string(p), "confirm_subscription") {
			t.Fatalf("expected confirm, got op=%d %q", op, p)
		}
		break
	}
}

func TestHandler_HandshakeErrors(t *testing.T) {
	ts := httptest.NewServer(Handler(NewServer(NewAsyncAdapter()),
		func(conn *Connection, id string, p map[string]any) (*Channel, bool) {
			return NewChannel(conn, "chat", id, p, nil), true
		}, nil))
	defer ts.Close()

	// plain GET: not a websocket upgrade -> 400
	resp, err := http.Get(ts.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("plain GET status: %d", resp.StatusCode)
	}
	// upgrade with unsupported sub-protocol -> 426
	req, _ := http.NewRequest(http.MethodGet, ts.URL, nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Header.Set("Sec-WebSocket-Protocol", "unsupported-proto")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUpgradeRequired {
		t.Fatalf("bad-subprotocol status: %d", resp.StatusCode)
	}
}
