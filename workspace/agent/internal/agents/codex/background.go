package codex

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"
)

// Background terminals: a command codex left running when its turn ended (unified exec with a
// yield, polled with write_stdin in later turns) keeps the session busy although the thread is
// idle. The app-server answers this directly with `thread/backgroundTerminals/list` — an
// experimental method, reachable because initialize declares experimentalApi.
//
// Measured on 0.159.2 against a live app-server: params `{threadId}`, result
// `{data: [{itemId, processId, command, cwd, osPid, cpuPercent, rssKb}], nextCursor}`. A
// `sleep 45` the model started and left listed from the turn's end until it exited, then the
// list was empty again. An unknown method answers "Invalid request: unknown variant …".

// bgTTL is how old a cached answer may be before a read asks again. The list poll comes every
// 4 s per session; this keeps it to one call per session per window, and it bounds how long a
// finished command keeps the badge lit.
var bgTTL = 5 * time.Second

// bgCallTimeout bounds one list call; it runs off the poll, so this only bounds the goroutine.
const bgCallTimeout = 5 * time.Second

const bgReasonShell = "shell" // claude.BGReasonShell's wire value

// bgCache is a handle's last answer. A read never waits on the app-server: the sessions list
// is polled for every session at once, and one slow server would stall all of them. So a read
// returns what is cached and, when it is stale, starts a refresh for the next read.
type bgCache struct {
	mu     sync.Mutex
	at     time.Time // when busy was last answered; zero = never
	busy   bool
	flight bool // a refresh is out
}

// BackgroundWork reports whether a managed session's thread has a background terminal running,
// and what it is. Cached; see bgCache.
func BackgroundWork(name string) (bool, string) {
	h := handleFor(name)
	if h == nil {
		return false, ""
	}
	h.bg.mu.Lock()
	busy := h.bg.busy
	stale := !h.bg.flight && time.Since(h.bg.at) >= bgTTL
	if stale {
		h.bg.flight = true
	}
	h.bg.mu.Unlock()
	if stale {
		go h.refreshBg()
	}
	if busy {
		return true, bgReasonShell
	}
	return false, ""
}

// kickBg asks again now, whatever the cache's age: a turn just ended, and the command it left
// running is exactly what the next read should see.
func (h *threadHandle) kickBg() {
	h.bg.mu.Lock()
	if h.bg.flight {
		h.bg.mu.Unlock()
		return
	}
	h.bg.flight = true
	h.bg.mu.Unlock()
	go h.refreshBg()
}

// refreshBg asks the app-server and records the answer. Any failure records "not busy": the
// badge is an addition to idle, so an app-server that cannot answer leaves the session reading
// the way it did before the question existed.
func (h *threadHandle) refreshBg() {
	h.mu.Lock()
	cl, tid, alive := h.client, h.tid, h.alive
	h.mu.Unlock()
	busy := false
	if alive && cl != nil && tid != "" && !cl.noBgTerminals.Load() {
		res, err := cl.call("thread/backgroundTerminals/list", map[string]any{"threadId": tid}, bgCallTimeout)
		switch {
		case err == nil:
			var r struct {
				Data []json.RawMessage `json:"data"`
			}
			busy = json.Unmarshal(res, &r) == nil && len(r.Data) > 0
		case unknownMethod(err):
			// An older pinned CLI: stop asking this connection. A restarted app-server is a new
			// client and asks again.
			cl.noBgTerminals.Store(true)
		}
	}
	h.bg.mu.Lock()
	h.bg.busy, h.bg.at, h.bg.flight = busy, time.Now(), false
	h.bg.mu.Unlock()
}

// unknownMethod reports whether err is the app-server refusing the method itself, as opposed to
// failing a call it understood: JSON-RPC's own code, or the text 0.159.2 answers with
// ("Invalid request: unknown variant `…`", measured).
func unknownMethod(err error) bool {
	var re *rpcError
	if !errors.As(err, &re) {
		return false
	}
	return re.Code == -32601 || strings.Contains(re.Message, "unknown variant")
}
