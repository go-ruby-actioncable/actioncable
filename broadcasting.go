package actioncable

import (
	"fmt"
	"strings"
)

// Parameterizer is satisfied by a model that knows its own #to_param, used when
// serializing a broadcasting name (mirrors Rails' object.to_param).
type Parameterizer interface {
	ToParam() string
}

// GIDParameterizer is satisfied by a model that exposes a GlobalID param,
// preferred over ToParam when serializing (mirrors object.to_gid_param).
type GIDParameterizer interface {
	ToGIDParam() string
}

// paramOf serializes a single broadcasting component the way Rails'
// Channel::Broadcasting#serialize_broadcasting does: a GlobalID param if the
// object responds to to_gid_param, else its to_param, else its string form.
func paramOf(o any) string {
	switch v := o.(type) {
	case GIDParameterizer:
		return v.ToGIDParam()
	case Parameterizer:
		return v.ToParam()
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	default:
		return fmt.Sprint(o)
	}
}

// SerializeBroadcasting joins the serialized components with ":", mirroring
// ActionCable::Channel::Broadcasting#serialize_broadcasting for an Array.
func SerializeBroadcasting(objects ...any) string {
	parts := make([]string, len(objects))
	for i, o := range objects {
		parts[i] = paramOf(o)
	}
	return strings.Join(parts, ":")
}

// BroadcastingName returns the stable broadcasting string for a channel and a
// model — the value ActionCable.server.broadcast fans out on. It mirrors
// ChannelClass.broadcasting_for(model) == serialize_broadcasting([channel_name, model]).
func BroadcastingName(channelName string, model any) string {
	return SerializeBroadcasting(channelName, model)
}

// ChannelName derives a channel's broadcasting-name component from its Ruby
// class name, mirroring ActionCable::Channel::Naming#channel_name:
// name.delete_suffix("Channel").gsub("::", ":").underscore.
//
//	"ChatChannel"                     -> "chat"
//	"Chat::RoomChannel"               -> "chat:room"
//	"AdminNotificationsChannel"       -> "admin_notifications"
//	"FooChats::BarAppearancesChannel" -> "foo_chats:bar_appearances"
//	"HTTPServerChannel"               -> "http_server"
func ChannelName(className string) string {
	s := strings.TrimSuffix(className, "Channel")
	s = strings.ReplaceAll(s, "::", ":")
	return underscore(s)
}

// underscore lower-cases and inserts "_" at word boundaries the way ActiveSupport's
// String#underscore does for ASCII class names, implementing both of its
// substitutions: the acronym boundary /([A-Z\d]+)([A-Z][a-z])/ (so "HTTPServer" ->
// "http_server") and the camel-hump boundary /([a-z\d])([A-Z])/ (so
// "AdminNotifications" -> "admin_notifications"); "-" becomes "_". Non-letter
// separators such as the ":" left by the "::" replacement pass through untouched.
func underscore(s string) string {
	isUpper := func(c byte) bool { return c >= 'A' && c <= 'Z' }
	isLower := func(c byte) bool { return c >= 'a' && c <= 'z' }
	isDigit := func(c byte) bool { return c >= '0' && c <= '9' }

	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '-' {
			b.WriteByte('_')
			continue
		}
		if isUpper(c) && i > 0 {
			prev := s[i-1]
			var next byte
			if i+1 < len(s) {
				next = s[i+1]
			}
			switch {
			case isLower(prev) || isDigit(prev): // /([a-z\d])([A-Z])/
				b.WriteByte('_')
			case isUpper(prev) && isLower(next): // /([A-Z\d]+)([A-Z][a-z])/
				b.WriteByte('_')
			}
		}
		if isUpper(c) {
			b.WriteByte(c - 'A' + 'a')
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}
