package claude

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// remoteURLs memoizes RemoteSessionURL per config dir + sid.
var remoteURLs = &remoteURLMemo{m: map[string]remoteURLEntry{}}

// remoteURLMemoMax bounds the memo: sessions are deleted and archived over a workspace's
// life. Past the cap the memo is dropped wholesale; the next poll refills what is live.
const remoteURLMemoMax = 4096

// openTranscript is os.Open, replaceable in tests so they can count opens.
var openTranscript = func(p string) (*os.File, error) { return os.Open(p) }

type remoteURLEntry struct {
	url string // non-empty: a hit, valid for good
	sig string // for a miss: signature of the transcripts that were scanned
}

type remoteURLMemo struct {
	mu sync.Mutex
	m  map[string]remoteURLEntry
}

func (r *remoteURLMemo) get(key string) (remoteURLEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.m[key]
	return e, ok
}

func (r *remoteURLMemo) put(key string, e remoteURLEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.m[key]; !ok && len(r.m) >= remoteURLMemoMax {
		r.m = map[string]remoteURLEntry{}
	}
	r.m[key] = e
}

// RemoteSessionURL derives the claude.ai Remote Control page for sid from its jsonl
// "bridge-session" line (written when RC connects). The web URL is
// "…/code/session_<bridgeSessionId without the cse_ prefix>". Returns "" when there is no
// bridge (RC off / not yet connected).
//
// It runs once per transcript per card on the polled sessions list, and opening a file on the
// claude mount costs network round-trips, so the answer is memoized (measured: ~30 OPEN+CLOSE/s
// on one production mount before this):
//
//   - A hit is kept for the process lifetime: the line is written once near the head and never
//     changes, so no stat or open is needed again.
//   - A miss is keyed by a signature of every sibling transcript (path, mtime, size) and re-read
//     only when it changes, so a bridge line appearing later, a resumed/forked session whose
//     newest file differs, or a log that grew are all picked up. A replacement with identical
//     path, mtime and size would go unnoticed; transcripts are append-only and mtime has
//     nanosecond granularity on the mounts we use, so that is accepted.
//   - A read that failed (open error) is never remembered as a miss.
func RemoteSessionURL(sid string) string {
	// Keyed by the live id, not the slot: after a self-restart (/tui, sign-in, model switch)
	// the ledger points the slot at a new conversation whose URL differs.
	live := LiveSID(sid) // resolved once: key and paths must agree even if a hook rewrites the ledger meanwhile
	key := ConfigDir() + "\x00" + live
	if e, ok := remoteURLs.get(key); ok && e.url != "" {
		return e.url
	}
	paths := rawJSONLPathsIn(live, session.CWDForUUID(sid))
	var sig strings.Builder
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			fmt.Fprintf(&sig, "%s|err;", p)
			continue
		}
		fmt.Fprintf(&sig, "%s|%d|%d;", p, fi.ModTime().UnixNano(), fi.Size())
	}
	if e, ok := remoteURLs.get(key); ok && e.sig == sig.String() {
		return ""
	}
	url, readOK := scanBridgeURL(paths)
	switch {
	case url != "":
		remoteURLs.put(key, remoteURLEntry{url: url})
	case readOK:
		remoteURLs.put(key, remoteURLEntry{sig: sig.String()})
	}
	return url
}

// scanBridgeURL reads the head of each transcript. readOK is false when any file failed to
// open, so the caller must not remember the empty answer.
func scanBridgeURL(paths []string) (url string, readOK bool) {
	readOK = true
	for _, p := range paths {
		f, err := openTranscript(p)
		if err != nil {
			readOK = false
			continue
		}
		buf := make([]byte, 64*1024)
		n, rerr := f.Read(buf)
		f.Close()
		if rerr != nil && rerr != io.EOF {
			readOK = false // a transient EIO/ESTALE must not pin an empty answer
		}
		for _, line := range strings.Split(string(buf[:n]), "\n") {
			if !strings.Contains(line, `"type":"bridge-session"`) {
				continue
			}
			var b struct {
				BridgeSessionID string `json:"bridgeSessionId"`
			}
			if json.Unmarshal([]byte(line), &b) == nil && b.BridgeSessionID != "" {
				return "https://claude.ai/code/session_" + strings.TrimPrefix(b.BridgeSessionID, "cse_"), true
			}
		}
	}
	return "", readOK
}
