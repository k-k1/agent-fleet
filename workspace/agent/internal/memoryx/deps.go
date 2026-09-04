package memoryx

// deps.go collects in one place every hand memoryx reaches out to its caller (package main).
//
// The whole outward dependency of this family is the 13 stable error codes in errcodes.go
// (the complete section, as listed by the compiler with `go build -gcflags=-e`). Not one
// function or type is needed: memory version management is closed over the live tree and its
// dedicated bare repo, and calls into no other family.
//
// The codes are not redefined on the memoryx side, for the same reason as
// internal/gitx/deps.go: they are the strings paired with the Console's i18n catalogue
// (ERR_TEXT in console/src/core/api/client.ts), and with two sources of truth the screen shows
// a raw code the day only one of them is fixed.
//
// A memoryx-only test has no main, so init does the wiring rather than TestMain
// (see deps_test.go).

import (
	"fmt"
	"reflect"
	"sort"
)

// Deps is the outside world as memoryx sees it. It holds no type from main.
type Deps struct {
	// --- stable error codes (errcodes.go) ---
	ErrCodeBadRequest     string
	ErrCodeBadRev         string
	ErrCodeBadPath        string
	ErrCodeNoSnapshots    string
	ErrCodeSnapshotFailed string
	ErrCodeDiffFailed     string
	ErrCodeBadScope       string
	ErrCodeRestoreFailed  string
	ErrCodeExportFailed   string
	ErrCodeImportFailed   string
	ErrCodeBadImport      string
	ErrCodeSecretDetected string
	ErrCodeTooLarge       string
}

var deps Deps

// Configure is called exactly once at startup (main's memory_wiring.go, or init in memoryx's
// own tests).
//
// Completeness is taken with reflect, never from a hand-written list. Every field here is a
// value type (a string), so an unwired one never fails with a nil dereference: it runs on
// quietly empty, the Console receives `""` as the code, i18n cannot resolve it, and the raw
// developer message is exposed.
//
// To exempt a field, tag it `memoryx:"optional"` rather than keeping a separate list.
func Configure(d Deps) {
	var missing []string
	v := reflect.ValueOf(d)
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Tag.Get("memoryx") == "optional" {
			continue
		}
		if v.Field(i).IsZero() {
			missing = append(missing, f.Name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		panic(fmt.Sprintf("memoryx.Configure: dependencies left unwired: %v", missing))
	}
	deps = d
	errCodeMemoryBadRequest = d.ErrCodeBadRequest
	errCodeMemoryBadRev = d.ErrCodeBadRev
	errCodeMemoryBadPath = d.ErrCodeBadPath
	errCodeMemoryNoSnapshots = d.ErrCodeNoSnapshots
	errCodeMemorySnapshotFailed = d.ErrCodeSnapshotFailed
	errCodeMemoryDiffFailed = d.ErrCodeDiffFailed
	errCodeMemoryBadScope = d.ErrCodeBadScope
	errCodeMemoryRestoreFailed = d.ErrCodeRestoreFailed
	errCodeMemoryExportFailed = d.ErrCodeExportFailed
	errCodeMemoryImportFailed = d.ErrCodeImportFailed
	errCodeMemoryBadImport = d.ErrCodeBadImport
	errCodeMemorySecretDetected = d.ErrCodeSecretDetected
	errCodeMemoryTooLarge = d.ErrCodeTooLarge
}

// Wired returns the current wiring. It is the read port for callers that verify end to end
// that the wiring is live; memoryx itself does not use it.
//
// Configure only catches a field left unwired, never one wired to the wrong thing (a
// different code attached).
func Wired() Deps { return deps }

// What is received by value. Configure writes these once and nothing reads them before that.
// The spellings match the const names from before the move, so the 2,951 lines that came here
// needed no edit at all.
var (
	errCodeMemoryBadRequest     string
	errCodeMemoryBadRev         string
	errCodeMemoryBadPath        string
	errCodeMemoryNoSnapshots    string
	errCodeMemorySnapshotFailed string
	errCodeMemoryDiffFailed     string
	errCodeMemoryBadScope       string
	errCodeMemoryRestoreFailed  string
	errCodeMemoryExportFailed   string
	errCodeMemoryImportFailed   string
	errCodeMemoryBadImport      string
	errCodeMemorySecretDetected string
	errCodeMemoryTooLarge       string
)
