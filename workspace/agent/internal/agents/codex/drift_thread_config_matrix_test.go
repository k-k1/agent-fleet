//go:build drift

// Replace or merge? docs/27 §9.3 recorded "thread config REPLACES the global set",
// measured on EPHEMERAL threads. Production managed sessions use PERSISTENT threads,
// and a project-config measurement contradicted the recorded table there — so the two
// variables (ephemeral vs persistent, empty vs non-empty map) have to be separated
// before anything can be concluded about af's actual path.
package codex

import (
	"fmt"
	"sort"
	"testing"
	"time"
)

// TestDriftCodexThreadConfigMergeMatrix walks all four combinations against one real
// app-server and reports what each does to a globally configured server.
func TestDriftCodexThreadConfigMergeMatrix(t *testing.T) {
	proj := driftProjectDir(t)
	cl := startDriftAppServerSeeded(t, seedProjectTrustedConfig(t, proj))

	cases := []struct {
		label     string
		ephemeral bool
		servers   map[string]any
	}{
		{"ephemeral + empty map", true, map[string]any{}},
		{"ephemeral + own server", true, map[string]any{"af_only": map[string]any{"command": "/bin/true"}}},
		{"persistent + empty map", false, map[string]any{}},
		{"persistent + own server", false, map[string]any{"af_only": map[string]any{"command": "/bin/true"}}},
	}

	got := map[string]bool{}
	for _, c := range cases {
		params := map[string]any{"cwd": proj, "config": map[string]any{"mcp_servers": c.servers}}
		if c.ephemeral {
			params["ephemeral"] = true
		}
		tid := driftStartThreadParams(t, cl, params)
		// Settle: a merged layer's servers appear asynchronously, so give the list a
		// moment before reading "absent" as a deny.
		if len(c.servers) > 0 {
			waitDriftMCPServer(t, cl, tid, "af_only")
		} else {
			driftSettle(t, cl, tid)
		}
		names := driftMCPServerNames(t, cl, tid)
		inherited := names[driftGlobalSrv] || names[driftProjectSrv]
		got[c.label] = inherited
		t.Logf("%-26s → %v (other layers %s)", c.label, driftSorted(names),
			map[bool]string{true: "INHERITED", false: "replaced"}[inherited])
	}

	// What af's implementation rests on: file-configured servers survive a thread map
	// in EVERY combination, so restating only the af entry is enough. If any cell flips
	// to "replaced", af must re-emit that layer or managed sessions silently lose it.
	for label, inherited := range got {
		if !inherited {
			t.Fatalf("%q no longer inherits the file layers: af's thread config must then "+
				"re-emit EVERY layer (global config.toml rows af does not own, and a trusted "+
				"project's .codex/config.toml) or managed sessions silently lose them.\n%v",
				label, got)
		}
	}
}

func driftStartThreadParams(t *testing.T, cl *appClient, params map[string]any) string {
	t.Helper()
	res, err := cl.call("thread/start", params, driftCallTimeout)
	if err != nil {
		t.Fatalf("thread/start %v: %v", params, err)
	}
	st, err := parseThreadResult(res)
	if err != nil || st.threadID == "" {
		t.Fatalf("thread/start returned no thread id: %v", err)
	}
	return st.threadID
}

// driftSettle waits for the server list to stop changing, so "nothing inherited" is a
// conclusion rather than a race.
func driftSettle(t *testing.T, cl *appClient, tid string) {
	t.Helper()
	prev := ""
	for i := 0; i < 20; i++ {
		cur := fmt.Sprint(driftSorted(driftMCPServerNames(t, cl, tid)))
		if i > 2 && cur == prev {
			return
		}
		prev = cur
		driftSleep()
	}
}

func driftSorted(names map[string]bool) []string {
	out := sortedNameList(names)
	sort.Strings(out)
	return out
}

const driftCallTimeout = 15 * time.Second

func driftSleep() { time.Sleep(150 * time.Millisecond) }
