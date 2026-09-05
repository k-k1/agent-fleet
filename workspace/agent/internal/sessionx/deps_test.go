package sessionx

// sessionx unit tests have no package main, so the outward dependencies are wired here.
//
// Why this is not just a row of fakes: before the move these tests ran against main's real
// implementations. Replacing them with fakes leaves the assertions and the branches taken
// unchanged while shrinking only the set of bugs that can be caught (the first pitfall in
// README §4). So which dependencies are actually reached was measured first:
//
//	measured (wiring replaced by one that only counts with `p(name)`, one full run of the
//	sessionx tests) ->
//	  SplitFrontmatter=22 / BrowseRoot=14 / FirstNonEmpty=13 / ToolchainShellPrefix=5 /
//	  IsSvnRepo=2 / RepoJobsRunning=1, and 0 for the other 7
//
// The 6 that are reached copy main's implementation verbatim (below). The 7 that are not
// panic: a fake return value would silently go green on a lie once a future test does reach
// here, so this errs on the side of making noise (the same shape as
// internal/gitx/deps_test.go).
//
// Do not fake `ToolchainShellPrefix` as returning the empty string. main's
// `env_toolchains.go` puts `defaultTimezone = "Asia/Tokyo"` in by default even with no
// selection file, so the real one returns `export TZ='Asia/Tokyo'; `, not "". Empty here
// leaves the program string handed to tmux different from production while the tests stay
// green (which is what happened once).

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func init() { Configure(testDeps()) }

// probe counts how often each dependency is reached; SESSIONX_PROBE=1 also prints to stderr.
// Use this to measure again, so the wiring never has to be turned back into fakes.
var (
	probeMu sync.Mutex
	probe   = map[string]int{}
)

func p(name string) {
	probeMu.Lock()
	probe[name]++
	probeMu.Unlock()
	if os.Getenv("SESSIONX_PROBE") != "" {
		fmt.Fprintln(os.Stderr, "PROBE "+name)
	}
}

// unreached wires a dependency that was measured never to be reached from the sessionx
// tests. Reaching it stops the run: prefer failing over running quietly, fakes included.
func unreached(name string) {
	panic("sessionx test deps: " + name + " was never reached in the measurement taken at the " +
		"time of the move. Arriving here means a new check needs main's implementation: " +
		"before putting a fake return value here, decide whether to copy the behaviour of " +
		"main's session_wiring.go, or to put the test in package main")
}

