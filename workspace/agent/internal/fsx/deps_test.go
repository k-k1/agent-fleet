package fsx

// fsx's unit tests have no package main, so the error codes are wired here with the real
// tokens. main's fs_wiring_test.go proves the production wiring against errcodes.go.

import (
	"reflect"
	"testing"
)

func init() { Configure(testDeps()) }

func testDeps() Deps {
	return Deps{
		ErrCodeFSBadPath:            "bad_path",
		ErrCodeFSBadRequest:         "bad_request",
		ErrCodeFSBinaryNotSupported: "binary_not_supported",
		ErrCodeFSDenied:             "denied",
		ErrCodeFSNotFile:            "not_file",
		ErrCodeFSReadFailed:         "read_failed",
		ErrCodeFSRevisionConflict:   "revision_conflict",
		ErrCodeFSSymlinkNotAllowed:  "symlink_not_allowed",
		ErrCodeFSTooLarge:           "too_large",
		ErrCodeFSUnsupportedMedia:   "unsupported_media_type",
		ErrCodeFSUnsupportedNewline: "unsupported_newline",
		ErrCodeFSWriteCancelled:     "write_cancelled",
		ErrCodeFSWriteFailed:        "write_failed",
		ErrCodeFSWriteStateUnknown:  "write_state_unknown",
		ErrCodeTitleFeatureDisabled: "title_feature_disabled",
		ErrCodeZipBusy:              "zip_busy",
		ErrCodeZipChanged:           "changed_during_zip",
		ErrCodeZipNoSpace:           "zip_no_space",
		ErrCodeZipNotDir:            "zip_not_dir",
		ErrCodeZipTooLarge:          "zip_too_large",
		ErrCodeZipUnsafe:            "unsafe_name",
	}
}

func TestConfigureRejectsEveryUnwiredField(t *testing.T) {
	full := testDeps()
	typ := reflect.TypeOf(full)
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		d := full
		reflect.ValueOf(&d).Elem().Field(i).Set(reflect.Zero(typ.Field(i).Type))
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("Configure accepted a Deps with %s left unwired", name)
				}
			}()
			Configure(d)
		}()
	}
	Configure(full) // leave the package wired for the tests that follow
}
