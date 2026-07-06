package actioncable

import "testing"

func TestEncodeFrames_ByteFaithful(t *testing.T) {
	cases := []struct {
		name string
		got  []byte
		want string
	}{
		{"welcome", EncodeWelcome(), `{"type":"welcome"}`},
		{"ping", EncodePing(1751800000), `{"type":"ping","message":1751800000}`},
		{"confirm", EncodeConfirmation(`{"channel":"ChatChannel"}`),
			`{"identifier":"{\"channel\":\"ChatChannel\"}","type":"confirm_subscription"}`},
		{"reject", EncodeRejection(`{"channel":"ChatChannel"}`),
			`{"identifier":"{\"channel\":\"ChatChannel\"}","type":"reject_subscription"}`},
		{"disconnect", EncodeDisconnect(DisconnectServerRestart, true),
			`{"type":"disconnect","reason":"server_restart","reconnect":true}`},
		{"disconnect-noreconnect", EncodeDisconnect(DisconnectUnauthorized, false),
			`{"type":"disconnect","reason":"unauthorized","reconnect":false}`},
	}
	for _, tc := range cases {
		if string(tc.got) != tc.want {
			t.Errorf("%s: got %s want %s", tc.name, tc.got, tc.want)
		}
	}
}

func TestEncodeMessage(t *testing.T) {
	got, err := EncodeMessage(`{"channel":"ChatChannel"}`, map[string]any{"text": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"identifier":"{\"channel\":\"ChatChannel\"}","message":{"text":"hi"}}`
	if string(got) != want {
		t.Errorf("got %s want %s", got, want)
	}
}

func TestEncodeMessage_NullAndNoHTMLEscape(t *testing.T) {
	got, err := EncodeMessage("id", nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"identifier":"id","message":null}` {
		t.Errorf("nil message: %s", got)
	}
	// ActiveSupport::JSON leaves <, >, & intact by default.
	got, err = EncodeMessage("id", "<b>&</b>")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"identifier":"id","message":"<b>&</b>"}` {
		t.Errorf("html escape leaked: %s", got)
	}
}

func TestEncodeMessage_Error(t *testing.T) {
	if _, err := EncodeMessage("id", make(chan int)); err == nil {
		t.Fatal("expected encode error for unencodable message")
	}
}

func TestMustEncode_Panics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	_ = mustEncode(make(chan int))
}

func TestDecodeCommand(t *testing.T) {
	cmd, err := DecodeCommand([]byte(`{"command":"subscribe","identifier":"{\"channel\":\"ChatChannel\"}"}`))
	if err != nil {
		t.Fatal(err)
	}
	if cmd.Command != CommandSubscribe || cmd.Identifier != `{"channel":"ChatChannel"}` {
		t.Errorf("bad decode: %+v", cmd)
	}
	if _, err := DecodeCommand([]byte(`not json`)); err == nil {
		t.Fatal("expected decode error")
	}
}

func TestParseIdentifier(t *testing.T) {
	m, err := parseIdentifier(`{"channel":"ChatChannel","room":"1"}`)
	if err != nil {
		t.Fatal(err)
	}
	if m["channel"] != "ChatChannel" || m["room"] != "1" {
		t.Errorf("bad params: %v", m)
	}
	empty, err := parseIdentifier("")
	if err != nil || len(empty) != 0 {
		t.Errorf("empty identifier: %v %v", empty, err)
	}
	if _, err := parseIdentifier("{bad"); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestProtocolConstants(t *testing.T) {
	if DefaultMountPath != "/cable" || len(Protocols) != 2 || Protocols[0] != "actioncable-v1-json" {
		t.Fatal("protocol constants drifted")
	}
}