// testDeps is the whole wiring for the sessionx unit tests, kept in one place because the
// exhaustiveness check uses the same thing.
func testDeps() Deps {
	return Deps{
		// --- the 6 reached in the measurement (copies of main's implementation) ---

		// A copy of connections.go.
		FirstNonEmpty: func(vals ...string) string {
			p("FirstNonEmpty")
			for _, v := range vals {
				if v != "" {
					return v
				}
			}
			return ""
		},

		// A copy of repo_prompts.go. Do not rewrite it approximately: unless the edge cases
		// (an unterminated block, CRLF, stripping quotes off a value) behave identically too,
		// only the coverage shrinks.
		SplitFrontmatter: func(s string) (map[string]string, string) {
			p("SplitFrontmatter")
			meta := map[string]string{}
			if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
				return meta, s
			}
			rest := s[strings.IndexByte(s, '\n')+1:]
			end := strings.Index(rest, "\n---")
			if end < 0 {
				return meta, s // unterminated block — treat the whole file as body
			}
			fm := rest[:end]
			body := rest[end+len("\n---"):]
			if i := strings.IndexByte(body, '\n'); i >= 0 { // drop the rest of the closing --- line
				body = body[i+1:]
			} else {
				body = ""
			}
			for _, ln := range strings.Split(fm, "\n") {
				ln = strings.TrimRight(ln, "\r")
				if i := strings.IndexByte(ln, ':'); i > 0 {
					k := strings.ToLower(strings.TrimSpace(ln[:i]))
					v := strings.Trim(strings.TrimSpace(ln[i+1:]), `"'`)
					meta[k] = v
				}
			}
			return meta, strings.TrimLeft(body, "\n")
		},

		// A copy of fs.go. With no env set it falls back to homeDir() (the tests set a temp HOME).
		BrowseRoot: func() string {
			p("BrowseRoot")
			if r := os.Getenv("AF_BROWSE_ROOT"); r != "" {
				return r
			}
			return homeDir()
		},

		// A copy of svn.go.
		IsSvnRepo: func(dir string) bool {
			p("IsSvnRepo")
			fi, err := os.Stat(filepath.Join(dir, ".svn"))
			return err == nil && fi.IsDir()
		},

		// Looks like a copy of repo_jobs.go, but the ledger itself only exists in main. The
		// sessionx test binary starts no import job, so the real one returns 0 as well. It is 0
		// because this process has no ledger, not because "it is 0 in tests". A check that uses
		// the ledger belongs in main.
		RepoJobsRunning: func() int { p("RepoJobsRunning"); return 0 },

		// A copy of env_toolchains.go, for the no-selection-file path only.
		// It must not return the empty string: `defaultTimezone` is applied even with no
		// selection, so the real one returns `export TZ='Asia/Tokyo'; `. Run in an environment
		// that does have a selection file, this copy cannot reproduce the real behaviour, so it
		// fails instead.
		ToolchainShellPrefix: func() string {
			p("ToolchainShellPrefix")
			path := filepath.Join(homeDir(), ".config", "agent-fleet", "toolchains.json")
			if b, err := os.ReadFile(path); err == nil {
				var t struct {
					Java, Node, Go, Timezone string
				}
				_ = json.Unmarshal(b, &t)
				if t.Java != "" || (t.Node != "" && t.Node != "system") || (t.Go != "" && t.Go != "system") {
					panic("sessionx test deps: ToolchainShellPrefix - in an environment where " + path +
						" holds a selection, this copy cannot reproduce the real thing (javaHomeFor / the nvm glob / goRootFor). " +
						"put this check in package main")
				}
			}
			// No selection: java / node / go stay empty, only TZ gets its default.
			const defaultTimezone = "Asia/Tokyo"
			if _, err := os.Stat("/usr/share/zoneinfo/" + defaultTimezone); err != nil {
				return ""
			}
			return "export TZ=" + session.ShellQuote(defaultTimezone) + "; "
		},

		// --- the 7 not reached in the measurement (reaching one fails) ---
		EnvOr:                 func(k, d string) string { unreached("EnvOr"); return d },
		MaxUploadBytes:        func() int64 { unreached("MaxUploadBytes"); return 0 },
		FinalizeSessionUsage:  func(session.Meta) { unreached("FinalizeSessionUsage") },
		MaybeFoldSessionUsage: func() { unreached("MaybeFoldSessionUsage") },
		RemoveTerminalHistory: func(string) { unreached("RemoveTerminalHistory") },
		MCPConvID:             func() string { unreached("MCPConvID"); return "" },
		RunOperatorTurn: func(conv, text string) (string, error) {
			unreached("RunOperatorTurn")
			return "", nil
		},

		// --- error codes ---
		//
		// Copy the real spellings here. With an arbitrary string, a check that expects the code
		// in a response body goes green merely because something is there. That these match the
		// real ones is guarded by session_wiring_test.go in main, against errcodes.go.
		ErrCodeAgentNotConnected:      "agent_not_connected",
		ErrCodeChatConversationNotFnd: "chat_conversation_not_found",
		ErrCodeForkAtUnsupported:      "fork_at_unsupported",
		ErrCodeForkBadAnchor:          "fork_bad_anchor",
		ErrCodeForkMissingDir:         "fork_missing_dir",
		ErrCodeForkUnsupportedKind:    "fork_unsupported_kind",
		ErrCodeLocked:                 "locked",
		ErrCodePasteTooLarge:          "paste_too_large",
		ErrCodePasteUnsupportedAgent:  "paste_unsupported_agent",
		ErrCodePasteUnsupportedKind:   "paste_unsupported_kind",
		ErrCodeTitleFeatureDisabled:   "title_feature_disabled",
		ErrCodeTitleNoContent:         "title_no_content",
	}
}
