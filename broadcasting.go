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
// class name, mirroring ActionCable::Channel::Base.channel_name:
// name.sub(/Channel$/, "").gsub("::", ":").underscore.
//
//	"ChatChannel"       -> "chat"
//	"Chat::RoomChannel" -> "chat:room"
//	"AdminNotifications"-> "admin_notifications"
func ChannelName(className string) string {
	s := strings.TrimSuffix(className, "Channel")
	s = strings.ReplaceAll(s, "::", ":")
	return underscore(s)
}

// underscore lower-cases and inserts "_" at camel-hump boundaries, matching the
// relevant part of ActiveSupport's String#underscore for ASCII class names.
func underscore(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			if i > 0 {
				prev := s[i-1]
				if (prev >= 'a' && prev <= 'z') || (prev >= '0' && prev <= '9') {
					b.WriteByte('_')
				}
			}
			b.WriteByte(c - 'A' + 'a')
		} else {
			b.WriteByte(c)
		}
	}
	return b.String()
}
