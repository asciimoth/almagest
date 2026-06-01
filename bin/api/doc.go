// Package api defines the transport-independent admin protocol shared by
// Almagest clients and the daemon.
//
// The protocol is intentionally small:
//   - every frame is one JSON Message;
//   - request and response messages are correlated by id;
//   - event messages are unprompted pushes, primarily for subscriptions;
//   - surface and method route messages without tying them to HTTP paths,
//     WebSocket frames, Unix sockets, or any other transport.
//
// Transport implementations may carry Message values as WebSocket JSON frames,
// newline-delimited JSON over a stream, or any other framing that preserves one
// JSON object per message.
package api
