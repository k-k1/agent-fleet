// Package msp speaks the Muse Session Protocol: newline-delimited JSON-RPC 2.0 over the
// stdio of a `muse serve` child.
//
// It is the transport and vocabulary layer only — one connection, requests, notifications and
// the wire types. Nothing here knows about Agent Fleet sessions; the muse kind's driver sits
// on top of it (ADR 0095 decisions 2 and 3: managed-only, one host per session).
//
// The wire types in types_gen.go are rendered from schema/msp.schema.json, which the vendor's
// own binary exports offline. Regenerate after replacing the bundle:
//
//	go generate ./internal/msp/...
//
// A replaced bundle that nobody regenerated, and a binary whose protocol moved under a
// checked-in bundle, are both caught by fingerprint_test.go.
package msp

//go:generate go run ./gen .
