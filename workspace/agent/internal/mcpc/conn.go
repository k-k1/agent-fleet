package mcpc

import "context"

// conn is one transport's JSON-RPC exchange primitive, independent of what era of the
// protocol is being spoken over it — the era (whether params carry `_meta`, and for
// HTTP which headers ride the request) is decided by the caller (server.go's
// handshake) and passed in per-call as stateless.
type conn interface {
	// call sends a request and returns the matching response (or ctx's error, or a
	// transport failure — a process dying, a socket closing).
	call(ctx context.Context, method string, params any, stateless bool) (rpcMsg, error)
	// notify sends a fire-and-forget message (no id, no reply expected).
	notify(ctx context.Context, method string, params any, stateless bool) error
	// notifications delivers the METHOD name of every server-initiated message this
	// conn cannot itself make sense of (id-less messages arriving outside a call's own
	// response) — server.go only acts on "notifications/tools/list_changed", but the
	// channel is not filtered here so a future caller is not blocked from other names.
	// Closed when the connection is torn down.
	notifications() <-chan string
	// close tears the transport down: for stdio, close stdin and kill/reap the child;
	// for HTTP, stop the notification stream. Idempotent.
	close() error
}
