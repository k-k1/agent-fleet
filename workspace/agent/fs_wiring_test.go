package main

// One check that fs_wiring.go is live: fsx.Configure only catches an unwired field, not one
// wired to the wrong constant. Each field is compared by value against errcodes.go.

import (
	"os"
	"reflect"
	"regexp"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/fsx"
)

func TestFSWiringIsLive(t *testing.T) {
	w := fsx.Wired()
	want := fsx.Deps{
		ErrCodeFSBadPath:            errCodeFSBadPath,
		ErrCodeFSBadRequest:         errCodeFSBadRequest,
		ErrCodeFSBinaryNotSupported: errCodeFSBinaryNotSupported,
		ErrCodeFSDenied:             errCodeFSDenied,
		ErrCodeFSNotFile:            errCodeFSNotFile,
		ErrCodeFSReadFailed:         errCodeFSReadFailed,
		ErrCodeFSRevisionConflict:   errCodeFSRevisionConflict,
		ErrCodeFSSymlinkNotAllowed:  errCodeFSSymlinkNotAllowed,
		ErrCodeFSTooLarge:           errCodeFSTooLarge,
		ErrCodeFSUnsupportedMedia:   errCodeFSUnsupportedMedia,
		ErrCodeFSUnsupportedNewline: errCodeFSUnsupportedNewline,
		ErrCodeFSWriteCancelled:     errCodeFSWriteCancelled,
		ErrCodeFSWriteFailed:        errCodeFSWriteFailed,
		ErrCodeFSWriteStateUnknown:  errCodeFSWriteStateUnknown,
		ErrCodeTitleFeatureDisabled: errCodeTitleFeatureDisabled,
		ErrCodeZipBusy:              errCodeZipBusy,
		ErrCodeZipChanged:           errCodeZipChanged,
		ErrCodeZipNoSpace:           errCodeZipNoSpace,
		ErrCodeZipNotDir:            errCodeZipNotDir,
		ErrCodeZipTooLarge:          errCodeZipTooLarge,
		ErrCodeZipUnsafe:            errCodeZipUnsafe,
	}
	if w != want {
		t.Errorf("fsx wiring differs from errcodes.go:\n got %+v\nwant %+v", w, want)
	}
}

// fsx's own tests wire the same tokens as string literals (internal/fsx/deps_test.go), because
// fsx cannot import errcodes.go. This keeps those literals from drifting from the real values:
// without it, renaming a token here leaves fsx's tests asserting the old one, green.
func TestFSTestDepsLiteralsMatchErrcodes(t *testing.T) {
	src, err := os.ReadFile("internal/fsx/deps_test.go")
	if err != nil {
		t.Fatal(err)
	}
	lits := map[string]string{}
	for _, m := range regexp.MustCompile(`(ErrCode\w+):\s*"([^"]*)"`).FindAllStringSubmatch(string(src), -1) {
		lits[m[1]] = m[2]
	}
	w := reflect.ValueOf(fsx.Wired())
	if len(lits) != w.NumField() {
		t.Fatalf("deps_test.go lists %d tokens, fsx.Deps has %d fields", len(lits), w.NumField())
	}
	for i := 0; i < w.NumField(); i++ {
		name := w.Type().Field(i).Name
		if got, want := lits[name], w.Field(i).String(); got != want {
			t.Errorf("internal/fsx/deps_test.go %s = %q, errcodes.go says %q", name, got, want)
		}
	}
}
