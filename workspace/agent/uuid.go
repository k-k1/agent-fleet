package main

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
)

// nsURL is RFC 4122 namespace "URL" — matches `uuidgen --sha1 --namespace @url`,
// which tmux-claude.sh uses to derive deterministic per-slot session IDs.
var nsURL = [16]byte{
	0x6b, 0xa7, 0xb8, 0x11, 0x9d, 0xad, 0x11, 0xd1,
	0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8,
}

// sessionUUID returns the deterministic UUIDv5 for a (dir, name) slot.
// Same slot => same id => claude --resume reuses the same web session,
// mirroring tmux-claude.sh's `uuidgen --sha1 --namespace @url --name "dir|name"`.
func sessionUUID(dir, name string) string {
	return uuidV5(nsURL, dir+"|"+name)
}

func uuidV5(ns [16]byte, name string) string {
	h := sha1.New()
	h.Write(ns[:])
	h.Write([]byte(name))
	sum := h.Sum(nil) // 20 bytes; use first 16

	var u [16]byte
	copy(u[:], sum[:16])
	u[6] = (u[6] & 0x0f) | 0x50 // version 5
	u[8] = (u[8] & 0x3f) | 0x80 // RFC 4122 variant

	b := hex.EncodeToString(u[:])
	return fmt.Sprintf("%s-%s-%s-%s-%s", b[0:8], b[8:12], b[12:16], b[16:20], b[20:32])
}
