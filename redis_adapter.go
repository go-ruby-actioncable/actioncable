package actioncable

// RedisPubSub is the minimal Redis pub-sub client seam the [RedisAdapter] drives.
// It is deliberately tiny so the host wires in a real client (e.g. go-ruby-redis)
// without this module taking a runtime dependency on it, and so the adapter is
// testable with an in-memory fake. Publish maps to Redis PUBLISH; Subscribe maps
// to SUBSCRIBE and returns an unsubscribe.
type RedisPubSub interface {
	Publish(channel string, payload []byte) error
	Subscribe(channel string, handler func(payload []byte)) (unsubscribe func(), err error)
}

// RedisAdapter is the pub-sub [Adapter] backed by Redis, the analogue of Action
// Cable's :redis adapter. It broadcasts across processes/servers by publishing
// on the Redis channel named after the broadcasting. It carries no Redis
// implementation itself: it forwards to an injected [RedisPubSub] client.
type RedisAdapter struct {
	client RedisPubSub
}

// NewRedisAdapter returns a RedisAdapter forwarding to client.
func NewRedisAdapter(client RedisPubSub) *RedisAdapter {
	return &RedisAdapter{client: client}
}

// Broadcast publishes payload on the Redis channel named broadcasting.
func (r *RedisAdapter) Broadcast(broadcasting string, payload []byte) error {
	return r.client.Publish(broadcasting, payload)
}

// Subscribe subscribes handler to the Redis channel named broadcasting.
func (r *RedisAdapter) Subscribe(broadcasting string, handler func([]byte)) (func(), error) {
	return r.client.Subscribe(broadcasting, handler)
}
