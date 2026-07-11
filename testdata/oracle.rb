# frozen_string_literal: true
#
# Differential oracle for the Go actioncable codec: it emits, as a single JSON
# object on stdout, the *exact* bytes the real MRI actioncable gem produces for
# every wire frame and name derivation, keyed by a case name the Go test mirrors.
# The Go test (oracle_test.go) computes each case through the Go API and asserts
# byte-for-byte equality, so the gem is the ground truth for the on-the-wire
# protocol. Skipped automatically where ruby or the gem is absent.
#
# The frames are encoded exactly as ActionCable does: ActiveSupport::JSON (the
# connection's default coder) with its default escape_html_entities_in_json = true,
# and the key order the gem builds each hash in (connection/channel #transmit).

require "action_cable"
require "active_support/json"
require "active_support/core_ext/string/inflections" # String#underscore, #delete_suffix
require "active_support/core_ext/object/to_param"     # Object#to_param

types = ActionCable::INTERNAL[:message_types]
enc = ->(h) { ActiveSupport::JSON.encode(h) }
ident = "{\"channel\":\"ChatChannel\"}"

out = {}

# Server -> client frames, key order matching the gem's transmit call sites.
out["welcome"]                       = enc.call(type: types[:welcome])
out["ping@1751800000"]               = enc.call(type: types[:ping], message: 1_751_800_000)
out["confirm@ident"]                 = enc.call(identifier: ident, type: types[:confirmation])
out["reject@ident"]                  = enc.call(identifier: ident, type: types[:rejection])
out["disconnect@remote,true"]        = enc.call(type: types[:disconnect], reason: "remote", reconnect: true)
out["disconnect@unauthorized,false"] = enc.call(type: types[:disconnect], reason: "unauthorized", reconnect: false)

# Channel message frame: connection.transmit(identifier: @identifier, message: data).
out["message@simple"] = enc.call(identifier: ident, message: { "text" => "hello" })
out["message@html"]   = enc.call(identifier: "id", message: { "html" => "<b>a&b</b>" })
out["message@nested"] = enc.call(identifier: "id", message: { "a" => [1, 2, { "b" => true }], "c" => nil })

# channel_name derivation (Channel::Naming#channel_name).
{
  "ChatChannel"                  => "ChatChannel",
  "Chat::RoomChannel"            => "Chat::RoomChannel",
  "Chats::AppearancesChannel"    => "Chats::AppearancesChannel",
  "FooChats::BarAppearancesChannel" => "FooChats::BarAppearancesChannel",
  "AdminNotificationsChannel"    => "AdminNotificationsChannel",
  "NotificationsChannel"         => "NotificationsChannel",
  "APIChannel"                   => "APIChannel",
  "HTTPServerChannel"            => "HTTPServerChannel",
  "Foo-BarChannel"               => "Foo-BarChannel",
}.each do |key, klass|
  out["channel_name:#{key}"] = klass.delete_suffix("Channel").gsub("::", ":").underscore
end

# serialize_broadcasting([channel_name, model]) (Channel::Broadcasting).
def serialize(object)
  case
  when object.is_a?(Array) then object.map { |m| serialize(m) }.join(":")
  when object.respond_to?(:to_gid_param) then object.to_gid_param
  else object.to_param
  end
end
out["bcast:chat,1"]       = serialize(["chat", "1"])
out["bcast:chat,42int"]   = serialize(["chat", 42])
out["bcast:comments,all"] = serialize(["comments", "all"])

require "json"
puts JSON.generate(out)
