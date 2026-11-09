package fsx

// deps.go — every hand fsx reaches out to package main with, on one sheet.
//
// fsx does not import main (the dependency already runs the other way), so what it needs from
// main arrives as values wired once at boot (main's fs_wiring.go). Configure panics on a gap:
// an empty code would reach the Console as a blank error code, which then falls back to the
// developer message instead of its localized text.

import (
	"fmt"
	"os"
	"reflect"
	"sort"
)

// Deps is the outside world as fsx sees it. It holds no type from main. Every field is an
// errcodes.go machine token the Console keys its localized text on (console err.<code>).
type Deps struct {
	ErrCodeFSBadPath            string
	ErrCodeFSBadRequest         string
	ErrCodeFSBinaryNotSupported string
	ErrCodeFSDenied             string
	ErrCodeFSNotFile            string
	ErrCodeFSReadFailed         string
	ErrCodeFSRevisionConflict   string
	ErrCodeFSSymlinkNotAllowed  string
	ErrCodeFSTooLarge           string
	ErrCodeFSUnsupportedMedia   string
	ErrCodeFSUnsupportedNewline string
	ErrCodeFSWriteCancelled     string
	ErrCodeFSWriteFailed        string
	ErrCodeFSWriteStateUnknown  string
	ErrCodeTitleFeatureDisabled string
	ErrCodeZipBusy              string
	ErrCodeZipChanged           string
	ErrCodeZipNoSpace           string
	ErrCodeZipNotDir            string
	ErrCodeZipTooLarge          string
	ErrCodeZipUnsafe            string
}

var deps Deps

// Configure is called once at boot (main's fs_wiring.go, or fsx's own tests). Completeness is
// taken with reflect so a field added later cannot be forgotten.
func Configure(d Deps) {
	var missing []string
	v := reflect.ValueOf(d)
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		if v.Field(i).IsZero() {
			missing = append(missing, t.Field(i).Name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		panic(fmt.Sprintf("fsx.Configure: dependencies left unwired: %v", missing))
	}
	deps = d
}

// Wired returns the current wiring, for a caller checking end to end that it is live.
// Configure catches only what is unwired, never what is wired wrong.
func Wired() Deps { return deps }

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
