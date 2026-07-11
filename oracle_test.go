package actioncable

import (
	"encoding/json"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

// TestOracle_MRIWireFidelity is the differential test against the real MRI
// actioncable gem: it runs testdata/oracle.rb, which emits the exact bytes the gem
// produces for every wire frame and name derivation, then asserts the Go codec
// reproduces each one byte-for-byte. The gem is the ground truth for the
// on-the-wire actioncable-v1-json protocol.
//
// It skips itself (rather than failing) where the oracle cannot run — on Windows,
// where ruby is not on PATH, or where the actioncable gem is not installed — so the
// deterministic, ruby-free tests keep the coverage gate green on every lane while
// the ruby lanes exercise the MRI oracle.
func TestOracle_MRIWireFidelity(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("oracle: ruby oracle not run on Windows")
	}
	ruby, err := exec.LookPath("ruby")
	if err != nil {
		t.Skip("oracle: ruby not on PATH")
	}
	out, err := exec.Command(ruby, "testdata/oracle.rb").Output()
	if err != nil {
		// Most commonly the actioncable gem is not installed on this machine.
		t.Skipf("oracle: could not run testdata/oracle.rb (gem missing?): %v", err)
	}

	var gem map[string]string
	if err := json.Unmarshal(out, &gem); err != nil {
		t.Fatalf("oracle: decoding ruby output: %v\n%s", err, out)
	}

	// The Go computation for each case name the oracle emits. Every key the gem
	// produces must be covered here (and vice versa), so the two stay in lockstep.
	ident := `{"channel":"ChatChannel"}`
	mustMsg := func(id string, v any) string {
		b, err := EncodeMessage(id, v)
		if err != nil {
			t.Fatalf("EncodeMessage(%q): %v", id, err)
		}
		return string(b)
	}
	got := map[string]string{
		"welcome":                       string(EncodeWelcome()),
		"ping@1751800000":               string(EncodePing(1751800000)),
		"confirm@ident":                 string(EncodeConfirmation(ident)),
		"reject@ident":                  string(EncodeRejection(ident)),
		"disconnect@remote,true":        string(EncodeDisconnect(DisconnectRemote, true)),
		"disconnect@unauthorized,false": string(EncodeDisconnect(DisconnectUnauthorized, false)),
		"message@simple":                mustMsg(ident, map[string]any{"text": "hello"}),
		"message@html":                  mustMsg("id", map[string]any{"html": "<b>a&b</b>"}),
		"message@nested":                mustMsg("id", map[string]any{"a": []any{1, 2, map[string]any{"b": true}}, "c": nil}),

		"channel_name:ChatChannel":                     ChannelName("ChatChannel"),
		"channel_name:Chat::RoomChannel":               ChannelName("Chat::RoomChannel"),
		"channel_name:Chats::AppearancesChannel":       ChannelName("Chats::AppearancesChannel"),
		"channel_name:FooChats::BarAppearancesChannel": ChannelName("FooChats::BarAppearancesChannel"),
		"channel_name:AdminNotificationsChannel":       ChannelName("AdminNotificationsChannel"),
		"channel_name:NotificationsChannel":            ChannelName("NotificationsChannel"),
		"channel_name:APIChannel":                      ChannelName("APIChannel"),
		"channel_name:HTTPServerChannel":               ChannelName("HTTPServerChannel"),
		"channel_name:Foo-BarChannel":                  ChannelName("Foo-BarChannel"),

		"bcast:chat,1":       BroadcastingName("chat", "1"),
		"bcast:chat,42int":   SerializeBroadcasting("chat", 42),
		"bcast:comments,all": SerializeBroadcasting("comments", "all"),
	}

	if len(got) != len(gem) {
		t.Fatalf("oracle case-set drift: Go covers %d cases, gem emits %d", len(got), len(gem))
	}
	for name, want := range gem {
		g, ok := got[name]
		if !ok {
			t.Errorf("oracle: gem emitted case %q the Go test does not cover", name)
			continue
		}
		if g != want {
			t.Errorf("oracle mismatch for %q:\n Go:  %s\n gem: %s", name, g, want)
		}
	}
}

// TestChannelName_HumpTable pins the channel_name derivations independently of the
// gem so the coverage lanes without ruby still assert them (the oracle above adds
// the MRI cross-check on top where ruby is present).
func TestChannelName_HumpTable(t *testing.T) {
	cases := map[string]string{
		"ChatChannel":                     "chat",
		"Chat::RoomChannel":               "chat:room",
		"Chats::AppearancesChannel":       "chats:appearances",
		"FooChats::BarAppearancesChannel": "foo_chats:bar_appearances",
		"AdminNotificationsChannel":       "admin_notifications",
		"NotificationsChannel":            "notifications",
		"APIChannel":                      "api",
		"HTTPServerChannel":               "http_server",
		"Foo-BarChannel":                  "foo_bar",
	}
	for in, want := range cases {
		if got := ChannelName(in); got != want {
			t.Errorf("ChannelName(%q) = %q, want %q", in, got, want)
		}
	}
	if got := SerializeBroadcasting("chat", 42); got != "chat:42" {
		t.Errorf("SerializeBroadcasting int model = %q", got)
	}
	if !strings.HasPrefix(BroadcastingName("chat", "1"), "chat:") {
		t.Error("BroadcastingName prefix")
	}
}
