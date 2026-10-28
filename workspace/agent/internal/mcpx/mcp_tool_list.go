package mcpx

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"
)

// mcpStdioToolList is the advertised tool set. The assistant surface gets read-only
// tools plus write tools under --write (docs/log/19 Q2); the session surface gets its
// explicit narrow set (docs/log/51 + docs/log/53).
func mcpStdioToolList() []map[string]any {
	if selfReportOnly() {
		tools := append([]map[string]any{}, mcpStdioSelfReportTools()...)
		if sessionChromiumEnabled() && mcpBrowserUnavailable == "" {
			tools = appendMatchingMCPTools(tools, mcpStdioTools, isChromiumReadTool)
			tools = appendMatchingMCPTools(tools, mcpStdioWriteTools, isChromiumWriteTool)
		}
		if mcpPeerMessagingEnabled {
			tools = append(tools, mcpStdioPeerTools()...)
		}
		tools = append(tools, mcpStdioFleetObserveTools()...)
		tools = append(tools, mcpStdioBranchTools()...)
		if mcpAgentMemoryEnabled {
			tools = append(tools, mcpStdioMemoryTools()...)
		}
		if mcpSessionSearchEnabled {
			tools = append(tools, mcpStdioSessionSearchTools()...)
		}
		if mcpFleetSpawnEnabled {
			tools = append(tools, mcpStdioFleetSpawnTools()...)
		}
		if offer, ok := mcpImageGenAdvertise(); ok {
			tools = append(tools, mcpStdioImageGenTools(offer)...)
		}
		if mcpStudioAdvertise() {
			tools = append(tools, mcpStdioStudioTools()...)
		}
		return tools
	}
	tools := mcpStdioTools
	if writeEnabled() {
		tools = append(append([]map[string]any{}, mcpStdioTools...), mcpStdioWriteTools...)
	}
	if mcpBrowserUnavailable != "" {
		return appendMatchingMCPTools(nil, tools, func(name string) bool {
			return !isChromiumReadTool(name) && !isChromiumWriteTool(name)
		})
	}
	return tools
}

func appendMatchingMCPTools(dst, src []map[string]any, keep func(string) bool) []map[string]any {
	for _, tool := range src {
		name, _ := tool["name"].(string)
		if keep(name) {
			dst = append(dst, tool)
		}
	}
	return dst
}

// mcpAdvertised remembers the names the LAST tools/list actually returned, so that the call-side
// scope check can be answered from this process instead of over the network.
//
// 🔴 It exists because re-deriving the list on the call path made an advertised tool refuse its
// own call (measured on the dev deployment, 2026-09-11, ADR 0072 H3). mcpStdioToolList() asks
// mcpImageGenAdvertise(), which is a live loopback GET to the Agent's /imagegen/status with a
// 3-second budget; that status asks every provider's Ready(), and for the fleet's own engines
// Ready() is a round trip to the Control Plane. So a tools/call arriving while the CP was busy —
// the trigger was a CP replaced with --force-new-deployment moments earlier — blew the three
// seconds, the tool came back "not advertised", and the model was told something that reads like
// a permission decision about a tool sitting in its own tool list.
//
// What the client may call is what it was TOLD it may call. That is a fact this process already
// has, and reading it back cannot time out.
var mcpAdvertised struct {
	mu    sync.Mutex
	names map[string]bool // nil until the first tools/list has been served
	fp    string          // fingerprint of that same answer, for the watcher below
	// props is the property set each advertised tool's schema declared in that answer; a call's
	// argument keys are held to it (mcpCheckCallArgs).
	props map[string]map[string]bool
}

// rememberAdvertised records what a tools/list answer actually contained.
func rememberAdvertised(tools []map[string]any) {
	names := make(map[string]bool, len(tools))
	for _, tool := range tools {
		if name, _ := tool["name"].(string); name != "" {
			names[name] = true
		}
	}
	fp := mcpToolListFingerprint(tools)
	mcpAdvertised.mu.Lock()
	mcpAdvertised.names, mcpAdvertised.fp, mcpAdvertised.props = names, fp, advertisedProps(tools)
	mcpAdvertised.mu.Unlock()
}

