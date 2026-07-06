package actioncable

import "testing"

type paramModel struct{ id string }

func (m paramModel) ToParam() string { return m.id }

type gidModel struct{ id string }

func (m gidModel) ToParam() string    { return "param-" + m.id }
func (m gidModel) ToGIDParam() string { return "gid://app/Room/" + m.id }

type stringerModel struct{ id string }

func (m stringerModel) String() string { return "str-" + m.id }

func TestChannelName(t *testing.T) {
	cases := map[string]string{
		"ChatChannel":        "chat",
		"Chat::RoomChannel":  "chat:room",
		"AdminNotifications": "admin_notifications",
		"ApplicationCable":   "application_cable",
		"AB9Channel":         "ab9",
	}
	for in, want := range cases {
		if got := ChannelName(in); got != want {
			t.Errorf("ChannelName(%q)=%q want %q", in, got, want)
		}
	}
}

func TestSerializeBroadcasting_AllParamForms(t *testing.T) {
	if got := BroadcastingName("chat", paramModel{"42"}); got != "chat:42" {
		t.Errorf("param form: %q", got)
	}
	if got := BroadcastingName("chat", gidModel{"7"}); got != "chat:gid://app/Room/7" {
		t.Errorf("gid form: %q", got)
	}
	if got := BroadcastingName("chat", stringerModel{"x"}); got != "chat:str-x" {
		t.Errorf("stringer form: %q", got)
	}
	if got := BroadcastingName("chat", "roomA"); got != "chat:roomA" {
		t.Errorf("string form: %q", got)
	}
	if got := BroadcastingName("chat", 99); got != "chat:99" {
		t.Errorf("default form: %q", got)
	}
	if got := SerializeBroadcasting("a", "b", "c"); got != "a:b:c" {
		t.Errorf("multi: %q", got)
	}
}
