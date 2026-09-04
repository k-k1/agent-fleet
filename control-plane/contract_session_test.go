package main

// contract_session_test.go — the smallest instance of a typed contract (family:
// sessionWire), the second leg of docs/log/23's "triple manual sync" diagnosis.
//
// The Console's `console/src/types/session.ts` is a HAND-WRITTEN TS type with no
// mechanical relationship to Go's `sessionWire`. Fix a json tag on one side and the
// other silently reads `undefined` — and the Go suite and the Console suite both stay
// green.
//
// This adds to the existing safety nets rather than replacing them:
//   - `routes_golden_test.go` = does the endpoint exist. Says nothing about JSON shape.
//   - `wire_golden_test.go`   = json keys, their JSON types and omitempty. Closed over
//     the Go side alone, never compared against the Console's type. It also does not
//     capture Go FIELD names, so swapping the json tags of two same-typed fields leaves
//     the key set identical and passes green (the binding check below closes that).
//   - `session_wire_test.go`  = nothing is dropped on the Agent → CP round trip. Does
//     not look at the Console.
//
// The wire itself is not changed by any of this: all these tests do is write down the
// wire as it already is.

import (
	"reflect"
)

// consoleSessionTS is where the Console's hand-written type lives. Existence is checked
// by RESOLVING this path, not by matching a pattern (when editing this constant, read the
// Fatal below too).
//
// Pass `go test -count=1` when applying a mutation by hand. This check reads OUTSIDE the
// module (the Console's TS), so editing only the TS leaves the test binary unchanged, the
// result comes back from `go test`'s cache as `ok (cached)`, and the mutation looks green
// when it is not. Every family of this design has that property (see the precedent,
// `workspace/agent/errcodes_catalog_test.go`). CI is unaffected — it starts clean — so
// what rots is the local claim that a mutation was applied and stayed green.
const consoleSessionTS = "../console/src/types/session.ts"

// --- 1. swap check: pin the Go field ↔ json key BINDING ---

// sessionWireBinding is sessionWire's "Go field name → json key".
//
// The binding, not the set of json keys: swapping the json tags of two same-typed fields
// (Branch / CurrentBranch, ExitReason / Carried) leaves the key set on the wire byte for
// byte identical. wire.golden and the TS key comparison both stay green, and the screen
// shows a different branch's name. Against this table the swap shows up as a diff.
//
// Capturing field names does not cause a false red when code is moved between packages.
// wire.golden deliberately does not capture Go TYPE names because a `main` → `internal/x`
// move necessarily changes them; field names do not change, because json tags only apply
// to exported fields and a move does not rename an exported field (measured on develop:
// json-tagged unexported fields across both modules: 0; exported fields: 3,001).
var sessionWireBinding = map[string]string{
	"Name":                 "name",
	"Kind":                 "kind",
	"Driver":               "driver",
	"Dir":                  "dir",
	"Subdir":               "subdir",
	"Repo":                 "repo",
	"WorkingCopyID":        "workingCopyId",
	"Title":                "title",
	"Display":              "display",
	"Color":                "color",
	"Label":                "label",
	"Started":              "started",
	"CreatedAt":            "createdAt",
	"RemoteUrl":            "remoteUrl",
	"State":                "state",
	"Alive":                "alive",
	"Resumable":            "resumable",
	"Locked":               "locked",
	"Archived":             "archived",
	"BackgroundBusy":       "backgroundBusy",
	"BackgroundBusyReason": "backgroundBusyReason",
	"RateLimitResumeAt":    "rateLimitResumeAt",
	"Context":              "context",
	"Branch":               "branch",
	"CurrentBranch":        "currentBranch",
	"BranchDrift":          "branchDrift",
	"Worktree":             "worktree",
	"ExitReason":           "exitReason",
	"ExitCode":             "exitCode",
	"ExitSignal":           "exitSignal",
	"KeepAwakeUntil":       "keepAwakeUntil",
	"Carried":              "carried",
}

// --- 2. exemption tables for the correspondence check (the family list itself is
// cpContractFamilies in contract_wire_test.go) ---

// consoleOnlyExempt holds the keys the Console's Session declares that Go's sessionWire
// INTENTIONALLY does not emit. Every addition must carry its reason; this is not a place
// to park "not fixed yet".
//
// The two entries below are not intended exemptions — they are holes this check found.
// Which side is right (drop the TS declaration, or add the field in Go) is a design
// decision about the wire, so they are exempted untouched and raised with the user
// instead. Closing one forces its exemption out, via the reverse check.
var consoleOnlyExempt = map[string]string{
	"model": "【穴】session.ts:51 が `model?: string; // claude model` を宣言しているが、" +
		"sessionWire にも Agent の session.Session にも該当キーが無い。Console 側の実読みも見つからない＝死んだ宣言の疑い。",
	"path": "【穴】session.ts:34 が `path?: string; // absolute working dir` を宣言しているが、" +
		"sessionWire にも Agent の session.Session にも該当キーが無い（実際の作業ディレクトリは dir）。",
}

// goOnlyExempt is the mirror image: keys sessionWire emits that the Console's Session
// type INTENTIONALLY does not declare. Same rule about reasons.
//
// All three below are holes. `started` in particular is patched locally by the Console —
// `console/src/features/sessions/ArchivedModal.tsx:19` declares
// `type ArchivedSession = Session & { started?: string };` — a feature filling in with an
// intersection type because the shared type has not caught up with the actual wire.
var goOnlyExempt = map[string]string{
	"started":  "【穴】ArchivedModal.tsx:19 が交差型で局所的に足している。共有の Session に載せるのが筋だが、既存の利用箇所の型が変わる。",
	"display":  "【穴】sessionWire が出しているが Session に宣言が無い。",
	"archived": "【穴】sessionWire が出しているが Session に宣言が無い。",
}

// sessionContractFamily describes the sessionWire family. The body of the check lives in
// checkContractFamily (contract_wire_test.go), table-driven so other families reuse it.
// The assertions are (1) the binding, (2) the pinned TS scan, and (3) the comparison in
// both directions plus the four-way lifecycle.
func sessionContractFamily() contractFamily {
	return contractFamily{
		name:    "sessionWire",
		goType:  reflect.TypeOf(sessionWire{}),
		binding: sessionWireBinding,
		tsPath:  consoleSessionTS,
		tsName:  "Session",
		tsKeys: keySet("name", "kind", "driver", "title", "color", "label", "repo", "workingCopyId",
			"path", "dir", "subdir", "remoteUrl", "state", "alive", "resumable", "backgroundBusy",
			"backgroundBusyReason", "rateLimitResumeAt", "createdAt", "model", "context", "branch",
			"currentBranch", "branchDrift", "worktree", "exitReason", "exitCode", "exitSignal",
			"carried", "locked", "keepAwakeUntil"),
		tsOnly: consoleOnlyExempt,
		goOnly: goOnlyExempt,
	}
}