// mcpToolListFingerprint is the whole answer, not just the names: what changes when an
// administrator enables a checkpoint is the `model` ENUM inside generate_image's schema, and a
// watcher that compared names alone would never notice it. json.Marshal sorts map keys, so the
// same tool list always fingerprints the same way.
func mcpToolListFingerprint(tools []map[string]any) string {
	b, err := json.Marshal(tools)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// mcpToolListWatchInterval is how often the server re-derives what it would advertise. One
// minute because the answer is a loopback GET to the Agent's /imagegen/status, served from the
// Agent's own ten-minute catalogue cache, and every provider's Ready() behind it is a LookPath
// and a local file read (measured: no provider dials out from Ready). It must also stay well
// under the stages above it — a watcher that ticked slower than the catalogue converges would
// just become the new floor.
const mcpToolListWatchInterval = time.Minute

var mcpToolListWatchOnce sync.Once

// mcpStartToolListWatch starts the watcher, once, after the first tools/list has been served.
//
// 🔥 It exists because a tool list is a SNAPSHOT the client takes when it connects, and nothing
// made it take another one: measured 2026-09-15, a claude session resumed with `--resume` — a
// process one minute old, with a freshly spawned server whose own tools/list answers with the new
// checkpoint — kept advertising the list from before the checkpoint existed, for the whole life
// of the session. Restarting the session is not a fix, because the Console's stop/resume IS
// `claude --resume`. Without this notification the only way a running session ever sees a new
// model is to be replaced by a brand-new one.
//
// After the first tools/list rather than at boot: a notification sent before the client has
// listed anything is one it has no snapshot to invalidate, and old-era clients are entitled to
// see initialize answered first. stdioOut is nil in the unit tests, which call dispatchMCPStdio
// directly and have no stdout — that is also what keeps the goroutine out of them.
func mcpStartToolListWatch() {
	if stdioOut == nil {
		return
	}
	mcpToolListWatchOnce.Do(func() { go mcpWatchToolList() })
}

func mcpWatchToolList() {
	t := time.NewTicker(mcpToolListWatchInterval)
	defer t.Stop()
	for range t.C {
		mcpCheckToolListOnce()
	}
}

// mcpCheckToolListOnce re-derives the list and tells the client when it differs from what the
// client was last told. The remembered set is updated FIRST: it is the promise the call-side
// check enforces, and after a checkpoint is withdrawn the honest answer to a call naming it is a
// refusal, not the stale permission from a list the client has not re-read yet.
func mcpCheckToolListOnce() bool {
	tools := mcpStdioToolList()
	fp := mcpToolListFingerprint(tools)
	mcpAdvertised.mu.Lock()
	same := fp == mcpAdvertised.fp
	mcpAdvertised.mu.Unlock()
	if same || fp == "" {
		return false
	}
	rememberAdvertised(tools)
	mcpNotifyToolListChanged()
	return true
}

// mcpNotifyToolListChanged is the 2025 spelling, sent to both eras — the same choice
// notifications/progress already makes, and an unknown notification is one a client drops rather
// than one it breaks on.
func mcpNotifyToolListChanged() {
	b, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/tools/list_changed"})
	if err != nil {
		return
	}
	stdioOut.writeLine(b)
}

// mcpStdioToolAdvertised answers "did this server offer that name".
//
// From the remembered set once a tools/list has been served. Before that — a client that calls
// without listing first — there is nothing to compare against, so the set is derived the old way.
// That path keeps the boundary intact for a guessed name; it just is not the path a real client
// takes, and it is the only one that can still pay for a network round trip.
func mcpStdioToolAdvertised(name string) bool {
	mcpAdvertised.mu.Lock()
	names := mcpAdvertised.names
	mcpAdvertised.mu.Unlock()
	if names != nil {
		return names[name]
	}
	for _, tool := range mcpStdioToolList() {
		if tool["name"] == name {
			return true
		}
	}
	return false
}
