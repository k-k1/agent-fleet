package memoryx

// The memoryx tests have no package main of their own, so they wire the outward
// dependencies themselves (the same shape as internal/gitx/deps_test.go).
//
// This family's only outward dependency is 13 stable error codes, all of them constants in
// errcodes.go. Do not copy the real values here: a copy becomes a second source of truth
// (the real one is wired by main's memory_wiring.go). The set of bugs this can catch is the
// same either way — the memoryx tests observe which code a handler picked, not the string
// itself, so matching a response's code against the value here is equivalent to matching it
// against the real constant.

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func init() { Configure(testDeps()) }

func testDeps() Deps {
	return Deps{
		ErrCodeBadRequest:     "memoryx-test-bad_request",
		ErrCodeBadRev:         "memoryx-test-bad_rev",
		ErrCodeBadPath:        "memoryx-test-bad_path",
		ErrCodeNoSnapshots:    "memoryx-test-no_snapshots",
		ErrCodeSnapshotFailed: "memoryx-test-snapshot_failed",
		ErrCodeDiffFailed:     "memoryx-test-diff_failed",
		ErrCodeBadScope:       "memoryx-test-bad_scope",
		ErrCodeRestoreFailed:  "memoryx-test-restore_failed",
		ErrCodeExportFailed:   "memoryx-test-export_failed",
		ErrCodeImportFailed:   "memoryx-test-import_failed",
		ErrCodeBadImport:      "memoryx-test-bad_import",
		ErrCodeSecretDetected: "memoryx-test-secret_detected",
		ErrCodeTooLarge:       "memoryx-test-too_large",
	}
}

// TestConfigureRejectsEveryUnwiredField pins that Configure panics when any single field of
// Deps is left unwired.
//
// The exhaustiveness check itself runs through reflect, so a field added later is covered
// automatically. Every field is a value type (a string), so an unwired one never fails with
// a nil dereference — it runs on silently, the Console receives the code "" which i18n
// cannot resolve, and the raw developer message is exposed.
func TestConfigureRejectsEveryUnwiredField(t *testing.T) {
	good := testDeps()
	v := reflect.ValueOf(good)
	typ := v.Type()
	if typ.NumField() != 13 {
		t.Fatalf("Deps has %d fields (the memory section of errcodes.go has 13: either the wrong struct is being read, or only one side grew)", typ.NumField())
	}
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		t.Run(f.Name, func(t *testing.T) {
			// Always restore the correct wiring (deps is shared by the whole package).
			defer Configure(good)
			broken := reflect.New(typ).Elem()
			broken.Set(v)
			broken.Field(i).Set(reflect.Zero(f.Type))
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("Configure accepted %s left unwired (a missing wiring slips through silently)", f.Name)
				}
				if !strings.Contains(fmt.Sprint(r), f.Name) {
					t.Fatalf("the panic does not name %s: %v", f.Name, r)
				}
			}()
			Configure(broken.Interface().(Deps))
		})
	}
}
